package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var wantSecurityHeaders = map[string]string{
	"Content-Security-Policy":    contentSecurityPolicy,
	"X-Content-Type-Options":     "nosniff",
	"X-Frame-Options":            "DENY",
	"Referrer-Policy":            "no-referrer",
	"Cross-Origin-Opener-Policy": "same-origin",
}

func checkSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder, wantNoStore bool) {
	t.Helper()
	for k, want := range wantSecurityHeaders {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, 期待 %q", k, got, want)
		}
	}
	// 個人情報を含む画面は保存させない。CSS（/static/）は保存してよい。
	got := rec.Header().Get("Cache-Control")
	if wantNoStore && got != "no-store" {
		t.Errorf("Cache-Control = %q, 期待 no-store", got)
	}
	if !wantNoStore && got == "no-store" {
		t.Error("静的ファイルに no-store が付いている")
	}
}

// 方針の要点を、定数との一致ではなく、個別に確かめる。
func TestContentSecurityPolicyDirectives(t *testing.T) {
	for _, want := range []string{"default-src 'none'", "style-src 'self'", "form-action 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(contentSecurityPolicy, want) {
			t.Errorf("CSP に %q がない", want)
		}
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "*", "script-src"} {
		if strings.Contains(contentSecurityPolicy, bad) {
			t.Errorf("CSP に %q が入っている", bad)
		}
	}
}

// HEAD にも付く。
func TestSecurityHeadersOnHead(t *testing.T) {
	rec := httptest.NewRecorder()
	newEnv(t).h.ServeHTTP(rec, httptest.NewRequest("HEAD", "/", nil))
	checkSecurityHeaders(t, rec, true)
}

// すべての種類の応答（成功・404・405・500・静的ファイル・他サイトからの拒否）に、同じヘッダが付く。
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	e := newEnv(t)
	broken := newEnv(t)
	broken.resv.err = errBoom

	cross := httptest.NewRequest("POST", "/rooms", strings.NewReader(url.Values{"name": {"x"}}.Encode()))
	cross.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	crossRec := httptest.NewRecorder()
	e.h.ServeHTTP(crossRec, cross)
	if crossRec.Code != http.StatusForbidden {
		t.Fatalf("他サイトからの POST = %d, 期待 403", crossRec.Code)
	}

	tests := []struct {
		name string
		rec  *httptest.ResponseRecorder
		code int
		page bool // 画面（/static/ 以外）か
	}{
		{"トップ", get(e.h, "/"), 200, true},
		{"カレンダー", get(e.h, "/calendar"), 200, true},
		{"404", get(e.h, "/nothing"), 404, true},
		{"405", post(e.h, "/", nil), 405, true},
		{"500", get(broken.h, "/"), 500, true},
		{"静的ファイル", get(e.h, "/static/style.css"), 200, false},
		{"静的ファイルの404", get(e.h, "/static/none.css"), 404, false},
		{"他サイトからの拒否", crossRec, 403, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.rec.Code != tt.code {
				t.Fatalf("ステータス = %d, 期待 %d", tt.rec.Code, tt.code)
			}
			checkSecurityHeaders(t, tt.rec, tt.page)
		})
	}
}

// CSP の方針（スクリプト・インラインのスタイル・外部読み込みなし）を、テンプレートと CSS が守っている。
// 破ると、画面が CSP に止められて崩れる。
func TestTemplatesAndCSSFollowCSP(t *testing.T) {
	inline := regexp.MustCompile(`(?i)<script|<style|<iframe|<object|<embed|http-equiv|["'\s]style\s*=|["'\s]on[a-z]+\s*=|javascript:`)
	external := regexp.MustCompile(`(?i)(src|href|action)\s*=\s*["']?(https?:)?//`)

	err := fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(assets, path)
		if err != nil {
			return err
		}
		s := string(b)
		switch {
		case strings.HasSuffix(path, ".html"):
			if m := inline.FindString(s); m != "" {
				t.Errorf("%s：CSP に反する記述 %q", path, m)
			}
			if m := external.FindString(s); m != "" {
				t.Errorf("%s：外部への参照 %q", path, m)
			}
		case strings.HasSuffix(path, ".css"):
			if strings.Contains(s, "@import") || regexp.MustCompile(`url\(\s*['"]?(https?:)?//`).MatchString(s) {
				t.Errorf("%s：外部の読み込みがある", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// 500 の画面：内部の内容は出さず、ナビ（ほかの画面へのリンク）を残す。
func TestServerErrorPageKeepsNavigation(t *testing.T) {
	e := newEnv(t)
	e.resv.err = errBoom
	for _, path := range []string{"/", "/calendar", "/reservations"} {
		rec := get(e.h, path)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s：ステータス = %d, 期待 500", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		if strings.Contains(body, "boom") {
			t.Errorf("%s：内部のエラー内容が出ている", path)
		}
		for _, s := range []string{"うまく処理できませんでした", `href="/calendar"`, `href="/rooms"`, `role="alert"`} {
			if !strings.Contains(body, s) {
				t.Errorf("%s：本文に %q がない", path, s)
			}
		}
	}
}
