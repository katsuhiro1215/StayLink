package web

// 月間カレンダー（行が部屋、列が日付の表）。
//
// 1マス＝1泊。予約は、チェックイン日のマスから、チェックアウト日の「前日」のマスまでの横棒で表す。
// チェックアウト日のマスは空く（その夜は泊まらない）ので、同じ日に次の人が入る様子が、そのまま表に出る。
//
// 表の組み立て（buildCalendar）は、DB にも HTTP にも依存しない関数にしてある。
// 月またぎ・隣接などの境界を、DB なしで表形式のテストにできる。

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
	"github.com/katsuhiro1215/StayLink/internal/room"
)

const monthLayout = "2006-01" // month=2026-11 の形

var weekdayNames = [...]string{"日", "月", "火", "水", "木", "金", "土"}

// calendarView はカレンダー画面の表示用データ。
type calendarView struct {
	Month      string // 表示中の月（2026-11）
	MonthLabel string // 2026年11月
	PrevMonth  string // 前の月（range 外なら空）
	NextMonth  string // 次の月（range 外なら空）
	IsCurrent  bool   // 表示中の月が、今日を含む月か
	DayCount   int    // その月の日数（28〜31。CSS の表の幅を決める）
	Days       []calDay
	Rows       []calRow
	Warning    string
}

type calDay struct {
	Num     int
	Weekday string
	Weekend bool
	Today   bool
}

type calRow struct {
	RoomName string
	Inactive bool
	Cells    []calCell
}

// calCell は表の1つのセル。予約があれば Span 日分にまたがる（colspan）。なければ1日分の空きマス。
type calCell struct {
	Span    int
	Booking *calBooking // nil なら空きマス
	Weekend bool
	Today   bool
}

type calBooking struct {
	ID              int64
	Guest           string
	Label           string // 読み上げ・ツールチップ用の全文
	ContinuesBefore bool   // 前の月から続いている
	ContinuesAfter  bool   // 次の月へ続く
}

// buildCalendar は、月の最初の日（monthStart、UTC の 0 時）と、その月に泊まりがある予約から、表を組み立てる。
//
//   - today は今日（UTC の 0 時の日付）。その月に含まれれば、その日を強調する。
//   - 部屋は、使用中のものは常に出し、停止中のものは、その月に予約がある場合だけ出す（過去の記録を見られるように）。
//   - 取消された予約は渡されない前提（Store.Between が除く）。
func buildCalendar(monthStart, today time.Time, rooms []room.Room, resv []reservation.Reservation) calendarView {
	nextStart := monthStart.AddDate(0, 1, 0)
	days := int(nextStart.Sub(monthStart).Hours() / 24)

	v := calendarView{
		Month:      monthStart.Format(monthLayout),
		MonthLabel: monthStart.Format("2006年1月"),
		IsCurrent:  today.Year() == monthStart.Year() && today.Month() == monthStart.Month(),
		DayCount:   days,
	}
	if prev := monthStart.AddDate(0, -1, 0); prev.Year() >= reservation.MinYear {
		v.PrevMonth = prev.Format(monthLayout)
	}
	if nextStart.Year() <= reservation.MaxYear {
		v.NextMonth = nextStart.Format(monthLayout)
	}

	for d := 0; d < days; d++ {
		date := monthStart.AddDate(0, 0, d)
		wd := date.Weekday()
		v.Days = append(v.Days, calDay{
			Num:     d + 1,
			Weekday: weekdayNames[wd],
			Weekend: wd == time.Saturday || wd == time.Sunday,
			Today:   date.Equal(today),
		})
	}

	byRoom := map[int64][]reservation.Reservation{}
	for _, r := range resv {
		byRoom[r.RoomID] = append(byRoom[r.RoomID], r)
	}

	for _, rm := range rooms {
		bookings := byRoom[rm.ID]
		if !rm.IsActive && len(bookings) == 0 {
			continue
		}
		sort.Slice(bookings, func(i, j int) bool {
			if !bookings[i].CheckIn.Equal(bookings[j].CheckIn) {
				return bookings[i].CheckIn.Before(bookings[j].CheckIn)
			}
			return bookings[i].ID < bookings[j].ID
		})
		v.Rows = append(v.Rows, calRow{
			RoomName: rm.Name,
			Inactive: !rm.IsActive,
			Cells:    buildCells(v.Days, monthStart, nextStart, bookings),
		})
	}
	return v
}

// buildCells は1つの部屋の1か月分のセルを作る。bookings はチェックイン日の順。
func buildCells(days []calDay, monthStart, nextStart time.Time, bookings []reservation.Reservation) []calCell {
	dayIndex := func(t time.Time) int { return int(t.Sub(monthStart).Hours() / 24) } // 日付は UTC の 0 時なので、ちょうど割り切れる

	var cells []calCell
	empty := func(from, to int) {
		for i := from; i < to; i++ {
			cells = append(cells, calCell{Span: 1, Weekend: days[i].Weekend, Today: days[i].Today})
		}
	}

	cursor := 0 // ここまで（含まない）のマスは作り終えた
	for _, b := range bookings {
		// 泊まりのある日は、チェックイン日 〜 チェックアウト日の前日。月の範囲に切り詰める。
		start := max(dayIndex(b.CheckIn), 0, cursor) // cursor：万一、同じ部屋で重なっていても、マスを二重に作らない
		end := min(dayIndex(b.CheckOut), len(days))
		if end <= start {
			continue
		}
		empty(cursor, start)
		cells = append(cells, calCell{
			Span: end - start,
			Booking: &calBooking{
				ID:              b.ID,
				Guest:           b.GuestName,
				Label:           bookingLabel(b),
				ContinuesBefore: b.CheckIn.Before(monthStart),
				ContinuesAfter:  b.CheckOut.After(nextStart),
			},
		})
		cursor = end
	}
	empty(cursor, len(days))
	return cells
}

// bookingLabel は、棒の全文（読み上げとツールチップ用）。棒の中には、幅の都合で名前しか出せない。
func bookingLabel(b reservation.Reservation) string {
	return fmt.Sprintf("%sさん、%s、%d泊、%d名", b.GuestName, japaneseRange(b.CheckIn, b.CheckOut), b.Nights(), b.NumGuests)
}

// calendar は GET /calendar?month=2026-11。month が無ければ、今日を含む月。
func (s *server) calendar(w http.ResponseWriter, r *http.Request) {
	today := s.today()
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	warning := ""

	if p := r.URL.Query().Get("month"); p != "" {
		m, err := time.ParseInLocation(monthLayout, p, time.UTC)
		if err != nil || m.Year() < reservation.MinYear || m.Year() > reservation.MaxYear {
			warning = "月の指定が正しくないため、今月を表示しています。"
		} else {
			monthStart = m
		}
	}

	rooms, ok := s.listRooms(w, r)
	if !ok {
		return
	}
	resv, err := s.reservations.Between(r.Context(), monthStart, monthStart.AddDate(0, 1, 0))
	if err != nil {
		s.serverError(w, "カレンダーの予約の取得に失敗しました", err)
		return
	}

	view := buildCalendar(monthStart, today, rooms, resv)
	view.Warning = warning
	s.pages.render(w, http.StatusOK, "calendar", pageData{Title: "カレンダー", Active: "calendar", Data: view})
}
