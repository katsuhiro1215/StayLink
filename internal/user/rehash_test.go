package user

import (
	"strings"
	"testing"

	"github.com/katsuhiro1215/StayLink/internal/testdb"
)

func TestNeedsRehash(t *testing.T) {
	weak, _ := hashWith("password-12345", 1000)
	current, _ := hashWith("password-12345", iterations)
	stronger, _ := hashWith("password-12345", iterations+1)
	for name, tt := range map[string]struct {
		stored string
		want   bool
	}{
		"回数が少ない":   {weak, true},
		"いまの回数":    {current, false},
		"いまより多い":   {stronger, false},
		"形式が不正":    {"broken", false},
		"方式が違う":    {"bcrypt$10$a$b", false},
		"回数が数字でない": {"pbkdf2-sha256$x$a$b", false},
		"回数が0":     {"pbkdf2-sha256$0$a$b", false},
		"空":        {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := NeedsRehash(tt.stored); got != tt.want {
				t.Errorf("NeedsRehash = %v, 期待 %v", got, tt.want)
			}
		})
	}
}

// 回数の少ない古いハッシュは、ログイン成功のときだけ、いまの回数で作り直される。
func TestAuthenticateRehashesWeakHash(t *testing.T) {
	db := testdb.Open(t)
	s := NewStore(db)
	ctx := t.Context()
	u, err := s.Create(ctx, "admin", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	weak, _ := hashWith("correct-horse-battery", 1000)
	set := func() {
		if _, err := db.ExecContext(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, u.ID, weak); err != nil {
			t.Fatal(err)
		}
	}
	stored := func() string {
		var h string
		if err := db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = $1`, u.ID).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}

	set()
	// パスワードが違う：作り直さない（平文が正しいと分かっていない）。
	if _, err := s.Authenticate(ctx, "admin", "wrong-password-1"); err == nil {
		t.Fatal("違うパスワードで入れた")
	}
	if stored() != weak {
		t.Error("ログインに失敗したのに、ハッシュが書き換わった")
	}
	// パスワードが正しい：作り直す。
	if _, err := s.Authenticate(ctx, "admin", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	got := stored()
	if !strings.HasPrefix(got, "pbkdf2-sha256$600000$") || got == weak {
		t.Errorf("作り直されていない: %q", got)
	}
	// 作り直したあとも、同じパスワードで入れる。
	if _, err := s.Authenticate(ctx, "admin", "correct-horse-battery"); err != nil {
		t.Errorf("作り直したあとに入れない: %v", err)
	}
}
