// Package testdb は、DB を使うテストの共通の準備を提供する。
//
// 使い方：事前に scripts/test-db.sh でテスト専用 DB（staylink_test）を作り、
// TEST_DATABASE_URL にその接続先を入れておく。
//
// 方針：テストは実際にコミットする（二重予約の検証など、同時実行やコミット後の見え方が
// 主題のテストは、1つのトランザクションに閉じ込められないため）。
// その代わり、テストの前後で全テーブルを空にする。開発用 DB を壊さないよう、
// DB 名が "_test" で終わらない接続先は拒否する。
package testdb

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql 用の PostgreSQL ドライバ
)

// lockKey は、テスト同士を直列にするための advisory lock の番号（適当な固定値）。
// go test は複数パッケージを同時に走らせるため、同じ DB を空にし合わないように使う。
const lockKey = 735100

// Open はテスト専用 DB につないで、空の状態で返す。
// TEST_DATABASE_URL が未設定のときは、そのテストを飛ばす。
// ただし STAYLINK_REQUIRE_DB=1 のときは飛ばさず失敗させる（飛ばしたまま緑になるのを防ぐ）。
func Open(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("STAYLINK_REQUIRE_DB") == "1" {
			t.Fatal("STAYLINK_REQUIRE_DB=1 なのに TEST_DATABASE_URL が未設定です")
		}
		t.Skip("TEST_DATABASE_URL が未設定のため、DBのテストを飛ばします（README 参照）")
	}
	if !strings.HasSuffix(dbName(t, dsn), "_test") {
		t.Fatalf("TEST_DATABASE_URL の DB 名が _test で終わっていません。開発用 DB を空にしないため中止します: %s", dbName(t, dsn))
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	ctx := context.Background()

	// 1本の接続で advisory lock を取り、このテストが終わるまで他のテストを待たせる。
	lockConn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("DB に接続できません（scripts/test-db.sh を実行しましたか）: %v", err)
	}
	if _, err := lockConn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		t.Fatalf("advisory lock: %v", err)
	}

	truncateAll(t, db)

	t.Cleanup(func() {
		truncateAll(t, db)
		lockConn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, lockKey)
		lockConn.Close()
		db.Close()
	})
	return db
}

func truncateAll(t *testing.T, db *sql.DB) {
	t.Helper()
	var tables sql.NullString
	err := db.QueryRow(`SELECT string_agg(quote_ident(tablename), ', ')
	                      FROM pg_tables
	                     WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`).Scan(&tables)
	if err != nil {
		t.Fatalf("テーブル一覧の取得: %v", err)
	}
	if !tables.Valid {
		t.Fatal("テーブルがありません（scripts/test-db.sh でマイグレーションを適用しましたか）")
	}
	if _, err := db.Exec(`TRUNCATE ` + tables.String + ` RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("テーブルを空にできません: %v", err)
	}
}

func dbName(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL を解釈できません: %v", err)
	}
	return strings.TrimPrefix(u.Path, "/")
}
