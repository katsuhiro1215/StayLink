package reservation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL のエラーコード。制約の種類ごとに違う（一意制約は 23505）。
const (
	foreignKeyViolation = "23503" // 外部キー制約違反
	exclusionViolation  = "23P01" // 排他制約違反（二重予約）
)

// 制約の名前。コードだけでなく名前でも判定して、別の制約違反を取り違えないようにする。
const (
	roomFK            = "reservations_room_id_fkey" // 予約から部屋への外部キー（migrations/002 で PostgreSQL が付ける既定名）
	overlapConstraint = "reservations_no_overlap"   // 同じ部屋の期間の重なりを禁じる排他制約（migrations/003）
)

// Store は PostgreSQL に予約を保存する。
//
// *sql.DB（1文ごとに自動でコミットされる）専用。*sql.Tx は受け取らない。
// 登録・更新はデッドロックで中断されたとき1文をやり直す（retryDeadlock）が、
// トランザクションの中では、中断でトランザクション全体が無効になり、1文だけやり直せないため。
type Store struct {
	db *sql.DB

	deadlockRetries atomic.Int64 // これまでにデッドロックでやり直した回数（確認・計測用）
}

// NewStore は Store を作る。
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// DeadlockRetries は、デッドロックで1文をやり直した回数の合計を返す。
// 実際の DB で、やり直しが働いているかを確かめるために使う。
func (s *Store) DeadlockRetries() int64 { return s.deadlockRetries.Load() }

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

// Between は、from（含む）〜 to（含まない）の間に泊まりがある、取消されていない予約を返す。
// 月間カレンダー用。部屋の順、チェックイン日の順に並べる。
//
// 期間は半開区間として扱う。予約のチェックアウト日が from と同じ日なら、その日の夜は泊まらないので、
// 含めない。チェックイン日が to と同じ日の予約も、to 以降の夜からなので、含めない。
func (s *Store) Between(ctx context.Context, from, to time.Time) ([]Reservation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+selectColumns+`
		   FROM reservations r JOIN rooms rm ON rm.id = r.room_id
		  WHERE r.status <> 'cancelled'
		    AND daterange(r.check_in, r.check_out) && daterange($1::date, $2::date)
		  ORDER BY r.room_id, r.check_in, r.id`, from, to)
	if err != nil {
		return nil, fmt.Errorf("reservation: 期間内の取得: %w", err)
	}
	defer rows.Close()

	var list []Reservation
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("reservation: 期間内の読み取り: %w", err)
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reservation: 期間内の読み取り: %w", err)
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
// 存在しない部屋なら ErrRoomNotFound。同じ部屋で期間が重なる予約があれば ErrOverlap。
// 重なりの判定は、アプリで「調べてから登録」せず、DB の排他制約に任せる
// （同時に登録されても、必ずどちらか一方だけが成功する）。
func (s *Store) Create(ctx context.Context, r *Reservation) error {
	retries, err := retryDeadlock(ctx, func() error {
		return s.db.QueryRowContext(ctx,
			`INSERT INTO reservations (room_id, guest_name, contact, check_in, check_out, num_guests, note)
			 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, status`,
			r.RoomID, r.GuestName, r.Contact, r.CheckIn, r.CheckOut, r.NumGuests, r.Note).
			Scan(&r.ID, &r.Status)
	})
	s.deadlockRetries.Add(int64(retries))
	if err != nil {
		return translate("登録", err)
	}
	return nil
}

// Update は予約の内容を更新する（状態は変えない）。
// なければ ErrNotFound、取消済みなら ErrNotEditable、存在しない部屋なら ErrRoomNotFound、
// 他の予約と期間が重なれば ErrOverlap（自分自身との重なりは数えない）。
func (s *Store) Update(ctx context.Context, r *Reservation) error {
	var res sql.Result
	retries, err := retryDeadlock(ctx, func() (err error) {
		res, err = s.db.ExecContext(ctx,
			`UPDATE reservations
			    SET room_id = $2, guest_name = $3, contact = $4, check_in = $5, check_out = $6,
			        num_guests = $7, note = $8, updated_at = now()
			  WHERE id = $1 AND status = 'reserved'`,
			r.ID, r.RoomID, r.GuestName, r.Contact, r.CheckIn, r.CheckOut, r.NumGuests, r.Note)
		return err
	})
	s.deadlockRetries.Add(int64(retries))
	if err != nil {
		return translate("更新", err)
	}
	return s.expectOneRow(ctx, res, r.ID, "更新")
}

