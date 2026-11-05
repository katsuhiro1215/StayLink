package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/user"
)

// ---- 回数制限：同時に届いた要求 ----

// 同じIPから同時に大量に来ても、許可されるのは上限まで（試行を、照合の前に数えているため）。
func TestLimiterReserveIsAtomicUnderConcurrency(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Reserve("203.0.113.5", now); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != maxAttempts {
		t.Errorf("同時の200件のうち、許可されたのは %d 件, 期待 %d", allowed, maxAttempts)
	}
}

func TestLimiterBoundaries(t *testing.T) {
	l := newLimiter()
	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= maxAttempts; i++ {
		if ok, _ := l.Reserve("k", t0); !ok {
			t.Fatalf("%d 回目が拒否された", i)
		}
	}
	ok, retry := l.Reserve("k", t0)
	if ok || retry != lockDuration {
		t.Fatalf("上限の次：ok=%v retry=%v, 期待 false / %v", ok, retry, lockDuration)
	}
	// 解除の1秒前はまだ。ちょうどで解除。
	if ok, retry := l.Reserve("k", t0.Add(lockDuration-time.Second)); ok || retry != time.Second {
		t.Errorf("解除の1秒前：ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Reserve("k", t0.Add(lockDuration)); !ok {
		t.Error("解除の時刻になっても、拒否されている")
	}
	// Reset すると、記録が消える。
	l.Reset("k")
	if len(l.entries) != 0 {
		t.Error("Reset で記録が消えていない")
	}
}

// 満杯のとき、ロック中の記録は消さない（大量のIPで押し出して、ロックを外せない）。
func TestLimiterKeepsLockedEntriesWhenFull(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	l.entries["victim"] = &attempt{windowStart: now, lockedUntil: now.Add(lockDuration)}
	for i := 0; i < maxLimiterKeys-1; i++ { // 窓の開始が新しい記録で、満杯にする
		l.entries["ip-"+strconv.Itoa(i)] = &attempt{count: 1, windowStart: now.Add(time.Minute)}
	}
	// 新しいIPが来て、掃除と押し出しが走る。
	for i := 0; i < 50; i++ {
		l.Reserve("new-"+strconv.Itoa(i), now.Add(2*time.Minute))
	}
	if ok, _ := l.Reserve("victim", now.Add(2*time.Minute)); ok {
		t.Error("ロック中の記録が、押し出されて消えた")
	}
	if len(l.entries) > maxLimiterKeys {
		t.Errorf("記録の数 = %d, 上限 %d を超えた", len(l.entries), maxLimiterKeys)
	}
}

// 全部がロック中で満杯なら、新しいIPは、記録せずに通す（メモリを守る割り切り）。ロック中の記録は残る。
func TestLimiterAllLockedFull(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for i := 0; i < maxLimiterKeys; i++ {
		l.entries["ip-"+strconv.Itoa(i)] = &attempt{windowStart: now, lockedUntil: now.Add(lockDuration)}
	}
	if ok, _ := l.Reserve("brand-new", now); !ok {
		t.Error("新しいIPが拒否された")
	}
	if len(l.entries) != maxLimiterKeys {
		t.Errorf("記録の数 = %d", len(l.entries))
	}
	if ok, _ := l.Reserve("ip-0", now); ok {
		t.Error("ロック中の記録が消えた")
	}
}

// ---- 接続元のキー ----

func TestClientKey(t *testing.T) {
	tests := []struct{ remote, want string }{
		{"192.0.2.1:5000", "192.0.2.1"},
		{"192.0.2.1:6000", "192.0.2.1"},               // ポートは除く
		{"[::ffff:192.0.2.1]:5000", "192.0.2.1"},      // IPv4-mapped は IPv4 に戻す
		{"[2001:db8:1:2::1]:80", "2001:db8:1:2::/64"}, // IPv6 は /64 に丸める
		{"[2001:db8:1:2:ffff:ffff:ffff:ffff]:80", "2001:db8:1:2::/64"},
		{"[2001:db8:1:3::1]:80", "2001:db8:1:3::/64"}, // 別の /64
		{"@", "@"}, // 解釈できないものは、そのまま
	}
	for _, tt := range tests {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = tt.remote
		if got := clientKey(req); got != tt.want {
			t.Errorf("clientKey(%q) = %q, 期待 %q", tt.remote, got, tt.want)
		}
	}
}

// 同じ /64 の別アドレスに替えても、回数制限を回避できない。
func TestThrottleIPv6SamePrefix(t *testing.T) {
	e := newAuthEnv(t, false)
	bad := loginForm_("admin", "wrong-password-1", "/")
	for i := 0; i < maxAttempts; i++ {
		e.do("POST", "/login", bad, "", "[2001:db8:1:2::"+strconv.Itoa(i+1)+"]:4000")
	}
	if rec := e.do("POST", "/login", loginForm_("admin", "correct-horse-battery", "/"), "", "[2001:db8:1:2::ffff]:4000"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("同じ /64 の別アドレス：%d, 期待 429", rec.Code)
	}
	if rec := e.do("POST", "/login", loginForm_("admin", "correct-horse-battery", "/"), "", "[2001:db8:1:9::1]:4000"); rec.Code != http.StatusSeeOther {
		t.Errorf("別の /64：%d, 期待 303", rec.Code)
	}
}

// ---- 429 の Retry-After ----

func TestThrottleResponseHasRetryAfter(t *testing.T) {
	e := newAuthEnv(t, false)
	const ip = "198.51.100.20:1"
	for i := 0; i < maxAttempts; i++ {
		e.do("POST", "/login", loginForm_("admin", "wrong-password-1", "/"), "", ip)
	}
	rec := e.do("POST", "/login", loginForm_("admin", "x", "/"), "", ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("%d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != strconv.Itoa(int(lockDuration.Seconds())+1) {
		t.Errorf("Retry-After = %q", got)
	}
}

// ---- 同時に走らせる照合の数 ----

// blockingUsers は、release が閉じられるまで、照合の中で止まる。
type blockingUsers struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingUsers) Authenticate(_ context.Context, _, _ string) (user.User, error) {
	b.entered <- struct{}{}
	<-b.release
	return user.User{}, user.ErrInvalidCredentials
}

// 照合が詰まっているあいだに、さらに来た要求は、待たせずに 503 にする（CPUを使い切られない）。
func TestLoginConcurrencyLimit(t *testing.T) {
	e := newAuthEnv(t, false)
	gate := &blockingUsers{release: make(chan struct{}), entered: make(chan struct{}, maxConcurrentLogins+4)}
	h, err := newHandler(e.rms, e.resv, func() time.Time { return e.now }, withAuth(gate, e.sessions, false))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < maxConcurrentLogins; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/login", strings.NewReader(loginForm_("admin", "x", "/").Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.RemoteAddr = "192.0.2." + strconv.Itoa(10+i) + ":1"
			h.ServeHTTP(httptest.NewRecorder(), req)
		}(i)
	}
	for i := 0; i < maxConcurrentLogins; i++ { // 全部が照合の中に入るまで待つ
		select {
		case <-gate.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("照合に入らない")
		}
	}

	// 満杯のあいだに来た要求は、待たずに終わる（待たされるなら、上限が効いていない）。
	req := httptest.NewRequest("POST", "/login", strings.NewReader(loginForm_("admin", "x", "/").Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.99:1"
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		close(gate.release) // 詰まっている要求を解放してから、失敗にする
		t.Fatal("満杯なのに、5つ目の要求が待たされている（同時数の上限が効いていない）")
	}
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("満杯のとき：%d Retry-After=%q, 期待 503 / 1", rec.Code, rec.Header().Get("Retry-After"))
	}
	close(gate.release)
	wg.Wait()

	// 空いたら、また照合できる。
	req = httptest.NewRequest("POST", "/login", strings.NewReader(loginForm_("admin", "x", "/").Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.98:1"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusServiceUnavailable {
		t.Error("空いたのに、503 のまま")
	}
}

