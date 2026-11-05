package reservation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// foreignKeyViolation は PostgreSQL の「外部キー制約違反」のエラーコード。
const foreignKeyViolation = "23503"

// roomFK は予約から部屋への外部キーの名前（migrations/002 で PostgreSQL が付ける既定名）。
const roomFK = "reservations_room_id_fkey"

// DBTX は *sql.DB と *sql.Tx の共通部分。
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store は PostgreSQL に予約を保存する。
type Store struct {
	db DBTX
}

// NewStore は Store を作る。
func NewStore(db DBTX) *Store { return &Store{db: db} }

// Filter は一覧の絞り込み条件。ゼロ値は「絞り込まない」。
type Filter struct {
	Status string    // StatusReserved / StatusCancelled。空なら全て
	From   time.Time // この日以降にチェックアウトする予約だけ（check_out >= From。当日発つ人も含む）。ゼロなら全期間
}

const selectColumns = `r.id, r.room_id, rm.name, r.guest_name, r.contact,
	r.check_in, r.check_out, r.num_guests, r.status, r.note`

func scan(row interface{ Scan(...any) error }) (Reservation, error) {
	var r Reservation
	err := row.Scan(&r.ID, &r.RoomID, &r.RoomName, &r.GuestName, &r.Contact,
		&r.CheckIn, &r.CheckOut, &r.NumGuests, &r.Status, &r.Note)
	return r, err
}

// List はチェックイン日の昇順（同じなら id 順）で予約を返す。
func (s *Store) List(ctx context.Context, f Filter) ([]Reservation, error) {
	// 条件は $n の位置を固定し、使わないものは NULL / 空を渡して無効にする（SQL を組み立てない）。
	var from any
	if !f.From.IsZero() {
		from = f.From
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+selectColumns+`
		   FROM reservations r JOIN rooms rm ON rm.id = r.room_id
		  WHERE ($1 = '' OR r.status = $1)
		    AND ($2::date IS NULL OR r.check_out >= $2::date)
		  ORDER BY r.check_in, r.id`, f.Status, from)
	if err != nil {
		return nil, fmt.Errorf("reservation: 一覧の取得: %w", err)
	}
	defer rows.Close()

	var list []Reservation
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("reservation: 一覧の読み取り: %w", err)
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reservation: 一覧の読み取り: %w", err)
	}
	return list, nil
}

// Get は ID で1件取得する。なければ ErrNotFound。
func (s *Store) Get(ctx context.Context, id int64) (Reservation, error) {
	r, err := scan(s.db.QueryRowContext(ctx,
		`SELECT `+selectColumns+`
		   FROM reservations r JOIN rooms rm ON rm.id = r.room_id
		  WHERE r.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Reservation{}, ErrNotFound
	}
	if err != nil {
		return Reservation{}, fmt.Errorf("reservation: 取得 id=%d: %w", id, err)
	}
	return r, nil
}

// Create は予約を登録し、採番された ID を r.ID に、状態を reserved にする。
// 存在しない部屋なら ErrRoomNotFound。
// 注意：段階3では、同じ部屋で期間が重なる予約も登録できる（段階4で防ぐ）。
func (s *Store) Create(ctx context.Context, r *Reservation) error {
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO reservations (room_id, guest_name, contact, check_in, check_out, num_guests, note)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, status`,
		r.RoomID, r.GuestName, r.Contact, r.CheckIn, r.CheckOut, r.NumGuests, r.Note).
		Scan(&r.ID, &r.Status)
	if err != nil {
		return translate("登録", err)
	}
	return nil
}

// Update は予約の内容を更新する（状態は変えない）。
// なければ ErrNotFound、取消済みなら ErrNotEditable、存在しない部屋なら ErrRoomNotFound。
func (s *Store) Update(ctx context.Context, r *Reservation) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE reservations
		    SET room_id = $2, guest_name = $3, contact = $4, check_in = $5, check_out = $6,
		        num_guests = $7, note = $8, updated_at = now()
		  WHERE id = $1 AND status = 'reserved'`,
		r.ID, r.RoomID, r.GuestName, r.Contact, r.CheckIn, r.CheckOut, r.NumGuests, r.Note)
	if err != nil {
		return translate("更新", err)
	}
	return s.expectOneRow(ctx, res, r.ID, "更新")
}

// Cancel は予約を取り消す（削除はせず、状態を cancelled にする）。
// なければ ErrNotFound、すでに取消済みなら ErrNotEditable。
func (s *Store) Cancel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE reservations SET status = 'cancelled', updated_at = now()
		  WHERE id = $1 AND status = 'reserved'`, id)
	if err != nil {
		return fmt.Errorf("reservation: 取消 id=%d: %w", id, err)
	}
	return s.expectOneRow(ctx, res, id, "取消")
}

// expectOneRow は UPDATE が1行に効いたかを確かめる。
// 効かなかったとき、「存在しない」のか「取消済みで対象外」のかを区別して返す。
func (s *Store) expectOneRow(ctx context.Context, res sql.Result, id int64, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reservation: %s id=%d: %w", op, id, err)
	}
	if n == 1 {
		return nil
	}
	if _, err := s.Get(ctx, id); errors.Is(err, ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return ErrNotEditable
}

// translate は PostgreSQL のエラーを、このパッケージのエラーに置き換える。
func translate(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == roomFK {
		return ErrRoomNotFound
	}
	return fmt.Errorf("reservation: %s: %w", op, err)
}
