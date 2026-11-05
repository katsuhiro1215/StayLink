package web

// 二重予約（期間の重なり）に対する画面の動き。
// 重なりの判定そのものは DB の排他制約が行う（reservation/overlap_test.go で確認済み）。
// ここでは、断られたときの案内が正しく出るかを、偽の保存先で確かめる。

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/katsuhiro1215/StayLink/internal/reservation"
	"github.com/katsuhiro1215/StayLink/internal/room"
	"github.com/katsuhiro1215/StayLink/internal/testdb"
)

func TestReservationCreateOverlapIsExplained(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t)) // 松の間 11/10〜11/12 の山田 太郎さん

	rec := post(e.h, "/reservations", with(validForm(), "guest_name", "新しい客", "check_in", "2026-11-11", "check_out", "2026-11-13"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ステータス = %d, 期待 422", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`role="alert">この部屋は、その期間に次の予約があります：山田 太郎さん（2026年11月10日〜11月12日）`, // 上部に、誰と重なったか
		"他の予約と期間が重なっています。",    // チェックイン欄のエラー
		`id="check_in-error"`, // 項目に結びつく（読み上げ用）
		`value="新しい客"`,        // 入力は残る
	} {
		if !strings.Contains(body, want) {
			t.Errorf("本文に %q が含まれない", want)
		}
	}
	if len(e.resv.items) != 1 {
		t.Errorf("保存件数 = %d, 期待 1（断られた予約は保存されない）", len(e.resv.items))
	}
}

// 前の人のチェックアウト日に、次の人がチェックインできる。
func TestReservationCreateAdjacentIsAllowed(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t)) // 11/10〜11/12

	rec := post(e.h, "/reservations", with(validForm(), "check_in", "2026-11-12", "check_out", "2026-11-14"))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("ステータス = %d, 期待 303（隣接は重ならない）", rec.Code)
	}
}

func TestReservationCreateOverlapDifferentRoomIsAllowed(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t)) // 松の間

	rec := post(e.h, "/reservations", with(validForm(), "room_id", "2"))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("ステータス = %d, 期待 303（別の部屋なら同じ期間でも登録できる）", rec.Code)
	}
}

func TestReservationOverlapListsAtMostThree(t *testing.T) {
	e := newEnv(t)
	for _, d := range [][2]string{
		{"2026-11-10", "2026-11-11"}, {"2026-11-11", "2026-11-12"},
		{"2026-11-12", "2026-11-13"}, {"2026-11-13", "2026-11-14"}, {"2026-11-14", "2026-11-15"},
	} {
		r := sample(t)
		r.CheckIn, r.CheckOut = day(t, d[0]), day(t, d[1])
		e.seed(t, r)
	}

	rec := post(e.h, "/reservations", with(validForm(), "check_in", "2026-11-10", "check_out", "2026-11-15"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ステータス = %d, 期待 422", rec.Code)
	}
	mustContain(t, rec, "ほか2件")
	if strings.Count(rec.Body.String(), "山田 太郎さん（") != 3 {
		t.Error("表示する予約は3件までのはず")
	}
}

// 案内用の検索が失敗しても、「重なっていて断られた」ことは伝える。500にはしない。
func TestReservationOverlapExplanationFallsBack(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t))
	e.resv.overlapErr = errors.New("db down")

	rec := post(e.h, "/reservations", with(validForm(), "check_in", "2026-11-11", "check_out", "2026-11-13"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ステータス = %d, 期待 422（500にしない）", rec.Code)
	}
	mustContain(t, rec, "この部屋は、その期間にすでに予約があります。日付か部屋を変えてください。")
	if strings.Contains(rec.Body.String(), "db down") {
		t.Error("内部のエラー内容を画面に出している")
	}
}

// 重なった予約の名前は、画面に出すのでエスケープされる。
func TestReservationOverlapEscapesGuestName(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.GuestName = `<script>alert(1)</script>`
	e.seed(t, r)

	rec := post(e.h, "/reservations", with(validForm(), "check_in", "2026-11-11", "check_out", "2026-11-13"))
	if strings.Contains(rec.Body.String(), "<script>alert(1)") {
		t.Error("重なった予約の名前がエスケープされていない")
	}
}

