package web

// 月間カレンダーを、実DBを通して確かめる（画面 → 保存 → Between → 表）。
// 日付の往復（DB の DATE ↔ Go の time.Time）に依存する部分は、偽の保存先では確かめられない。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
	"github.com/katsuhiro1215/StayLink/internal/room"
	"github.com/katsuhiro1215/StayLink/internal/testdb"
)

func TestRealDBCalendar(t *testing.T) {
	db := testdb.Open(t)
	rooms := room.NewStore(db)
	resv := reservation.NewStore(db)
	ctx := t.Context()

	matsu := room.Room{Name: "松の間", Capacity: 4, IsActive: true}
	if err := rooms.Create(ctx, &matsu); err != nil {
		t.Fatal(err)
	}
	mk := func(guest, in, out string) *reservation.Reservation {
		r := &reservation.Reservation{RoomID: matsu.ID, GuestName: guest, CheckIn: day(t, in), CheckOut: day(t, out), NumGuests: 2}
		if err := resv.Create(ctx, r); err != nil {
			t.Fatalf("%s: %v", guest, err)
		}
		return r
	}
	mk("佐藤", "2026-10-28", "2026-11-03") // 前の月から続く（11/1・11/2 の2泊）
	mk("山田", "2026-11-10", "2026-11-13") // 月の途中（3泊）
	mk("鈴木", "2026-11-13", "2026-11-15") // 山田さんのチェックアウト日にチェックイン（隣接）
	mk("田中", "2026-11-29", "2026-12-04") // 次の月へ続く（11/29・11/30 の2泊。12月は 12/1〜12/3 の3泊）
	cancelled := mk("取消", "2026-11-20", "2026-11-22")
	if err := resv.Cancel(ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}

	// 表の組み立てまで（実DBが返す日付で、棒の位置と長さが合うか）。
	for _, tt := range []struct {
		month string
		want  string
	}{
		{"2026-11-01", "[佐藤@1×2< 山田@10×3 鈴木@13×2 田中@29×2>]"},
		{"2026-12-01", "[田中@1×3<]"},
		{"2026-10-01", "[佐藤@28×4>]"}, // 10/28・29・30・31 の4泊。11月へ続く
	} {
		monthStart := day(t, tt.month)
		got, err := resv.Between(ctx, monthStart, monthStart.AddDate(0, 1, 0))
		if err != nil {
			t.Fatal(err)
		}
		allRooms, err := rooms.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		v := buildCalendar(monthStart, day(t, "2026-11-05"), allRooms, got)
		if len(v.Rows) != 1 {
			t.Fatalf("%s: 行数 = %d, 期待 1", tt.month, len(v.Rows))
		}
		if s := fmt.Sprint(bookingsOf(v.Rows[0])); s != tt.want {
			t.Errorf("%s: 棒 = %s, 期待 %s", tt.month, s, tt.want)
		}
		if n := spanSum(v.Rows[0]); n != v.DayCount {
			t.Errorf("%s: マスの合計 = %d, 期待 %d（月の日数）", tt.month, n, v.DayCount)
		}
	}

	// 画面（HTML）まで。
	h, err := newHandler(rooms, resv, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	rec := get(h, "/calendar?month=2026-11")
	if rec.Code != http.StatusOK {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	for _, s := range []string{
		`colspan="3"`, // 山田さんの3泊
		`aria-label="山田さん、2026年11月10日〜11月13日、3泊、2名"`,
		`aria-label="鈴木さん、2026年11月13日〜11月15日、2泊、2名"`,
		"cont-before", "cont-after", // 佐藤さん（前の月から）・田中さん（次の月へ）
	} {
		mustContain(t, rec, s)
	}
	if strings.Contains(rec.Body.String(), "取消") {
		t.Error("取り消した予約が、カレンダーに出ている")
	}
}

// DB の DATE を、タイムゾーンのある時刻として読み込んでも、棒の位置がずれない（時刻やタイムゾーンに頼らない）。
func TestBuildCalendarIgnoresTimeOfDayAndZone(t *testing.T) {
	jstMidnight := func(s string) time.Time {
		d := day(t, s)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, jst)
	}
	noonJST := func(s string) time.Time {
		d := day(t, s)
		return time.Date(d.Year(), d.Month(), d.Day(), 12, 30, 0, 0, jst)
	}

	for name, conv := range map[string]func(string) time.Time{"日本時間の0時": jstMidnight, "日本時間の昼": noonJST} {
		t.Run(name, func(t *testing.T) {
			r := res(t, 1, "山田", "2026-11-10", "2026-11-13")
			r.CheckIn, r.CheckOut = conv("2026-11-10"), conv("2026-11-13")

			v := buildCalendar(day(t, "2026-11-01"), day(t, "2026-11-05"), []room.Room{matsu}, []reservation.Reservation{r})
			if got := fmt.Sprint(bookingsOf(v.Rows[0])); got != "[山田@10×3]" {
				t.Errorf("棒 = %s, 期待 [山田@10×3]（1日ずれていないこと）", got)
			}
		})
	}
}
