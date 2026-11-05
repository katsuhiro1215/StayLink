package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
)

func guestsOf(list []reservation.Reservation) string {
	var names []string
	for _, r := range list {
		names = append(names, r.GuestName)
	}
	return fmt.Sprint(names)
}

// 今日（2026-11-05）を基準に、到着・出発・滞在中へ分ける。境界を網羅する。
func TestBuildToday(t *testing.T) {
	today := day(t, "2026-11-05")
	mk := func(id int64, guest, room, in, out string) reservation.Reservation {
		r := res(t, id, guest, in, out)
		r.RoomName = room
		return r
	}

	tests := []struct {
		name                       string
		resv                       []reservation.Reservation
		arrivals, departures, stay string
	}{
		{"何もない", nil, "[]", "[]", "[]"},
		{"今日到着（1泊）", []reservation.Reservation{mk(1, "到着", "松の間", "2026-11-05", "2026-11-06")}, "[到着]", "[]", "[]"},
		{"今日出発", []reservation.Reservation{mk(1, "出発", "松の間", "2026-11-03", "2026-11-05")}, "[]", "[出発]", "[]"},
		{"滞在中（昨日到着・明日出発）", []reservation.Reservation{mk(1, "滞在", "松の間", "2026-11-04", "2026-11-06")}, "[]", "[]", "[滞在]"},
		{"昨日に出発済み", []reservation.Reservation{mk(1, "昨日", "松の間", "2026-11-03", "2026-11-04")}, "[]", "[]", "[]"},
		{"明日に到着", []reservation.Reservation{mk(1, "明日", "松の間", "2026-11-06", "2026-11-08")}, "[]", "[]", "[]"},
		{"同じ日の入れ替わり（同じ部屋）", []reservation.Reservation{
			mk(1, "前の人", "松の間", "2026-11-03", "2026-11-05"),
			mk(2, "次の人", "松の間", "2026-11-05", "2026-11-07"),
		}, "[次の人]", "[前の人]", "[]"},
		{"部屋名・IDの順に並ぶ", []reservation.Reservation{
			mk(3, "竹B", "竹の間", "2026-11-05", "2026-11-06"),
			mk(2, "松B", "松の間", "2026-11-05", "2026-11-06"),
			mk(1, "松A", "松の間", "2026-11-05", "2026-11-06"),
		}, "[松A 松B 竹B]", "[]", "[]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := buildToday(today, tt.resv)
			if got := guestsOf(v.Arrivals); got != tt.arrivals {
				t.Errorf("到着 = %s, 期待 %s", got, tt.arrivals)
			}
			if got := guestsOf(v.Departures); got != tt.departures {
				t.Errorf("出発 = %s, 期待 %s", got, tt.departures)
			}
			if got := guestsOf(v.Staying); got != tt.stay {
				t.Errorf("滞在中 = %s, 期待 %s", got, tt.stay)
			}
		})
	}
}

func TestBuildTodayLabel(t *testing.T) {
	if got := buildToday(day(t, "2026-11-05"), nil).Label; got != "2026年11月5日（木）" {
		t.Errorf("Label = %q", got)
	}
	if got := buildToday(day(t, "2026-12-31"), nil).Label; got != "2026年12月31日（木）" {
		t.Errorf("Label = %q", got)
	}
}

// 日本時間の0時・昼の値が混ざっても、同じ日として分ける（日付だけを比べる）。
func TestBuildTodayIgnoresTimeOfDayAndZone(t *testing.T) {
	today := time.Date(2026, 11, 5, 15, 30, 0, 0, jst) // 時刻つきの「今日」でもよい
	in := time.Date(2026, 11, 5, 0, 0, 0, 0, jst)
	out := time.Date(2026, 11, 6, 12, 30, 0, 0, jst)
	r := res(t, 1, "到着", "2026-11-05", "2026-11-06")
	r.CheckIn, r.CheckOut = in, out

	v := buildToday(today, []reservation.Reservation{r})
	if got := guestsOf(v.Arrivals); got != "[到着]" {
		t.Errorf("到着 = %s, 期待 [到着]", got)
	}
}

func TestHomeShowsTodaysArrivalsAndDepartures(t *testing.T) {
	e := newEnv(t) // 今日は 2026-11-05（木）
	add := func(guest, in, out string) {
		r := sample(t)
		r.GuestName, r.CheckIn, r.CheckOut = guest, day(t, in), day(t, out)
		e.seed(t, r)
	}
	add("到着さん", "2026-11-05", "2026-11-07")
	add("出発さん", "2026-11-03", "2026-11-05")
	// 同じ部屋で重ならないよう、別の部屋に置く
	r := sample(t)
	r.RoomID, r.RoomName = 2, "竹の間"
	r.GuestName, r.CheckIn, r.CheckOut = "滞在さん", day(t, "2026-11-04"), day(t, "2026-11-08")
	e.seed(t, r)
	r2 := sample(t)
	r2.RoomID, r2.RoomName = 2, "竹の間"
	r2.GuestName, r2.CheckIn, r2.CheckOut = "取消さん", day(t, "2026-11-05"), day(t, "2026-11-06")
	r2.Status = reservation.StatusCancelled
	e.seed(t, r2)

	rec := get(e.h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	for _, s := range []string{
		"2026年11月5日（木）", "到着 1件、出発 1件、滞在中 1件",
		"今日の到着（1件）", "今日の出発（1件）", "滞在中（1件）",
		"到着さん", "出発さん", "滞在さん",
	} {
		mustContain(t, rec, s)
	}
	if strings.Contains(rec.Body.String(), "取消さん") {
		t.Error("取消済みの予約が、今日の一覧に出ている")
	}
	// 昨日の夜〜今夜に泊まりがある予約を探している（昨日〜明日）
	if e.resv.lastBetween[0] != day(t, "2026-11-04") || e.resv.lastBetween[1] != day(t, "2026-11-06") {
		t.Errorf("検索の期間 = %v", e.resv.lastBetween)
	}
}

func TestHomeWithoutReservations(t *testing.T) {
	rec := get(newEnv(t).h, "/")
	for _, s := range []string{"今日到着する予約はありません。", "今日出発する予約はありません。", "今夜、泊まっている予約はありません。", "到着 0件、出発 0件、滞在中 0件"} {
		mustContain(t, rec, s)
	}
}

func TestHomeEscapesGuestName(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.GuestName, r.CheckIn, r.CheckOut = `<script>alert(1)</script>`, day(t, "2026-11-05"), day(t, "2026-11-06")
	e.seed(t, r)

	if strings.Contains(get(e.h, "/").Body.String(), "<script>alert(1)") {
		t.Error("名前がエスケープされていない")
	}
}

func TestHomeStoreError(t *testing.T) {
	e := newEnv(t)
	e.resv.err = errBoom
	rec := get(e.h, "/")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("ステータス = %d（500 で、内部のエラー内容を出さないこと）", rec.Code)
	}
}
