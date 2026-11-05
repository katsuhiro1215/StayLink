#!/usr/bin/env bash
# migrations/*.sql を番号順に、未適用のものだけ適用する（学習用の最小版）。
# 使い方: scripts/migrate.sh   （事前に docker compose up -d --wait で db を起動しておく）
set -euo pipefail
cd "$(dirname "$0")/.."

psql_db() { docker compose exec -T db sh -c 'psql -v ON_ERROR_STOP=1 -q -U "$POSTGRES_USER" -d "$POSTGRES_DB"'; }

echo "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());" | psql_db

for f in migrations/*.sql; do
  version="$(basename "$f" .sql)"
  applied="$(echo "SELECT 1 FROM schema_migrations WHERE version = '$version';" | docker compose exec -T db sh -c 'psql -At -U "$POSTGRES_USER" -d "$POSTGRES_DB"')"
  if [ -n "$applied" ]; then
    echo "適用済み: $version"
    continue
  fi
  echo "適用中:   $version"
  # 1ファイル＝1トランザクション。途中で失敗したら全部取り消す。
  { echo "BEGIN;"; cat "$f"; echo "INSERT INTO schema_migrations (version) VALUES ('$version');"; echo "COMMIT;"; } | psql_db
done
echo "完了"
