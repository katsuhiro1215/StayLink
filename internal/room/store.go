package room

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolation は PostgreSQL の「一意制約違反」のエラーコード。
const uniqueViolation = "23505"

// DBTX は *sql.DB と *sql.Tx の共通部分。
// テストでトランザクションを渡し、終わったら取り消すために分けている。
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
		return nil, err
	}
	defer rows.Close()

	var rooms []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.Name, &r.Capacity, &r.SortOrder, &r.IsActive); err != nil {
			return nil, err
		}
		rooms = append(rooms, r)
	}
	return rooms, rows.Err()
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
	return r, err
}

// Create は部屋を登録し、採番された ID を r.ID に入れる。同名があれば ErrDuplicateName。
func (s *Store) Create(ctx context.Context, r *Room) error {
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO rooms (name, capacity, sort_order, is_active)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		r.Name, r.Capacity, r.SortOrder, r.IsActive).Scan(&r.ID)
	return translate(err)
}

// Update は部屋を更新する。なければ ErrNotFound、同名があれば ErrDuplicateName。
func (s *Store) Update(ctx context.Context, r *Room) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE rooms
		    SET name = $2, capacity = $3, sort_order = $4, is_active = $5, updated_at = now()
		  WHERE id = $1`,
		r.ID, r.Name, r.Capacity, r.SortOrder, r.IsActive)
	if err != nil {
		return translate(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// translate は PostgreSQL のエラーを、このパッケージのエラーに置き換える。
func translate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return ErrDuplicateName
	}
	return err
}
