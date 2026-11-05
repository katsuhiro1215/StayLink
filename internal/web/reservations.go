package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
	"github.com/katsuhiro1215/StayLink/internal/room"
)

// ReservationStore は画面が必要とする予約の保存先。実体は reservation.Store。
type ReservationStore interface {
	List(ctx context.Context, f reservation.Filter) ([]reservation.Reservation, error)
	Get(ctx context.Context, id int64) (reservation.Reservation, error)
	Create(ctx context.Context, r *reservation.Reservation) error
	Update(ctx context.Context, r *reservation.Reservation) error
	Cancel(ctx context.Context, id int64) error
	// Overlapping は、重なった予約を案内するための検索（重なりを防ぐのは DB の排他制約）。
	Overlapping(ctx context.Context, roomID int64, checkIn, checkOut time.Time, excludeID int64) ([]reservation.Reservation, error)
	// Between は、期間（from 含む〜to 含まない）に泊まりがある、取消されていない予約（月間カレンダー用）。
	Between(ctx context.Context, from, to time.Time) ([]reservation.Reservation, error)
}

// formError は、特定の項目に結びつかないフォーム全体のエラーを入れるキー（Errors マップ内）。
// フォーム上部のお知らせにだけ出す。
const formError = "_form"

// busyMessage は、同時の操作が重なって保存できなかったときの案内。入力は正しいので、もう一度押せば通る。
const busyMessage = "同時に他の操作があり、保存できませんでした。入力は残してあります。もう一度、ボタンを押してください。"

// statusFor はフォームの誤りに応じた HTTP ステータスを返す。
// 入力の誤りは 422。同時操作の混み合い（再試行すれば通る）は、入力の誤りではないので 503。
func statusFor(errs map[string]string) int {
	if errs[formError] == busyMessage {
		return http.StatusServiceUnavailable
	}
	return http.StatusUnprocessableEntity
}

// jst は日本時間。「今日」を日本の日付で決めるために使う（tzdata に頼らず固定の +9 時間）。
var jst = time.FixedZone("JST", 9*60*60)

// today は日本の今日の日付を、UTC の 0 時の time.Time で返す。
func (s *server) today() time.Time {
	n := s.now().In(jst)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

var reservationSavedMessages = map[string]string{
	"created":   "予約を登録しました。",
	"updated":   "予約を更新しました。",
	"cancelled": "予約を取り消しました。",
}

// reservationsPage は予約一覧の表示用データ。
type reservationsPage struct {
	Reservations []reservation.Reservation
	Message      string
	Warning      string
	Status       string // 絞り込み中の状態。reserved / cancelled / all
	From         string // 絞り込み中の日付（YYYY-MM-DD。空なら全期間）
}

// reservationForm は予約フォームの表示用データ。入力は誤りのとき戻せるよう、全て文字列で持つ。
type reservationForm struct {
	IsNew     bool
	Action    string
	RoomID    string
	GuestName string
	Contact   string
	CheckIn   string
	CheckOut  string
	NumGuests string
	Note      string
	Rooms     []roomOption
	Errors    map[string]string
}

type roomOption struct {
	ID       int64
	Label    string
	Selected bool
}

func (s *server) reservationList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	status := q.Get("status")
	if status != reservation.StatusCancelled && status != "all" {
		status = reservation.StatusReserved
	}

	// from が無ければ今日以降。from= と空で送られたら全期間。形式が違えば今日にして知らせる。
	from, fromText, warning := s.today(), "", ""
	switch {
	case !q.Has("from"):
		fromText = from.Format(reservation.DateLayout)
	case q.Get("from") == "":
		from = time.Time{}
	default:
		if d, err := reservation.ParseDate(q.Get("from")); err == nil {
			from, fromText = d, q.Get("from")
		} else {
			fromText = from.Format(reservation.DateLayout)
			warning = "日付の形式が正しくないため、今日以降で表示しています。"
		}
	}

	filter := reservation.Filter{From: from}
	if status != "all" {
		filter.Status = status
	}
	list, err := s.reservations.List(r.Context(), filter)
	if err != nil {
		s.serverError(w, "予約の一覧の取得に失敗しました", err)
		return
	}
	s.pages.render(w, http.StatusOK, "reservations", pageData{
		Title:  "予約",
		Active: "reservations",
		Data: reservationsPage{
			Reservations: list,
			Message:      reservationSavedMessages[q.Get("saved")],
			Warning:      warning,
			Status:       status,
			From:         fromText,
		},
	})
}

