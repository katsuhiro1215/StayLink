package main

import (
	"strings"
	"testing"
)

func TestReadPassword(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"改行つき", "secret-password\n", "secret-password", false},
		{"CRLF", "secret-password\r\n", "secret-password", false},
		{"改行なし（最後の行）", "secret-password", "secret-password", false},
		{"空白はパスワードの一部", " secret password \n", " secret password ", false},
		{"2行目は読まない", "first-line\nsecond-line\n", "first-line", false},
		{"空の入力", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readPassword(strings.NewReader(tt.in))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("readPassword = %q, %v", got, err)
			}
		})
	}
}

func TestRunRejectsMissingArguments(t *testing.T) {
	if err := run("admin", strings.NewReader("long-enough-password\n"), ""); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("DATABASE_URL が無いのに、エラーにならない: %v", err)
	}
	if err := run("", strings.NewReader("long-enough-password\n"), "postgres://x"); err == nil || !strings.Contains(err.Error(), "-username") {
		t.Errorf("-username が無いのに、エラーにならない: %v", err)
	}
}
