// Package room は部屋（客室）の定義と、その保存を扱う。
package room

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 入力の上限。画面の確認用で、業務上の厳密な値ではない。
const (
	MaxNameLen  = 50
	MaxCapacity = 100
)

var (
	// ErrNotFound は指定の部屋が存在しないことを表す。
	ErrNotFound = errors.New("room: 部屋が見つかりません")
	// ErrDuplicateName は同じ名前の部屋がすでにあることを表す。
	ErrDuplicateName = errors.New("room: 同じ名前の部屋があります")
)

// Room は部屋。使わなくなっても削除せず IsActive を false にする（過去の予約が参照するため）。
type Room struct {
	ID        int64
	Name      string
	Capacity  int
	SortOrder int
	IsActive  bool
}

// hasControl は制御文字（改行・タブ・NUL など）を含むかを返す。
func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// Normalize は名前の前後の空白を取り除く。保存する前に呼ぶ。
// Validate は値を書き換えない（検証だけ）ので、整える処理はここに分けている。
func (r *Room) Normalize() {
	r.Name = strings.TrimSpace(r.Name)
}

// Validate は入力の誤りを項目名ごとに返す。誤りがなければ空。値は書き換えない。
// 前後の空白は無視して調べる（空白だけの名前は「空」として誤りにする）。
func (r Room) Validate() map[string]string {
	errs := map[string]string{}

	name := strings.TrimSpace(r.Name)
	switch {
	case name == "":
		errs["name"] = "部屋の名前を入力してください。"
	case utf8.RuneCountInString(name) > MaxNameLen:
		errs["name"] = "部屋の名前は50文字以内にしてください。"
	case !utf8.ValidString(name) || hasControl(name):
		// 不正なバイト列や NUL は PostgreSQL が拒否して 500 になるので、先に弾く。改行・タブも不要。
		errs["name"] = "部屋の名前に使えない文字が含まれています。"
	}

	if r.Capacity < 1 || r.Capacity > MaxCapacity {
		errs["capacity"] = "定員は1〜100の数字で入力してください。"
	}

	return errs
}
