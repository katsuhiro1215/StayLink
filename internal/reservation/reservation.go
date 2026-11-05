// Package reservation は予約の定義と、その保存を扱う。
package reservation

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// 状態。取消しても削除せず Cancelled にする（台帳として履歴を残す）。
const (
	StatusReserved  = "reserved"
	StatusCancelled = "cancelled"
)

// 入力の上限。画面の確認用で、業務上の厳密な値ではない。
const (
	MaxNameLen    = 50
	MaxContactLen = 100
	MaxNoteLen    = 500
	MaxNights     = 60
	MinYear       = 2000 // 日付として受け付ける範囲（入力ミスの検出用）
	MaxYear       = 2100
)

// DateLayout は画面とやり取りする日付の形式（YYYY-MM-DD）。
const DateLayout = "2006-01-02"

var (
	// ErrNotFound は指定の予約が存在しないことを表す。
	ErrNotFound = errors.New("reservation: 予約が見つかりません")
	// ErrNotEditable は取消済みの予約を変更しようとしたことを表す。
	ErrNotEditable = errors.New("reservation: 取消済みの予約は変更できません")
	// ErrRoomNotFound は存在しない部屋を指定したことを表す。
	ErrRoomNotFound = errors.New("reservation: 部屋が見つかりません")
	// ErrOverlap は、同じ部屋で期間が重なる予約があることを表す（二重予約）。
	ErrOverlap = errors.New("reservation: その期間は、この部屋にすでに予約があります")
)

// Reservation は予約。日付は UTC の 0 時の time.Time で持ち、時刻は意味を持たない。
type Reservation struct {
	ID        int64
	RoomID    int64
	RoomName  string // 取得時に部屋から引く。保存はしない
	GuestName string
	Contact   string
	CheckIn   time.Time // 含む
	CheckOut  time.Time // 含まない
	NumGuests int
	Status    string
	Note      string
}

// Nights は泊数を返す。
func (r Reservation) Nights() int {
	return int(r.CheckOut.Sub(r.CheckIn).Hours() / 24)
}

// ParseDate は YYYY-MM-DD の日付を、UTC の 0 時の time.Time にする。
// 存在しない日付（2月30日など）や、形式の違うものは誤りにする。
func ParseDate(s string) (time.Time, error) {
	return time.ParseInLocation(DateLayout, s, time.UTC)
}

// Normalize は文字の前後の空白を取り除く。保存する前に呼ぶ。
// Validate は値を書き換えない（検証だけ）ので、整える処理はここに分けている。
func (r *Reservation) Normalize() {
	r.GuestName = strings.TrimSpace(r.GuestName)
	r.Contact = strings.TrimSpace(r.Contact)
	r.Note = strings.TrimSpace(r.Note)
}

// Validate は入力の誤りを項目名ごとに返す。誤りがなければ空。値は書き換えない。
// 前後の空白は無視して調べる（空白だけの名前は「空」として誤りにする）。
// roomCapacity は選んだ部屋の定員で、人数がそれを超えていないかを確かめる（0 以下なら確かめない）。
func (r Reservation) Validate(roomCapacity int) map[string]string {
	errs := map[string]string{}

	name := strings.TrimSpace(r.GuestName)
	switch {
	case name == "":
		errs["guest_name"] = "宿泊者の名前を入力してください。"
	case utf8.RuneCountInString(name) > MaxNameLen:
		errs["guest_name"] = "宿泊者の名前は50文字以内にしてください。"
	case !utf8.ValidString(name) || hasControl(name, false):
		errs["guest_name"] = "宿泊者の名前に使えない文字が含まれています。"
	}

	contact := strings.TrimSpace(r.Contact)
	switch {
	case utf8.RuneCountInString(contact) > MaxContactLen:
		errs["contact"] = "連絡先は100文字以内にしてください。"
	case !utf8.ValidString(contact) || hasControl(contact, false):
		errs["contact"] = "連絡先に使えない文字が含まれています。"
	}

	note := strings.TrimSpace(r.Note)
	switch {
	case utf8.RuneCountInString(note) > MaxNoteLen:
		errs["note"] = "備考は500文字以内にしてください。"
	case !utf8.ValidString(note) || hasControl(note, true):
		errs["note"] = "備考に使えない文字が含まれています。"
	}

	if r.RoomID < 1 {
		errs["room_id"] = "部屋を選んでください。"
	}

	validateDates(r, errs)

	switch {
	case r.NumGuests < 1:
		errs["num_guests"] = "人数は1名以上で入力してください。"
	case roomCapacity > 0 && r.NumGuests > roomCapacity:
		errs["num_guests"] = "人数が部屋の定員を超えています。"
	}

	return errs
}

func validateDates(r Reservation, errs map[string]string) {
	if r.CheckIn.IsZero() {
		errs["check_in"] = "チェックイン日を入力してください。"
	}
	if r.CheckOut.IsZero() {
		errs["check_out"] = "チェックアウト日を入力してください。"
	}
	if r.CheckIn.IsZero() || r.CheckOut.IsZero() {
		return
	}
	for field, d := range map[string]time.Time{"check_in": r.CheckIn, "check_out": r.CheckOut} {
		if d.Year() < MinYear || d.Year() > MaxYear {
			errs[field] = "日付は2000年から2100年の間で入力してください。"
		}
	}
	if len(errs["check_in"])+len(errs["check_out"]) > 0 {
		return
	}
	switch {
	case !r.CheckOut.After(r.CheckIn):
		errs["check_out"] = "チェックアウト日は、チェックイン日より後にしてください。"
	case r.Nights() > MaxNights:
		errs["check_out"] = "宿泊は60泊までにしてください。"
	}
}

// hasControl は制御文字を含むかを返す。allowNewline が true なら、改行とタブは許す（備考用）。
func hasControl(s string, allowNewline bool) bool {
	return strings.IndexFunc(s, func(c rune) bool {
		if allowNewline && (c == '\n' || c == '\r' || c == '\t') {
			return false
		}
		return unicode.IsControl(c)
	}) >= 0
}
