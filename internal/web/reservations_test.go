package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
	"github.com/katsuhiro1215/StayLink/internal/room"
)

// fakeReservations はメモリ上の ReservationStore。
type fakeReservations struct {
	items       []reservation.Reservation
	nextID      int64
	err         error
	overlapErr  error              // 設定すると、Overlapping（案内用の検索）だけが失敗する
	writeErr    error              // 設定すると、Create / Update だけがこのエラーで失敗する
	lastFilter  reservation.Filter // 直近の List に渡された条件
	lastBetween [2]time.Time       // 直近の Between に渡された期間（from, to）
}

func (f *fakeReservations) List(_ context.Context, flt reservation.Filter) ([]reservation.Reservation, error) {
	f.lastFilter = flt
	return f.items, f.err
}

func (f *fakeReservations) index(id int64) int {
	for i := range f.items {
		if f.items[i].ID == id {
			return i
		}
	}
	return -1
}

func (f *fakeReservations) Get(_ context.Context, id int64) (reservation.Reservation, error) {
	if f.err != nil {
		return reservation.Reservation{}, f.err
	}
	if i := f.index(id); i >= 0 {
		return f.items[i], nil
	}
	return reservation.Reservation{}, reservation.ErrNotFound
}

// overlapping は、同じ部屋で期間が重なる「予約中」の予約を返す（半開区間。excludeID は除く）。
// 実物は DB の排他制約と Overlapping の SQL が行う。ここは同じ意味の簡易版。
func (f *fakeReservations) overlapping(roomID int64, in, out time.Time, excludeID int64) []reservation.Reservation {
	var hit []reservation.Reservation
	for _, it := range f.items {
		if it.RoomID == roomID && it.Status == reservation.StatusReserved && it.ID != excludeID &&
			it.CheckIn.Before(out) && in.Before(it.CheckOut) {
			hit = append(hit, it)
		}
	}
	return hit
}

// Between は、期間（from 含む〜to 含まない）に泊まりがある、取消されていない予約を返す（半開区間）。
func (f *fakeReservations) Between(_ context.Context, from, to time.Time) ([]reservation.Reservation, error) {
	f.lastBetween = [2]time.Time{from, to}
	if f.err != nil {
		return nil, f.err
	}
	var hit []reservation.Reservation
	for _, it := range f.items {
		if it.Status != reservation.StatusCancelled && it.CheckIn.Before(to) && from.Before(it.CheckOut) {
			hit = append(hit, it)
		}
	}
	return hit, nil
}

func (f *fakeReservations) Overlapping(_ context.Context, roomID int64, in, out time.Time, excludeID int64) ([]reservation.Reservation, error) {
	if f.overlapErr != nil {
		return nil, f.overlapErr
	}
	return f.overlapping(roomID, in, out, excludeID), nil
}

func (f *fakeReservations) Create(_ context.Context, r *reservation.Reservation) error {
	if f.err != nil {
		return f.err
	}
	if f.writeErr != nil {
		return f.writeErr
	}
	if len(f.overlapping(r.RoomID, r.CheckIn, r.CheckOut, 0)) > 0 {
		return reservation.ErrOverlap
	}
	f.nextID++
	r.ID, r.Status = f.nextID, reservation.StatusReserved
	f.items = append(f.items, *r)
	return nil
}

func (f *fakeReservations) Update(_ context.Context, r *reservation.Reservation) error {
	i := f.index(r.ID)
	switch {
	case f.err != nil:
		return f.err
	case i < 0:
		return reservation.ErrNotFound
	case f.items[i].Status == reservation.StatusCancelled:
		return reservation.ErrNotEditable
	case f.writeErr != nil:
		return f.writeErr
	}
	if len(f.overlapping(r.RoomID, r.CheckIn, r.CheckOut, r.ID)) > 0 {
		return reservation.ErrOverlap
	}
	r.Status = f.items[i].Status
	f.items[i] = *r
	return nil
}

func (f *fakeReservations) Cancel(_ context.Context, id int64) error {
	i := f.index(id)
	switch {
	case f.err != nil:
		return f.err
	case i < 0:
		return reservation.ErrNotFound
	case f.items[i].Status == reservation.StatusCancelled:
		return reservation.ErrNotEditable
	}
	f.items[i].Status = reservation.StatusCancelled
	return nil
}

// fixedNow は 2026-11-05 10:00（日本時間）。
func fixedNow() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, jst) }

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := reservation.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type env struct {
	h    http.Handler
	rms  *fakeStore
	resv *fakeReservations
}

