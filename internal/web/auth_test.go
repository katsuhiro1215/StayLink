package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/room"
	"github.com/katsuhiro1215/StayLink/internal/session"
	"github.com/katsuhiro1215/StayLink/internal/user"
)

// ---- 偽の照合・セッション ----

type fakeUsers struct {
	username, password string
	calls              int
	err                error // 設定すると、照合がこのエラーで失敗する
}

func (f *fakeUsers) Authenticate(_ context.Context, username, password string) (user.User, error) {
	f.calls++
	if f.err != nil {
		return user.User{}, f.err
	}
	if user.NormalizeUsername(username) == f.username && password == f.password {
		return user.User{ID: 1, Username: f.username}, nil
	}
	return user.User{}, user.ErrInvalidCredentials
}

type fakeSessions struct {
	tokens    map[string]user.User
	created   int
	deleted   []string
	err       error // 設定すると、Lookup がこのエラーで失敗する
	createErr error
}

func (f *fakeSessions) Create(_ context.Context, userID int64, _ time.Time) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	f.created++
	tok := "token-" + strconv.Itoa(f.created) + strings.Repeat("x", 30)
	f.tokens[tok] = user.User{ID: userID, Username: "admin"}
	return tok, nil
}

func (f *fakeSessions) Lookup(_ context.Context, token string, _ time.Time) (user.User, error) {
	if f.err != nil {
		return user.User{}, f.err
	}
	if u, ok := f.tokens[token]; ok {
		return u, nil
	}
	return user.User{}, session.ErrNoSession
}

func (f *fakeSessions) Delete(_ context.Context, token string) error {
	f.deleted = append(f.deleted, token)
	delete(f.tokens, token)
	return nil
}

type authEnv struct {
	h        http.Handler
	users    *fakeUsers
	sessions *fakeSessions
	rms      *fakeStore
	resv     *fakeReservations
	now      time.Time // テストの中で進められる
}

func newAuthEnv(t *testing.T, secure bool) *authEnv {
	t.Helper()
	e := &authEnv{
		users:    &fakeUsers{username: "admin", password: "correct-horse-battery"},
		sessions: &fakeSessions{tokens: map[string]user.User{}},
		rms:      &fakeStore{nextID: 3, rooms: []room.Room{{ID: 1, Name: "松の間", Capacity: 4, IsActive: true}}},
		resv:     &fakeReservations{},
		now:      fixedNow(),
	}
	h, err := newHandler(e.rms, e.resv, func() time.Time { return e.now }, withAuth(e.users, e.sessions, secure))
	if err != nil {
		t.Fatal(err)
	}
	e.h = h
	return e
}

// do はリクエストを1つ送る。cookie が空でなければセッションの Cookie を付ける。remote は接続元（空なら 192.0.2.1）。
func (e *authEnv) do(method, path string, form url.Values, cookie, remote string) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	if remote != "" {
		req.RemoteAddr = remote
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func loginForm_(username, password, next string) url.Values {
	return url.Values{"username": {username}, "password": {password}, "next": {next}}
}

func (e *authEnv) login(t *testing.T) string {
	t.Helper()
	rec := e.do("POST", "/login", loginForm_("admin", "correct-horse-battery", "/"), "", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("ログインのステータス = %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c.Value
		}
	}
	t.Fatal("ログインしたのに Cookie がない")
	return ""
}

func sessionCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	return nil
}

// ---- 保護 ----

// 未ログインは、画面（存在しないアドレスを含む）も、変更も、ログインへ送られる。画面の中身は出ない。
func TestUnauthenticatedGetsRedirectedToLogin(t *testing.T) {
	e := newAuthEnv(t, false)
	for _, path := range []string{"/", "/calendar", "/calendar?month=2026-12", "/reservations", "/reservations/new", "/reservations/1/edit", "/rooms", "/rooms/new", "/nothing-here"} {
		t.Run(path, func(t *testing.T) {
			rec := e.do("GET", path, nil, "", "")
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("ステータス = %d, 期待 303", rec.Code)
			}
			want := "/login?next=" + url.QueryEscape(path)
			if got := rec.Header().Get("Location"); got != want {
				t.Errorf("Location = %q, 期待 %q", got, want)
			}
			if strings.Contains(rec.Body.String(), "松の間") {
				t.Error("未ログインなのに、中身が出ている")
			}
		})
	}
}

