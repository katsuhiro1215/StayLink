package user

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/testdb"
)

func TestStoreCreateAndAuthenticate(t *testing.T) {
	db := testdb.Open(t)
	s := NewStore(db)
	ctx := t.Context()

	u, err := s.Create(ctx, "  Admin ", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || u.Username != "admin" {
		t.Errorf("作成されたユーザー = %+v（ユーザー名は小文字に整える）", u)
	}

	// 保存されているのは、平文ではなくハッシュ。
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = $1`, u.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "correct-horse-battery") || !strings.HasPrefix(stored, "pbkdf2-sha256$") {
		t.Errorf("保存された値 = %q", stored)
	}

	tests := []struct {
		name, username, password string
		wantErr                  error
	}{
		{"正しい", "admin", "correct-horse-battery", nil},
		{"ユーザー名は大文字小文字を区別しない", "ADMIN", "correct-horse-battery", nil},
		{"前後の空白は無視", " admin ", "correct-horse-battery", nil},
		{"パスワードが違う", "admin", "correct-horse-batterY", ErrInvalidCredentials},
		{"パスワードが空", "admin", "", ErrInvalidCredentials},
		{"ユーザーがいない", "nobody", "correct-horse-battery", ErrInvalidCredentials},
		{"ユーザー名が空", "", "correct-horse-battery", ErrInvalidCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Authenticate(ctx, tt.username, tt.password)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("エラー = %v, 期待 %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && got.ID != u.ID {
				t.Errorf("ユーザー = %+v", got)
			}
			if tt.wantErr != nil && got != (User{}) {
				t.Errorf("失敗なのにユーザーが返っている: %+v", got)
			}
		})
	}
}

func TestStoreCreateValidation(t *testing.T) {
	s := NewStore(testdb.Open(t))
	ctx := t.Context()

	tests := []struct {
		name, username, password string
		want                     error
	}{
		{"ユーザー名が短い", "ab", "long-enough-password", ErrInvalidUsername},
		{"ユーザー名に不可の文字", "a b c", "long-enough-password", ErrInvalidUsername},
		{"パスワードが短い", "admin", "short", ErrPasswordTooShort},
		{"パスワードが長い", "admin", strings.Repeat("a", 129), ErrPasswordTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Create(ctx, tt.username, tt.password); !errors.Is(err, tt.want) {
				t.Errorf("エラー = %v, 期待 %v", err, tt.want)
			}
		})
	}
	// 検証で断ったものは、保存されていない。
	if _, err := s.Authenticate(ctx, "admin", "short"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("断ったはずのユーザーでログインできる: %v", err)
	}
}

func TestStoreCreateDuplicate(t *testing.T) {
	s := NewStore(testdb.Open(t))
	ctx := t.Context()
	if _, err := s.Create(ctx, "admin", "first-password-1"); err != nil {
		t.Fatal(err)
	}
	// 大文字小文字だけ違う名前も、同じ名前として断る。
	if _, err := s.Create(ctx, "ADMIN", "second-password-2"); !errors.Is(err, ErrDuplicateUsername) {
		t.Errorf("エラー = %v, 期待 ErrDuplicateUsername", err)
	}
	// 最初のパスワードが、上書きされていない。
	if _, err := s.Authenticate(ctx, "admin", "first-password-1"); err != nil {
		t.Errorf("最初のパスワードで入れない: %v", err)
	}
	if _, err := s.Authenticate(ctx, "admin", "second-password-2"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("2つ目のパスワードで入れてしまう: %v", err)
	}
}

// 保存されたハッシュが壊れているときは、「違う」ではなく内部のエラーとして返す（ログで気づけるように）。
func TestAuthenticateCorruptedStoredHash(t *testing.T) {
	db := testdb.Open(t)
	s := NewStore(db)
	ctx := t.Context()
	u, err := s.Create(ctx, "admin", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET password_hash = 'broken' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Authenticate(ctx, "admin", "correct-horse-battery")
	if !errors.Is(err, ErrInvalidHash) || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("エラー = %v, 期待 ErrInvalidHash（ErrInvalidCredentials ではない）", err)
	}
}

// DB の制約：整えていないユーザー名・空のハッシュは、直接入れても拒否される。
func TestUsersTableConstraints(t *testing.T) {
	db := testdb.Open(t)
	ctx := t.Context()
	for name, q := range map[string]string{
		"大文字":      `INSERT INTO users (username, password_hash) VALUES ('Admin', 'x')`,
		"短い":       `INSERT INTO users (username, password_hash) VALUES ('ab', 'x')`,
		"空のハッシュ":   `INSERT INTO users (username, password_hash) VALUES ('admin', '')`,
		"日本語の名前":   `INSERT INTO users (username, password_hash) VALUES ('管理者です', 'x')`,
		"末尾に改行を含む": `INSERT INTO users (username, password_hash) VALUES (E'admin\n', 'x')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, q); err == nil {
				t.Error("制約に拒否されなかった")
			}
		})
	}
}

// ユーザーがいない場合も、同じ重さの計算をする（応答時間でユーザーの有無を推測されにくくする）。
// 時間の比較は不安定になりうるので、「桁違いに速くない」ことだけを確かめる。
func TestAuthenticateUnknownUserDoesComparableWork(t *testing.T) {
	s := NewStore(testdb.Open(t))
	ctx := t.Context()
	if _, err := s.Create(ctx, "admin", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	s.dummy() // 最初の1回のハッシュ計算（dummyHash の作成）を、計測から外す

	timeIt := func(username string) time.Duration {
		start := time.Now()
		_, _ = s.Authenticate(ctx, username, "wrong-password-1")
		return time.Since(start)
	}
	known, unknown := timeIt("admin"), timeIt("nobody")
	if unknown < known/4 {
		t.Errorf("存在しないユーザーの照合が速すぎる：%v（既存のユーザーは %v）", unknown, known)
	}
}
