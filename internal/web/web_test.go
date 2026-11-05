package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouting(t *testing.T) {
	h, err := NewHandler()
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string // 本文に含まれるべき文字列
	}{
		{"トップ", "GET", "/", http.StatusOK, "民宿のための予約台帳"},
		{"部屋", "GET", "/rooms", http.StatusOK, "部屋の一覧と登録"},
		{"CSS", "GET", "/static/style.css", http.StatusOK, "--gold"},
		{"存在しないページは404", "GET", "/nothing", http.StatusNotFound, "お探しのページはありません"},
		{"存在しない静的ファイルは404", "GET", "/static/none.css", http.StatusNotFound, ""},
		// 全メソッドを受ける "/" が最後の受け皿になるため、405 ではなく 404 になる。
		{"トップにPOSTすると受け皿の404", "POST", "/", http.StatusNotFound, "お探しのページはありません"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("ステータス = %d, 期待 %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("本文に %q が含まれない", tt.wantBody)
			}
		})
	}
}

func TestNavigationHighlightsCurrentPage(t *testing.T) {
	h, err := NewHandler()
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/rooms", nil))

	if !strings.Contains(rec.Body.String(), `href="/rooms" aria-current="page"`) {
		t.Error("現在のページ（部屋）に aria-current が付いていない")
	}
	if strings.Contains(rec.Body.String(), `href="/" aria-current="page"`) {
		t.Error("トップにも aria-current が付いている")
	}
}
