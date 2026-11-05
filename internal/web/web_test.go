package web

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := NewHandler(&fakeStore{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestRouting(t *testing.T) {
	h := newTestHandler(t)

	tests := []struct {
		name            string
		method          string
		path            string
		wantStatus      int
		wantBody        string // 本文に含まれるべき文字列（空なら検証しない）
		wantContentType string // 空なら検証しない
		wantAllow       string // 405 のときの Allow ヘッダ（空なら検証しない）
	}{
		{"トップ", "GET", "/", 200, "民宿のための予約台帳", "text/html; charset=utf-8", ""},
		{"トップはHEADも受ける", "HEAD", "/", 200, "", "text/html; charset=utf-8", ""},
		{"部屋", "GET", "/rooms", 200, "部屋を登録する", "text/html; charset=utf-8", ""},
		{"CSS", "GET", "/static/style.css", 200, "--accent", "text/css; charset=utf-8", ""},
		{"存在しないページは404画面", "GET", "/nothing", 404, "お探しのページはありません", "text/html; charset=utf-8", ""},
		{"末尾スラッシュ付きの/rooms/は404", "GET", "/rooms/", 404, "お探しのページはありません", "", ""},
		{"存在しない静的ファイルは404", "GET", "/static/none.css", 404, "404 page not found", "", ""},
		{"登録済みパスへの未対応メソッドは405", "PUT", "/rooms", 405, "", "", "GET, HEAD, POST"},
		{"トップへのPOSTも405", "POST", "/", 405, "", "", "GET, HEAD"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("ステータス = %d, 期待 %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("本文に %q が含まれない", tt.wantBody)
			}
			if tt.wantContentType != "" {
				if got := rec.Header().Get("Content-Type"); got != tt.wantContentType {
					t.Errorf("Content-Type = %q, 期待 %q", got, tt.wantContentType)
				}
			}
			if tt.wantAllow != "" {
				if got := rec.Header().Get("Allow"); got != tt.wantAllow {
					t.Errorf("Allow = %q, 期待 %q", got, tt.wantAllow)
				}
			}
		})
	}
}

func TestNavigationHighlightsCurrentPage(t *testing.T) {
	h := newTestHandler(t)

	tests := []struct {
		name        string
		path        string
		wantCurrent string // aria-current が付くはずのリンク先（空なら、どこにも付かない）
	}{
		{"トップ", "/", "/"},
		{"部屋", "/rooms", "/rooms"},
		{"404画面ではどれも強調しない", "/nothing", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", tt.path, nil))
			body := rec.Body.String()

			for _, href := range []string{"/", "/rooms"} {
				has := strings.Contains(body, `href="`+href+`" aria-current="page"`)
				want := href == tt.wantCurrent
				if has != want {
					t.Errorf("href=%q の aria-current: あり=%v, 期待 %v", href, has, want)
				}
			}
		})
	}
}

// 描画に失敗したときは、壊れた HTML を 200 で返さず 500 にする。
func TestRenderFailureReturns500(t *testing.T) {
	// 存在しないフィールドを参照するテンプレートは、実行時に失敗する。
	broken := template.Must(template.New("layout").Parse("{{.Missing}}"))
	p := pages{"broken": broken}

	rec := httptest.NewRecorder()
	p.render(rec, http.StatusOK, "broken", pageData{})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("ステータス = %d, 期待 500", rec.Code)
	}
}

func TestRenderUnknownTemplateReturns500(t *testing.T) {
	rec := httptest.NewRecorder()
	pages{}.render(rec, http.StatusOK, "unknown", pageData{})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("ステータス = %d, 期待 500", rec.Code)
	}
}
