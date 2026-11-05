package room

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/katsuhiro1215/StayLink/internal/testdb"
)

// newTestStore はテスト専用DB（実際にコミットする。前後で空にする）につないだ Store を返す。
// DB が未設定なら飛ばす。準備は README の「テスト」を参照。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(testdb.Open(t))
}

func TestStoreCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := &Room{Name: "松の間", Capacity: 4, SortOrder: 10, IsActive: true}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r.ID != 1 {
		t.Fatalf("ID = %d, 期待 1（テストの前にテーブルを空にして採番も戻している）", r.ID)
	}

	got, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != *r {
		t.Errorf("Get = %+v, 期待 %+v", got, *r)
	}
}

func TestStoreGetNotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Get(context.Background(), 999999999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, 期待 ErrNotFound", err)
	}
}

func TestStoreCreateDuplicateName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Create(ctx, &Room{Name: "竹の間", Capacity: 2, IsActive: true}); err != nil {
		t.Fatalf("1件目の Create: %v", err)
	}
	// 同名は、無効な部屋でも重複として扱う。
	err := s.Create(ctx, &Room{Name: "竹の間", Capacity: 3, IsActive: false})
	if !errors.Is(err, ErrDuplicateName) {
		t.Errorf("err = %v, 期待 ErrDuplicateName", err)
	}

	// コミットされるテストなので、違反の後も同じ接続で続けて操作できる。
	rooms, err := s.List(ctx)
	if err != nil {
		t.Fatalf("違反の後の List: %v", err)
	}
	if len(rooms) != 1 {
		t.Errorf("部屋数 = %d, 期待 1（重複した2件目は保存されない）", len(rooms))
	}
}

// 同じ名前を同時に登録しても、DBの一意索引によって必ず1件だけが成功する。
// アプリ側で「既にあるか調べてから登録」していたら、ここで両方通ってしまう。
func TestStoreCreateDuplicateNameConcurrently(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const workers = 8
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 全員そろってから一斉に登録する
			errs[i] = s.Create(ctx, &Room{Name: "同時登録の間", Capacity: 2, IsActive: true})
		}()
	}
	close(start)
	wg.Wait()

	var ok, dup int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrDuplicateName):
			dup++
		default:
			t.Errorf("想定外のエラー: %v", err)
		}
	}
	if ok != 1 || dup != workers-1 {
		t.Errorf("成功 %d 件・重複 %d 件, 期待 成功1・重複%d", ok, dup, workers-1)
	}
}

func TestStoreUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := &Room{Name: "梅の間", Capacity: 2, SortOrder: 5, IsActive: true}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	r.Name = "梅の間（改）"
	r.Capacity = 6
	r.IsActive = false // 削除ではなく無効化
	if err := s.Update(ctx, r); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != *r {
		t.Errorf("Get = %+v, 期待 %+v", got, *r)
	}
}

func TestStoreUpdateNotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.Update(context.Background(), &Room{ID: 999999999, Name: "なし", Capacity: 1})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, 期待 ErrNotFound", err)
	}
}

func TestStoreUpdateDuplicateName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := &Room{Name: "A", Capacity: 2, IsActive: true}
	b := &Room{Name: "B", Capacity: 2, IsActive: true}
	for _, r := range []*Room{a, b} {
		if err := s.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	b.Name = "A"
	if err := s.Update(ctx, b); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("err = %v, 期待 ErrDuplicateName", err)
	}
}

func TestStoreListOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// sort_order の昇順、同じなら id の昇順で返る。
	for _, r := range []*Room{
		{Name: "順3", Capacity: 1, SortOrder: 3, IsActive: true},
		{Name: "順1", Capacity: 1, SortOrder: 1, IsActive: true},
		{Name: "順2a", Capacity: 1, SortOrder: 2, IsActive: false},
		{Name: "順2b", Capacity: 1, SortOrder: 2, IsActive: true},
	} {
		if err := s.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	rooms, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"順1", "順2a", "順2b", "順3"}
	if len(rooms) != len(want) {
		t.Fatalf("件数 = %d, 期待 %d", len(rooms), len(want))
	}
	for i := range want {
		if rooms[i].Name != want[i] {
			t.Errorf("rooms[%d] = %q, 期待 %q", i, rooms[i].Name, want[i])
		}
	}
}

// DBの CHECK 制約は、アプリの検証をすり抜けた不正な値を最後に止める。
func TestStoreRejectsInvalidRowsAtDB(t *testing.T) {
	s := newTestStore(t)

	for _, r := range []*Room{
		{Name: "定員0", Capacity: 0},
		{Name: "   ", Capacity: 2}, // 空白だけの名前
	} {
		if err := s.Create(context.Background(), r); err == nil {
			t.Errorf("%+v が保存できてしまった", *r)
		} else if errors.Is(err, ErrDuplicateName) {
			t.Errorf("%+v: CHECK 違反が重複エラーに化けている", *r)
		}
	}
}