// newEnv は、部屋が3つ（松=有効・定員4、竹=有効・定員2、梅=停止中）ある状態の画面を作る。
func newEnv(t *testing.T) env {
	t.Helper()
	rms := &fakeStore{nextID: 3, rooms: []room.Room{
		{ID: 1, Name: "松の間", Capacity: 4, IsActive: true},
		{ID: 2, Name: "竹の間", Capacity: 2, IsActive: true},
		{ID: 3, Name: "梅の間", Capacity: 6, IsActive: false},
	}}
	resv := &fakeReservations{}
	h, err := newHandler(rms, resv, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	return env{h: h, rms: rms, resv: resv}
}

func (e env) seed(t *testing.T, r reservation.Reservation) {
	t.Helper()
	e.resv.nextID++
	r.ID = e.resv.nextID
	if r.Status == "" {
		r.Status = reservation.StatusReserved
	}
	e.resv.items = append(e.resv.items, r)
}

func sample(t *testing.T) reservation.Reservation {
	t.Helper()
	return reservation.Reservation{
		RoomID: 1, RoomName: "松の間", GuestName: "山田 太郎", Contact: "090-0000-0000",
		CheckIn: day(t, "2026-11-10"), CheckOut: day(t, "2026-11-12"), NumGuests: 2, Note: "18時ごろ到着",
	}
}

func validForm() url.Values {
	return url.Values{
		"room_id": {"1"}, "guest_name": {"山田 太郎"}, "contact": {"090"},
		"check_in": {"2026-11-10"}, "check_out": {"2026-11-12"}, "num_guests": {"2"}, "note": {"メモ"},
	}
}

func with(v url.Values, kv ...string) url.Values {
	c := url.Values{}
	for k, vals := range v {
		c[k] = append([]string(nil), vals...)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		c.Set(kv[i], kv[i+1])
	}
	return c
}

func TestReservationListDefaultFilter(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t))
	rec := get(e.h, "/reservations")

	if rec.Code != 200 {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	want := reservation.Filter{Status: reservation.StatusReserved, From: day(t, "2026-11-05")}
	if e.resv.lastFilter != want {
		t.Errorf("既定の絞り込み = %+v, 期待 %+v（予約中・今日以降）", e.resv.lastFilter, want)
	}
	for _, s := range []string{"山田 太郎", "松の間", "2026-11-10（火）", "2026-11-12（木）", "2泊", "2名", "予約中", `href="/reservations/1/edit"`, `value="2026-11-05"`} {
		mustContain(t, rec, s)
	}
}

// 「今日」は日本の日付で決める。UTC ではまだ 11/05 でも、日本ではすでに 11/06。
func TestReservationListTodayIsJapanDate(t *testing.T) {
	e := newEnv(t)
	h, _ := newHandler(e.rms, e.resv, func() time.Time { return time.Date(2026, 11, 5, 16, 30, 0, 0, time.UTC) })
	get(h, "/reservations")

	if got, want := e.resv.lastFilter.From, day(t, "2026-11-06"); !got.Equal(want) {
		t.Errorf("From = %v, 期待 %v（日本時間 11/06 1:30）", got, want)
	}
}

func TestReservationListFilters(t *testing.T) {
	tests := []struct {
		name string
		path string
		want reservation.Filter
		text string // 画面に含まれる文字列
	}{
		{"取消済みだけ", "/reservations?status=cancelled", reservation.Filter{Status: "cancelled", From: day(t, "2026-11-05")}, ""},
		{"すべての状態", "/reservations?status=all", reservation.Filter{From: day(t, "2026-11-05")}, ""},
		{"想定外の状態は予約中にする", "/reservations?status=zzz", reservation.Filter{Status: "reserved", From: day(t, "2026-11-05")}, ""},
		{"期間を指定", "/reservations?from=2026-12-01", reservation.Filter{Status: "reserved", From: day(t, "2026-12-01")}, `value="2026-12-01"`},
		{"全期間（from を空で送る）", "/reservations?status=all&from=", reservation.Filter{}, ""},
		{"日付の形式が違えば今日にして知らせる", "/reservations?from=abc", reservation.Filter{Status: "reserved", From: day(t, "2026-11-05")}, "日付の形式が正しくないため"},
		{"存在しない日付", "/reservations?from=2026-02-30", reservation.Filter{Status: "reserved", From: day(t, "2026-11-05")}, "日付の形式が正しくないため"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			rec := get(e.h, tt.path)
			if rec.Code != 200 {
				t.Fatalf("ステータス = %d", rec.Code)
			}
			if e.resv.lastFilter != tt.want {
				t.Errorf("Filter = %+v, 期待 %+v", e.resv.lastFilter, tt.want)
			}
			if tt.text != "" {
				mustContain(t, rec, tt.text)
			}
		})
	}
}