func TestUnauthenticatedCannotChangeAnything(t *testing.T) {
	e := newAuthEnv(t, false)
	before := len(e.rms.rooms)
	for _, path := range []string{"/rooms", "/rooms/1", "/reservations", "/reservations/1", "/reservations/1/cancel"} {
		rec := e.do("POST", path, validForm(), "", "")
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("POST %s：%d %q（303 で /login を期待）", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if len(e.rms.rooms) != before || len(e.resv.items) != 0 {
		t.Error("未ログインの POST で、データが変わった")
	}
}

// 間違った・期限切れの Cookie も、未ログインと同じ。
func TestInvalidCookieIsUnauthenticated(t *testing.T) {
	e := newAuthEnv(t, false)
	for _, c := range []string{"garbage", strings.Repeat("a", 43), "' OR 1=1 --"} {
		if rec := e.do("GET", "/", nil, c, ""); rec.Code != http.StatusSeeOther {
			t.Errorf("Cookie %q：%d, 期待 303", c, rec.Code)
		}
	}
}

// /login と /static/ は、未ログインでも見える。
func TestLoginPageAndStaticArePublic(t *testing.T) {
	e := newAuthEnv(t, false)
	rec := e.do("GET", "/login", nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/login = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, s := range []string{`action="/login"`, `name="username"`, `type="password"`, `autocomplete="current-password"`, "ログインする"} {
		if !strings.Contains(body, s) {
			t.Errorf("ログイン画面に %q がない", s)
		}
	}
	// ログイン前は、各画面へのリンク（ナビ）とログアウトを出さない。
	for _, s := range []string{`href="/calendar"`, `href="/rooms"`, "ログアウト"} {
		if strings.Contains(body, s) {
			t.Errorf("ログイン画面に %q が出ている", s)
		}
	}
	checkSecurityHeaders(t, rec, true)
	if rec := e.do("GET", "/static/style.css", nil, "", ""); rec.Code != http.StatusOK {
		t.Errorf("/static/style.css = %d", rec.Code)
	}
}

// ---- ログイン ----

func TestLoginSuccess(t *testing.T) {
	e := newAuthEnv(t, false)
	rec := e.do("POST", "/login", loginForm_("  ADMIN ", "correct-horse-battery", "/calendar?month=2026-12"), "", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/calendar?month=2026-12" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionCookieOf(rec)
	if c == nil {
		t.Fatal("Cookie がない")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 12*60*60 {
		t.Errorf("Cookie の属性 = %+v", c)
	}
	if c.Secure {
		t.Error("HTTP のとき、Secure は付けない（付けるとブラウザが送り返さない）")
	}
	// Cookie があれば、保護された画面が開き、ログアウトのボタンが出る。
	rec = e.do("GET", "/", nil, c.Value, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ログイン後の / = %d", rec.Code)
	}
	mustContain(t, rec, "ログアウト")
	mustContain(t, rec, `action="/logout"`)
}

func TestSecureCookie(t *testing.T) {
	t.Run("COOKIE_SECURE=1", func(t *testing.T) {
		e := newAuthEnv(t, true)
		rec := e.do("POST", "/login", loginForm_("admin", "correct-horse-battery", "/"), "", "")
		if c := sessionCookieOf(rec); c == nil || !c.Secure {
			t.Errorf("Secure が付いていない: %+v", c)
		}
	})
	t.Run("TLS 接続のとき", func(t *testing.T) {
		e := newAuthEnv(t, false)
		req := httptest.NewRequest("POST", "https://example.test/login", strings.NewReader(loginForm_("admin", "correct-horse-battery", "/").Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if c := sessionCookieOf(rec); c == nil || !c.Secure {
			t.Errorf("TLS なのに Secure が付いていない: %+v", c)
		}
	})
}

// 失敗：理由は区別せず、Cookie は出さず、入力したユーザー名は残し、パスワードは返さない。
func TestLoginFailureIsGenericAndKeepsNoPassword(t *testing.T) {
	e := newAuthEnv(t, false)
	for name, f := range map[string]url.Values{
		"パスワードが違う": loginForm_("admin", "wrong-password-1", "/rooms"),
		"ユーザーがいない": loginForm_("nobody", "correct-horse-battery", "/rooms"),
		"空":        loginForm_("", "", "/rooms"),
	} {
		t.Run(name, func(t *testing.T) {
			rec := e.do("POST", "/login", f, "", "10.0.0."+strconv.Itoa(len(name))+":1") // 回数制限に当たらないよう、接続元を変える
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("ステータス = %d, 期待 401", rec.Code)
			}
			if c := sessionCookieOf(rec); c != nil {
				t.Errorf("失敗したのに Cookie が出ている: %+v", c)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "ユーザー名かパスワードが違います。") {
				t.Error("メッセージがない")
			}
			if strings.Contains(body, "wrong-password-1") || strings.Contains(body, "correct-horse-battery") {
				t.Error("パスワードが画面に戻っている")
			}
			if !strings.Contains(body, `name="next" value="/rooms"`) {
				t.Error("戻り先が引き継がれていない")
			}
		})
	}
	// 同じメッセージで、ユーザーの有無が分からない。
	a := e.do("POST", "/login", loginForm_("admin", "x", "/"), "", "10.1.0.1:1").Body.String()
	b := e.do("POST", "/login", loginForm_("nobody", "x", "/"), "", "10.1.0.2:1").Body.String()
	strip := func(s, name string) string { return strings.ReplaceAll(s, name, "") }
	if strip(a, "admin") != strip(b, "nobody") {
		t.Error("ユーザーがいる場合といない場合で、応答が違う")
	}
	// ユーザー名は入れ直さなくてよいよう残す。
	mustContain(t, e.do("POST", "/login", loginForm_("admin", "x", "/"), "", "10.1.0.3:1"), `value="admin"`)
}

// ログイン後の戻り先は、自分のサイトのパスだけ（オープンリダイレクトの防止）。
func TestLoginNextIsSanitized(t *testing.T) {
	tests := []struct{ next, want string }{
		{"/calendar?month=2026-11", "/calendar?month=2026-11"},
		{"/reservations/3/edit", "/reservations/3/edit"},
		{"", "/"},
		{"calendar", "/"},
		{"//evil.example/path", "/"},
		{"///evil.example", "/"},
		{"https://evil.example/", "/"},
		{"http://evil.example/", "/"},
		{"javascript:alert(1)", "/"},
		{`/\evil.example`, "/"},
		{`/\\evil.example`, "/"},
		{"/path\r\nSet-Cookie: x=1", "/"},
		{"/path\tx", "/"},
		{"/login", "/"},
		{"/login?next=/rooms", "/"},
		{"/static/style.css", "/"},
		{"/ok@evil.example", "/ok@evil.example"}, // パスの一部。ホストにはならない
	}
	for _, tt := range tests {
		t.Run(tt.next, func(t *testing.T) {
			e := newAuthEnv(t, false)
			rec := e.do("POST", "/login", loginForm_("admin", "correct-horse-battery", tt.next), "", "")
			if got := rec.Header().Get("Location"); got != tt.want {
				t.Errorf("戻り先 = %q, 期待 %q", got, tt.want)
			}
		})
	}
	// GET /login?next=... をフォームに引き継ぐときも、同じ検査を通る。
	e := newAuthEnv(t, false)
	body := e.do("GET", "/login?next="+url.QueryEscape("//evil.example"), nil, "", "").Body.String()
	if strings.Contains(body, "evil.example") {
		t.Error("危険な next がフォームに入っている")
	}
}

// ログインのたびに新しい値を発行し、前のセッションは消す（セッション固定化の対策）。
func TestLoginIssuesFreshSessionAndDropsOld(t *testing.T) {
	e := newAuthEnv(t, false)
	old := e.login(t)
	rec := e.do("POST", "/login", loginForm_("admin", "correct-horse-battery", "/"), old, "")
	fresh := sessionCookieOf(rec)
	if fresh == nil || fresh.Value == old {
		t.Fatalf("新しい値が発行されていない: %+v", fresh)
	}
	if len(e.sessions.deleted) != 1 || e.sessions.deleted[0] != old {
		t.Errorf("以前のセッションを消していない: %v", e.sessions.deleted)
	}
	if rec := e.do("GET", "/", nil, old, ""); rec.Code != http.StatusSeeOther {
		t.Error("以前のセッションの値が、まだ使える")
	}
}

func TestLoginPageWhenAlreadyLoggedInRedirects(t *testing.T) {
	e := newAuthEnv(t, false)
	tok := e.login(t)
	rec := e.do("GET", "/login?next=/rooms", nil, tok, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/rooms" {
		t.Errorf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// ---- ログアウト ----

func TestLogout(t *testing.T) {
	e := newAuthEnv(t, false)
	tok := e.login(t)
	rec := e.do("POST", "/logout", url.Values{}, tok, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := sessionCookieOf(rec); c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("Cookie を失効させていない: %+v", c)
	}
	if len(e.sessions.deleted) != 1 || e.sessions.deleted[0] != tok {
		t.Errorf("セッションを消していない: %v", e.sessions.deleted)
	}
	if rec := e.do("GET", "/", nil, tok, ""); rec.Code != http.StatusSeeOther {
		t.Error("ログアウトしたのに、まだ入れる")
	}
}

// ログアウトは POST だけ（リンクを踏ませて外されない）。
func TestLogoutOnlyPost(t *testing.T) {
	e := newAuthEnv(t, false)
	tok := e.login(t)
	// GET は、どの画面にも一致せず 404 になる（ログアウトしない）。
	if rec := e.do("GET", "/logout", nil, tok, ""); rec.Code != http.StatusNotFound || len(e.sessions.deleted) != 0 {
		t.Errorf("GET /logout = %d, 削除=%v（404 で、セッションは消えないこと）", rec.Code, e.sessions.deleted)
	}
	if rec := e.do("GET", "/", nil, tok, ""); rec.Code != http.StatusOK {
		t.Error("GET /logout で、ログアウトしてしまった")
	}
}

// ---- 他サイトからの送信 ----

func TestCrossOriginLoginIsRejected(t *testing.T) {
	e := newAuthEnv(t, false)
	rec := post(e.h, "/login", loginForm_("admin", "correct-horse-battery", "/"), "Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusForbidden {
		t.Errorf("ステータス = %d, 期待 403", rec.Code)
	}
	if e.users.calls != 0 {
		t.Error("他サイトからの送信で、照合が行われた")
	}
	tok := e.login(t)
	req := httptest.NewRequest("POST", "/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || len(e.sessions.deleted) != 0 {
		t.Errorf("他サイトからのログアウト：%d, 削除=%v", rec.Code, e.sessions.deleted)
	}
}

// ---- 回数制限 ----

func TestLoginThrottle(t *testing.T) {
	e := newAuthEnv(t, false)
	const ip = "198.51.100.7:4000"
	bad := loginForm_("admin", "wrong-password-1", "/")
	good := loginForm_("admin", "correct-horse-battery", "/")

	for i := 1; i <= 5; i++ {
		if rec := e.do("POST", "/login", bad, "", ip); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%d 回目：%d, 期待 401", i, rec.Code)
		}
	}
	calls := e.users.calls
	// ロック中は、正しいパスワードでも拒否する。照合（重い計算）もしない。
	rec := e.do("POST", "/login", good, "", ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("ロック中：%d, 期待 429", rec.Code)
	}
	mustContain(t, rec, "しばらく使えません")
	if sessionCookieOf(rec) != nil {
		t.Error("ロック中なのに、セッションが発行された")
	}
	if e.users.calls != calls {
		t.Error("ロック中に、照合が行われた")
	}
	// 別のIPは影響を受けない。ポート番号が違っても、同じIPとして数える。
	if rec := e.do("POST", "/login", good, "", "198.51.100.8:4000"); rec.Code != http.StatusSeeOther {
		t.Errorf("別のIP：%d, 期待 303", rec.Code)
	}
	if rec := e.do("POST", "/login", good, "", "198.51.100.7:5555"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("同じIPで別ポート：%d, 期待 429", rec.Code)
	}
	// 14分59秒後はまだ。15分後に解除される。
	e.now = e.now.Add(lockDuration - time.Second)
	if rec := e.do("POST", "/login", good, "", ip); rec.Code != http.StatusTooManyRequests {
		t.Errorf("解除の直前：%d, 期待 429", rec.Code)
	}
	e.now = e.now.Add(time.Second)
	if rec := e.do("POST", "/login", good, "", ip); rec.Code != http.StatusSeeOther {
		t.Errorf("解除後：%d, 期待 303", rec.Code)
	}
}

// 成功すると数え直す。4回失敗 → 成功 → 4回失敗でも、ロックされない。
func TestLoginSuccessResetsFailures(t *testing.T) {
	e := newAuthEnv(t, false)
	const ip = "198.51.100.9:1"
	bad := loginForm_("admin", "wrong-password-1", "/")
	for i := 0; i < 4; i++ {
		e.do("POST", "/login", bad, "", ip)
	}
	if rec := e.do("POST", "/login", loginForm_("admin", "correct-horse-battery", "/"), "", ip); rec.Code != http.StatusSeeOther {
		t.Fatalf("成功のはずが %d", rec.Code)
	}
	for i := 0; i < 4; i++ {
		if rec := e.do("POST", "/login", bad, "", ip); rec.Code != http.StatusUnauthorized {
			t.Fatalf("成功のあとの %d 回目の失敗：%d, 期待 401（ロックされない）", i+1, rec.Code)
		}
	}
}

// 窓（15分）が過ぎれば、古い失敗は数えない。
func TestLoginFailuresExpireAfterWindow(t *testing.T) {
	e := newAuthEnv(t, false)
	const ip = "198.51.100.10:1"
	bad := loginForm_("admin", "wrong-password-1", "/")
	for i := 0; i < 4; i++ {
		e.do("POST", "/login", bad, "", ip)
	}
	e.now = e.now.Add(failureWindow)
	for i := 0; i < 4; i++ { // 窓が切れて数え直し：あと4回失敗してもロックされない
		if rec := e.do("POST", "/login", bad, "", ip); rec.Code != http.StatusUnauthorized {
			t.Fatalf("窓のあとの %d 回目：%d, 期待 401", i+1, rec.Code)
		}
	}
}

// 長すぎる入力は、照合せずに失敗として扱う（失敗に数える）。大きすぎる本文は 413。
func TestLoginInputLimits(t *testing.T) {
	e := newAuthEnv(t, false)

	rec := e.do("POST", "/login", loginForm_("admin", strings.Repeat("a", maxPasswordBytes+1), "/"), "", "203.0.113.1:1")
	if rec.Code != http.StatusUnauthorized || e.users.calls != 0 {
		t.Errorf("長いパスワード：%d, 照合=%d回（401・照合なしを期待）", rec.Code, e.users.calls)
	}
	rec = e.do("POST", "/login", loginForm_(strings.Repeat("a", maxUsernameBytes+1), "x", "/"), "", "203.0.113.2:1")
	if rec.Code != http.StatusUnauthorized || e.users.calls != 0 {
		t.Errorf("長いユーザー名：%d, 照合=%d回", rec.Code, e.users.calls)
	}
	rec = e.do("POST", "/login", loginForm_("admin", strings.Repeat("a", maxLoginBody+1), "/"), "", "203.0.113.3:1")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("大きすぎる本文：%d, 期待 413", rec.Code)
	}
	// 上限ちょうどのパスワードは、照合まで進む。
	e.do("POST", "/login", loginForm_("admin", strings.Repeat("a", maxPasswordBytes), "/"), "", "203.0.113.4:1")
	if e.users.calls != 1 {
		t.Errorf("上限ちょうどの入力が、照合されていない（%d回）", e.users.calls)
	}
}

// ---- 障害 ----

// セッションの確認で DB が失敗したら、通さず（失敗を未ログインに読み替えず）、500 にする。内部の内容は出さない。
func TestSessionStoreFailureIsServerError(t *testing.T) {
	e := newAuthEnv(t, false)
	tok := e.login(t)
	e.sessions.err = errBoom
	for _, path := range []string{"/", "/login"} {
		rec := e.do("GET", path, nil, tok, "")
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("%s：%d（500 で、内部の内容を出さないこと）", path, rec.Code)
		}
	}
}

func TestLoginServerErrors(t *testing.T) {
	t.Run("照合が失敗", func(t *testing.T) {
		e := newAuthEnv(t, false)
		e.users.err = errBoom
		rec := e.do("POST", "/login", loginForm_("admin", "x", "/"), "", "")
		if rec.Code != http.StatusInternalServerError || sessionCookieOf(rec) != nil {
			t.Errorf("%d", rec.Code)
		}
	})
	t.Run("セッションを作れない", func(t *testing.T) {
		e := newAuthEnv(t, false)
		e.sessions.createErr = errBoom
		rec := e.do("POST", "/login", loginForm_("admin", "correct-horse-battery", "/"), "", "")
		if rec.Code != http.StatusInternalServerError || sessionCookieOf(rec) != nil {
			t.Errorf("%d", rec.Code)
		}
	})
}

// ログインを求めない構成（既存のテスト）では、ログアウトのボタンを出さない。
func TestNoLogoutButtonWithoutAuth(t *testing.T) {
	rec := get(newEnv(t).h, "/")
	if strings.Contains(rec.Body.String(), "ログアウト") {
		t.Error("ログインを求めないのに、ログアウトが出ている")
	}
}

// ---- 回数制限の部品 ----

func TestLimiterSweepKeepsMemoryBounded(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for i := 0; i < maxLimiterKeys+500; i++ {
		l.Fail("ip-"+strconv.Itoa(i), now)
	}
	if n := len(l.entries); n > maxLimiterKeys {
		t.Errorf("記録の数 = %d, 上限 %d を超えた", n, maxLimiterKeys)
	}
	// 窓が切れたものは、掃除で消える。
	l.Fail("new", now.Add(failureWindow))
	if n := len(l.entries); n > 2 {
		t.Errorf("期限切れの掃除をしていない: %d", n)
	}
}
