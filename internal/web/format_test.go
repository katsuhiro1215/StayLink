package web

import "testing"

func TestJapaneseRange(t *testing.T) {
	tests := []struct {
		in, out string
		want    string
	}{
		{"2026-11-10", "2026-11-13", "2026年11月10日〜11月13日"},
		{"2026-11-30", "2026-12-02", "2026年11月30日〜12月2日"},     // 月またぎ
		{"2026-12-30", "2027-01-02", "2026年12月30日〜2027年1月2日"}, // 年またぎは、後ろの年も書く
		{"2026-01-05", "2026-01-06", "2026年1月5日〜1月6日"},        // 0 埋めしない
	}
	for _, tt := range tests {
		if got := japaneseRange(day(t, tt.in), day(t, tt.out)); got != tt.want {
			t.Errorf("%s〜%s: %q, 期待 %q", tt.in, tt.out, got, tt.want)
		}
	}
}
