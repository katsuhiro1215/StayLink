package web

// トップ画面の「今日の到着・出発」。
//
// 今日の分けかた（取消済みは渡されない前提）：
//   - 到着：チェックイン日が今日
//   - 出発：チェックアウト日が今日
//   - 滞在中：昨日以前にチェックインし、明日以降にチェックアウトする（今夜も泊まる）
//
// 分ける処理（buildToday）は、DB にも HTTP にも依存しない関数にして、境界を DB なしで確かめる。
// 検索は、既存の Between（昨日の夜〜今夜に泊まりがある予約）で足りるので、保存先に新しい問い合わせは足さない。

import (
	"net/http"
	"sort"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
)

// todayView はトップ画面の表示用データ。
type todayView struct {
	Label      string // 2026年11月5日（木）
	Arrivals   []reservation.Reservation
	Departures []reservation.Reservation
	Staying    []reservation.Reservation
	Sections   []todaySection // 画面に出す順（到着・出発・滞在中）
}

// todaySection は画面の1つの一覧。
type todaySection struct {
	ID          string
	Title       string
	Empty       string // 0件のときの文
	ShowCheckIn bool   // 日付の列に、チェックインを出す（出さなければチェックアウト）
	List        []reservation.Reservation
}

// dateOnly は、値が示す年月日だけを取り出して、UTC の 0 時に揃える（時刻やタイムゾーンが混ざっても同じ日として比べる）。
func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// buildToday は、予約を、今日の到着・出発・滞在中に分ける。today は今日の日付（時刻は見ない）。
// 同じ予約が到着と出発の両方に入ることはない（泊数は1泊以上で、チェックイン日 < チェックアウト日）。
// 今日に関係しない予約（昨日に出発済み、明日以降に到着など）は、どこにも入れない。
func buildToday(today time.Time, resv []reservation.Reservation) todayView {
	today = dateOnly(today)
	v := todayView{Label: today.Format("2006年1月2日") + "（" + weekdayNames[today.Weekday()] + "）"}

	for _, r := range resv {
		in, out := dateOnly(r.CheckIn), dateOnly(r.CheckOut)
		switch {
		case in.Equal(today):
			v.Arrivals = append(v.Arrivals, r)
		case out.Equal(today):
			v.Departures = append(v.Departures, r)
		case in.Before(today) && out.After(today):
			v.Staying = append(v.Staying, r)
		}
	}
	for _, list := range [][]reservation.Reservation{v.Arrivals, v.Departures, v.Staying} {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].RoomName != list[j].RoomName {
				return list[i].RoomName < list[j].RoomName
			}
			return list[i].ID < list[j].ID
		})
	}
	v.Sections = []todaySection{
		{ID: "arrivals", Title: "今日の到着", Empty: "今日到着する予約はありません。", List: v.Arrivals},
		{ID: "departures", Title: "今日の出発", Empty: "今日出発する予約はありません。", ShowCheckIn: true, List: v.Departures},
		{ID: "staying", Title: "滞在中", Empty: "今夜、泊まっている予約はありません。", List: v.Staying},
	}
	return v
}

// home は GET /（トップ）。
func (s *server) home(w http.ResponseWriter, r *http.Request) {
	today := s.today()
	// 昨日の夜〜今夜に泊まりがある予約：今日に出発する人（昨夜まで泊まった）と、今日に到着する人（今夜から泊まる）の両方が入る。
	resv, err := s.reservations.Between(r.Context(), today.AddDate(0, 0, -1), today.AddDate(0, 0, 1))
	if err != nil {
		s.serverError(w, "今日の予約の取得に失敗しました", err)
		return
	}
	s.pages.render(w, http.StatusOK, "home", pageData{Title: "トップ", Active: "home", Data: buildToday(today, resv)})
}
