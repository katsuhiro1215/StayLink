package web

import (
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// ログインの回数制限（総当たりへの対策）。接続元のIPアドレスごとに数える。メモリ内だけ（再起動で消える）。
//
//   - 窓の15分のあいだに、試行を5回まで許す。5回目の試行が始まった時点で、そこから15分ロックする。
//   - 試行は、照合の「前」に数える（Reserve）。照合は重い（約0.2秒）ので、後で数えると、
//     その間に同時に届いた要求が、すべて制限をすり抜けてしまうため。
//   - ロック中は、正しいパスワードでも拒否する。成功したら、そのIPの記録を消す（Reset）。
//   - IPv6 は、先頭64ビット（/64）を1つのIPとして数える（端末は /64 の中の多数のアドレスを使えるため）。
//
// 限界：
//   - 同じIPを共有する人（NAT）を巻き込む。リバースプロキシ越しだと、全員が同じIPに見える（X-Forwarded-For は信用しない）。
//     その場合、LAN内の誰か1人の失敗で、管理者もロックされる。解除は、サーバーの再起動（メモリ上の記録が消える）。
//   - 多数のIPから少しずつ試す攻撃は、IPごとの制限では止まらない（パスワードの強さと、ハッシュの重さで守る）。
//   - 記録するIPは1万件まで。ロック中の記録は、掃除でも消さない。全部がロック中で満杯のときに来た新しいIPは、
//     記録されず、制限なしで通る（メモリを守るための割り切り）。
const (
	maxAttempts    = 5
	attemptWindow  = 15 * time.Minute
	lockDuration   = 15 * time.Minute
	maxLimiterKeys = 10_000
)

type attempt struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

type limiter struct {
	mu      sync.Mutex
	entries map[string]*attempt
}

func newLimiter() *limiter { return &limiter{entries: map[string]*attempt{}} }

// Reserve は、試行を1回、先に数える。許可なら ok=true。ロック中なら ok=false と、解除までの時間を返す。
// 許可した試行が、窓の中で上限（maxAttempts）に達したら、そこからロックする（その試行自体は許可する）。
func (l *limiter) Reserve(key string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	a := l.entries[key]
	if a != nil && now.Before(a.lockedUntil) {
		return false, a.lockedUntil.Sub(now)
	}
	if a == nil {
		if len(l.entries) >= maxLimiterKeys {
			l.sweep(now)
		}
		if len(l.entries) >= maxLimiterKeys {
			return true, 0 // 全部がロック中で満杯：記録せずに通す（上の限界を参照）
		}
		a = &attempt{}
		l.entries[key] = a
	}
	if a.count == 0 || !now.Before(a.windowStart.Add(attemptWindow)) {
		a.count, a.windowStart = 0, now // 窓が切れていたら、数え直す
	}
	a.count++
	if a.count >= maxAttempts {
		a.lockedUntil = now.Add(lockDuration)
		a.count = 0
	}
	return true, 0
}

// Reset は、成功したIPの記録を消す。
func (l *limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// sweep は、ロックも窓も切れた記録を消す。呼ぶ側が mu を持っていること。
// ロック中の記録は消さない（大量のIPで押し出して、ロックを外されないように）。
func (l *limiter) sweep(now time.Time) {
	for k, a := range l.entries {
		if !now.Before(a.lockedUntil) && !now.Before(a.windowStart.Add(attemptWindow)) {
			delete(l.entries, k)
		}
	}
	if len(l.entries) < maxLimiterKeys {
		return
	}
	// まだ満杯：ロック中でない記録のうち、窓がいちばん古いものを1つ消す。
	var oldestKey string
	var oldest time.Time
	for k, a := range l.entries {
		if now.Before(a.lockedUntil) {
			continue
		}
		if oldestKey == "" || a.windowStart.Before(oldest) {
			oldestKey, oldest = k, a.windowStart
		}
	}
	if oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

// clientKey は、回数制限に使う、接続元のキー。ポート番号は除く。IPv4-mapped IPv6 は IPv4 に戻し、
// IPv6 は /64 に丸める。解釈できないときは、アドレスの文字列そのまま。
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}
