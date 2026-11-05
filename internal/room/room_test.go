package room

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name       string
		room       Room
		wantFields []string // エラーになるべき項目
	}{
		{"正常", Room{Name: "松の間", Capacity: 4}, nil},
		{"名前の前後の空白は取り除く", Room{Name: "  竹の間  ", Capacity: 2}, nil},
		{"名前が空", Room{Name: "", Capacity: 2}, []string{"name"}},
		{"名前が空白だけ", Room{Name: "　 ", Capacity: 2}, []string{"name"}},
		{"名前が50文字ちょうど", Room{Name: strings.Repeat("あ", 50), Capacity: 2}, nil},
		{"名前が51文字", Room{Name: strings.Repeat("あ", 51), Capacity: 2}, []string{"name"}},
		{"定員0", Room{Name: "梅の間", Capacity: 0}, []string{"capacity"}},
		{"定員が負", Room{Name: "梅の間", Capacity: -1}, []string{"capacity"}},
		{"定員100", Room{Name: "大広間", Capacity: 100}, nil},
		{"定員101", Room{Name: "大広間", Capacity: 101}, []string{"capacity"}},
		{"両方誤り", Room{Name: "", Capacity: 0}, []string{"name", "capacity"}},
		{"不正なUTF-8", Room{Name: "松\xffの間", Capacity: 2}, []string{"name"}},
		{"NUL文字", Room{Name: "松\x00の間", Capacity: 2}, []string{"name"}},
		{"改行", Room{Name: "松の\n間", Capacity: 2}, []string{"name"}},
		{"タブ", Room{Name: "松の\t間", Capacity: 2}, []string{"name"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.room.Validate()

			if len(errs) != len(tt.wantFields) {
				t.Fatalf("エラー項目 = %v, 期待 %v", errs, tt.wantFields)
			}
			for _, f := range tt.wantFields {
				if errs[f] == "" {
					t.Errorf("%q のエラーがない（得られたエラー: %v）", f, errs)
				}
			}
		})
	}
}

func TestNormalizeTrimsName(t *testing.T) {
	r := Room{Name: "  竹の間  ", Capacity: 2}
	r.Normalize()
	if r.Name != "竹の間" {
		t.Errorf("Name = %q, 期待 %q", r.Name, "竹の間")
	}
}

// Validate は検証だけで、値を書き換えない。
func TestValidateDoesNotModify(t *testing.T) {
	r := Room{Name: "  竹の間  ", Capacity: 2}
	if errs := r.Validate(); len(errs) != 0 {
		t.Fatalf("前後に空白があっても誤りではない: %v", errs)
	}
	if r.Name != "  竹の間  " {
		t.Errorf("Validate が Name を書き換えた: %q", r.Name)
	}
}
