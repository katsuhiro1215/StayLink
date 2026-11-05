package user

import (
	"errors"
	"strings"
	"testing"
)

// 回数を小さくしたハッシュで、仕組み（形式・照合）を確かめる。本番の回数は別のテストで1回だけ確かめる。
const testIter = 1000

func TestHashAndVerify(t *testing.T) {
	h, err := hashWith("correct horse battery", testIter)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, password string
		want           bool
	}{
		{"正しいパスワード", "correct horse battery", true},
		{"違うパスワード", "correct horse batterY", false},
		{"空", "", false},
		{"末尾の空白は別物", "correct horse battery ", false},
		{"全角は別物", "ｃorrect horse battery", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VerifyPassword(tt.password, h)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("VerifyPassword = %v, 期待 %v", got, tt.want)
			}
		})
	}
}

func TestHashFormatAndSalt(t *testing.T) {
	a, _ := hashWith("same-password-1", testIter)
	b, _ := hashWith("same-password-1", testIter)
	if a == b {
		t.Error("同じパスワードで、ソルトが毎回同じ（同じハッシュになった）")
	}
	parts := strings.Split(a, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "1000" {
		t.Errorf("形式 = %q", a)
	}
	if strings.Contains(a, "same-password-1") {
		t.Error("ハッシュに平文が含まれている")
	}
}

// 本番の回数で作ったハッシュが、本番の回数で検証でき、保存形式に回数が入っている。
func TestHashPasswordUsesProductionIterations(t *testing.T) {
	h, err := HashPassword("a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$600000$") {
		t.Errorf("回数が違う: %q", h)
	}
	if ok, err := VerifyPassword("a-long-enough-password", h); err != nil || !ok {
		t.Errorf("VerifyPassword = %v, %v", ok, err)
	}
}

// 回数を後から変えても、古いハッシュ（別の回数）を検証できる。
func TestVerifyAcceptsOtherIterations(t *testing.T) {
	for _, iter := range []int{1, 1000, 2000} {
		h, _ := hashWith("another-password", iter)
		if ok, err := VerifyPassword("another-password", h); err != nil || !ok {
			t.Errorf("回数 %d：%v, %v", iter, ok, err)
		}
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	good, _ := hashWith("password-12345", testIter)
	parts := strings.Split(good, "$")
	join := func(p ...string) string { return strings.Join(p, "$") }

	tests := map[string]string{
		"空":        "",
		"平文そのもの":   "password-12345",
		"別の方式":     join("bcrypt", parts[1], parts[2], parts[3]),
		"区切りが足りない": join(parts[0], parts[1], parts[2]),
		"区切りが多い":   join(good, "x"),
		"回数が数字でない": join(parts[0], "abc", parts[2], parts[3]),
		"回数が0":     join(parts[0], "0", parts[2], parts[3]),
		"回数が負":     join(parts[0], "-5", parts[2], parts[3]),
		"回数が上限超え（計算が止まらない）": join(parts[0], "999999999", parts[2], parts[3]),
		"ソルトが不正なbase64":     join(parts[0], parts[1], "!!!", parts[3]),
		"ソルトが短い":            join(parts[0], parts[1], "AAAA", parts[3]),
		"ハッシュが不正なbase64":    join(parts[0], parts[1], parts[2], "!!!"),
		"ハッシュが短い":           join(parts[0], parts[1], parts[2], "AAAA"),
	}
	for name, stored := range tests {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword("password-12345", stored)
			if ok || !errors.Is(err, ErrInvalidHash) {
				t.Errorf("VerifyPassword = %v, %v（false と ErrInvalidHash を期待）", ok, err)
			}
		})
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name string
		pw   string
		want error
	}{
		{"10文字ちょうど", "abcdefghij", nil},
		{"9文字", "abcdefghi", ErrPasswordTooShort},
		{"空", "", ErrPasswordTooShort},
		{"日本語は文字数で数える（10文字）", "あいうえおかきくけこ", nil},
		{"日本語9文字", "あいうえおかきくけ", ErrPasswordTooShort},
		{"128文字ちょうど", strings.Repeat("a", 128), nil},
		{"129文字", strings.Repeat("a", 129), ErrPasswordTooLong},
		{"空白だけでも文字数は数える", strings.Repeat(" ", 10), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidatePassword(tt.pw); !errors.Is(got, tt.want) {
				t.Errorf("ValidatePassword = %v, 期待 %v", got, tt.want)
			}
		})
	}
}

func TestUsernameNormalizeAndValidate(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"admin", "admin", false},
		{"  Admin  ", "admin", false},
		{"ADMIN", "admin", false},
		{"a.b_c-d9", "a.b_c-d9", false},
		{"ab", "ab", true},                               // 短い
		{strings.Repeat("a", 33), "", true},              // 長い（want は使わない）
		{"管理者", "管理者", true},                             // 英小文字・数字以外
		{"ad min", "ad min", true},                       // 空白を含む
		{"admin@example.com", "admin@example.com", true}, // @ は不可
		{"", "", true},
	}
	for _, tt := range tests {
		got := NormalizeUsername(tt.in)
		if !tt.wantErr || tt.want != "" {
			if tt.want != "" && got != tt.want {
				t.Errorf("Normalize(%q) = %q, 期待 %q", tt.in, got, tt.want)
			}
		}
		if err := ValidateUsername(got); (err != nil) != tt.wantErr {
			t.Errorf("Validate(%q) = %v, 誤りを期待=%v", got, err, tt.wantErr)
		}
	}
}
