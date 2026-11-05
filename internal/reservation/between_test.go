package reservation

import (
	"context"
	"fmt"
	"testing"

	"github.com/katsuhiro1215/StayLink/internal/room"
)

// Between は、月間カレンダー用に「期間内に泊まりがある、取消されていない予約」を返す。
// 期間は半開区間：チェックアウト日が from と同じ予約、チェックイン日が to と同じ予約は含めない。
func TestStoreBetween(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	other := room.Room{Name: "竹の間", Capacity: 2, IsActive: true}
	if err := f.rooms.Create(ctx, &other); err != nil {
		t.Fatal(err)
	}
	third := room.Room{Name: "梅の間", Capacity: 2, IsActive: true}
	if err := f.rooms.Create(ctx, &third); err != nil {
		t.Fatal(err)
	}

	mk := func(guest string, roomID int64, in, out string) *Reservation {
		r := f.newReservation(t, in, out)
		r.GuestName, r.RoomID = guest, roomID
		if err := f.store.Create(ctx, r); err != nil {
			t.Fatalf("%s: %v", guest, err)
		}
		return r
	}
	// 調べる期間は 11/01（含む）〜 12/01（含まない）＝11月
	mk("前月から続く", f.room.ID, "2026-10-28", "2026-11-03")
	mk("月内", f.room.ID, "2026-11-10", "2026-11-12")
	mk("翌月へ続く", f.room.ID, "2026-11-29", "2026-12-03")
	mk("10/31 に退出（11/1 の夜は泊まらない）", other.ID, "2026-10-29", "2026-11-01") // 含めない
	mk("12/1 に入室（11月の夜は泊まらない）", other.ID, "2026-12-01", "2026-12-03")    // 含めない
	mk("11/30 の1泊だけ", other.ID, "2026-11-30", "2026-12-01")              // 含める（最終日の夜）
	mk("月をまたいで包む", third.ID, "2026-10-01", "2027-01-01")                 // 含める（同じ部屋の他の予約と重ならないよう、別の部屋）
	cancelled := mk("取消済み", f.room.ID, "2026-11-20", "2026-11-22")       // 含めない
	if err := f.store.Cancel(ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}

	got, err := f.store.Between(ctx, date(t, "2026-11-01"), date(t, "2026-12-01"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.GuestName)
	}
	// 部屋の順（松の間→竹の間→梅の間）、各部屋の中はチェックイン日の順。
	want := []string{"前月から続く", "月内", "翌月へ続く", "11/30 の1泊だけ", "月をまたいで包む"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("got  %v\n期待 %v", names, want)
	}
	for _, r := range got {
		if r.RoomName == "" {
			t.Errorf("%s: 部屋の名前が入っていない", r.GuestName)
		}
	}
}

func TestStoreBetweenEmpty(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.Between(context.Background(), date(t, "2026-11-01"), date(t, "2026-12-01"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("件数 = %d, 期待 0", len(got))
	}
}
