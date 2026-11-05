package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/testdb"
	"github.com/katsuhiro1215/StayLink/internal/user"
)

var t0 = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*Store, user.User) {
	t.Helper()
	db := testdb.Open(t)
	u, err := user.NewStore(db).Create(t.Context(), "admin", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(db), u
}

func TestCreateAndLookup(t *testing.T) {
	s, u := setup(t)
	ctx := t.Context()

	token, err := s.Create(ctx, u.ID, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != tokenLen {
		t.Errorf("値の長さ = %d, 期待 %d", len(token), tokenLen)
	}
	got, err := s.Lookup(ctx, token, t0.Add(time.Minute))
	if err != nil || got.ID != u.ID || got.Username != "admin" {
		t.Errorf("Lookup = %+v, %v", got, err)
	}
}

// 毎回違う値になり、DB には値そのものが入っていない。
func TestTokensAreRandomAndStoredHashed(t *testing.T) {
	s, u := setup(t)
	ctx := t.Context()
	seen := map[string]bool{}
	var last string
	for i := 0; i < 20; i++ {
		tok, err := s.Create(ctx, u.ID, t0)
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatal("同じ値が2回作られた")
		}
		seen[tok] = true
		last = tok
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE encode(token_hash, 'escape') LIKE '%' || $1 || '%'`, last).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("DB に Cookie の値そのものが入っている")
	}
	var hashLen int
	if err := s.db.QueryRowContext(ctx, `SELECT octet_length(token_hash) FROM sessions LIMIT 1`).Scan(&hashLen); err != nil || hashLen != 32 {
		t.Errorf("保存された値の長さ = %d, %v（SHA-256 の32バイトを期待）", hashLen, err)
	}
}

// 有効期間の境界：絶対（12時間）と無操作（2時間）。ちょうどの時刻は「期限切れ」（Before で判定）。
func TestExpiry(t *testing.T) {
	tests := []struct {
		name   string
		visits []time.Duration // t0 からの経過時間（順に Lookup する。最後が判定対象）
		valid  bool
	}{
		{"作った直後", []time.Duration{0}, true},
		{"無操作の1時間59分59秒", []time.Duration{time.Hour*2 - time.Second}, true},
		{"無操作がちょうど2時間", []time.Duration{2 * time.Hour}, false},
		{"使い続ければ無操作では切れない（1時間ごと）", []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour}, true},
		{"使い続けても、12時間で切れる（絶対）", []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 5 * time.Hour, 6 * time.Hour, 7 * time.Hour, 8 * time.Hour, 9 * time.Hour, 10 * time.Hour, 11 * time.Hour, 12 * time.Hour}, false},
		{"11時間59分59秒はまだ有効", []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 5 * time.Hour, 6 * time.Hour, 7 * time.Hour, 8 * time.Hour, 9 * time.Hour, 10 * time.Hour, 11 * time.Hour, 12*time.Hour - time.Second}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, u := setup(t)
			ctx := t.Context()
			token, err := s.Create(ctx, u.ID, t0)
			if err != nil {
				t.Fatal(err)
			}
			var lastErr error
			for _, d := range tt.visits {
				_, lastErr = s.Lookup(ctx, token, t0.Add(d))
			}
			if tt.valid && lastErr != nil {
				t.Errorf("有効のはずが %v", lastErr)
			}
			if !tt.valid && !errors.Is(lastErr, ErrNoSession) {
				t.Errorf("無効のはずが %v", lastErr)
			}
		})
	}
}

// 期限切れを調べたとき、そのセッションは消える（再度調べても無効のまま。時計が戻っても復活しない）。
func TestExpiredSessionIsDeleted(t *testing.T) {
	s, u := setup(t)
	ctx := t.Context()
	token, _ := s.Create(ctx, u.ID, t0)
	if _, err := s.Lookup(ctx, token, t0.Add(3*time.Hour)); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Lookup = %v", err)
	}
	if _, err := s.Lookup(ctx, token, t0.Add(time.Minute)); !errors.Is(err, ErrNoSession) {
		t.Errorf("期限切れで消したセッションが、時計が戻ると復活した: %v", err)
	}
}

func TestLookupRejectsBadTokens(t *testing.T) {
	s, _ := setup(t)
	for name, tok := range map[string]string{
		"空":         "",
		"短い":        "abc",
		"長い":        strings.Repeat("a", 44),
		"長さは合うが未登録": strings.Repeat("a", tokenLen),
		"SQLらしい文字列": "' OR 1=1 --" + strings.Repeat("a", 32),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Lookup(t.Context(), tok, t0); !errors.Is(err, ErrNoSession) {
				t.Errorf("Lookup = %v, 期待 ErrNoSession", err)
			}
		})
	}
}

func TestDeleteLogsOut(t *testing.T) {
	s, u := setup(t)
	ctx := t.Context()
	a, _ := s.Create(ctx, u.ID, t0)
	b, _ := s.Create(ctx, u.ID, t0) // 別の端末
	if err := s.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, a, t0); !errors.Is(err, ErrNoSession) {
		t.Errorf("ログアウトしたのに、まだ入れる: %v", err)
	}
	if _, err := s.Lookup(ctx, b, t0); err != nil {
		t.Errorf("別の端末のセッションまで消えた: %v", err)
	}
	// 何度消してもエラーにならない。不正な値も同様。
	for _, tok := range []string{a, "", "x"} {
		if err := s.Delete(ctx, tok); err != nil {
			t.Errorf("Delete(%q) = %v", tok, err)
		}
	}
}

// 作るときに、全員分の期限切れが消える。期限内のものは残る。
func TestCreatePurgesExpired(t *testing.T) {
	s, u := setup(t)
	ctx := t.Context()
	old, _ := s.Create(ctx, u.ID, t0)
	fresh, _ := s.Create(ctx, u.ID, t0.Add(10*time.Hour))
	// 12時間と1秒後に作る：old は絶対期限切れ。fresh は2時間の無操作内ではないが、まだ作成から2時間 → 無操作で切れる。
	if _, err := s.Create(ctx, u.ID, t0.Add(10*time.Hour+30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 { // fresh と、いま作ったもの（old は消えた）
		t.Errorf("残ったセッション = %d, 期待 2", n)
	}
	if _, err := s.Lookup(ctx, old, t0.Add(10*time.Hour+30*time.Minute)); !errors.Is(err, ErrNoSession) {
		t.Errorf("old = %v", err)
	}
	if _, err := s.Lookup(ctx, fresh, t0.Add(10*time.Hour+30*time.Minute)); err != nil {
		t.Errorf("fresh = %v", err)
	}
}

// ユーザーを消すと、そのセッションも消える（外部キー）。
func TestSessionsDeletedWithUser(t *testing.T) {
	s, u := setup(t)
	ctx := t.Context()
	token, _ := s.Create(ctx, u.ID, t0)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, token, t0); !errors.Is(err, ErrNoSession) {
		t.Errorf("Lookup = %v", err)
	}
}

// 最後に使った時刻は、1分以上たっていれば更新する（無操作の判定が延びる）。
func TestLookupTouchesLastSeen(t *testing.T) {
	s, u := setup(t)
	ctx := t.Context()
	token, _ := s.Create(ctx, u.ID, t0)

	read := func() time.Time {
		var ts time.Time
		if err := s.db.QueryRowContext(ctx, `SELECT last_seen_at FROM sessions LIMIT 1`).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		return ts.UTC()
	}
	if _, err := s.Lookup(ctx, token, t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := read(); !got.Equal(t0) {
		t.Errorf("30秒後は更新しない: %v", got)
	}
	if _, err := s.Lookup(ctx, token, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := read(); !got.Equal(t0.Add(time.Minute)) {
		t.Errorf("1分後に更新する: %v", got)
	}
}
