-- 予約。取消しても削除せず status を cancelled にして、台帳として履歴を残す。
-- 宿泊日は check_in（含む）〜 check_out（含まない）の半開区間。
-- 注意：同じ部屋で期間が重なる予約を防ぐ制約は、まだ入れていない（段階4で排他制約を追加する）。
CREATE TABLE reservations (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    room_id    BIGINT      NOT NULL REFERENCES rooms (id),
    guest_name TEXT        NOT NULL CHECK (btrim(guest_name) <> ''),
    contact    TEXT        NOT NULL DEFAULT '',
    check_in   DATE        NOT NULL,
    check_out  DATE        NOT NULL,
    num_guests INTEGER     NOT NULL CHECK (num_guests > 0),
    status     TEXT        NOT NULL DEFAULT 'reserved' CHECK (status IN ('reserved', 'cancelled')),
    note       TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT reservations_dates_check CHECK (check_out > check_in)
);

-- 部屋ごと・日付順に探す（台帳ビューや重複の判定で使う）。
CREATE INDEX reservations_room_check_in_idx ON reservations (room_id, check_in);
