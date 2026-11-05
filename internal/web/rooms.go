package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/katsuhiro1215/StayLink/internal/room"
)

// RoomStore は画面が必要とする部屋の保存先。
// 実体は room.Store（PostgreSQL）だが、テストでは入れ替えられるよう interface にしている。
type RoomStore interface {
	List(ctx context.Context) ([]room.Room, error)
	Get(ctx context.Context, id int64) (room.Room, error)
	Create(ctx context.Context, r *room.Room) error
	Update(ctx context.Context, r *room.Room) error
}

// maxFormBytes はフォーム1回分の本文サイズの上限。
const maxFormBytes = 64 << 10

// 一覧に出す完了メッセージ。クエリ文字列の値は、ここにある固定の文だけを引く（入力をそのまま表示しない）。
var savedMessages = map[string]string{
	"created": "部屋を登録しました。",
	"updated": "部屋を更新しました。",
}

type roomsPage struct {
	Rooms   []room.Room
	Message string
}

// roomForm は登録・編集フォームの表示用データ。
// 誤りがあったときに入力をそのまま戻せるよう、数値も文字列で持つ。
type roomForm struct {
	IsNew     bool
	Action    string // フォームの送信先
	Name      string
	Capacity  string
	SortOrder string
	IsActive  bool
	Errors    map[string]string
}

func (s *server) roomList(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.rooms.List(r.Context())
	if err != nil {
		s.serverError(w, "部屋の一覧の取得に失敗しました", err)
		return
	}
	s.pages.render(w, http.StatusOK, "rooms", pageData{
		Title:  "部屋",
		Active: "rooms",
		Data:   roomsPage{Rooms: rooms, Message: savedMessages[r.URL.Query().Get("saved")]},
	})
}

func (s *server) roomNew(w http.ResponseWriter, r *http.Request) {
	s.renderForm(w, http.StatusOK, "部屋の登録", roomForm{
		IsNew:     true,
		Action:    "/rooms",
		Capacity:  "2",
		SortOrder: "0",
		IsActive:  true,
	})
}

func (s *server) roomCreate(w http.ResponseWriter, r *http.Request) {
	form, rm, errs, ok := readRoomForm(w, r)
	if !ok {
		return
	}
	form.IsNew = true
	form.Action = "/rooms"
	rm.IsActive = true // 新しい部屋は、はじめから有効
	form.IsActive = true

	if len(errs) == 0 {
		err := s.rooms.Create(r.Context(), &rm)
		if errors.Is(err, room.ErrDuplicateName) {
			errs["name"] = "同じ名前の部屋がすでにあります。"
		} else if err != nil {
			s.serverError(w, "部屋の登録に失敗しました", err)
			return
		}
	}
	if len(errs) > 0 {
		form.Errors = errs
		s.renderForm(w, http.StatusUnprocessableEntity, "部屋の登録", form)
		return
	}
	http.Redirect(w, r, "/rooms?saved=created", http.StatusSeeOther)
}

func (s *server) roomEdit(w http.ResponseWriter, r *http.Request) {
	rm, ok := s.findRoom(w, r)
	if !ok {
		return
	}
	s.renderForm(w, http.StatusOK, "部屋の編集", roomForm{
		Action:    "/rooms/" + strconv.FormatInt(rm.ID, 10),
		Name:      rm.Name,
		Capacity:  strconv.Itoa(rm.Capacity),
		SortOrder: strconv.Itoa(rm.SortOrder),
		IsActive:  rm.IsActive,
	})
}

func (s *server) roomUpdate(w http.ResponseWriter, r *http.Request) {
	current, ok := s.findRoom(w, r)
	if !ok {
		return
	}
	form, rm, errs, ok := readRoomForm(w, r)
	if !ok {
		return
	}
	rm.ID = current.ID
	rm.IsActive = r.PostFormValue("is_active") == "on"
	form.Action = "/rooms/" + strconv.FormatInt(current.ID, 10)
	form.IsActive = rm.IsActive

	if len(errs) == 0 {
		err := s.rooms.Update(r.Context(), &rm)
		switch {
		case errors.Is(err, room.ErrDuplicateName):
			errs["name"] = "同じ名前の部屋がすでにあります。"
		case errors.Is(err, room.ErrNotFound):
			s.notFound(w)
			return
		case err != nil:
			s.serverError(w, "部屋の更新に失敗しました", err)
			return
		}
	}
	if len(errs) > 0 {
		form.Errors = errs
		s.renderForm(w, http.StatusUnprocessableEntity, "部屋の編集", form)
		return
	}
	http.Redirect(w, r, "/rooms?saved=updated", http.StatusSeeOther)
}

// findRoom は URL の {id} から部屋を取得する。見つからなければ 404 を書いて ok=false を返す。
func (s *server) findRoom(w http.ResponseWriter, r *http.Request) (room.Room, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.notFound(w)
		return room.Room{}, false
	}
	rm, err := s.rooms.Get(r.Context(), id)
	if errors.Is(err, room.ErrNotFound) {
		s.notFound(w)
		return room.Room{}, false
	}
	if err != nil {
		s.serverError(w, "部屋の取得に失敗しました", err)
		return room.Room{}, false
	}
	return rm, true
}

// readRoomForm はフォームを読み、入力値の表示用データ・部屋・入力の誤りを返す。
// 本文が大きすぎるなど読めないときは、応答を書いて ok=false を返す。
func readRoomForm(w http.ResponseWriter, r *http.Request) (form roomForm, rm room.Room, errs map[string]string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "入力を読み取れませんでした", http.StatusBadRequest)
		return form, rm, nil, false
	}

	form = roomForm{
		Name:      strings.TrimSpace(r.PostFormValue("name")),
		Capacity:  strings.TrimSpace(r.PostFormValue("capacity")),
		SortOrder: strings.TrimSpace(r.PostFormValue("sort_order")),
	}
	rm.Name = form.Name
	rm.Normalize()

	extra := map[string]string{}
	if n, err := strconv.Atoi(form.Capacity); err == nil {
		rm.Capacity = n
	} // 数字でなければ 0 のまま。Validate が「1〜100の数字」の誤りにする。
	if form.SortOrder != "" {
		if n, err := strconv.Atoi(form.SortOrder); err == nil && n >= 0 && n <= 9999 {
			rm.SortOrder = n
		} else {
			extra["sort_order"] = "表示順は0〜9999の数字で入力してください。"
		}
	}

	errs = rm.Validate()
	for k, v := range extra {
		errs[k] = v
	}
	return form, rm, errs, true
}

func (s *server) renderForm(w http.ResponseWriter, status int, title string, form roomForm) {
	s.pages.render(w, status, "room_form", pageData{Title: title, Active: "rooms", Data: form})
}

func (s *server) notFound(w http.ResponseWriter) {
	s.pages.render(w, http.StatusNotFound, "notfound", pageData{Title: "ページが見つかりません"})
}

func (s *server) serverError(w http.ResponseWriter, msg string, err error) {
	log.Printf("%s: %v", msg, err)
	http.Error(w, "内部エラーが発生しました", http.StatusInternalServerError)
}
