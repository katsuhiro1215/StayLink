-- 二重予約の防止。「同じ部屋で、宿泊期間が重なる予約」をDB自身が拒否する（排他制約）。
--
-- 仕組み：
--   * daterange(check_in, check_out) は、チェックイン日を含み、チェックアウト日を含まない期間（[)）。
--     そのため、前の人のチェックアウト日に次の人がチェックインしても「重ならない」。
--   * && は「期間が重なる」、room_id WITH = は「部屋が同じ」。両方が成り立つ行の組を禁止する。
--   * 取消済みの予約は対象外（WHERE status <> 'cancelled'）。取り消せば、その期間は再び予約できる。
--   * アプリ側で「空いているか調べてから登録」する方式だと、同時に2人が登録したときに両方通ってしまう。
--     DBの制約なら、同時でも必ずどちらか一方だけが成功する。
--
-- 部屋（整数）の「=」を GiST 索引で扱うために、btree_gist 拡張が要る。拡張はデータベースごとに入れる。
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- すでに期間が重なった予約（段階3までは防いでいなかった）があると、制約を追加できない。
-- 先に検出して、分かる形で知らせる。見つかったら、このマイグレーションは全体が取り消される。
DO $$
DECLARE
    pairs  integer;
    detail text;
BEGIN
    -- 重なっている予約の組（a.id < b.id で、同じ組を2回数えない）。
    WITH overlapping AS (
        SELECT a.room_id,
               a.id AS a_id, a.check_in AS a_in, a.check_out AS a_out,
               b.id AS b_id, b.check_in AS b_in, b.check_out AS b_out
          FROM reservations a
          JOIN reservations b
            ON a.room_id = b.room_id
           AND a.id < b.id
           AND daterange(a.check_in, a.check_out) && daterange(b.check_in, b.check_out)
         WHERE a.status <> 'cancelled'
           AND b.status <> 'cancelled'
    )
    SELECT (SELECT count(*) FROM overlapping),
           -- 多いと読みにくいので、先頭の5組だけを説明に入れる。
           (SELECT string_agg(
                       format('部屋ID %s：予約ID %s（%s〜%s）と 予約ID %s（%s〜%s）',
                              room_id, a_id, a_in, a_out, b_id, b_in, b_out),
                       E'\n' ORDER BY room_id, a_id, b_id)
              FROM (SELECT * FROM overlapping ORDER BY room_id, a_id, b_id LIMIT 5) first_five)
      INTO pairs, detail;

    IF pairs > 0 THEN
        RAISE EXCEPTION '期間が重なっている予約が % 組あります。二重予約の制約を追加できません。', pairs
            USING DETAIL = detail,
                  HINT = '上の予約IDを予約の一覧で確認して、どちらかを取り消すか期間を直してから、もう一度実行してください。';
    END IF;
END
$$;

ALTER TABLE reservations
    ADD CONSTRAINT reservations_no_overlap
    EXCLUDE USING gist (
        room_id WITH =,
        daterange(check_in, check_out) WITH &&
    ) WHERE (status <> 'cancelled');