func (s *server) reservationNew(w http.ResponseWriter, r *http.Request) {
	rooms, ok := s.listRooms(w, r)
	if !ok {
		return
	}
	today := s.today()
	form := reservationForm{
		IsNew:     true,
		Action:    "/reservations",
		RoomID:    r.URL.Query().Get("room_id"),
		CheckIn:   today.Format(reservation.DateLayout),
		CheckOut:  today.AddDate(0, 0, 1).Format(reservation.DateLayout),
		NumGuests: "2",
	}
	form.Rooms = roomOptions(rooms, form.RoomID, 0)
	s.renderReservationForm(w, http.StatusOK, "予約の登録", form)
}

func (s *server) reservationCreate(w http.ResponseWriter, r *http.Request) {
	rooms, ok := s.listRooms(w, r)
	if !ok {
		return
	}
	form, res, errs, ok := readReservationForm(w, r)
	if !ok {
		return
	}
	form.IsNew = true
	form.Action = "/reservations"
	s.checkRoom(&res, rooms, 0, errs)

	if len(errs) == 0 {
		err := s.reservations.Create(r.Context(), &res)
		if errors.Is(err, reservation.ErrRoomNotFound) {
			errs["room_id"] = "選んだ部屋が見つかりません。"
		} else if errors.Is(err, reservation.ErrOverlap) {
			s.explainOverlap(r.Context(), &res, 0, errs)
		} else if errors.Is(err, reservation.ErrBusy) {
			errs[formError] = busyMessage
		} else if err != nil {
			s.serverError(w, "予約の登録に失敗しました", err)
			return
		}
	}
	if len(errs) > 0 {
		form.Errors = errs
		form.Rooms = roomOptions(rooms, form.RoomID, 0)
		s.renderReservationForm(w, statusFor(errs), "予約の登録", form)
		return
	}
	http.Redirect(w, r, "/reservations?saved=created", http.StatusSeeOther)
}

func (s *server) reservationEdit(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.findReservation(w, r)
	if !ok {
		return
	}
	if cur.Status == reservation.StatusCancelled {
		s.pages.render(w, http.StatusOK, "reservation_view", pageData{
			Title: "取り消した予約", Active: "reservations", Data: cur})
		return
	}
	rooms, ok := s.listRooms(w, r)
	if !ok {
		return
	}
	form := reservationForm{
		Action:    "/reservations/" + strconv.FormatInt(cur.ID, 10),
		RoomID:    strconv.FormatInt(cur.RoomID, 10),
		GuestName: cur.GuestName,
		Contact:   cur.Contact,
		CheckIn:   cur.CheckIn.Format(reservation.DateLayout),
		CheckOut:  cur.CheckOut.Format(reservation.DateLayout),
		NumGuests: strconv.Itoa(cur.NumGuests),
		Note:      cur.Note,
	}
	form.Rooms = roomOptions(rooms, form.RoomID, cur.RoomID)
	s.renderReservationForm(w, http.StatusOK, "予約の編集", form)
}

func (s *server) reservationUpdate(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.findReservation(w, r)
	if !ok {
		return
	}
	rooms, ok := s.listRooms(w, r)
	if !ok {
		return
	}
	form, res, errs, ok := readReservationForm(w, r)
	if !ok {
		return
	}
	res.ID = cur.ID
	form.Action = "/reservations/" + strconv.FormatInt(cur.ID, 10)
	s.checkRoom(&res, rooms, cur.RoomID, errs)

	if len(errs) == 0 {
		err := s.reservations.Update(r.Context(), &res)
		switch {
		case errors.Is(err, reservation.ErrNotFound):
			s.notFound(w)
			return
		case errors.Is(err, reservation.ErrNotEditable):
			errs[formError] = "この予約は取り消されているため、変更できません。" // 特定の項目の誤りではない
		case errors.Is(err, reservation.ErrRoomNotFound):
			errs["room_id"] = "選んだ部屋が見つかりません。"
		case errors.Is(err, reservation.ErrOverlap):
			s.explainOverlap(r.Context(), &res, cur.ID, errs)
		case errors.Is(err, reservation.ErrBusy):
			errs[formError] = busyMessage
		case err != nil:
			s.serverError(w, "予約の更新に失敗しました", err)
			return
		}
	}
	if len(errs) > 0 {
		form.Errors = errs
		form.Rooms = roomOptions(rooms, form.RoomID, cur.RoomID)
		s.renderReservationForm(w, statusFor(errs), "予約の編集", form)
		return
	}
	http.Redirect(w, r, "/reservations?saved=updated", http.StatusSeeOther)
}