func TestReservationListEmptyAndSavedMessage(t *testing.T) {
	e := newEnv(t)
	mustContain(t, get(e.h, "/reservations"), "該当する予約はありません")
	mustContain(t, get(e.h, "/reservations?saved=created"), "予約を登録しました。")
	mustContain(t, get(e.h, "/reservations?saved=updated"), "予約を更新しました。")
	mustContain(t, get(e.h, "/reservations?saved=cancelled"), "予約を取り消しました。")

	rec := get(e.h, "/reservations?saved=<b>x</b>")
	if strings.Contains(rec.Body.String(), "<b>x</b>") || strings.Contains(rec.Body.String(), "notice-ok") {
		t.Error("想定外の saved の値が表示された")
	}
}

func TestReservationListEscapes(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.GuestName = `<script>alert(1)</script>`
	e.seed(t, r)

	if strings.Contains(get(e.h, "/reservations").Body.String(), "<script>alert(1)") {
		t.Error("宿泊者名がエスケープされていない")
	}
}

func TestReservationListStoreError(t *testing.T) {
	e := newEnv(t)
	e.resv.err = errors.New("db down")
	rec := get(e.h, "/reservations")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "db down") {
		t.Errorf("ステータス = %d（500 で、内部のエラー内容を出さないこと）", rec.Code)
	}
}

func TestReservationNewForm(t *testing.T) {
	e := newEnv(t)
	rec := get(e.h, "/reservations/new?room_id=2")

	if rec.Code != 200 {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, s := range []string{`action="/reservations"`, "松の間（定員4名）", "竹の間（定員2名）", `value="2026-11-05"`, `value="2026-11-06"`, `value="2"`, "登録する"} {
		if !strings.Contains(body, s) {
			t.Errorf("本文に %q が含まれない", s)
		}
	}
	if strings.Contains(body, "梅の間") {
		t.Error("停止中の部屋が、新規の選択肢に出ている")
	}
	if !strings.Contains(body, `<option value="2" selected>`) {
		t.Error("room_id=2 の部屋が選ばれていない")
	}
}

// カレンダーの空きマスから来たとき：check_in が正しければ、その日から1泊を入れる。誤りなら今日（既定）のまま。
func TestReservationNewFormCheckInFromCalendar(t *testing.T) {
	tests := []struct {
		name, query string
		in, out     string
	}{
		{"正しい日付", "check_in=2026-12-31", "2026-12-31", "2027-01-01"}, // 年またぎ
		{"うるう日", "check_in=2028-02-28", "2028-02-28", "2028-02-29"},
		{"存在しない日", "check_in=2026-02-30", "2026-11-05", "2026-11-06"},
		{"形式が違う", "check_in=abc", "2026-11-05", "2026-11-06"},
		{"空", "check_in=", "2026-11-05", "2026-11-06"},
		{"指定なし", "room_id=1", "2026-11-05", "2026-11-06"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(newEnv(t).h, "/reservations/new?"+tt.query)
			if rec.Code != 200 {
				t.Fatalf("ステータス = %d", rec.Code)
			}
			mustContain(t, rec, `name="check_in" type="date" value="`+tt.in+`"`)
			mustContain(t, rec, `name="check_out" type="date" value="`+tt.out+`"`)
		})
	}
}

func TestReservationNewFormWithoutRooms(t *testing.T) {
	e := newEnv(t)
	e.rms.rooms = nil
	mustContain(t, get(e.h, "/reservations/new"), "選べる部屋がありません")
}

func TestReservationCreate(t *testing.T) {
	e := newEnv(t)
	rec := post(e.h, "/reservations", validForm())

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/reservations?saved=created" {
		t.Fatalf("ステータス = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(e.resv.items) != 1 {
		t.Fatalf("保存件数 = %d", len(e.resv.items))
	}
	got := e.resv.items[0]
	if got.RoomID != 1 || got.GuestName != "山田 太郎" || got.NumGuests != 2 || got.Nights() != 2 ||
		!got.CheckIn.Equal(day(t, "2026-11-10")) || got.Status != reservation.StatusReserved {
		t.Errorf("保存内容が違う: %+v", got)
	}
}

