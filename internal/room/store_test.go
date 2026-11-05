package room

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql 用の PostgreSQL ドライバ
)

// newTestStore は実際の PostgreSQL につなぎ、トランザクション内の Store を返す。
// テストが終わると取り消す（ロールバック）ので、データは残らない。
// TEST_DATABASE_URL が未設定なら、DB が要るテストは飛ばす。
// 事前に docker compose up -d --wait と scripts/migrate.sh を実行しておくこと。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL が未設定のため、DBのテストを飛ばします")
	}

	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx（DBが起動していますか）: %v", err)
	}
	t.Cleanup(func() { tx.Rollback() })

	return NewStore(tx)
}

func TestStoreCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := &Room{Name: "テスト松の間", Capacity: 4, SortOrder: 10, IsActive: true}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r.ID == 0 {
		t.Fatal("ID が採番されていない")
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

	if err := s.Create(ctx, &Room{Name: "テスト竹の間", Capacity: 2, IsActive: true}); err != nil {
		t.Fatalf("1件目の Create: %v", err)
	}
	// 同名は、無効な部屋でも重複として扱う。
	err := s.Create(ctx, &Room{Name: "テスト竹の間", Capacity: 3, IsActive: false})
	if !errors.Is(err, ErrDuplicateName) {
		t.Errorf("err = %v, 期待 ErrDuplicateName", err)
	}
}

func TestStoreUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := &Room{Name: "テスト梅の間", Capacity: 2, SortOrder: 5, IsActive: true}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	r.Name = "テスト梅の間（改）"
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

	a := &Room{Name: "テストA", Capacity: 2, IsActive: true}
	b := &Room{Name: "テストB", Capacity: 2, IsActive: true}
	for _, r := range []*Room{a, b} {
		if err := s.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	b.Name = "テストA"
	if err := s.Update(ctx, b); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("err = %v, 期待 ErrDuplicateName", err)
	}
}

func TestStoreListOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// sort_order の昇順、同じなら id の昇順で返る。
	for _, r := range []*Room{
		{Name: "テスト順3", Capacity: 1, SortOrder: 3, IsActive: true},
		{Name: "テスト順1", Capacity: 1, SortOrder: 1, IsActive: true},
		{Name: "テスト順2a", Capacity: 1, SortOrder: 2, IsActive: false},
		{Name: "テスト順2b", Capacity: 1, SortOrder: 2, IsActive: true},
	} {
		if err := s.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	rooms, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// 開発用DBに既存の部屋があっても動くよう、このテストで作った分だけを抜き出して比べる。
	var names []string
	for _, r := range rooms {
		if len(r.Name) >= len("テスト順") && r.Name[:len("テスト順")] == "テスト順" {
			names = append(names, r.Name)
		}
	}
	want := []string{"テスト順1", "テスト順2a", "テスト順2b", "テスト順3"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, 期待 %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, 期待 %q", i, names[i], want[i])
		}
	}
}