// reservationCancelConfirm は取消の確認画面。JavaScript を使わず、確認を1画面挟む。
func (s *server) reservationCancelConfirm(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.findReservation(w, r)
	if !ok {
		return
	}
	if cur.Status == reservation.StatusCancelled {
		http.Redirect(w, r, "/reservations/"+strconv.FormatInt(cur.ID, 10)+"/edit", http.StatusSeeOther)
		return
	}
	s.pages.render(w, http.StatusOK, "reservation_cancel", pageData{
		Title: "予約の取消", Active: "reservations", Data: cur})
}

func (s *server) reservationCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		s.notFound(w)
		return
	}
	err := s.reservations.Cancel(r.Context(), id)
	switch {
	case errors.Is(err, reservation.ErrNotFound):
		s.notFound(w)
	case errors.Is(err, reservation.ErrNotEditable):
		// すでに取消済み。二重送信などなので、一覧へ戻すだけにする。
		http.Redirect(w, r, "/reservations?saved=cancelled", http.StatusSeeOther)
	case err != nil:
		s.serverError(w, "予約の取消に失敗しました", err)
	default:
		http.Redirect(w, r, "/reservations?saved=cancelled", http.StatusSeeOther)
	}
}

// japaneseRange は期間を「2026年11月10日〜11月13日」の形にする（年が同じなら、後ろの年は省く）。
// 文中に入れるので、数字の途中で折り返されにくい日本語の書き方にしている。
func japaneseRange(in, out time.Time) string {
	if in.Year() == out.Year() {
		return in.Format("2006年1月2日") + "〜" + out.Format("1月2日")
	}
	return in.Format("2006年1月2日") + "〜" + out.Format("2006年1月2日")
}

// explainOverlap は、期間が重なって断られたことを、どの予約と重なったかを添えて errs に入れる。
// 重なった予約の検索に失敗しても、断られた事実は変わらないので、詳細なしの文面にする。
func (s *server) explainOverlap(ctx context.Context, res *reservation.Reservation, excludeID int64, errs map[string]string) {
	errs["check_in"] = "他の予約と期間が重なっています。"
	errs[formError] = "この部屋は、その期間にすでに予約があります。日付か部屋を変えてください。"

	conflicts, err := s.reservations.Overlapping(ctx, res.RoomID, res.CheckIn, res.CheckOut, excludeID)
	if err != nil {
		log.Printf("重なった予約の検索に失敗しました: %v", err)
		return
	}
	if len(conflicts) == 0 {
		return // 検索の間に取り消された、など。詳細なしの文面のままにする
	}

	const show = 3
	var parts []string
	for i, c := range conflicts {
		if i == show {
			parts = append(parts, "ほか"+strconv.Itoa(len(conflicts)-show)+"件")
			break
		}
		parts = append(parts, c.GuestName+"さん（"+japaneseRange(c.CheckIn, c.CheckOut)+"）")
	}
	errs[formError] = "この部屋は、その期間に次の予約があります：" + strings.Join(parts, "、") + "。日付か部屋を変えてください。"
}