func TestReservationCreateInvalid(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		text string
	}{
		{"名前が空", with(validForm(), "guest_name", ""), "宿泊者の名前を入力してください。"},
		{"部屋が未選択", with(validForm(), "room_id", ""), "部屋を選んでください。"},
		{"存在しない部屋", with(validForm(), "room_id", "99"), "選んだ部屋が見つかりません。"},
		{"停止中の部屋", with(validForm(), "room_id", "3"), "停止中の部屋は選べません。"},
		{"部屋IDが数字でない", with(validForm(), "room_id", "abc"), "部屋を選んでください。"},
		{"定員超え", with(validForm(), "room_id", "2", "num_guests", "3"), "人数が部屋の定員を超えています。"},
		{"人数が数字でない", with(validForm(), "num_guests", "abc"), "人数は1名以上で入力してください。"},
		{"人数0", with(validForm(), "num_guests", "0"), "人数は1名以上で入力してください。"},
		{"日付の形式が違う", with(validForm(), "check_in", "11/10"), "日付を正しく入力してください"},
		{"存在しない日付", with(validForm(), "check_out", "2026-02-30"), "日付を正しく入力してください"},
		{"チェックインが未入力", with(validForm(), "check_in", ""), "チェックイン日を入力してください。"},
		{"チェックアウトがチェックイン以前", with(validForm(), "check_out", "2026-11-10"), "チェックアウト日は、チェックイン日より後にしてください。"},
		{"61泊", with(validForm(), "check_out", "2027-01-10"), "宿泊は60泊までにしてください。"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			rec := post(e.h, "/reservations", tt.form)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("ステータス = %d, 期待 422", rec.Code)
			}
			mustContain(t, rec, tt.text)
			if len(e.resv.items) != 0 {
				t.Error("誤った入力が保存された")
			}
		})
	}
}

func TestReservationCreateKeepsInputOnError(t *testing.T) {
	e := newEnv(t)
	rec := post(e.h, "/reservations", with(validForm(), "guest_name", `山田"<b>`, "num_guests", "abc", "note", "改行\nあり"))

	for _, s := range []string{`value="山田&#34;&lt;b&gt;"`, `value="abc"`, "改行\nあり", `<option value="1" selected>`, `value="2026-11-10"`} {
		mustContain(t, rec, s)
	}
}

func TestReservationCreateStoreError(t *testing.T) {
	e := newEnv(t)
	e.resv.err = errors.New("db down")
	if rec := post(e.h, "/reservations", validForm()); rec.Code != http.StatusInternalServerError {
		t.Errorf("ステータス = %d, 期待 500", rec.Code)
	}
}

func TestReservationEditForm(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t))
	rec := get(e.h, "/reservations/1/edit")

	if rec.Code != 200 {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	for _, s := range []string{`action="/reservations/1"`, `value="山田 太郎"`, `value="2026-11-10"`, `value="2026-11-12"`, `<option value="1" selected>`, "18時ごろ到着", "更新する"} {
		mustContain(t, rec, s)
	}
}

func TestReservationEditKeepsInactiveRoomOption(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.RoomID, r.RoomName = 3, "梅の間"
	e.seed(t, r)

	rec := get(e.h, "/reservations/1/edit")
	mustContain(t, rec, "梅の間（停止中）")
	mustContain(t, rec, `<option value="3" selected>`)
}

func TestReservationEditCancelledIsReadOnly(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.Status = reservation.StatusCancelled
	e.seed(t, r)

	rec := get(e.h, "/reservations/1/edit")
	if rec.Code != 200 {
		t.Fatalf("ステータス = %d", rec.Code)
	}
	mustContain(t, rec, "この予約は取り消されています")
	if strings.Contains(rec.Body.String(), "<form") && strings.Contains(rec.Body.String(), `action="/reservations/1"`) {
		t.Error("取消済みの予約に、更新フォームが出ている")
	}
}

func TestReservationNotFoundPaths(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{
		"/reservations/99/edit", "/reservations/abc/edit", "/reservations/0/edit", "/reservations/-1/edit",
		"/reservations/99/cancel", "/reservations/abc/cancel",
	} {
		if rec := get(e.h, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: ステータス = %d, 期待 404", path, rec.Code)
		}
	}
	for _, path := range []string{"/reservations/99", "/reservations/99/cancel", "/reservations/abc"} {
		if rec := post(e.h, path, validForm()); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s: ステータス = %d, 期待 404", path, rec.Code)
		}
	}
}

func TestReservationUpdate(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t))
	rec := post(e.h, "/reservations/1", with(validForm(), "guest_name", "鈴木 花子", "num_guests", "4", "check_out", "2026-11-13"))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/reservations?saved=updated" {
		t.Fatalf("ステータス = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
	}
	got := e.resv.items[0]
	if got.GuestName != "鈴木 花子" || got.NumGuests != 4 || got.Nights() != 3 || got.ID != 1 {
		t.Errorf("更新内容が違う: %+v", got)
	}
}

