package reservation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func pgError(code string) error {
	return fmt.Errorf("reservation: 登録: %w", &pgconn.PgError{Code: code})
}

func TestRetryDeadlock(t *testing.T) {
	deadlock := pgError(deadlockDetected)
	other := pgError("23505")
	overlap := pgError(exclusionViolation)

	tests := []struct {
		name      string
		results   []error // 呼ぶたびに返す値（尽きたら最後の値を返し続ける）
		wantCalls int
		wantErr   error // errors.Is で比べる。nil なら成功を期待
	}{
		{"最初から成功", []error{nil}, 1, nil},
		{"デッドロックが2回続いて、3回目で成功", []error{deadlock, deadlock, nil}, 3, nil},
		{"デッドロックが続くと、上限で諦める", []error{deadlock}, maxAttempts, deadlock},
		{"デッドロック以外のエラーは、やり直さない", []error{other}, 1, other},
		{"排他制約違反（重なり）も、やり直さない", []error{overlap}, 1, overlap},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			retries, err := retryDeadlock(context.Background(), func() error {
				i := calls
				calls++
				if i >= len(tt.results) {
					i = len(tt.results) - 1
				}
				return tt.results[i]
			})

			if calls != tt.wantCalls {
				t.Errorf("呼び出し回数 = %d, 期待 %d", calls, tt.wantCalls)
			}
			if retries != tt.wantCalls-1 {
				t.Errorf("やり直した回数 = %d, 期待 %d（呼び出し回数 - 1）", retries, tt.wantCalls-1)
			}
			switch {
			case tt.wantErr == nil && err != nil:
				t.Errorf("err = %v, 成功を期待", err)
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Errorf("err = %v, 期待 %v", err, tt.wantErr)
			}
		})
	}
}

func TestRetryDeadlockStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := retryDeadlock(ctx, func() error {
		calls++
		cancel() // 1回目の失敗の直後に、呼び出し元がやめた
		return pgError(deadlockDetected)
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, 期待 context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("呼び出し回数 = %d, 期待 1（キャンセル後はやり直さない）", calls)
	}
}

func TestTranslateOverlap(t *testing.T) {
	overlap := &pgconn.PgError{Code: exclusionViolation, ConstraintName: overlapConstraint}
	otherExclusion := &pgconn.PgError{Code: exclusionViolation, ConstraintName: "some_other_exclusion"}
	unique := &pgconn.PgError{Code: "23505", ConstraintName: overlapConstraint}

	if err := translate("登録", overlap); !errors.Is(err, ErrOverlap) {
		t.Errorf("排他制約違反: err = %v, 期待 ErrOverlap", err)
	}
	// やり直しが尽きたデッドロックは、重なりではなく ErrBusy（入力は正しいので、もう一度で通りうる）。
	err := translate("登録", &pgconn.PgError{Code: deadlockDetected})
	if !errors.Is(err, ErrBusy) || errors.Is(err, ErrOverlap) {
		t.Errorf("デッドロック: err = %v, 期待 ErrBusy（ErrOverlap ではない）", err)
	}
	// コードか制約名のどちらかが違えば、重なりとは扱わない（別の失敗を取り違えない）。
	for name, e := range map[string]error{"別の排他制約": otherExclusion, "一意制約違反": unique} {
		if err := translate("登録", e); errors.Is(err, ErrOverlap) {
			t.Errorf("%s が ErrOverlap に化けている", name)
		}
	}
}
