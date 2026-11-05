package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
	"github.com/katsuhiro1215/StayLink/internal/room"
)

// ---- 表の組み立て（DB・HTTP なし） ----

// bookingsOf は、1行の予約の棒を「名前@開始日×日数」の文字列にする。
// 前の月から続くなら末尾に「<」、次の月へ続くなら「>」を付ける。
func bookingsOf(row calRow) []string {
	var out []string
	day := 1
	for _, c := range row.Cells {
		if c.Booking != nil {
			s := fmt.Sprintf("%s@%d×%d", c.Booking.Guest, day, c.Span)
			if c.Booking.ContinuesBefore {
				s += "<"
			}
			if c.Booking.ContinuesAfter {
				s += ">"
			}
			out = append(out, s)
		}
		day += c.Span
	}
	return out
}

func spanSum(row calRow) int {
	n := 0
	for _, c := range row.Cells {
		n += c.Span
	}
	return n
}

func res(t *testing.T, id int64, guest, in, out string) reservation.Reservation {
	t.Helper()
	return reservation.Reservation{
		ID: id, RoomID: 1, RoomName: "松の間", GuestName: guest,
		CheckIn: day(t, in), CheckOut: day(t, out), NumGuests: 2, Status: reservation.StatusReserved,
	}
}

var matsu = room.Room{ID: 1, Name: "松の間", Capacity: 4, IsActive: true}

// 予約の棒が、月のどこに、何日分で出るか。境界（月の最初・最後、隣接、月またぎ）を網羅する。
func TestBuildCalendarBookings(t *testing.T) {
	tests := []struct {
		name string
		resv func(t *testing.T) []reservation.Reservation
		want []string
	}{
		{"月の途中", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "山田", "2026-11-10", "2026-11-13")}
		}, []string{"山田@10×3"}},
		{"1泊", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "山田", "2026-11-10", "2026-11-11")}
		}, []string{"山田@10×1"}},
		{"隣接（前の人のチェックアウト日に、次の人がチェックイン）", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "A", "2026-11-10", "2026-11-12"), res(t, 2, "B", "2026-11-12", "2026-11-14")}
		}, []string{"A@10×2", "B@12×2"}},
		{"前の月から続く（11/1・11/2 の2泊）", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "山田", "2026-10-28", "2026-11-03")}
		}, []string{"山田@1×2<"}},
		{"次の月へ続く（11/29・11/30 の2泊）", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "山田", "2026-11-29", "2026-12-03")}
		}, []string{"山田@29×2>"}},
		{"月全体を包む", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "山田", "2026-10-01", "2026-12-05")}
		}, []string{"山田@1×30<>"}},
		{"月の最初の日にチェックアウト（11月の夜は泊まらない）は出さない", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "山田", "2026-10-30", "2026-11-01")}
		}, nil},
		{"翌月の最初の日にチェックイン（11月の夜は泊まらない）は出さない", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "山田", "2026-12-01", "2026-12-03")}
		}, nil},
		{"11/30 の1泊だけ（最終日の夜）。翌月へは続かない", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "山田", "2026-11-30", "2026-12-01")}
		}, []string{"山田@30×1"}},
		{"11/1 の1泊だけ（最初の日の夜）。前の月からは続かない", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "山田", "2026-11-01", "2026-11-02")}
		}, []string{"山田@1×1"}},
		{"渡された順が日付順でなくても、日付順に並べる", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 2, "後", "2026-11-20", "2026-11-22"), res(t, 1, "先", "2026-11-05", "2026-11-07")}
		}, []string{"先@5×2", "後@20×2"}},
		{"万一、同じ部屋で重なっていても、マスを二重に作らない（後の予約は、前の棒の後ろから）", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "A", "2026-11-10", "2026-11-14"), res(t, 2, "B", "2026-11-12", "2026-11-16")}
		}, []string{"A@10×4", "B@14×2"}},
		{"万一、完全に包まれていても、マスを二重に作らない", func(t *testing.T) []reservation.Reservation {
			return []reservation.Reservation{res(t, 1, "A", "2026-11-10", "2026-11-16"), res(t, 2, "B", "2026-11-12", "2026-11-14")}
		}, []string{"A@10×6"}},
		{"予約なし", func(t *testing.T) []reservation.Reservation { return nil }, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := buildCalendar(day(t, "2026-11-01"), day(t, "2026-11-05"), []room.Room{matsu}, tt.resv(t))

			if len(v.Rows) != 1 {
				t.Fatalf("行数 = %d, 期待 1", len(v.Rows))
			}
			if got := bookingsOf(v.Rows[0]); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("棒 = %v, 期待 %v", got, tt.want)
			}
			// どの場合も、1行のマスの合計は、その月の日数ちょうどになる（ずれると表が崩れる）。
			if got := spanSum(v.Rows[0]); got != 30 {
				t.Errorf("マスの合計 = %d, 期待 30（11月の日数）", got)
			}
		})
	}
}

