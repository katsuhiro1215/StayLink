package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	uniqueViolation = "23505"
	// usernameIndex は migrations/004 の一意索引の名前（コードだけでなく名前でも判定する）。
	usernameIndex = "users_username_key"
)

// DBTX は *sql.DB と *sql.Tx の共通部分。
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store は PostgreSQL にユーザーを保存する。
type Store struct {
	db DBTX

	// dummyHash は、存在しないユーザー名でも、同じ重さのハッシュ計算をするための、使い捨てのハッシュ。
	dummyOnce sync.Once
	dummyHash string
}

func NewStore(db DBTX) *Store { return &Store{db: db} }

// Create は、ユーザー名とパスワードから、ユーザーを作る。
// ユーザー名は整えてから検証し、パスワードは長さを検証してからハッシュにする。
func (s *Store) Create(ctx context.Context, username, password string) (User, error) {
	username = NormalizeUsername(username)
	if err := ValidateUsername(username); err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	u := User{Username: username}
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`, username, hash).Scan(&u.ID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == usernameIndex {
			return User{}, ErrDuplicateUsername
		}
		return User{}, fmt.Errorf("user: 作成: %w", err)
	}
	return u, nil
}

// Authenticate は、ユーザー名とパスワードを照合する。
// 失敗の理由（ユーザーがいない／パスワードが違う）は区別せず、どちらも ErrInvalidCredentials。
// ユーザーがいない場合も、同じ重さのハッシュ計算をして、応答時間でユーザーの有無を推測されにくくする。
func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	username = NormalizeUsername(username)
	u := User{Username: username}
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash FROM users WHERE username = $1`, username).Scan(&u.ID, &hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, _ = VerifyPassword(password, s.dummy())
		return User{}, ErrInvalidCredentials
	case err != nil:
		return User{}, fmt.Errorf("user: 照合: %w", err)
	}
	ok, err := VerifyPassword(password, hash)
	if err != nil {
		return User{}, fmt.Errorf("user: 保存されたハッシュの検証（ユーザーID %d）: %w", u.ID, err)
	}
	if !ok {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

func (s *Store) dummy() string {
	s.dummyOnce.Do(func() {
		h, err := HashPassword("dummy-password-for-timing")
		if err != nil {
			h = "" // 形式不正になり、検証が即座に失敗するだけ（動作は変わらない）
		}
		s.dummyHash = h
	})
	return s.dummyHash
}
