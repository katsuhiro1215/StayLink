package user

import (
	"errors"
	"regexp"
	"strings"
)

var (
	ErrNotFound           = errors.New("user: ユーザーが見つかりません")
	ErrDuplicateUsername  = errors.New("user: そのユーザー名はすでに使われています")
	ErrInvalidCredentials = errors.New("user: ユーザー名かパスワードが違います")
	ErrInvalidUsername    = errors.New("user: ユーザー名は、英小文字・数字・ . _ - の3〜32文字にしてください")
)

// User はログインできる管理者。パスワードのハッシュは、画面などに渡さないよう、このまま持ち回らない。
type User struct {
	ID       int64
	Username string
}

// 入力と migrations/004 の CHECK を同じにする。
var usernamePattern = regexp.MustCompile(`^[a-z0-9._-]{3,32}$`)

// NormalizeUsername は、前後の空白を除いて、小文字にする（大文字小文字を区別しない）。
func NormalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidateUsername は、整えたあとのユーザー名を確かめる。
func ValidateUsername(s string) error {
	if !usernamePattern.MatchString(s) {
		return ErrInvalidUsername
	}
	return nil
}