// checkRoom は、選んだ部屋が使えるかと、人数が定員内かを確かめて、誤りを errs に足す。
// 検証はここで一度に行うので、部屋の誤りと他の項目の誤りを同時に画面へ戻せる。
// keepRoomID は編集中の予約の部屋で、停止中でもそのまま残せる（0 なら例外なし）。
func (s *server) checkRoom(res *reservation.Reservation, rooms []room.Room, keepRoomID int64, errs map[string]string) {
	var chosen *room.Room
	for i := range rooms {
		if rooms[i].ID == res.RoomID {
			chosen = &rooms[i]
		}
	}
	switch {
	case res.RoomID < 1:
		errs["room_id"] = "部屋を選んでください。"
	case chosen == nil:
		errs["room_id"] = "選んだ部屋が見つかりません。"
	case !chosen.IsActive && chosen.ID != keepRoomID:
		errs["room_id"] = "停止中の部屋は選べません。"
	}

	capacity := 0
	if chosen != nil {
		capacity = chosen.Capacity
	}
	for field, msg := range res.Validate(capacity) {
		if _, exists := errs[field]; !exists {
			errs[field] = msg
		}
	}
}

// roomOptions は部屋の選択肢を作る。停止中の部屋は、編集中の予約の部屋だけ残す。
func roomOptions(rooms []room.Room, selected string, keepRoomID int64) []roomOption {
	var opts []roomOption
	for _, rm := range rooms {
		if !rm.IsActive && rm.ID != keepRoomID {
			continue
		}
		label := rm.Name + "（定員" + strconv.Itoa(rm.Capacity) + "名）"
		if !rm.IsActive {
			label = rm.Name + "（停止中）"
		}
		opts = append(opts, roomOption{ID: rm.ID, Label: label, Selected: strconv.FormatInt(rm.ID, 10) == selected})
	}
	return opts
}

func (s *server) listRooms(w http.ResponseWriter, r *http.Request) ([]room.Room, bool) {
	rooms, err := s.rooms.List(r.Context())
	if err != nil {
		s.serverError(w, "部屋の一覧の取得に失敗しました", err)
		return nil, false
	}
	return rooms, true
}

func parseID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id >= 1
}

func (s *server) findReservation(w http.ResponseWriter, r *http.Request) (reservation.Reservation, bool) {
	id, ok := parseID(r)
	if !ok {
		s.notFound(w)
		return reservation.Reservation{}, false
	}
	res, err := s.reservations.Get(r.Context(), id)
	if errors.Is(err, reservation.ErrNotFound) {
		s.notFound(w)
		return reservation.Reservation{}, false
	}
	if err != nil {
		s.serverError(w, "予約の取得に失敗しました", err)
		return reservation.Reservation{}, false
	}
	return res, true
}

// readReservationForm はフォームを読み、表示用データ・予約・入力の誤りを返す。
// 本文が大きすぎるなど読めないときは、応答を書いて ok=false を返す。
func readReservationForm(w http.ResponseWriter, r *http.Request) (form reservationForm, res reservation.Reservation, errs map[string]string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "入力を読み取れませんでした", http.StatusBadRequest)
		return form, res, nil, false
	}

	v := func(name string) string { return strings.TrimSpace(r.PostFormValue(name)) }
	form = reservationForm{
		RoomID: v("room_id"), GuestName: v("guest_name"), Contact: v("contact"),
		CheckIn: v("check_in"), CheckOut: v("check_out"), NumGuests: v("num_guests"), Note: v("note"),
	}
	res = reservation.Reservation{GuestName: form.GuestName, Contact: form.Contact, Note: form.Note}
	res.Normalize()

	// 数字・日付として読めないものは、ここで項目の誤りにする（読めた分だけ Validate に渡す）。
	errs = map[string]string{}
	if n, err := strconv.ParseInt(form.RoomID, 10, 64); err == nil {
		res.RoomID = n
	}
	if n, err := strconv.Atoi(form.NumGuests); err == nil {
		res.NumGuests = n
	}
	for _, d := range []struct {
		field, text string
		dst         *time.Time
	}{
		{"check_in", form.CheckIn, &res.CheckIn},
		{"check_out", form.CheckOut, &res.CheckOut},
	} {
		if d.text == "" {
			continue // 未入力は Validate が「入力してください」にする
		}
		if t, err := reservation.ParseDate(d.text); err == nil {
			*d.dst = t
		} else {
			errs[d.field] = "日付を正しく入力してください（例：2026-11-05）。"
		}
	}
	return form, res, errs, true
}

func (s *server) renderReservationForm(w http.ResponseWriter, status int, title string, form reservationForm) {
	s.pages.render(w, status, "reservation_form", pageData{Title: title, Active: "reservations", Data: form})
}
