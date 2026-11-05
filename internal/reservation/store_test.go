package reservation

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/katsuhiro1215/StayLink/internal/room"
	"github.com/katsuhiro1215/StayLink/internal/testdb"
)

type fixture struct {
	rooms *room.Store
	store *Store
	room  room.Room // 最初から1部屋だけ用意する
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := testdb.Open(t)
	f := fixture{rooms: room.NewStore(db), store: NewStore(db)}
	f.room = room.Room{Name: "松の間", Capacity: 4, IsActive: true}
	if err := f.rooms.Create(context.Background(), &f.room); err != nil {
		t.Fatalf("部屋の準備: %v", err)
	}
	return f
}

func (f fixture) newReservation(t *testing.T, in, out string) *Reservation {
	t.Helper()
	return &Reservation{
		RoomID: f.room.ID, GuestName: "山田 太郎", Contact: "090-0000-0000",
		CheckIn: date(t, in), CheckOut: date(t, out), NumGuests: 2, Note: "メモ",
	}
}

func TestStoreCreateAndGet(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	r := f.newReservation(t, "2026-11-05", "2026-11-07")
	if err := f.store.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r.ID != 1 || r.Status != StatusReserved {
		t.Errorf("ID = %d, Status = %q, 期待 1 と %q", r.ID, r.Status, StatusReserved)
	}

	got, err := f.store.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := *r
	want.RoomName = "松の間" // 部屋の名前は結合して取得する
	if got != want {
		t.Errorf("Get = %+v\n期待 %+v", got, want)
	}
	if !got.CheckIn.Equal(date(t, "2026-11-05")) || got.Nights() != 2 {
		t.Errorf("日付の往復が合わない: %v 〜 %v", got.CheckIn, got.CheckOut)
	}
}

func TestStoreGetNotFound(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.Get(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, 期待 ErrNotFound", err)
	}
}

func TestStoreCreateUnknownRoom(t *testing.T) {
	f := newFixture(t)
	r := f.newReservation(t, "2026-11-05", "2026-11-07")
	r.RoomID = 999

	if err := f.store.Create(context.Background(), r); !errors.Is(err, ErrRoomNotFound) {
		t.Errorf("err = %v, 期待 ErrRoomNotFound", err)
	}
}

// アプリの検証をすり抜けた不正な行は、DB の CHECK 制約が止める。
func TestStoreRejectsInvalidRowsAtDB(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// どの制約が止めたかまで確かめる（別の理由で失敗していても通ってしまうのを防ぐ）。
	tests := []struct {
		name       string
		mutate     func(r *Reservation)
		constraint string
	}{
		{"チェックアウトがチェックイン以前", func(r *Reservation) { r.CheckOut = r.CheckIn }, "reservations_dates_check"},
		{"人数0", func(r *Reservation) { r.NumGuests = 0 }, "reservations_num_guests_check"},
		{"名前が空白だけ", func(r *Reservation) { r.GuestName = "   " }, "reservations_guest_name_check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := f.newReservation(t, "2026-11-05", "2026-11-07")
			tt.mutate(r)

			err := f.store.Create(ctx, r)
			if err == nil {
				t.Fatal("保存できてしまった")
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) {
				t.Fatalf("PostgreSQL のエラーではない: %v", err)
			}
			if pgErr.Code != "23514" || pgErr.ConstraintName != tt.constraint {
				t.Errorf("コード=%s 制約=%s, 期待 23514 と %s", pgErr.Code, pgErr.ConstraintName, tt.constraint)
			}
		})
	}
}

func TestStoreUpdate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.newReservation(t, "2026-11-05", "2026-11-07")
	if err := f.store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}

	r.GuestName = "鈴木 花子"
	r.NumGuests = 3
	r.CheckOut = date(t, "2026-11-08")
	r.Note = ""
	if err := f.store.Update(ctx, r); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := f.store.Get(ctx, r.ID)
	if got.GuestName != "鈴木 花子" || got.NumGuests != 3 || got.Nights() != 3 || got.Note != "" {
		t.Errorf("更新が反映されていない: %+v", got)
	}
}

func TestStoreUpdateErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.store.Update(ctx, &Reservation{ID: 999, RoomID: f.room.ID, GuestName: "a", NumGuests: 1,
		CheckIn: date(t, "2026-11-05"), CheckOut: date(t, "2026-11-06")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("存在しない予約: err = %v, 期待 ErrNotFound", err)
	}

	r := f.newReservation(t, "2026-11-05", "2026-11-07")
	if err := f.store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Cancel(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Update(ctx, r); !errors.Is(err, ErrNotEditable) {
		t.Errorf("取消済み: err = %v, 期待 ErrNotEditable", err)
	}

	r2 := f.newReservation(t, "2026-12-01", "2026-12-02")
	if err := f.store.Create(ctx, r2); err != nil {
		t.Fatal(err)
	}
	r2.RoomID = 999
	if err := f.store.Update(ctx, r2); !errors.Is(err, ErrRoomNotFound) {
		t.Errorf("存在しない部屋: err = %v, 期待 ErrRoomNotFound", err)
	}
}

func TestStoreCancel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.newReservation(t, "2026-11-05", "2026-11-07")
	if err := f.store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}

	if err := f.store.Cancel(ctx, r.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, err := f.store.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("取消後も予約は残るはず: %v", err)
	}
	if got.Status != StatusCancelled {
		t.Errorf("Status = %q, 期待 %q", got.Status, StatusCancelled)
	}

	if err := f.store.Cancel(ctx, r.ID); !errors.Is(err, ErrNotEditable) {
		t.Errorf("2回目: err = %v, 期待 ErrNotEditable", err)
	}
	if err := f.store.Cancel(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("存在しない予約: err = %v, 期待 ErrNotFound", err)
	}
}

func TestStoreListFilterAndOrder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	mk := func(name, in, out string) *Reservation {
		r := f.newReservation(t, in, out)
		r.GuestName = name
		if err := f.store.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	mk("過去", "2026-10-01", "2026-10-03")
	mk("後", "2026-12-10", "2026-12-12")
	cancelled := mk("取消", "2026-11-20", "2026-11-22")
	mk("前", "2026-11-05", "2026-11-07")
	if err := f.store.Cancel(ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}

	names := func(flt Filter) []string {
		t.Helper()
		list, err := f.store.List(ctx, flt)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var out []string
		for _, r := range list {
			out = append(out, r.GuestName)
		}
		return out
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	tests := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"絞り込みなし（チェックイン日の順）", Filter{}, []string{"過去", "前", "取消", "後"}},
		{"予約中だけ", Filter{Status: StatusReserved}, []string{"過去", "前", "後"}},
		{"取消済みだけ", Filter{Status: StatusCancelled}, []string{"取消"}},
		{"11/05 以降にチェックアウトするもの", Filter{From: date(t, "2026-11-05")}, []string{"前", "取消", "後"}},
		{"チェックアウト当日も含む（check_out >= From。今朝発つ人は当日の一覧に出す）", Filter{From: date(t, "2026-11-07")}, []string{"前", "取消", "後"}},
		{"チェックアウトの翌日になれば含まない", Filter{From: date(t, "2026-11-08")}, []string{"取消", "後"}},
		{"状態と期間の組み合わせ", Filter{Status: StatusReserved, From: date(t, "2026-11-05")}, []string{"前", "後"}},
		{"該当なし", Filter{From: date(t, "2027-01-01")}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := names(tt.filter); !equal(got, tt.want) {
				t.Errorf("got %v, 期待 %v", got, tt.want)
			}
		})
	}
}

// 現状の限界を記録するテスト：段階3では、同じ部屋で期間が重なる予約も登録できてしまう。
// 段階4で排他制約を入れたら、このテストは「重複は拒否される」に書き換える。
func TestStoreCurrentlyAllowsOverlap(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a := f.newReservation(t, "2026-11-05", "2026-11-08")
	b := f.newReservation(t, "2026-11-06", "2026-11-09")
	for _, r := range []*Reservation{a, b} {
		if err := f.store.Create(ctx, r); err != nil {
			t.Fatalf("段階3では重複も登録できるはず: %v", err)
		}
	}
	list, _ := f.store.List(ctx, Filter{})
	if len(list) != 2 {
		t.Errorf("件数 = %d, 期待 2", len(list))
	}
}
