// Package web は StayLink の画面（HTTP ハンドラとテンプレート）を提供する。
package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
)

//go:embed templates/*.html static/*
var assets embed.FS

// pages は画面ごとのテンプレートを持つ。
// 画面ごとに layout.html と組み合わせて別々に読み込むので、
// 各画面が同じ名前のブロック "content" を定義しても衝突しない。
type pages map[string]*template.Template

func loadPages() (pages, error) {
	names := []string{"home", "rooms", "room_form", "notfound"}
	p := make(pages, len(names))
	for _, name := range names {
		t, err := template.ParseFS(assets, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		p[name] = t
	}
	return p, nil
}

// server は各ハンドラが共有するもの（テンプレートと保存先）を持つ。
type server struct {
	pages pages
	rooms RoomStore
}

// NewHandler はルーティングを組み立てて返す。
func NewHandler(rooms RoomStore) (http.Handler, error) {
	p, err := loadPages()
	if err != nil {
		return nil, err
	}
	s := &server{pages: p, rooms: rooms}

	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	// "GET /{$}" は "/" ちょうどだけに一致する（それ以外は下の "/" へ）。
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		p.render(w, http.StatusOK, "home", pageData{Title: "トップ", Active: "home"})
	})
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

	// 他のサイトからの POST などを拒否する（CSRF 対策の基本。標準ライブラリの機能）。
	return http.NewCrossOriginProtection().Handler(mux), nil
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
