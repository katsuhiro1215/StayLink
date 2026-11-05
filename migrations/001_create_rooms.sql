-- 部屋。過去の予約が参照するため、削除せず is_active で無効化する。
CREATE TABLE rooms (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT        NOT NULL CHECK (btrim(name) <> ''),
    capacity   INTEGER     NOT NULL CHECK (capacity > 0),
    sort_order INTEGER     NOT NULL DEFAULT 0,
    is_active  BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 同じ名前の部屋を2つ作らせない（有効・無効を問わず）。
CREATE UNIQUE INDEX rooms_name_key ON rooms (name);
