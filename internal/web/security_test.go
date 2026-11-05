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

func checkSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for k, want := range wantSecurityHeaders {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, 期待 %q", k, got, want)
		}
	}
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
	}{
		{"トップ", get(e.h, "/"), 200},
		{"カレンダー", get(e.h, "/calendar"), 200},
		{"404", get(e.h, "/nothing"), 404},
		{"405", post(e.h, "/", nil), 405},
		{"500", get(broken.h, "/"), 500},
		{"静的ファイル", get(e.h, "/static/style.css"), 200},
		{"静的ファイルの404", get(e.h, "/static/none.css"), 404},
		{"他サイトからの拒否", crossRec, 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.rec.Code != tt.code {
				t.Fatalf("ステータス = %d, 期待 %d", tt.rec.Code, tt.code)
			}
			checkSecurityHeaders(t, tt.rec)
		})
	}
}

// CSP の方針（スクリプト・インラインのスタイル・外部読み込みなし）を、テンプレートと CSS が守っている。
// 破ると、画面が CSP に止められて崩れる。
func TestTemplatesAndCSSFollowCSP(t *testing.T) {
	inline := regexp.MustCompile(`(?i)<script|<style|\sstyle\s*=|\son[a-z]+\s*=|javascript:`)
	external := regexp.MustCompile(`(?i)(src|href|action)\s*=\s*"(https?:)?//`)

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
