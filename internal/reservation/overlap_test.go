package reservation

// 二重予約の防止（段階4）のテスト。
// 防ぐのは、アプリの検査ではなく DB の排他制約（migrations/003）。ここではそれを実DBで確かめる。
//
// 既存の予約：松の間 11/10 〜 11/13（11/10・11/11・11/12 の3泊。11/13 の夜は泊まらない）

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/katsuhiro1215/StayLink/internal/room"
)

const (
	existingIn  = "2026-11-10"
	existingOut = "2026-11-13"
)

// newWithExisting は、既存の予約を1件登録した状態の fixture を返す。
func newWithExisting(t *testing.T) (fixture, *Reservation) {
	t.Helper()
	f := newFixture(t)
	ex := f.newReservation(t, existingIn, existingOut)
	ex.GuestName = "先客"
	if err := f.store.Create(context.Background(), ex); err != nil {
		t.Fatalf("既存の予約の登録: %v", err)
	}
	return f, ex
}

// 期間の重なり方を網羅する。境界（隣接）が、この仕組みの要点。
func TestStoreOverlapRules(t *testing.T) {
	tests := []struct {
		name        string
		in, out     string
		sameRoom    bool
		wantOverlap bool
	}{
		{"完全に同じ期間", "2026-11-10", "2026-11-13", true, true},
		{"既存の中に収まる", "2026-11-11", "2026-11-12", true, true},
		{"既存を包む", "2026-11-09", "2026-11-14", true, true},
		{"前半が重なる（手前から入って途中まで）", "2026-11-08", "2026-11-11", true, true},
		{"後半が重なる（途中から入って後まで）", "2026-11-12", "2026-11-15", true, true},
		{"1泊だけ重なる（既存の最終日の夜）", "2026-11-12", "2026-11-13", true, true},
		{"1泊だけ重なる（既存の初日の夜）", "2026-11-09", "2026-11-11", true, true},

		// 半開区間：チェックアウト日の夜は泊まらないので、同じ日に次の人が入れる。
		{"隣接：既存のチェックアウト日にチェックイン", "2026-11-13", "2026-11-15", true, false},
		{"隣接：既存のチェックイン日にチェックアウト", "2026-11-08", "2026-11-10", true, false},
		{"離れている（前）", "2026-11-01", "2026-11-05", true, false},
		{"離れている（後）", "2026-11-20", "2026-11-22", true, false},

		{"期間が同じでも、別の部屋なら重ならない", "2026-11-10", "2026-11-13", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := newWithExisting(t)
			ctx := context.Background()

			other := room.Room{Name: "竹の間", Capacity: 2, IsActive: true}
			if err := f.rooms.Create(ctx, &other); err != nil {
				t.Fatal(err)
			}

			r := f.newReservation(t, tt.in, tt.out)
			if !tt.sameRoom {
				r.RoomID = other.ID
			}
			err := f.store.Create(ctx, r)

			if tt.wantOverlap {
				if !errors.Is(err, ErrOverlap) {
					t.Errorf("err = %v, 期待 ErrOverlap", err)
				}
			} else if err != nil {
				t.Errorf("登録できるはずが失敗: %v", err)
			}
		})
	}
}

// 拒否された登録は、何も残さない。
func TestStoreOverlapLeavesNothingBehind(t *testing.T) {
	f, _ := newWithExisting(t)
	ctx := context.Background()

	if err := f.store.Create(ctx, f.newReservation(t, "2026-11-11", "2026-11-12")); !errors.Is(err, ErrOverlap) {
		t.Fatalf("err = %v, 期待 ErrOverlap", err)
	}
	list, err := f.store.List(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].GuestName != "先客" {
		t.Errorf("既存の1件だけが残るはず: %+v", list)
	}
}

