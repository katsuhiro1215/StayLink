#!/usr/bin/env bash
# テスト専用DB（staylink_test）を作り、マイグレーションを適用する。何度実行してもよい。
# 使い方: scripts/test-db.sh   （事前に docker compose up -d --wait で db を起動しておく）
set -euo pipefail
cd "$(dirname "$0")/.."

# 無ければ作る（CREATE DATABASE は IF NOT EXISTS が使えないので、先に存在を調べる）。
docker compose exec -T db sh -c '
  exists=$(psql -At -U "$POSTGRES_USER" -d postgres -c "SELECT 1 FROM pg_database WHERE datname = '"'"'staylink_test'"'"'")
  if [ -z "$exists" ]; then createdb -U "$POSTGRES_USER" staylink_test && echo "作成: staylink_test"; else echo "あります: staylink_test"; fi
'

MIGRATE_DB=staylink_test scripts/migrate.sh
