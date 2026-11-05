package web

import (
	"strings"
	"testing"
)

// 重なりの案内文でも、名前が敬称で終わっているときに敬称を重ねない（「山田様さん」「山田さんさん」にならない）。
func TestOverlapExplanationDoesNotDoubleHonorific(t *testing.T) {
	for _, name := range []string{"山田様", "山田さん"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			r := sample(t)
			r.GuestName = name
			e.seed(t, r) // 松の間 11/10〜11/12

			rec := post(e.h, "/reservations", with(validForm(), "guest_name", "新しい客", "check_in", "2026-11-11", "check_out", "2026-11-13"))
			body := rec.Body.String()
			if !strings.Contains(body, "次の予約があります："+name+"（2026年11月10日〜11月12日）") {
				t.Errorf("案内文に %q が、敬称を重ねずに出ていない", name)
			}
			if strings.Contains(body, name+"さん（") {
				t.Errorf("敬称が重なっている：%q", name+"さん")
			}
		})
	}
}
