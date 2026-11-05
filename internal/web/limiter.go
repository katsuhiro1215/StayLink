package web

import (
	"sync"
	"time"
)

// ログインの失敗の回数制限（総当たりへの対策）。接続元のIPアドレスごとに数える。メモリ内だけ（再起動で消える）。
//
//   - 窓の15分のあいだに5回失敗したら、そこから15分ロックする。
//   - ロック中は、正しいパスワードでも拒否する（ロックしないと、総当たりが止まらないため）。
//   - 成功したら、そのIPの記録を消す。
//
// 限界：
//   - 同じIPを共有する人（NAT）を巻き込む。リバースプロキシ越しだと、全員が同じIPに見える（X-Forwarded-For は信用しない）。
//   - 攻撃者が自分のIPでロックしても、他のIPからの正しいログインは止まらない。ただし、管理者と同じIPからの攻撃は、管理者を締め出す。
//   - 多数のIPから少しずつ試す攻撃は、IPごとの制限では止まらない（パスワードの強さと、ハッシュの重さで守る）。
const (
	maxFailures    = 5
	failureWindow  = 15 * time.Minute
	lockDuration   = 15 * time.Minute
	maxLimiterKeys = 10_000 // 記録するIPの上限。超えたら、期限の切れたものを掃除する
)

type attempt struct {
	failures    int
	windowStart time.Time
	lockedUntil time.Time
}

type limiter struct {
	mu      sync.Mutex
	entries map[string]*attempt
}

func newLimiter() *limiter { return &limiter{entries: map[string]*attempt{}} }

// Locked は、いま（now）ロック中か。ロック中なら、解除される時刻も返す。
func (l *limiter) Locked(key string, now time.Time) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.entries[key]
	if a != nil && now.Before(a.lockedUntil) {
		return true, a.lockedUntil
	}
	return false, time.Time{}
}

// Fail は、失敗を1回記録する。上限に達したら、ロックする。
func (l *limiter) Fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) >= maxLimiterKeys {
		l.sweep(now)
	}
	a := l.entries[key]
	if a == nil {
		a = &attempt{}
		l.entries[key] = a
	}
	if a.failures == 0 || !now.Before(a.windowStart.Add(failureWindow)) {
		a.failures, a.windowStart = 0, now // 窓が切れていたら、数え直す
	}
	a.failures++
	if a.failures >= maxFailures {
		a.lockedUntil = now.Add(lockDuration)
		a.failures = 0
	}
}

// Reset は、成功したIPの記録を消す。
func (l *limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// sweep は、ロックも窓も切れた記録を消す。呼ぶ側が mu を持っていること。
// 掃除しても上限を超えたままなら（全部が有効）、新しい記録を増やさないよう、いちばん古い窓のものを消す。
func (l *limiter) sweep(now time.Time) {
	for k, a := range l.entries {
		if !now.Before(a.lockedUntil) && !now.Before(a.windowStart.Add(failureWindow)) {
			delete(l.entries, k)
		}
	}
	if len(l.entries) < maxLimiterKeys {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, a := range l.entries {
		if oldestKey == "" || a.windowStart.Before(oldest) {
			oldestKey, oldest = k, a.windowStart
		}
	}
	delete(l.entries, oldestKey)
}
