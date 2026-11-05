package web

import (
	"strings"
	"testing"
	"time"
)

// section は、id の見出し（`id="arrivals-title"` など）から、その節の終わり（</section>）までの本文を返す。
// 「どの一覧に入ったか」を確かめるために使う。
func section(t *testing.T, body, id string) string {
	t.Helper()
	start := strings.Index(body, `id="`+id+`-title"`)
	if start < 0 {
		t.Fatalf("節 %s が見つからない", id)
	}
	end := strings.Index(body[start:], "</section>")
	if end < 0 {
		t.Fatalf("節 %s の終わりが見つからない", id)
	}
	return body[start : start+end]
}

// どの一覧に入ったか（入れ替わりの2人が、逆の一覧に入らないこと）。
func TestHomeGuestsAreInTheRightSection(t *testing.T) {
	e := newEnv(t)
	add := func(roomID int64, roomName, guest, in, out string) {
		r := sample(t)
		r.RoomID, r.RoomName, r.GuestName, r.CheckIn, r.CheckOut = roomID, roomName, guest, day(t, in), day(t, out)
		e.seed(t, r)
	}
	add(1, "松の間", "前の人", "2026-11-03", "2026-11-05")
	add(1, "松の間", "次の人", "2026-11-05", "2026-11-07")
	add(2, "竹の間", "滞在さん", "2026-11-04", "2026-11-08")

	body := get(e.h, "/").Body.String()
	for id, want := range map[string]string{"arrivals": "次の人", "departures": "前の人", "staying": "滞在さん"} {
		s := section(t, body, id)
		if !strings.Contains(s, want) {
			t.Errorf("%s の一覧に %q がない", id, want)
		}
		for _, other := range []string{"前の人", "次の人", "滞在さん"} {
			if other != want && strings.Contains(s, other) {
				t.Errorf("%s の一覧に、別の一覧の %q が入っている", id, other)
			}
		}
	}
	// 泊数も出る（前の人は2泊、次の人は2泊、滞在さんは4泊）。
	if s := section(t, body, "staying"); !strings.Contains(s, "2026-11-08（日）、4泊") {
		t.Errorf("滞在中の日付・泊数の表示 = %s", s)
	}
}

// 停止中の部屋の予約も、今日の一覧に出る（停止後も残っている予約を見落とさない）。
func TestHomeShowsReservationsOfInactiveRoom(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.RoomID, r.RoomName, r.GuestName = 3, "梅の間", "停止中の部屋の人"
	r.CheckIn, r.CheckOut = day(t, "2026-11-05"), day(t, "2026-11-06")
	e.seed(t, r)

	mustContain(t, get(e.h, "/"), "停止中の部屋の人")
}

// 日本時間の早朝（0:00〜8:59。UTC ではまだ前日）でも、今日は日本の日付になる。
func TestHomeUsesJapanDateInTheEarlyMorning(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.GuestName, r.CheckIn, r.CheckOut = "朝の到着", day(t, "2026-11-05"), day(t, "2026-11-06")
	e.seed(t, r)

	// 2026-11-05 00:30 JST ＝ 2026-11-04 15:30 UTC
	h, err := newHandler(e.rms, e.resv, func() time.Time { return time.Date(2026, 11, 5, 0, 30, 0, 0, jst) })
	if err != nil {
		t.Fatal(err)
	}
	rec := get(h, "/")
	mustContain(t, rec, "2026年11月5日（木）")
	if s := section(t, rec.Body.String(), "arrivals"); !strings.Contains(s, "朝の到着") {
		t.Error("日本時間の早朝に、今日の到着が出ていない")
	}
}
