-- 管理者（ログインするユーザー）。パスワードは平文を持たず、ハッシュ（形式は internal/user/password.go）だけを保存する。
CREATE TABLE users (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- ユーザー名は、小文字に揃えた英数字と . _ - の3〜32文字（internal/user/user.go の usernamePattern と同じ）。
    username      TEXT        NOT NULL CHECK (username ~ '^[a-z0-9._-]{3,32}$'),
    password_hash TEXT        NOT NULL CHECK (password_hash <> ''),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 同じユーザー名を2つ作らせない（小文字に揃えて保存する）。
CREATE UNIQUE INDEX users_username_key ON users (username);