// Cancel は予約を取り消す（削除はせず、状態を cancelled にする）。
// なければ ErrNotFound、すでに取消済みなら ErrNotEditable。
// 取消は予約を減らすだけで、新しい重なりを作らない。そのため排他制約に当たらず、やり直し（retryDeadlock）も使わない。
// 将来「取消を取り消して復活させる」操作を足すなら、重なる可能性があるので、Update と同じ扱いが要る。
func (s *Store) Cancel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE reservations SET status = 'cancelled', updated_at = now()
		  WHERE id = $1 AND status = 'reserved'`, id)
	if err != nil {
		return fmt.Errorf("reservation: 取消 id=%d: %w", id, err)
	}
	return s.expectOneRow(ctx, res, id, "取消")
}

// Overlapping は、指定の部屋・期間と重なる、取消されていない予約を返す（excludeID の予約は除く）。
// 登録・更新が ErrOverlap で断られたとき、画面に「どの予約と重なったか」を示すために使う。
// 重なりを防ぐのは DB の排他制約で、この検索は防ぐ役目を持たない（案内用）。
// 判定の条件（取消されていない＝ status <> 'cancelled'）は、排他制約と同じ表現に揃えてある。
//
// 注意：断られた「後」の別の問い合わせなので、断った時点の状態と一致しないことがある
// （その間に取り消された、など）。そのときは、画面は詳細なしの案内にする。
func (s *Store) Overlapping(ctx context.Context, roomID int64, checkIn, checkOut time.Time, excludeID int64) ([]Reservation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+selectColumns+`
		   FROM reservations r JOIN rooms rm ON rm.id = r.room_id
		  WHERE r.room_id = $1
		    AND r.status <> 'cancelled'
		    AND r.id <> $4
		    AND daterange(r.check_in, r.check_out) && daterange($2::date, $3::date)
		  ORDER BY r.check_in, r.id`, roomID, checkIn, checkOut, excludeID)
	if err != nil {
		return nil, fmt.Errorf("reservation: 重なりの検索: %w", err)
	}
	defer rows.Close()

	var list []Reservation
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("reservation: 重なりの読み取り: %w", err)
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reservation: 重なりの読み取り: %w", err)
	}
	return list, nil
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

// デッドロックのやり直し。
//
// 排他制約は、同時に入る別々の予約が互いの未確定の行を待ち合って、デッドロック（40P01）になることがある。
// PostgreSQL は片方を中断するので、その1文をやり直せば、成功するか「重なっています」になる。
// やり直してよいのは、1文ごとに自動でコミットされる使い方（*sql.DB）のときだけ（Store が *sql.DB 専用なのはこのため）。
//
// 待ち時間には「ばらつき（ジッタ）」を入れる。同時に中断された複数の文が、同じ間隔で
// 一斉にやり直すと、また同じ組み合わせで衝突してしまうため。
// それでも尽きたら ErrBusy にして、画面で「もう一度押してください」と案内する（500にしない）。
const (
	deadlockDetected = "40P01"
	maxAttempts      = 8
	baseBackoff      = 10 * time.Millisecond // 待ち時間の目安。回数に比例して延ばし、同じ幅のばらつきを足す
)

// retryDeadlock は fn を実行し、デッドロックで中断されたときだけ、ばらつきのある時間を待ってやり直す。
// やり直した回数を、結果と一緒に返す。上限まで失敗したら、最後のエラーを返す。
func retryDeadlock(ctx context.Context, fn func() error) (retries int, err error) {
	for attempt := 1; ; attempt++ {
		if err = fn(); !isDeadlock(err) || attempt == maxAttempts {
			return retries, err // 成功・別のエラー・上限（最後の失敗の後は待たない）
		}
		retries++
		wait := time.Duration(attempt) * baseBackoff
		wait += rand.N(wait) // 0 〜 wait のばらつきを足す
		select {
		case <-ctx.Done():
			return retries, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == deadlockDetected
}

// translate は PostgreSQL のエラーを、このパッケージのエラーに置き換える。
func translate(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == roomFK:
			return ErrRoomNotFound
		case pgErr.Code == exclusionViolation && pgErr.ConstraintName == overlapConstraint:
			return ErrOverlap
		case pgErr.Code == deadlockDetected:
			return ErrBusy // やり直しが尽きた。重なりとは限らないので、別のエラーとして伝える
		}
	}
	return fmt.Errorf("reservation: %s: %w", op, err)
}