// 取り消した予約は、重なりの対象から外れる。取り消せば、その期間はまた予約できる。
func TestStoreCancelledReservationDoesNotBlock(t *testing.T) {
	f, ex := newWithExisting(t)
	ctx := context.Background()

	if err := f.store.Create(ctx, f.newReservation(t, existingIn, existingOut)); !errors.Is(err, ErrOverlap) {
		t.Fatalf("取消前は拒否されるはず: %v", err)
	}
	if err := f.store.Cancel(ctx, ex.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Create(ctx, f.newReservation(t, existingIn, existingOut)); err != nil {
		t.Errorf("取消後は同じ期間を予約できるはず: %v", err)
	}
}

func TestStoreUpdateOverlap(t *testing.T) {
	f, ex := newWithExisting(t)
	ctx := context.Background()

	next := f.newReservation(t, "2026-11-13", "2026-11-15") // 既存の翌日から（隣接）
	next.GuestName = "次の客"
	if err := f.store.Create(ctx, next); err != nil {
		t.Fatalf("隣接は登録できるはず: %v", err)
	}

	t.Run("自分自身との重なりは数えない（同じ期間で内容だけ更新）", func(t *testing.T) {
		ex.NumGuests = 3
		if err := f.store.Update(ctx, ex); err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("自分の期間を縮める・元に戻す（自分とは重なるが他とは重ならない）", func(t *testing.T) {
		ex.CheckOut = date(t, "2026-11-12")
		if err := f.store.Update(ctx, ex); err != nil {
			t.Errorf("縮める: %v", err)
		}
		ex.CheckOut = date(t, existingOut)
		if err := f.store.Update(ctx, ex); err != nil {
			t.Errorf("元に戻す: %v", err)
		}
	})
	t.Run("延ばして他の予約と重なると拒否される", func(t *testing.T) {
		ex.CheckOut = date(t, "2026-11-14") // 次の客の 11/13 の夜と重なる
		if err := f.store.Update(ctx, ex); !errors.Is(err, ErrOverlap) {
			t.Errorf("err = %v, 期待 ErrOverlap", err)
		}
		ex.CheckOut = date(t, existingOut)
	})
	t.Run("拒否された更新は、元の内容を変えない", func(t *testing.T) {
		got, err := f.store.Get(ctx, ex.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.CheckOut.Equal(date(t, existingOut)) {
			t.Errorf("CheckOut = %v, 期待 %s", got.CheckOut, existingOut)
		}
	})
	t.Run("別の部屋へ移して、そこで重なると拒否される", func(t *testing.T) {
		other := room.Room{Name: "竹の間", Capacity: 4, IsActive: true}
		if err := f.rooms.Create(ctx, &other); err != nil {
			t.Fatal(err)
		}
		blocker := f.newReservation(t, "2026-11-11", "2026-11-12")
		blocker.RoomID = other.ID
		if err := f.store.Create(ctx, blocker); err != nil {
			t.Fatal(err)
		}
		moved := *ex
		moved.RoomID = other.ID
		if err := f.store.Update(ctx, &moved); !errors.Is(err, ErrOverlap) {
			t.Errorf("err = %v, 期待 ErrOverlap", err)
		}
	})
}

// 同じ部屋・同じ期間を同時に登録しても、成功は必ず1件だけ。
// アプリで「空いているか調べてから登録」していたら、ここで複数が通ってしまう（競合）。
// DB の排他制約なら、同時でも必ず一方だけが成功する。
func TestStoreConcurrentSamePeriod(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const workers = 8
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.newReservation(t, "2026-12-01", "2026-12-04")
			r.GuestName = fmt.Sprintf("客%d", i)
			<-start // 全員そろってから一斉に登録する
			errs[i] = f.store.Create(ctx, r)
		}()
	}
	close(start)
	wg.Wait()

	var ok, overlap int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrOverlap):
			overlap++
		default:
			t.Errorf("想定外のエラー: %v", err)
		}
	}
	if ok != 1 || overlap != workers-1 {
		t.Errorf("成功 %d 件・重複 %d 件, 期待 成功1・重複%d", ok, overlap, workers-1)
	}
	if list, _ := f.store.List(ctx, Filter{}); len(list) != 1 {
		t.Errorf("保存された件数 = %d, 期待 1", len(list))
	}
}

// 期間が少しずつずれた予約を同時に登録しても、保存された予約同士は決して重ならない。
func TestStoreConcurrentShiftedPeriodsNeverOverlap(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const workers = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := date(t, "2026-12-01").AddDate(0, 0, i) // 1日ずつずらして、3泊
			r := f.newReservation(t, "2026-12-01", "2026-12-02")
			r.CheckIn, r.CheckOut = in, in.AddDate(0, 0, 3)
			<-start
			if err := f.store.Create(ctx, r); err != nil && !errors.Is(err, ErrOverlap) {
				t.Errorf("想定外のエラー: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	list, err := f.store.List(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("1件も保存されていない")
	}
	for i := range list {
		for j := i + 1; j < len(list); j++ {
			a, b := list[i], list[j]
			if a.CheckIn.Before(b.CheckOut) && b.CheckIn.Before(a.CheckOut) {
				t.Errorf("保存された予約が重なっている: %s〜%s と %s〜%s",
					a.CheckIn.Format(DateLayout), a.CheckOut.Format(DateLayout),
					b.CheckIn.Format(DateLayout), b.CheckOut.Format(DateLayout))
			}
		}
	}
}

func TestStoreOverlapping(t *testing.T) {
	f, ex := newWithExisting(t)
	ctx := context.Background()

	cancelled := f.newReservation(t, "2026-11-20", "2026-11-22")
	cancelled.GuestName = "取消の客"
	if err := f.store.Create(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Cancel(ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		roomID    int64
		in, out   string
		excludeID int64
		want      []string
	}{
		{"重なる予約を返す", f.room.ID, "2026-11-12", "2026-11-14", 0, []string{"先客"}},
		{"隣接は返さない", f.room.ID, "2026-11-13", "2026-11-15", 0, nil},
		{"自分自身は除く", f.room.ID, "2026-11-12", "2026-11-14", ex.ID, nil},
		{"別の部屋は返さない", f.room.ID + 99, "2026-11-12", "2026-11-14", 0, nil},
		{"取消済みは返さない", f.room.ID, "2026-11-20", "2026-11-22", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := f.store.Overlapping(ctx, tt.roomID, date(t, tt.in), date(t, tt.out), tt.excludeID)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, r := range got {
				names = append(names, r.GuestName)
			}
			if fmt.Sprint(names) != fmt.Sprint(tt.want) {
				t.Errorf("got %v, 期待 %v", names, tt.want)
			}
		})
	}
}
