// Package session は、ログイン後のセッション（Cookie の乱数 ⇔ ユーザー）を DB に保存する。
//
// Cookie には、32バイトの乱数（base64url で43文字）を入れる。DB には、そのSHA-256だけを保存するので、
// DB が漏れても Cookie の値は分からない。ログインのたびに新しい値を作る（セッション固定化攻撃の対策）。
//
// 有効期間：作ってから12時間（絶対）、最後に使ってから2時間（無操作）。どちらかを過ぎたら無効。
// 時刻は、呼び出し側が now として渡す（テストで時間を進められるように）。
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/user"
)

const (
	MaxAge      = 12 * time.Hour // 作ってからの上限
	IdleTimeout = 2 * time.Hour  // 最後に使ってからの上限

	// touchInterval より新しければ、最後に使った時刻を更新しない（毎回の書き込みを避ける）。
	touchInterval = time.Minute

	tokenBytes = 32
	// tokenLen は、32バイトを RawURLEncoding にした長さ。これ以外の長さの値は、DB に問い合わせず無効にする。
	tokenLen = 43
)

// ErrNoSession は、セッションがない・期限切れ・値が不正なとき。
var ErrNoSession = errors.New("session: セッションがありません")

// DBTX は *sql.DB と *sql.Tx の共通部分。
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store は PostgreSQL にセッションを保存する。
type Store struct{ db DBTX }

func NewStore(db DBTX) *Store { return &Store{db: db} }

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Create は、ユーザーの新しいセッションを作り、Cookie に入れる値を返す。
// ついでに、期限切れのセッション（全ユーザー分）を消す。
func (s *Store) Create(ctx context.Context, userID int64, now time.Time) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("session: 乱数の生成: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE created_at <= $1 OR last_seen_at <= $2`,
		now.Add(-MaxAge), now.Add(-IdleTimeout)); err != nil {
		return "", fmt.Errorf("session: 期限切れの削除: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, last_seen_at) VALUES ($1, $2, $3, $3)`,
		hashToken(token), userID, now); err != nil {
		return "", fmt.Errorf("session: 作成: %w", err)
	}
	return token, nil
}

// Lookup は、Cookie の値から、ログイン中のユーザーを返す。
// なし・期限切れ・値が不正なら ErrNoSession（期限切れは、ついでに消す）。
// 使うたびに「最後に使った時刻」を更新する（touchInterval より新しければ省く）。
func (s *Store) Lookup(ctx context.Context, token string, now time.Time) (user.User, error) {
	if len(token) != tokenLen {
		return user.User{}, ErrNoSession
	}
	hash := hashToken(token)

	var (
		u        user.User
		created  time.Time
		lastSeen time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.username, s.created_at, s.last_seen_at
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = $1`, hash).Scan(&u.ID, &u.Username, &created, &lastSeen)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return user.User{}, ErrNoSession
	case err != nil:
		return user.User{}, fmt.Errorf("session: 取得: %w", err)
	}

	if !now.Before(created.Add(MaxAge)) || !now.Before(lastSeen.Add(IdleTimeout)) {
		if err := s.Delete(ctx, token); err != nil {
			return user.User{}, err
		}
		return user.User{}, ErrNoSession
	}
	if now.Sub(lastSeen) >= touchInterval {
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE token_hash = $1`, hash, now); err != nil {
			return user.User{}, fmt.Errorf("session: 更新: %w", err)
		}
	}
	return u, nil
}

// Delete は、セッションを消す（ログアウト）。なくてもエラーにしない。
func (s *Store) Delete(ctx context.Context, token string) error {
	if len(token) != tokenLen {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token)); err != nil {
		return fmt.Errorf("session: 削除: %w", err)
	}
	return nil
}
