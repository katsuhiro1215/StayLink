// Package user は、ログインする管理者と、そのパスワードの扱いを持つ。
package user

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// パスワードの保存形式：
//
//	pbkdf2-sha256$回数$ソルト$ハッシュ   （ソルトとハッシュは、パディングなしの base64url）
//
// 回数を値の中に持つので、あとで回数を増やしても、古いハッシュを検証できる。
// 標準ライブラリだけで作るために PBKDF2 を使う（argon2id・bcrypt は golang.org/x/crypto が要る）。
// 総当たりには argon2id のほうが強い。限界は docs/13-login-design.md に記載。
const (
	scheme = "pbkdf2-sha256"

	// iterations は新しく作るハッシュの回数。OWASP の目安（PBKDF2-HMAC-SHA256 で 60万回）。最新の目安は未確認。
	iterations = 600_000
	// maxIterations は、検証するハッシュに許す回数の上限。壊れた・悪意ある値で、検証が止まらなくなるのを防ぐ。
	maxIterations = 10_000_000

	saltBytes = 16
	keyBytes  = 32

	// パスワードの長さの範囲（文字数）。長すぎる入力で計算を重くされないよう、上限を持つ。
	MinPasswordRunes = 10
	MaxPasswordRunes = 128
)

var (
	// ErrInvalidHash は、保存されたハッシュの形式が不正なとき。
	ErrInvalidHash = errors.New("user: パスワードのハッシュの形式が不正です")
	// ErrPasswordTooShort / ErrPasswordTooLong は、パスワードの長さの範囲外。
	ErrPasswordTooShort = fmt.Errorf("user: パスワードは%d文字以上にしてください", MinPasswordRunes)
	ErrPasswordTooLong  = fmt.Errorf("user: パスワードは%d文字以下にしてください", MaxPasswordRunes)
)

// ValidatePassword は、新しく決めるパスワードの長さを確かめる（ログイン時の照合には使わない）。
func ValidatePassword(p string) error {
	n := utf8.RuneCountInString(p)
	switch {
	case n < MinPasswordRunes:
		return ErrPasswordTooShort
	case n > MaxPasswordRunes:
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword は、パスワードから保存用のハッシュを作る（毎回ソルトが変わる）。
func HashPassword(password string) (string, error) {
	return hashWith(password, iterations)
}

func hashWith(password string, iter int) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("user: ソルトの生成: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, keyBytes)
	if err != nil {
		return "", fmt.Errorf("user: ハッシュの計算: %w", err)
	}
	enc := base64.RawURLEncoding
	return strings.Join([]string{scheme, strconv.Itoa(iter), enc.EncodeToString(salt), enc.EncodeToString(key)}, "$"), nil
}

// VerifyPassword は、パスワードが保存されたハッシュと一致するか。
// 形式が不正なら ErrInvalidHash。一致しないだけなら (false, nil)。比較は一定の時間で行う。
func VerifyPassword(password, stored string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != scheme {
		return false, ErrInvalidHash
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > maxIterations {
		return false, ErrInvalidHash
	}
	enc := base64.RawURLEncoding
	salt, err := enc.DecodeString(parts[2])
	if err != nil || len(salt) < saltBytes {
		return false, ErrInvalidHash
	}
	want, err := enc.DecodeString(parts[3])
	if err != nil || len(want) != keyBytes {
		return false, ErrInvalidHash
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false, fmt.Errorf("user: ハッシュの計算: %w", err)
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
