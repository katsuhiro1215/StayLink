-- ログインのセッション。Cookie に入れる乱数の値そのものは保存せず、そのSHA-256（32バイト）だけを保存する
-- （DB が漏れても、Cookie の値は分からない）。有効期限は、作成時刻・最後に使った時刻から、アプリが判定する。
CREATE TABLE sessions (
    token_hash   BYTEA       PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
