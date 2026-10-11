package tool

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{name: "under limit", in: "short", limit: 10, want: "short"},
		{name: "keeps head and tail", in: "aaaaXXXXXXbbbb", limit: 8, want: "aaaa\n... [6 bytes truncated] ...\nbbbb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truncate(tt.in, tt.limit); got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.limit, got, tt.want)
			}
		})
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	got := Truncate(strings.Repeat("é", 50), 11)
	if !utf8.ValidString(got) {
		t.Errorf("Truncate produced invalid UTF-8: %q", got)
	}
}
