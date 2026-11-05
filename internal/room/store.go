package room

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// uniqueViolation は PostgreSQL の「一意制約違反」のエラーコード。
	uniqueViolation = "23505"
	// nameIndex は migrations/001 で作る、部屋名の一意索引の名前。
	// 一意制約違反が増えても取り違えないよう、コードだけでなく名前でも判定する。
	nameIndex = "rooms_name_key"
)

// DBTX は *sql.DB と *sql.Tx の共通部分。
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store は PostgreSQL に部屋を保存する。
type Store struct {
	db DBTX
}

// NewStore は Store を作る。
func NewStore(db DBTX) *Store { return &Store{db: db} }

// List は表示順（sort_order、次に id）で全部屋を返す。無効な部屋も含む。
func (s *Store) List(ctx context.Context) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, capacity, sort_order, is_active FROM rooms ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("room: 一覧の取得: %w", err)
	}
	defer rows.Close()

	var rooms []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.Name, &r.Capacity, &r.SortOrder, &r.IsActive); err != nil {
			return nil, fmt.Errorf("room: 一覧の読み取り: %w", err)
		}
		rooms = append(rooms, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("room: 一覧の読み取り: %w", err)
	}
	return rooms, nil
}

// Get は ID で1件取得する。なければ ErrNotFound。
func (s *Store) Get(ctx context.Context, id int64) (Room, error) {
	var r Room
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, capacity, sort_order, is_active FROM rooms WHERE id = $1`, id).
		Scan(&r.ID, &r.Name, &r.Capacity, &r.SortOrder, &r.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	if err != nil {
		return Room{}, fmt.Errorf("room: 取得 id=%d: %w", id, err)
	}
	return r, nil
}

// Create は部屋を登録し、採番された ID を r.ID に入れる。同名があれば ErrDuplicateName。
func (s *Store) Create(ctx context.Context, r *Room) error {
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO rooms (name, capacity, sort_order, is_active)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		r.Name, r.Capacity, r.SortOrder, r.IsActive).Scan(&r.ID)
	if err != nil {
		return translate("登録", err)
	}
	return nil
}

// Update は部屋を更新する。なければ ErrNotFound、同名があれば ErrDuplicateName。
func (s *Store) Update(ctx context.Context, r *Room) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE rooms
		    SET name = $2, capacity = $3, sort_order = $4, is_active = $5, updated_at = now()
		  WHERE id = $1`,
		r.ID, r.Name, r.Capacity, r.SortOrder, r.IsActive)
	if err != nil {
		return translate("更新", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("room: 更新 id=%d: %w", r.ID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// translate は PostgreSQL のエラーを、このパッケージのエラーに置き換える。
// 部屋名の一意索引に違反したときだけ ErrDuplicateName にし、他は操作名を付けて包む。
func translate(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == nameIndex {
		return ErrDuplicateName
	}
	return fmt.Errorf("room: %s: %w", op, err)
}
