package web

// トップの「今日の到着・出発」を、実DBを通して確かめる（保存 → Between → 分ける → 画面）。

import (
	"net/http"
	"strings"
	"testing"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
	"github.com/katsuhiro1215/StayLink/internal/room"
	"github.com/katsuhiro1215/StayLink/internal/testdb"
)

func TestRealDBHome(t *testing.T) {
	db := testdb.Open(t)
	rooms := room.NewStore(db)
	resv := reservation.NewStore(db)
	ctx := t.Context()

	matsu := room.Room{Name: "松の間", Capacity: 4, IsActive: true}
	take := room.Room{Name: "竹の間", Capacity: 2, IsActive: true}
	for _, r := range []*room.Room{&matsu, &take} {
		if err := rooms.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(rm room.Room, guest, in, out string) *reservation.Reservation {
		r := &reservation.Reservation{RoomID: rm.ID, GuestName: guest, CheckIn: day(t, in), CheckOut: day(t, out), NumGuests: 2}
		if err := resv.Create(ctx, r); err != nil {
			t.Fatalf("%s: %v", guest, err)
		}
		return r
	}
	// 今日は 2026-11-05。
	mk(matsu, "前の人", "2026-11-03", "2026-11-05") // 今日出発
	mk(matsu, "次の人", "2026-11-05", "2026-11-07") // 同じ日に到着（入れ替わり）
	mk(take, "滞在さん", "2026-11-04", "2026-11-08")
	mk(take, "昨日出発さん", "2026-10-30", "2026-11-04") // 出ない
	mk(take, "明日到着さん", "2026-11-08", "2026-11-09") // 出ない
	cancelled := mk(matsu, "取消さん", "2026-11-09", "2026-11-10")
	if err := resv.Cancel(ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	// 今日（11/5）に到着する取消済みも出ない
	c2 := mk(take, "取消到着さん", "2026-11-09", "2026-11-11")
	if err := resv.Cancel(ctx, c2.ID); err != nil {
		t.Fatal(err)
	}

	h, err := newHandler(rooms, resv, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	rec := get(h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	mustContain(t, rec, "到着 1件、出発 1件、滞在中 1件")
	// どの一覧に入ったか（入れ替わりの2人が、逆の一覧に入らないこと）。
	body := rec.Body.String()
	for id, want := range map[string]string{"arrivals": "次の人", "departures": "前の人", "staying": "滞在さん"} {
		if s := section(t, body, id); !strings.Contains(s, want) {
			t.Errorf("%s の一覧に %q がない", id, want)
		}
	}
	if s := section(t, body, "arrivals"); strings.Contains(s, "前の人") {
		t.Error("到着の一覧に、出発する人が入っている")
	}
	for _, s := range []string{"昨日出発さん", "明日到着さん", "取消さん", "取消到着さん"} {
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("今日の一覧に %q が出ている", s)
		}
	}
}