func TestBuildCalendarDays(t *testing.T) {
	tests := []struct {
		month   string
		days    int
		firstWD string // 1日の曜日
		lastWD  string
	}{
		{"2026-11-01", 30, "日", "月"}, // 2026-11-01 は日曜、11-30 は月曜
		{"2026-12-01", 31, "火", "木"},
		{"2027-02-01", 28, "月", "日"}, // うるう年ではない
		{"2028-02-01", 29, "火", "火"}, // うるう年
	}
	for _, tt := range tests {
		t.Run(tt.month, func(t *testing.T) {
			v := buildCalendar(day(t, tt.month), day(t, "2000-01-01"), nil, nil)
			if v.DayCount != tt.days || len(v.Days) != tt.days {
				t.Fatalf("日数 = %d / %d, 期待 %d", v.DayCount, len(v.Days), tt.days)
			}
			if v.Days[0].Weekday != tt.firstWD || v.Days[tt.days-1].Weekday != tt.lastWD {
				t.Errorf("曜日 = %s〜%s, 期待 %s〜%s", v.Days[0].Weekday, v.Days[tt.days-1].Weekday, tt.firstWD, tt.lastWD)
			}
			if v.Days[0].Num != 1 || v.Days[tt.days-1].Num != tt.days {
				t.Errorf("日の番号が 1〜%d になっていない", tt.days)
			}
		})
	}
}

func TestBuildCalendarWeekendAndToday(t *testing.T) {
	v := buildCalendar(day(t, "2026-11-01"), day(t, "2026-11-05"), []room.Room{matsu}, nil)

	weekend := map[int]bool{}
	var todays []int
	for _, d := range v.Days {
		if d.Weekend {
			weekend[d.Num] = true
		}
		if d.Today {
			todays = append(todays, d.Num)
		}
	}
	// 2026年11月の土日：1(日) 7(土) 8(日) 14 15 21 22 28 29
	for _, n := range []int{1, 7, 8, 14, 15, 21, 22, 28, 29} {
		if !weekend[n] {
			t.Errorf("%d日が週末になっていない", n)
		}
	}
	if len(weekend) != 9 {
		t.Errorf("週末の数 = %d, 期待 9", len(weekend))
	}
	if fmt.Sprint(todays) != "[5]" {
		t.Errorf("今日の日 = %v, 期待 [5]", todays)
	}
	// 空きマスにも、週末と今日が伝わる（色づけのため）。
	if c := v.Rows[0].Cells[4]; !c.Today || c.Weekend {
		t.Errorf("11/5 のマス: Today=%v Weekend=%v, 期待 true / false", c.Today, c.Weekend)
	}
	if c := v.Rows[0].Cells[6]; c.Today || !c.Weekend {
		t.Errorf("11/7 のマス: Today=%v Weekend=%v, 期待 false / true", c.Today, c.Weekend)
	}
}

func TestBuildCalendarTodayOutsideMonth(t *testing.T) {
	v := buildCalendar(day(t, "2026-12-01"), day(t, "2026-11-05"), nil, nil)
	for _, d := range v.Days {
		if d.Today {
			t.Errorf("今日が含まれない月で、%d日が今日になっている", d.Num)
		}
	}
	if v.IsCurrent {
		t.Error("今日を含まない月が、今月になっている")
	}
}

func TestBuildCalendarMonthNavigation(t *testing.T) {
	tests := []struct {
		month, prev, next string
		current           bool
	}{
		{"2026-11-01", "2026-10", "2026-12", true},
		{"2026-01-01", "2025-12", "2026-02", false}, // 年をまたぐ（前）
		{"2026-12-01", "2026-11", "2027-01", false}, // 年をまたぐ（後）
		{"2000-01-01", "", "2000-02", false},        // 受け付ける範囲の下限：これより前へは進めない
		{"2100-12-01", "2100-11", "", false},        // 上限
	}
	for _, tt := range tests {
		t.Run(tt.month, func(t *testing.T) {
			today := day(t, "2026-11-05")
			v := buildCalendar(day(t, tt.month), today, nil, nil)
			if v.PrevMonth != tt.prev || v.NextMonth != tt.next {
				t.Errorf("前=%q 次=%q, 期待 前=%q 次=%q", v.PrevMonth, v.NextMonth, tt.prev, tt.next)
			}
			if v.IsCurrent != (tt.month == "2026-11-01") {
				t.Errorf("IsCurrent = %v", v.IsCurrent)
			}
		})
	}
}

