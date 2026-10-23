package childenv

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestHides(t *testing.T) {
	f := Filter{Hide: []string{"MY_CORP_*"}, Pass: []string{"GITHUB_TOKEN"}}
	tests := []struct {
		name string
		want bool
	}{
		{"ANTHROPIC_API_KEY", true},
		{"anthropic_api_key", true},
		{"OPENAI_API_KEY", true},
		{"GH_TOKEN", true},
		{"AWS_SECRET_ACCESS_KEY", true},
		{"DB_PASSWORD", true},
		{"MY_CORP_THING", true},
		{"GITHUB_TOKEN", false},
		{"PATH", false},
		{"HOME", false},
		{"TOKENIZER_MODE", false},
	}
	for _, tt := range tests {
		if got := f.Hides(tt.name); got != tt.want {
			t.Errorf("Hides(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestZeroFilterHidesDefaults(t *testing.T) {
	if !(Filter{}).Hides("SOME_API_KEY") {
		t.Error("zero Filter does not hide SOME_API_KEY")
	}
}

func TestApply(t *testing.T) {
	environ := []string{"PATH=/bin", "ANTHROPIC_API_KEY=sk-test", "GH_TOKEN=x", "HOME=/home/u"}
	f := Filter{}
	if diff := cmp.Diff([]string{"PATH=/bin", "HOME=/home/u"}, f.Apply(environ)); diff != "" {
		t.Errorf("Apply mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"ANTHROPIC_API_KEY", "GH_TOKEN"}, f.Hidden(environ)); diff != "" {
		t.Errorf("Hidden mismatch (-want +got):\n%s", diff)
	}
	if environ[1] != "ANTHROPIC_API_KEY=sk-test" {
		t.Error("Apply changed its input")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		filter Filter
		want   string
	}{
		{"ok", Filter{Hide: []string{"X_*"}, Pass: []string{"GITHUB_TOKEN"}}, ""},
		{"bad pattern", Filter{Hide: []string{"X_["}}, "not a valid pattern"},
		{"empty pattern", Filter{Hide: []string{""}}, "not a valid pattern"},
		{"pattern in pass", Filter{Pass: []string{"*_TOKEN"}}, "not a variable name"},
		{"loom's key", Filter{Pass: []string{"anthropic_api_key"}}, "never passed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.filter.Validate()
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Errorf("Validate() = %v, want %q", err, tt.want)
			}
		})
	}
}
