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
    pairs integer;
BEGIN
    SELECT count(*) INTO pairs
      FROM reservations a
      JOIN reservations b
        ON a.room_id = b.room_id
       AND a.id < b.id
       AND daterange(a.check_in, a.check_out) && daterange(b.check_in, b.check_out)
     WHERE a.status <> 'cancelled'
       AND b.status <> 'cancelled';

    IF pairs > 0 THEN
        RAISE EXCEPTION '期間が重なっている予約が % 組あります。二重予約の制約を追加できません。', pairs
            USING HINT = '一覧で確認して、どちらかを取り消すか期間を直してから、もう一度実行してください。重なりを調べるSQLは、このファイルの DO ブロックにあります。';
    END IF;
END
$$;

ALTER TABLE reservations
    ADD CONSTRAINT reservations_no_overlap
    EXCLUDE USING gist (
        room_id WITH =,
        daterange(check_in, check_out) WITH &&
    ) WHERE (status <> 'cancelled');
