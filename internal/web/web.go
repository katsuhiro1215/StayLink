// Package web は StayLink の画面（HTTP ハンドラとテンプレート）を提供する。
package web

import (
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
	names := []string{"home", "rooms", "notfound"}
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

// NewHandler はルーティングを組み立てて返す。
func NewHandler() (http.Handler, error) {
	p, err := loadPages()
	if err != nil {
		return nil, err
	}

	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	// "GET /{$}" は "/" ちょうどだけに一致する（それ以外は下の "/" へ）。
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		p.render(w, http.StatusOK, "home", pageData{Title: "トップ", Active: "home"})
	})
	mux.HandleFunc("GET /rooms", func(w http.ResponseWriter, r *http.Request) {
		p.render(w, http.StatusOK, "rooms", pageData{Title: "部屋", Active: "rooms"})
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	// 上のどれにも一致しなかったリクエストは 404 画面にする。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p.render(w, http.StatusNotFound, "notfound", pageData{Title: "ページが見つかりません"})
	})
	return mux, nil
}

// pageData は全画面で共通のテンプレート入力。
type pageData struct {
	Title  string // <title> と見出しに使う
	Active string // ナビゲーションで強調する項目
}

func (p pages) render(w http.ResponseWriter, status int, name string, data pageData) {
	t, ok := p[name]
	if !ok {
		log.Printf("テンプレートがありません: %s", name)
		http.Error(w, "内部エラー", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		// ステータスは送信済みなので、ログに残すだけにする。
		log.Printf("描画に失敗しました (%s): %v", name, err)
	}
}