func TestBuildCalendarRooms(t *testing.T) {
	rooms := []room.Room{
		{ID: 1, Name: "松の間", Capacity: 4, IsActive: true},
		{ID: 2, Name: "竹の間", Capacity: 2, IsActive: true},
		{ID: 3, Name: "梅の間", Capacity: 6, IsActive: false}, // 停止中・予約なし → 出さない
		{ID: 4, Name: "桜の間", Capacity: 6, IsActive: false}, // 停止中・予約あり → 出す（過去の記録を見られるように）
	}
	r4 := res(t, 5, "過去の客", "2026-11-03", "2026-11-05")
	r4.RoomID, r4.RoomName = 4, "桜の間"

	v := buildCalendar(day(t, "2026-11-01"), day(t, "2026-11-05"), rooms, []reservation.Reservation{r4})

	var names []string
	for _, row := range v.Rows {
		names = append(names, row.RoomName)
	}
	if fmt.Sprint(names) != "[松の間 竹の間 桜の間]" {
		t.Fatalf("行 = %v, 期待 [松の間 竹の間 桜の間]（渡された部屋の順）", names)
	}
	if v.Rows[0].Inactive || v.Rows[1].Inactive || !v.Rows[2].Inactive {
		t.Error("停止中の印が、正しい部屋に付いていない")
	}
	// 予約のない部屋は、30個の空きマス。
	if len(v.Rows[0].Cells) != 30 || spanSum(v.Rows[0]) != 30 {
		t.Errorf("予約のない部屋のマス = %d", len(v.Rows[0].Cells))
	}
}

func TestBuildCalendarOnlyAssignsToOwnRoom(t *testing.T) {
	r2 := res(t, 2, "竹の客", "2026-11-10", "2026-11-12")
	r2.RoomID, r2.RoomName = 2, "竹の間"
	rooms := []room.Room{matsu, {ID: 2, Name: "竹の間", Capacity: 2, IsActive: true}}

	v := buildCalendar(day(t, "2026-11-01"), day(t, "2026-11-05"), rooms, []reservation.Reservation{res(t, 1, "松の客", "2026-11-10", "2026-11-12"), r2})

	if got := bookingsOf(v.Rows[0]); fmt.Sprint(got) != "[松の客@10×2]" {
		t.Errorf("松の間 = %v", got)
	}
	if got := bookingsOf(v.Rows[1]); fmt.Sprint(got) != "[竹の客@10×2]" {
		t.Errorf("竹の間 = %v", got)
	}
}

func TestBookingLabel(t *testing.T) {
	r := res(t, 1, "山田 太郎", "2026-11-10", "2026-11-13")
	if got, want := bookingLabel(r), "山田 太郎さん、2026年11月10日〜11月13日、3泊、2名"; got != want {
		t.Errorf("Label = %q, 期待 %q", got, want)
	}
}

// ---- 画面（偽の保存先） ----

