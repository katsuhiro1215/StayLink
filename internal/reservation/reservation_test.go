package reservation

import (
	"strings"
	"testing"
	"time"
)

func date(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatalf("ParseDate(%q): %v", s, err)
	}
	return d
}

func TestParseDate(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"2026-11-05", false},
		{"2028-02-29", false}, // うるう年
		{"2027-02-29", true},  // うるう年ではない
		{"2026-02-30", true},  // 存在しない日
		{"2026-13-01", true},
		{"2026-1-5", true}, // 0 埋めなし
		{"2026/11/05", true},
		{"", true},
		{"abc", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := ParseDate(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, 誤りを期待 = %v", err, tt.wantErr)
			}
		})
	}
}

func TestNights(t *testing.T) {
	tests := []struct {
		in, out string
		want    int
	}{
		{"2026-11-05", "2026-11-06", 1},
		{"2026-11-05", "2026-11-08", 3},
		{"2026-12-30", "2027-01-02", 3}, // 年またぎ
		{"2026-02-27", "2026-03-02", 3}, // 月またぎ（2月は28日まで）
		{"2028-02-28", "2028-03-01", 2}, // うるう年
	}
	for _, tt := range tests {
		r := Reservation{CheckIn: date(t, tt.in), CheckOut: date(t, tt.out)}
		if got := r.Nights(); got != tt.want {
			t.Errorf("%s〜%s: Nights = %d, 期待 %d", tt.in, tt.out, got, tt.want)
		}
	}
}

func valid(t *testing.T) Reservation {
	t.Helper()
	return Reservation{
		RoomID: 1, GuestName: "山田 太郎", Contact: "090-0000-0000",
		CheckIn: date(t, "2026-11-05"), CheckOut: date(t, "2026-11-07"),
		NumGuests: 2, Note: "到着は18時ごろ",
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(r *Reservation)
		capacity   int
		wantFields []string
	}{
		{"正常", func(r *Reservation) {}, 4, nil},
		{"連絡先と備考は空でよい", func(r *Reservation) { r.Contact, r.Note = "", "" }, 4, nil},
		{"名前が空", func(r *Reservation) { r.GuestName = "  " }, 4, []string{"guest_name"}},
		{"名前が51文字", func(r *Reservation) { r.GuestName = strings.Repeat("あ", 51) }, 4, []string{"guest_name"}},
		{"名前に制御文字", func(r *Reservation) { r.GuestName = "山田\x00" }, 4, []string{"guest_name"}},
		{"名前に不正なUTF-8", func(r *Reservation) { r.GuestName = "山田\xff" }, 4, []string{"guest_name"}},
		{"名前に改行", func(r *Reservation) { r.GuestName = "山田\n太郎" }, 4, []string{"guest_name"}},
		{"連絡先が101文字", func(r *Reservation) { r.Contact = strings.Repeat("1", 101) }, 4, []string{"contact"}},
		{"備考は改行を許す", func(r *Reservation) { r.Note = "1行目\n2行目" }, 4, nil},
		{"備考が501文字", func(r *Reservation) { r.Note = strings.Repeat("あ", 501) }, 4, []string{"note"}},
		{"備考にNUL", func(r *Reservation) { r.Note = "a\x00b" }, 4, []string{"note"}},
		{"部屋が未選択", func(r *Reservation) { r.RoomID = 0 }, 4, []string{"room_id"}},
		{"チェックインが未入力", func(r *Reservation) { r.CheckIn = time.Time{} }, 4, []string{"check_in"}},
		{"チェックアウトが未入力", func(r *Reservation) { r.CheckOut = time.Time{} }, 4, []string{"check_out"}},
		{"同じ日（0泊）", func(r *Reservation) { r.CheckOut = r.CheckIn }, 4, []string{"check_out"}},
		{"チェックアウトが前", func(r *Reservation) { r.CheckOut = date(t, "2026-11-04") }, 4, []string{"check_out"}},
		{"60泊ちょうど", func(r *Reservation) { r.CheckOut = r.CheckIn.AddDate(0, 0, 60) }, 4, nil},
		{"61泊", func(r *Reservation) { r.CheckOut = r.CheckIn.AddDate(0, 0, 61) }, 4, []string{"check_out"}},
		{"過去の日付も登録できる（後から台帳に記録するため）", func(r *Reservation) {
			r.CheckIn, r.CheckOut = date(t, "2020-01-10"), date(t, "2020-01-12")
		}, 4, nil},
		{"1999年", func(r *Reservation) {
			r.CheckIn, r.CheckOut = date(t, "1999-12-30"), date(t, "2000-01-02")
		}, 4, []string{"check_in"}},
		{"2101年", func(r *Reservation) {
			r.CheckIn, r.CheckOut = date(t, "2101-01-01"), date(t, "2101-01-03")
		}, 4, []string{"check_in", "check_out"}},
		{"人数0", func(r *Reservation) { r.NumGuests = 0 }, 4, []string{"num_guests"}},
		{"人数が定員ちょうど", func(r *Reservation) { r.NumGuests = 4 }, 4, nil},
		{"人数が定員超え", func(r *Reservation) { r.NumGuests = 5 }, 4, []string{"num_guests"}},
		{"定員が不明（0）なら超過は見ない", func(r *Reservation) { r.NumGuests = 99 }, 0, nil},
		{"複数の誤り", func(r *Reservation) { r.GuestName = ""; r.NumGuests = 0 }, 4, []string{"guest_name", "num_guests"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := valid(t)
			tt.mutate(&r)
			errs := r.Validate(tt.capacity)

			if len(errs) != len(tt.wantFields) {
				t.Fatalf("エラー項目 = %v, 期待 %v", errs, tt.wantFields)
			}
			for _, f := range tt.wantFields {
				if errs[f] == "" {
					t.Errorf("%q のエラーがない（得られたエラー: %v）", f, errs)
				}
			}
		})
	}
}

func TestValidateTrimsText(t *testing.T) {
	r := valid(t)
	r.GuestName, r.Contact, r.Note = "  山田  ", " 090 ", "  メモ  "
	r.Validate(4)
	if r.GuestName != "山田" || r.Contact != "090" || r.Note != "メモ" {
		t.Errorf("Trim されていない: %q %q %q", r.GuestName, r.Contact, r.Note)
	}
}