func TestReservationUpdateRoomRules(t *testing.T) {
	// 停止中の部屋の予約は、その部屋のままなら更新できる。別の停止中の部屋への変更はできない。
	e := newEnv(t)
	r := sample(t)
	r.RoomID, r.RoomName = 3, "梅の間"
	e.seed(t, r)

	if rec := post(e.h, "/reservations/1", with(validForm(), "room_id", "3", "num_guests", "5")); rec.Code != http.StatusSeeOther {
		t.Errorf("同じ停止中の部屋のまま: ステータス = %d, 期待 303", rec.Code)
	}

	e2 := newEnv(t)
	e2.rms.rooms = append(e2.rms.rooms, room.Room{ID: 4, Name: "桜の間", Capacity: 4, IsActive: false})
	e2.seed(t, sample(t)) // 松の間（有効）の予約
	rec := post(e2.h, "/reservations/1", with(validForm(), "room_id", "4"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("別の停止中の部屋へ変更: ステータス = %d, 期待 422", rec.Code)
	}
	mustContain(t, rec, "停止中の部屋は選べません。")
}

func TestReservationUpdateInvalidAndCancelled(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t))
	if rec := post(e.h, "/reservations/1", with(validForm(), "guest_name", "")); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("入力誤り: ステータス = %d, 期待 422", rec.Code)
	}

	c := newEnv(t)
	r := sample(t)
	r.Status = reservation.StatusCancelled
	c.seed(t, r)
	rec := post(c.h, "/reservations/1", validForm())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("取消済み: ステータス = %d, 期待 422", rec.Code)
	}
	mustContain(t, rec, "取り消されているため、変更できません。")
	// 特定の項目の誤りではないので、名前欄のエラー（読み上げでは名前の誤りに聞こえる）には付けない。
	if strings.Contains(rec.Body.String(), `id="guest_name-error"`) {
		t.Error("取消済みのエラーが、宿泊者の名前欄に付いている")
	}
	mustContain(t, rec, `role="alert">この予約は取り消されているため`)
}

func TestReservationEditHasCancelLinkButNewDoesNot(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t))

	mustContain(t, get(e.h, "/reservations/1/edit"), `href="/reservations/1/cancel"`)
	if strings.Contains(get(e.h, "/reservations/new").Body.String(), "この予約を取り消す") {
		t.Error("新規登録の画面に、取消のリンクが出ている")
	}
}

func TestReservationCancelFlow(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t))

	// 確認画面：まだ取り消さない。
	rec := get(e.h, "/reservations/1/cancel")
	if rec.Code != 200 {
		t.Fatalf("確認画面: ステータス = %d", rec.Code)
	}
	mustContain(t, rec, "この予約を取り消す")
	mustContain(t, rec, `action="/reservations/1/cancel"`)
	if e.resv.items[0].Status != reservation.StatusReserved {
		t.Fatal("確認画面を開いただけで取り消された")
	}

	// 取消：削除せず状態を変える。
	rec = post(e.h, "/reservations/1/cancel", url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/reservations?saved=cancelled" {
		t.Errorf("ステータス = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(e.resv.items) != 1 || e.resv.items[0].Status != reservation.StatusCancelled {
		t.Errorf("取消後の状態が違う: %+v", e.resv.items)
	}

	// 二重送信：エラーにせず一覧へ戻す。
	if rec := post(e.h, "/reservations/1/cancel", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Errorf("2回目: ステータス = %d, 期待 303", rec.Code)
	}
	// 取消済みの確認画面は、詳細へ飛ばす。
	rec = get(e.h, "/reservations/1/cancel")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/reservations/1/edit" {
		t.Errorf("取消済みの確認画面: ステータス = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestReservationCancelStoreError(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t))
	e.resv.err = errors.New("db down")
	if rec := post(e.h, "/reservations/1/cancel", url.Values{}); rec.Code != http.StatusInternalServerError {
		t.Errorf("ステータス = %d, 期待 500", rec.Code)
	}
}

func TestReservationFormEscapesAttributes(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.GuestName, r.Note = `a"onfocus="x`, `</textarea><script>1</script>`
	e.seed(t, r)

	body := get(e.h, "/reservations/1/edit").Body.String()
	if strings.Contains(body, `value="a"onfocus=`) || strings.Contains(body, "</textarea><script>") {
		t.Error("属性や textarea の中でエスケープされていない")
	}
}

func TestNavigationHasReservations(t *testing.T) {
	e := newEnv(t)
	mustContain(t, get(e.h, "/reservations"), `href="/reservations" aria-current="page"`)
	mustContain(t, get(e.h, "/rooms"), `href="/reservations">予約</a>`)
}
