# StayLink

小さな民宿向けの予約台帳。Go言語の学習用に、標準ライブラリを中心に作る試験的なプロジェクトです。

- 学習用です。宿泊者名簿など法令への適合は保証しません（業態により義務がある可能性があります。根拠法・項目は要確認）。
- サンプルデータはすべて架空です。
- 本番公開は想定していません。

## 構成

Go（標準 `net/http`・`html/template`）、PostgreSQL 18（Docker Compose）、DBドライバは `pgx`（`database/sql` 経由）。

## 動かし方

必要なもの：Go 1.27 以上、Docker（Docker Compose）。

```bash
# 1. 設定ファイルを作る（値は学習用のローカル専用）
cp .env.example .env

# 2. データベースを起動する（healthy になるまで待つ）
docker compose up -d --wait

# 3. テーブルを作る（未適用の migrations/*.sql だけ適用。何度実行してもよい）
scripts/migrate.sh

# 4. .env を読み込んで起動する
set -a; . ./.env; set +a
go run .
```

ブラウザで http://localhost:8090/ を開きます。ポートは環境変数 `PORT` で変えられます。

- 待ち受けは、自分のPCだけが接続できる `127.0.0.1` が既定です（`HOST` で変更可）。ログイン機能がないため、`HOST=0.0.0.0` でLANへ公開するのは、認証を付けるまで避けてください。
- 他のサイトのページからのPOSTは、標準の `CrossOriginProtection` で拒否します。ただし `curl` などヘッダのない送信は通すので、認証の代わりにはなりません。
- `scripts/migrate.sh` は排他していないので、同時に2つ実行しないでください。SQLファイルの中に `BEGIN` / `COMMIT` は書かないでください（スクリプトが1ファイル1トランザクションで包みます）。

止めるとき：アプリは `Ctrl+C`、データベースは `docker compose down`（データは残ります。消すときは `docker compose down -v`）。

## テスト

```bash
go test ./...                       # DBなしで動くテストだけ（DBが要るテストは自動で飛ばす）

scripts/test-db.sh                  # 初回だけ：テスト専用DB（staylink_test）を作って、テーブルを用意する
set -a; . ./.env; set +a
go test ./...                       # DBのテストも実行
STAYLINK_REQUIRE_DB=1 go test ./... # DBが使えないときは、飛ばさずに失敗させる（飛ばしたまま緑になるのを防ぐ）
```

- 以前から `.env` を使っている場合は、`.env.example` にある `TEST_DATABASE_URL` の行を `.env` にも足してください。足し忘れると、DBのテストが黙って飛ばされます（`STAYLINK_REQUIRE_DB=1` を付けると気づけます）。
- DBのテストは、**実際にコミットする**専用DBで動き、テストの前後で全テーブルを空にします（同時実行や、コミット後の見え方を確かめるため）。
- 開発用DBを空にしてしまわないよう、`TEST_DATABASE_URL` のDB名が `_test` で終わらない場合は、テストを中止します。
- 新しいマイグレーションを足したら、`scripts/migrate.sh`（開発用）と `scripts/test-db.sh`（テスト用）の両方を実行してください。
- このPCにはCコンパイラがなく、`go test -race`（データ競合の検出）は使えません。

## 状況

- 段階0〜1：Goのサーバー、ルーティング、共通レイアウト（完了）
- 段階2：PostgreSQL接続、部屋の登録・一覧・編集（無効化）（完了）
- 段階3：予約の登録・一覧（状態・期間で絞り込み）・編集・取消。**同じ部屋の日程の重なりは、まだ防いでいません**
- 次：二重予約の防止（PostgreSQLの排他制約）、月間カレンダー