func TestReservationUpdateOverlap(t *testing.T) {
	e := newEnv(t)
	e.seed(t, sample(t)) // id=1: 松の間 11/10〜11/12
	second := sample(t)
	second.GuestName = "次の客"
	second.CheckIn, second.CheckOut = day(t, "2026-11-12"), day(t, "2026-11-14")
	e.seed(t, second) // id=2: 松の間 11/12〜11/14（隣接）

	t.Run("自分自身との重なりは数えない（同じ期間のまま内容を更新）", func(t *testing.T) {
		rec := post(e.h, "/reservations/1", with(validForm(), "num_guests", "3"))
		if rec.Code != http.StatusSeeOther {
			t.Errorf("ステータス = %d, 期待 303", rec.Code)
		}
	})
	t.Run("延ばして他の予約と重なると、誰と重なったかを示して断る", func(t *testing.T) {
		rec := post(e.h, "/reservations/1", with(validForm(), "check_out", "2026-11-13"))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("ステータス = %d, 期待 422", rec.Code)
		}
		mustContain(t, rec, "次の客さん（2026年11月12日〜11月14日）")
		if strings.Contains(rec.Body.String(), "山田 太郎さん（") {
			t.Error("自分自身の予約が、重なった相手として表示されている")
		}
	})
}

// ---- 実DBを通したテスト：画面 → 保存 → DB の制約 まで一続きで確かめる ----

func newRealEnv(t *testing.T) (http.Handler, int64) {
	t.Helper()
	db := testdb.Open(t)
	rooms := room.NewStore(db)
	rm := room.Room{Name: "松の間", Capacity: 4, IsActive: true}
	if err := rooms.Create(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	h, err := newHandler(rooms, reservation.NewStore(db), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	return h, rm.ID
}

func realForm(roomID int64, guest, in, out string) url.Values {
	return url.Values{
		"room_id": {strconv.FormatInt(roomID, 10)}, "guest_name": {guest},
		"check_in": {in}, "check_out": {out}, "num_guests": {"2"},
	}
}

func TestRealDBDoubleBookingFlow(t *testing.T) {
	h, roomID := newRealEnv(t)

	// 1件目は登録できる。
	if rec := post(h, "/reservations", realForm(roomID, "山田 太郎", "2026-11-10", "2026-11-13")); rec.Code != http.StatusSeeOther {
		t.Fatalf("1件目: ステータス = %d, 期待 303", rec.Code)
	}

	// 期間が重なる2件目は、DBの制約に断られ、画面に誰と重なったかが出る。
	rec := post(h, "/reservations", realForm(roomID, "鈴木 花子", "2026-11-12", "2026-11-15"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("重なる予約: ステータス = %d, 期待 422", rec.Code)
	}
	mustContain(t, rec, "山田 太郎さん（2026年11月10日〜11月13日）")

	// 隣接（山田さんのチェックアウト日にチェックイン）は登録できる。
	if rec := post(h, "/reservations", realForm(roomID, "鈴木 花子", "2026-11-13", "2026-11-15")); rec.Code != http.StatusSeeOther {
		t.Fatalf("隣接: ステータス = %d, 期待 303", rec.Code)
	}

	// 山田さんを取り消すと、その期間を別の人が予約できる。
	if rec := post(h, "/reservations/1/cancel", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("取消: ステータス = %d, 期待 303", rec.Code)
	}
	if rec := post(h, "/reservations", realForm(roomID, "佐藤 次郎", "2026-11-10", "2026-11-13")); rec.Code != http.StatusSeeOther {
		t.Fatalf("取消後の同じ期間: ステータス = %d, 期待 303", rec.Code)
	}

	// 一覧には、予約中の2件（鈴木さん・佐藤さん）が出て、断られた予約は残っていない。
	list := get(h, "/reservations?from=&status=reserved")
	mustContain(t, list, "鈴木 花子")
	mustContain(t, list, "佐藤 次郎")
	if strings.Contains(list.Body.String(), "山田 太郎") {
		t.Error("取り消した予約が、予約中の一覧に出ている")
	}
}

// 画面から同時に同じ予約を送っても、成功は1件だけ（HTTPから保存まで通した競合のテスト）。
func TestRealDBConcurrentPostsOnlyOneSucceeds(t *testing.T) {
	h, roomID := newRealEnv(t)

	const workers = 8
	codes := make([]int, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			form := realForm(roomID, fmt.Sprintf("客%d", i), "2026-12-01", "2026-12-04")
			<-start
			codes[i] = post(h, "/reservations", form).Code
		}()
	}
	close(start)
	wg.Wait()

	var created, refused int
	for _, c := range codes {
		switch c {
		case http.StatusSeeOther:
			created++
		case http.StatusUnprocessableEntity:
			refused++
		default:
			t.Errorf("想定外のステータス: %d", c)
		}
	}
	if created != 1 || refused != workers-1 {
		t.Errorf("登録 %d 件・拒否 %d 件, 期待 登録1・拒否%d", created, refused, workers-1)
	}
}
