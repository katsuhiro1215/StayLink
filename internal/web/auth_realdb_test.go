package web

// ログインを、実DBを通して確かめる（管理者の作成 → ログイン → 保護された画面 → 時間の経過 → ログアウト）。
// セッションの有効期間と、DB の時刻・タイムゾーンの往復は、偽の保存先では確かめられない。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
	"github.com/katsuhiro1215/StayLink/internal/room"
	"github.com/katsuhiro1215/StayLink/internal/session"
	"github.com/katsuhiro1215/StayLink/internal/testdb"
	"github.com/katsuhiro1215/StayLink/internal/user"
)

func TestRealDBLoginFlow(t *testing.T) {
	db := testdb.Open(t)
	ctx := t.Context()
	users := user.NewStore(db)
	if _, err := users.Create(ctx, "Admin", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	h, err := newHandler(room.NewStore(db), reservation.NewStore(db), func() time.Time { return now },
		withAuth(users, session.NewStore(db), false))
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		var req *http.Request
		if form != nil {
			req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 未ログイン → ログインへ。
	if rec := do("GET", "/rooms", nil, nil); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("未ログイン：%d %q", rec.Code, rec.Header().Get("Location"))
	}
	// 違うパスワード → 入れない。
	if rec := do("POST", "/login", loginForm_("admin", "wrong-password-1", "/rooms"), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("違うパスワード：%d", rec.Code)
	}
	// 正しいパスワード（ユーザー名は大文字小文字を問わない）→ 入れる。
	rec := do("POST", "/login", loginForm_(" ADMIN", "correct-horse-battery", "/rooms"), nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/rooms" {
		t.Fatalf("ログイン：%d %q", rec.Code, rec.Header().Get("Location"))
	}
	cookie := sessionCookieOf(rec)
	if cookie == nil {
		t.Fatal("Cookie がない")
	}
	if rec := do("GET", "/rooms", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("ログイン後の /rooms = %d", rec.Code)
	}
	// 保護された変更も、ログイン後は通る。
	if rec := do("POST", "/rooms", url.Values{"name": {"松の間"}, "capacity": {"4"}, "sort_order": {"0"}}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("ログイン後の部屋の登録：%d", rec.Code)
	}

	// 時間が進む：無操作の2時間を過ぎると、入れない（実DBの時刻の往復で判定）。
	now = now.Add(session.IdleTimeout)
	if rec := do("GET", "/rooms", nil, cookie); rec.Code != http.StatusSeeOther {
		t.Errorf("無操作の2時間後：%d, 期待 303（入れない）", rec.Code)
	}

	// 入り直して、ログアウト → 入れない。
	rec = do("POST", "/login", loginForm_("admin", "correct-horse-battery", "/"), nil)
	cookie = sessionCookieOf(rec)
	if cookie == nil {
		t.Fatal("入り直せない")
	}
	if rec := do("POST", "/logout", url.Values{}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("ログアウト：%d", rec.Code)
	}
	if rec := do("GET", "/rooms", nil, cookie); rec.Code != http.StatusSeeOther {
		t.Errorf("ログアウト後：%d, 期待 303", rec.Code)
	}
	// DB に、セッションが残っていない。
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("残っているセッション = %d, %v", n, err)
	}
}
