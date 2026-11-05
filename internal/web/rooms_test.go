package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/katsuhiro1215/StayLink/internal/room"
)

// fakeStore はメモリ上の RoomStore。DB なしで画面の動きを確かめるために使う。
type fakeStore struct {
	rooms  []room.Room
	nextID int64
	err    error // 設定すると、全操作がこのエラーで失敗する
}

func (f *fakeStore) List(context.Context) ([]room.Room, error) { return f.rooms, f.err }

func (f *fakeStore) Get(_ context.Context, id int64) (room.Room, error) {
	if f.err != nil {
		return room.Room{}, f.err
	}
	for _, r := range f.rooms {
		if r.ID == id {
			return r, nil
		}
	}
	return room.Room{}, room.ErrNotFound
}

func (f *fakeStore) dup(name string, exceptID int64) bool {
	for _, r := range f.rooms {
		if r.Name == name && r.ID != exceptID {
			return true
		}
	}
	return false
}

func (f *fakeStore) Create(_ context.Context, r *room.Room) error {
	if f.err != nil {
		return f.err
	}
	if f.dup(r.Name, 0) {
		return room.ErrDuplicateName
	}
	f.nextID++
	r.ID = f.nextID
	f.rooms = append(f.rooms, *r)
	return nil
}

func (f *fakeStore) Update(_ context.Context, r *room.Room) error {
	if f.err != nil {
		return f.err
	}
	if f.dup(r.Name, r.ID) {
		return room.ErrDuplicateName
	}
	for i := range f.rooms {
		if f.rooms[i].ID == r.ID {
			f.rooms[i] = *r
			return nil
		}
	}
	return room.ErrNotFound
}

func newRoomsHandler(t *testing.T, f *fakeStore) http.Handler {
	t.Helper()
	h, err := NewHandler(f)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func post(h http.Handler, path string, form url.Values, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func mustContain(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("本文に %q が含まれない", want)
	}
}

func TestRoomListShowsRooms(t *testing.T) {
	f := &fakeStore{rooms: []room.Room{
		{ID: 1, Name: "松の間", Capacity: 4, IsActive: true},
		{ID: 2, Name: "竹の間", Capacity: 2, IsActive: false},
	}}
	rec := get(newRoomsHandler(t, f), "/rooms")

	if rec.Code != 200 {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	for _, want := range []string{"松の間", "竹の間", "使用中", "停止中", `href="/rooms/1/edit"`, "4名"} {
		mustContain(t, rec, want)
	}
}

func TestRoomListEmpty(t *testing.T) {
	rec := get(newRoomsHandler(t, &fakeStore{}), "/rooms")
	mustContain(t, rec, "まだ部屋がありません")
}

func TestRoomListEscapesNames(t *testing.T) {
	f := &fakeStore{rooms: []room.Room{{ID: 1, Name: `<script>alert(1)</script>`, Capacity: 1, IsActive: true}}}
	rec := get(newRoomsHandler(t, f), "/rooms")

	if strings.Contains(rec.Body.String(), "<script>alert(1)") {
		t.Error("部屋名がエスケープされていない")
	}
}

func TestRoomListSavedMessage(t *testing.T) {
	h := newRoomsHandler(t, &fakeStore{})

	mustContain(t, get(h, "/rooms?saved=created"), "部屋を登録しました。")
	mustContain(t, get(h, "/rooms?saved=updated"), "部屋を更新しました。")

	// 想定外の値は、そのまま表示せず無視する。
	rec := get(h, "/rooms?saved=<b>x</b>")
	if strings.Contains(rec.Body.String(), "<b>x</b>") || strings.Contains(rec.Body.String(), "notice-ok") {
		t.Error("想定外の saved の値が表示された")
	}
}

func TestRoomListStoreError(t *testing.T) {
	rec := get(newRoomsHandler(t, &fakeStore{err: errors.New("db down")}), "/rooms")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("ステータス = %d, 期待 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "db down") {
		t.Error("内部のエラー内容を画面に出している")
	}
}

func TestRoomNewForm(t *testing.T) {
	rec := get(newRoomsHandler(t, &fakeStore{}), "/rooms/new")
	if rec.Code != 200 {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	mustContain(t, rec, `action="/rooms"`)
	mustContain(t, rec, "登録する")
	if strings.Contains(rec.Body.String(), `name="is_active"`) {
		t.Error("新規登録に「使用中」のチェックがある（登録時は常に使用中のはず）")
	}
}

func TestRoomCreate(t *testing.T) {
	f := &fakeStore{}
	rec := post(newRoomsHandler(t, f), "/rooms", url.Values{
		"name": {"  桜の間 "}, "capacity": {"3"}, "sort_order": {"7"},
	})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("ステータス = %d, 期待 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/rooms?saved=created" {
		t.Errorf("Location = %q", loc)
	}
	if len(f.rooms) != 1 {
		t.Fatalf("保存件数 = %d, 期待 1", len(f.rooms))
	}
	want := room.Room{ID: 1, Name: "桜の間", Capacity: 3, SortOrder: 7, IsActive: true}
	if f.rooms[0] != want {
		t.Errorf("保存内容 = %+v, 期待 %+v", f.rooms[0], want)
	}
}

func TestRoomCreateInvalid(t *testing.T) {
	tests := []struct {
		name     string
		form     url.Values
		wantText string
	}{
		{"名前が空", url.Values{"name": {""}, "capacity": {"2"}}, "部屋の名前を入力してください。"},
		{"定員が数字でない", url.Values{"name": {"梅の間"}, "capacity": {"abc"}}, "定員は1〜100の数字で入力してください。"},
		{"定員が0", url.Values{"name": {"梅の間"}, "capacity": {"0"}}, "定員は1〜100の数字で入力してください。"},
		{"表示順が負", url.Values{"name": {"梅の間"}, "capacity": {"2"}, "sort_order": {"-1"}}, "表示順は0〜9999の数字で入力してください。"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			rec := post(newRoomsHandler(t, f), "/rooms", tt.form)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("ステータス = %d, 期待 422", rec.Code)
			}
			mustContain(t, rec, tt.wantText)
			if len(f.rooms) != 0 {
				t.Error("誤った入力が保存された")
			}
		})
	}
}

func TestRoomCreateKeepsInputOnError(t *testing.T) {
	rec := post(newRoomsHandler(t, &fakeStore{}), "/rooms", url.Values{
		"name": {"入力した名前"}, "capacity": {"abc"},
	})
	mustContain(t, rec, `value="入力した名前"`)
	mustContain(t, rec, `value="abc"`)
}

func TestRoomCreateDuplicateName(t *testing.T) {
	f := &fakeStore{rooms: []room.Room{{ID: 1, Name: "松の間", Capacity: 4, IsActive: true}}, nextID: 1}
	rec := post(newRoomsHandler(t, f), "/rooms", url.Values{"name": {"松の間"}, "capacity": {"2"}})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("ステータス = %d, 期待 422", rec.Code)
	}
	mustContain(t, rec, "同じ名前の部屋がすでにあります。")
}

func TestRoomCreateStoreError(t *testing.T) {
	rec := post(newRoomsHandler(t, &fakeStore{err: errors.New("db down")}), "/rooms",
		url.Values{"name": {"松の間"}, "capacity": {"2"}})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("ステータス = %d, 期待 500", rec.Code)
	}
}

