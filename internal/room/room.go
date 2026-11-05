// Package room は部屋（客室）の定義と、その保存を扱う。
package room

import (
	"errors"
	"strings"
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

// Validate は入力の誤りを項目名ごとに返す。誤りがなければ空。
func (r *Room) Validate() map[string]string {
	errs := map[string]string{}

	r.Name = strings.TrimSpace(r.Name)
	switch {
	case r.Name == "":
		errs["name"] = "部屋の名前を入力してください。"
	case utf8.RuneCountInString(r.Name) > MaxNameLen:
		errs["name"] = "部屋の名前は50文字以内にしてください。"
	}

	if r.Capacity < 1 || r.Capacity > MaxCapacity {
		errs["capacity"] = "定員は1〜100の数字で入力してください。"
	}

	return errs
}
