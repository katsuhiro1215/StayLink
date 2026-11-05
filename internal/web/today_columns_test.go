package web

import (
	"strings"
	"testing"
)

// 日付の列：到着・滞在中はチェックアウト日、出発はチェックイン日を出し、泊数を添える。
func TestHomeDateColumnPerSection(t *testing.T) {
	e := newEnv(t)
	add := func(roomID int64, roomName, guest, in, out string) {
		r := sample(t)
		r.RoomID, r.RoomName, r.GuestName, r.CheckIn, r.CheckOut = roomID, roomName, guest, day(t, in), day(t, out)
		e.seed(t, r)
	}
	add(1, "松の間", "前の人", "2026-11-03", "2026-11-05") // 2泊
	add(1, "松の間", "次の人", "2026-11-05", "2026-11-08") // 3泊
	body := get(e.h, "/").Body.String()

	tests := []struct{ id, want, header string }{
		{"arrivals", "2026-11-08（日）、3泊", "チェックアウト"},
		{"departures", "2026-11-03（火）、2泊", "チェックイン"},
	}
	for _, tt := range tests {
		s := section(t, body, tt.id)
		if !strings.Contains(s, tt.want) {
			t.Errorf("%s：日付・泊数に %q がない", tt.id, tt.want)
		}
		if !strings.Contains(s, ">"+tt.header+"</th>") {
			t.Errorf("%s：列見出しが %q でない", tt.id, tt.header)
		}
	}
}
