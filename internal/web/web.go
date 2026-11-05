// Package web は StayLink の画面（HTTP ハンドラとテンプレート）を提供する。
package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed templates/*.html static/*
var assets embed.FS

// pages は画面ごとのテンプレートを持つ。
// 画面ごとに layout.html と組み合わせて別々に読み込むので、
// 各画面が同じ名前のブロック "content" を定義しても衝突しない。
type pages map[string]*template.Template

// loadPages はテンプレートを読み込む。auth は、ログインを求めるか（ナビにログアウトを出すかの判断に使う）。
func loadPages(auth bool) (pages, error) {
	fm := template.FuncMap{"authEnabled": func() bool { return auth }}
	for k, v := range funcs {
		fm[k] = v
	}
	names := []string{
		"home", "notfound", "error", "login",
		"rooms", "room_form",
		"reservations", "reservation_form", "reservation_view", "reservation_cancel",
		"calendar",
	}
	p := make(pages, len(names))
	for _, name := range names {
		t, err := template.New(name).Funcs(fm).ParseFS(assets, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		p[name] = t
	}
	return p, nil
}

// funcs はテンプレートで使う関数。日付は UTC の 0 時の time.Time で持っているので、そのまま整形する。
var funcs = template.FuncMap{
	"honorific": honorific,
	"ymd":       func(t time.Time) string { return t.Format("2006-01-02") },
	"weekday": func(t time.Time) string {
		return [...]string{"日", "月", "火", "水", "木", "金", "土"}[t.Weekday()]
	},
}

// server は各ハンドラが共有するもの（テンプレート・保存先・時計）を持つ。
type server struct {
	pages        pages
	rooms        RoomStore
	reservations ReservationStore
	now          func() time.Time // 「今日」を決める。テストで差し替える
	auth         *authConfig      // nil ならログインを求めない（テスト用。NewHandler は必ず付ける）
}

// NewHandler はルーティングを組み立てて返す。/login と /static/ 以外は、ログインしている人だけに見せる。
// secureCookie は、HTTPS で使うときに true（セッションの Cookie に Secure を常に付ける）。
func NewHandler(rooms RoomStore, reservations ReservationStore, users Authenticator, sessions SessionStore, secureCookie bool) (http.Handler, error) {
	return newHandler(rooms, reservations, time.Now, withAuth(users, sessions, secureCookie))
}

func newHandler(rooms RoomStore, reservations ReservationStore, now func() time.Time, opts ...option) (http.Handler, error) {
	s := &server{rooms: rooms, reservations: reservations, now: now}
	for _, opt := range opts {
		opt(s)
	}
	p, err := loadPages(s.auth != nil)
	if err != nil {
		return nil, err
	}
	s.pages = p

	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	// "GET /{$}" は "/" ちょうどだけに一致する（それ以外は下の "/" へ）。
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /calendar", s.calendar)
	mux.HandleFunc("GET /reservations", s.reservationList)
	mux.HandleFunc("GET /reservations/new", s.reservationNew)
	mux.HandleFunc("POST /reservations", s.reservationCreate)
	mux.HandleFunc("GET /reservations/{id}/edit", s.reservationEdit)
	mux.HandleFunc("POST /reservations/{id}", s.reservationUpdate)
	mux.HandleFunc("GET /reservations/{id}/cancel", s.reservationCancelConfirm)
	mux.HandleFunc("POST /reservations/{id}/cancel", s.reservationCancel)
	mux.HandleFunc("GET /rooms", s.roomList)
	mux.HandleFunc("GET /rooms/new", s.roomNew)
	mux.HandleFunc("POST /rooms", s.roomCreate)
	mux.HandleFunc("GET /rooms/{id}/edit", s.roomEdit)
	mux.HandleFunc("POST /rooms/{id}", s.roomUpdate)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	// 上のどれにも一致しなかった GET は 404 画面にする。
	// GET に限るので、登録済みのパスへ別メソッドで来た場合は標準の 405（Allow 付き）になる。
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		s.notFound(w)
	})

	var handler http.Handler = mux
	if s.auth != nil {
		mux.HandleFunc("GET /login", s.loginPage)
		mux.HandleFunc("POST /login", s.login)
		mux.HandleFunc("POST /logout", s.logout)
		handler = s.requireLogin(mux) // /login と /static/ 以外は、ログインを求める
	}

	// 他のサイトのページからブラウザ経由で送られた POST などを拒否する（標準ライブラリの機能。ログインの POST も含む）。
	// 限界：Sec-Fetch-Site / Origin を持たないリクエスト（curl など）は通す。Host の検証もしない。
	// 通しても、Cookie（SameSite=Lax）がなければ、ログインを求められて止まる。
	// 外側にセキュリティヘッダを付ける（CrossOriginProtection が拒否した応答にも付く）。
	return securityHeaders(http.NewCrossOriginProtection().Handler(handler)), nil
}

// pageData は全画面で共通のテンプレート入力。
type pageData struct {
	Title  string // <title> と見出しに使う
	Active string // ナビゲーションで強調する項目
	Data   any    // 画面ごとの中身（一覧やフォームの値）
}

func (p pages) render(w http.ResponseWriter, status int, name string, data pageData) {
	t, ok := p[name]
	if !ok {
		log.Printf("テンプレートがありません: %s", name)
		http.Error(w, "内部エラー", http.StatusInternalServerError)
		return
	}
	// 先にバッファへ描画する。途中で失敗しても、壊れたHTMLを 200 で返さず 500 にできる。
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("描画に失敗しました (%s): %v", name, err)
		http.Error(w, "内部エラー", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("送信に失敗しました (%s): %v", name, err)
	}
}

// honorific は、名前に敬称をつける。すでに敬称で終わっているときは、重ねない（「山田様さん」を防ぐ）。
// 末尾の文字だけで判定するので、「山田様（団体）」「山田様ご一行」「○○御中」「サン」「Mr.」などは対象外（敬称が重なる）。
func honorific(name string) string {
	for _, h := range []string{"さん", "様", "さま", "殿", "氏", "先生"} {
		if strings.HasSuffix(name, h) {
			return name
		}
	}
	return name + "さん"
}