func TestCalendarPage(t *testing.T) {
	e := newEnv(t)       // 松の間・竹の間（使用中）、梅の間（停止中）。今日は 2026-11-05
	e.seed(t, sample(t)) // 松の間 11/10〜11/12 の山田 太郎さん
	rec := get(e.h, "/calendar")

	if rec.Code != 200 {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	for _, s := range []string{
		"2026年11月",
		`href="/calendar?month=2026-10"`, `href="/calendar?month=2026-12"`,
		`aria-current="date"`,         // 今日の列
		`href="/reservations/1/edit"`, // 棒は編集画面へのリンク
		`aria-label="山田 太郎さん、2026年11月10日〜11月12日、2泊、2名"`, // 読み上げ用の全文
		`colspan="2"`,
		"松の間", "竹の間",
		`role="region"`, `tabindex="0"`, // 横スクロール領域をキーボードで操作できる
		`href="/calendar" aria-current="page"`, // メニューの現在地
	} {
		mustContain(t, rec, s)
	}
	if strings.Contains(rec.Body.String(), "梅の間") {
		t.Error("停止中で予約もない部屋が出ている")
	}
	if strings.Contains(rec.Body.String(), "今月に戻る") {
		t.Error("今月を表示しているのに、「今月に戻る」が出ている")
	}
	// 月の範囲は 11/1（含む）〜 12/1（含まない）で問い合わせる。
	if want := [2]time.Time{day(t, "2026-11-01"), day(t, "2026-12-01")}; e.resv.lastBetween != want {
		t.Errorf("Between の期間 = %v, 期待 %v", e.resv.lastBetween, want)
	}
}

func TestCalendarPageOtherMonth(t *testing.T) {
	e := newEnv(t)
	rec := get(e.h, "/calendar?month=2027-02")

	mustContain(t, rec, "2027年2月")
	mustContain(t, rec, "今月に戻る")
	if got, want := strings.Count(rec.Body.String(), `<th scope="col" class="day`), 28; got != want {
		t.Errorf("日の列 = %d, 期待 %d（2027年2月）", got, want)
	}
	mustContain(t, rec, "days-28")
	if strings.Contains(rec.Body.String(), `aria-current="date"`) {
		t.Error("今日を含まない月で、今日の印が出ている")
	}
	if want := [2]time.Time{day(t, "2027-02-01"), day(t, "2027-03-01")}; e.resv.lastBetween != want {
		t.Errorf("Between の期間 = %v, 期待 %v", e.resv.lastBetween, want)
	}
}

func TestCalendarInvalidMonthFallsBackWithWarning(t *testing.T) {
	for _, m := range []string{"abc", "2026-13", "2026-1", "1999-12", "2101-01", "2026-11-05", "<b>x</b>"} {
		t.Run(m, func(t *testing.T) {
			e := newEnv(t)
			rec := get(e.h, "/calendar?month="+m)

			if rec.Code != 200 {
				t.Fatalf("ステータス = %d, 期待 200（誤った月でも画面は返す）", rec.Code)
			}
			mustContain(t, rec, "月の指定が正しくないため、今月を表示しています。")
			mustContain(t, rec, "2026年11月")
			if strings.Contains(rec.Body.String(), "<b>x</b>") {
				t.Error("入力がそのまま表示されている")
			}
		})
	}
}

func TestCalendarMonthParamEmptyIsCurrentMonthWithoutWarning(t *testing.T) {
	e := newEnv(t)
	rec := get(e.h, "/calendar?month=")
	mustContain(t, rec, "2026年11月")
	if strings.Contains(rec.Body.String(), "正しくないため") {
		t.Error("空の month で警告が出ている")
	}
}

func TestCalendarEscapesGuestName(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.GuestName = `<script>alert(1)</script>"onmouseover="x`
	e.seed(t, r)

	body := get(e.h, "/calendar").Body.String()
	if strings.Contains(body, "<script>alert(1)") {
		t.Error("名前がエスケープされていない（本文）")
	}
	if strings.Contains(body, `"onmouseover="x`) {
		t.Error("名前がエスケープされていない（属性：title / aria-label）")
	}
}

func TestCalendarCancelledReservationIsNotShown(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.Status = reservation.StatusCancelled
	e.seed(t, r)

	if strings.Contains(get(e.h, "/calendar").Body.String(), "山田 太郎") {
		t.Error("取消済みの予約が、カレンダーに出ている")
	}
}

func TestCalendarWithoutRooms(t *testing.T) {
	e := newEnv(t)
	e.rms.rooms = nil
	rec := get(e.h, "/calendar")
	if rec.Code != 200 {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	mustContain(t, rec, "表示できる部屋がありません")
}

func TestCalendarStoreErrors(t *testing.T) {
	t.Run("予約の取得に失敗", func(t *testing.T) {
		e := newEnv(t)
		e.resv.err = errBoom
		rec := get(e.h, "/calendar")
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("ステータス = %d（500 で、内部のエラー内容を出さないこと）", rec.Code)
		}
	})
	t.Run("部屋の取得に失敗", func(t *testing.T) {
		e := newEnv(t)
		e.rms.err = errBoom
		if rec := get(e.h, "/calendar"); rec.Code != http.StatusInternalServerError {
			t.Errorf("ステータス = %d, 期待 500", rec.Code)
		}
	})
}

func TestCalendarOnlyAllowsGET(t *testing.T) {
	e := newEnv(t)
	rec := post(e.h, "/calendar", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("ステータス = %d, 期待 405", rec.Code)
	}
}

// 空きマスだけが「この部屋・この日から登録」のリンクを持つ。予約のマスは持たない。
func TestBuildCalendarEmptyCellsLinkToNewReservation(t *testing.T) {
	v := buildCalendar(day(t, "2026-11-01"), day(t, "2026-11-05"), []room.Room{matsu},
		[]reservation.Reservation{res(t, 1, "山田", "2026-11-10", "2026-11-13")})

	empties, bookings := 0, 0
	date := 1 // 各セルの先頭の日
	for _, c := range v.Rows[0].Cells {
		if c.Booking != nil {
			bookings++
			if c.NewURL != "" || c.NewText != "" {
				t.Errorf("%d日：予約のマスにリンクがある", date)
			}
		} else {
			empties++
			want := fmt.Sprintf("/reservations/new?check_in=2026-11-%02d&room_id=1", date)
			if c.NewURL != want {
				t.Errorf("%d日：リンク = %q, 期待 %q", date, c.NewURL, want)
			}
		}
		date += c.Span
	}
	if bookings != 1 || empties != 27 { // 30日 − 予約の3泊
		t.Errorf("予約 %d・空き %d, 期待 1・27", bookings, empties)
	}

	first, last := v.Rows[0].Cells[0], v.Rows[0].Cells[len(v.Rows[0].Cells)-1]
	if first.NewText != "松の間、11月1日に予約を登録" || last.NewText != "松の間、11月30日に予約を登録" {
		t.Errorf("説明 = %q / %q", first.NewText, last.NewText)
	}
	// チェックアウト日（13日）は空いていて、そこから登録できる。
	if want := "/reservations/new?check_in=2026-11-13&room_id=1"; !hasURL(v.Rows[0], want) {
		t.Errorf("チェックアウト日のマスに %q がない", want)
	}
}

func hasURL(row calRow, url string) bool {
	for _, c := range row.Cells {
		if c.NewURL == url {
			return true
		}
	}
	return false
}

// 画面（HTML）：リンクは、読み上げとキーボードの移動から外してある（マウス用）。数は空きマスと一致する。
func TestCalendarEmptyCellLinksInHTML(t *testing.T) {
	e := newEnv(t) // 松の間・竹の間（使用中）、梅の間（停止中）。今日は 2026-11-05
	rec := get(e.h, "/calendar?month=2026-11")
	body := rec.Body.String()

	links := strings.Count(body, `class="cell-new"`)
	if links != 60 { // 使用中の2部屋 × 30日（予約なし）
		t.Errorf("空きマスのリンク = %d, 期待 60", links)
	}
	mustContain(t, rec, `href="/reservations/new?check_in=2026-11-01&amp;room_id=1"`)
	mustContain(t, rec, `href="/reservations/new?check_in=2026-11-30&amp;room_id=2"`)
	for _, attr := range []string{`tabindex="-1"`, `aria-hidden="true"`} {
		if n := strings.Count(body, attr); n != links {
			t.Errorf("%s は %d 個, 期待 %d（リンクと同数）", attr, n, links)
		}
	}
}

// 停止中の部屋は、登録画面で選べないので、予約があって行が出ていても、空きマスにリンクを付けない。
func TestCalendarInactiveRoomHasNoLinks(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.RoomID, r.RoomName = 3, "梅の間" // 停止中の部屋の予約（過去の記録）
	e.seed(t, r)

	rec := get(e.h, "/calendar?month=2026-11")
	mustContain(t, rec, "梅の間（停止中）") // 行は出る
	if strings.Contains(rec.Body.String(), "room_id=3") {
		t.Error("停止中の部屋の空きマスに、登録のリンクがある")
	}
	if n := strings.Count(rec.Body.String(), `class="cell-new"`); n != 60 {
		t.Errorf("リンク = %d, 期待 60（使用中の2部屋のみ）", n)
	}
}

// 年をまたぐ月（12月）でも、日付と、月末・翌月初のリンクが合う。
func TestBuildCalendarEmptyCellLinksDecember(t *testing.T) {
	v := buildCalendar(day(t, "2026-12-01"), day(t, "2026-11-05"), []room.Room{matsu}, nil)
	cells := v.Rows[0].Cells
	if len(cells) != 31 {
		t.Fatalf("マス = %d, 期待 31", len(cells))
	}
	if cells[30].NewURL != "/reservations/new?check_in=2026-12-31&room_id=1" || cells[30].NewText != "松の間、12月31日に予約を登録" {
		t.Errorf("12/31 のリンク = %q %q", cells[30].NewURL, cells[30].NewText)
	}
}

var errBoom = fmt.Errorf("boom")