// ---- 認証の迂回の揺れ ----

// パスの書き方を変えても、未ログインでは、保護された画面が見えない。
func TestAuthPathVariantsDoNotBypass(t *testing.T) {
	e := newAuthEnv(t, false)
	for _, path := range []string{
		"/static", "/staticfoo", "/staticx/style.css", // /static/ のプレフィックスの取り違え
		"/login/", "/login/x", "/loginx", "/LOGIN", "/Static/style.css", // /login・/static/ の取り違え
		"//rooms", "/rooms/", "/rooms/../rooms", "/./rooms", "/rooms%2f", "/%72ooms", // パスの揺れ
	} {
		t.Run(path, func(t *testing.T) {
			rec := e.do("GET", path, nil, "", "")
			// /login ちょうどと /static/ で始まるもの以外は、すべてログインへ送られる（404 の画面も見せない）。
			if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
				t.Errorf("%d %q（303 で /login を期待）", rec.Code, rec.Header().Get("Location"))
			}
			if strings.Contains(rec.Body.String(), "松の間") {
				t.Error("未ログインなのに、中身が出ている")
			}
		})
	}
	// /static/ の外は、メソッドを変えても、ログインへ送られる。
	for _, m := range []string{"HEAD", "OPTIONS", "PUT", "DELETE", "PATCH"} {
		rec := e.do(m, "/rooms", nil, "", "")
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s /rooms = %d, 期待 303", m, rec.Code)
		}
	}
	// /static/../ を使っても、保護された画面の中身は出ない（ServeMux が整えて、ログインへ送る）。
	rec := e.do("GET", "/static/../rooms", nil, "", "")
	if strings.Contains(rec.Body.String(), "松の間") {
		t.Error("/static/../rooms で中身が出た")
	}
}

// ログイン後の戻り先に、POST専用の /logout は入れない。
func TestSafeNextExcludesLogout(t *testing.T) {
	for _, n := range []string{"/logout", "/logout?x=1"} {
		if got := safeNext(n); got != "/" {
			t.Errorf("safeNext(%q) = %q, 期待 /", n, got)
		}
	}
}