func TestRoomEditForm(t *testing.T) {
	f := &fakeStore{rooms: []room.Room{{ID: 5, Name: "松の間", Capacity: 4, SortOrder: 2, IsActive: true}}}
	rec := get(newRoomsHandler(t, f), "/rooms/5/edit")

	if rec.Code != 200 {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	for _, want := range []string{`action="/rooms/5"`, `value="松の間"`, `value="4"`, `value="2"`, "更新する", `name="is_active"`, " checked"} {
		mustContain(t, rec, want)
	}
}

func TestRoomEditNotFound(t *testing.T) {
	h := newRoomsHandler(t, &fakeStore{})
	for _, path := range []string{"/rooms/99/edit", "/rooms/abc/edit", "/rooms/0/edit", "/rooms/-1/edit"} {
		if rec := get(h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: ステータス = %d, 期待 404", path, rec.Code)
		}
	}
}

func TestRoomUpdate(t *testing.T) {
	f := &fakeStore{rooms: []room.Room{{ID: 1, Name: "松の間", Capacity: 4, IsActive: true}}, nextID: 1}
	rec := post(newRoomsHandler(t, f), "/rooms/1", url.Values{
		"name": {"松の間（改）"}, "capacity": {"5"}, "sort_order": {"3"}, "is_active": {"on"},
	})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("ステータス = %d, 期待 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/rooms?saved=updated" {
		t.Errorf("Location = %q", loc)
	}
	want := room.Room{ID: 1, Name: "松の間（改）", Capacity: 5, SortOrder: 3, IsActive: true}
	if f.rooms[0] != want {
		t.Errorf("保存内容 = %+v, 期待 %+v", f.rooms[0], want)
	}
}

// チェックボックスは、外すと項目ごと送られてこない。その場合は「停止中」にする。
func TestRoomUpdateDeactivate(t *testing.T) {
	f := &fakeStore{rooms: []room.Room{{ID: 1, Name: "松の間", Capacity: 4, IsActive: true}}, nextID: 1}
	post(newRoomsHandler(t, f), "/rooms/1", url.Values{"name": {"松の間"}, "capacity": {"4"}})

	if f.rooms[0].IsActive {
		t.Error("is_active を送らなかったのに、有効のまま")
	}
}

func TestRoomUpdateInvalidAndNotFound(t *testing.T) {
	f := &fakeStore{rooms: []room.Room{
		{ID: 1, Name: "松の間", Capacity: 4, IsActive: true},
		{ID: 2, Name: "竹の間", Capacity: 2, IsActive: true},
	}, nextID: 2}
	h := newRoomsHandler(t, f)

	rec := post(h, "/rooms/1", url.Values{"name": {""}, "capacity": {"4"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("入力誤り: ステータス = %d, 期待 422", rec.Code)
	}

	rec = post(h, "/rooms/1", url.Values{"name": {"竹の間"}, "capacity": {"4"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("同名: ステータス = %d, 期待 422", rec.Code)
	}
	mustContain(t, rec, "同じ名前の部屋がすでにあります。")

	rec = post(h, "/rooms/99", url.Values{"name": {"新"}, "capacity": {"1"}})
	if rec.Code != http.StatusNotFound {
		t.Errorf("存在しない部屋: ステータス = %d, 期待 404", rec.Code)
	}
}

// 他のサイトからのフォーム送信（CSRF）は、保存せずに拒否する。
func TestCrossSitePostIsRejected(t *testing.T) {
	f := &fakeStore{}
	h := newRoomsHandler(t, f)
	form := url.Values{"name": {"松の間"}, "capacity": {"2"}}

	rec := post(h, "/rooms", form, "Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: ステータス = %d, 期待 403", rec.Code)
	}
	if len(f.rooms) != 0 {
		t.Error("cross-site の POST が保存された")
	}

	// 同じサイトからなら通る。
	rec = post(h, "/rooms", form, "Sec-Fetch-Site", "same-origin")
	if rec.Code != http.StatusSeeOther {
		t.Errorf("same-origin: ステータス = %d, 期待 303", rec.Code)
	}
}
