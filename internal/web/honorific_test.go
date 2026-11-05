package web

import (
	"strings"
	"testing"
)

func TestHonorific(t *testing.T) {
	tests := map[string]string{
		"山田 太郎": "山田 太郎さん",
		"山田様":   "山田様",
		"山田さん":  "山田さん",
		"田中さま":  "田中さま",
		"佐藤殿":   "佐藤殿",
		"鈴木先生":  "鈴木先生",
		"さんま":   "さんまさん", // 先頭の「さん」は敬称ではない
		"":      "さん",
	}
	for in, want := range tests {
		if got := honorific(in); got != want {
			t.Errorf("honorific(%q) = %q, 期待 %q", in, got, want)
		}
	}
}

// 名前が敬称で終わっていても、画面（カレンダーの読み上げ・重なり案内・一覧）で「様さん」にならない。
func TestHonorificNotDoubledOnScreens(t *testing.T) {
	e := newEnv(t)
	r := sample(t)
	r.GuestName, r.CheckIn, r.CheckOut = "山田様", day(t, "2026-11-05"), day(t, "2026-11-07")
	e.seed(t, r)

	for _, path := range []string{"/", "/calendar", "/reservations"} {
		body := get(e.h, path).Body.String()
		if strings.Contains(body, "様さん") {
			t.Errorf("%s：「様さん」になっている", path)
		}
		if !strings.Contains(body, "山田様") {
			t.Errorf("%s：名前が出ていない", path)
		}
	}
}
