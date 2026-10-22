package childenv

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestWithoutSecrets(t *testing.T) {
	environ := []string{"PATH=/bin", "ANTHROPIC_API_KEY=sk-test", "HOME=/home/u", "ANTHROPIC_API_KEY_NOTE=kept"}
	got := WithoutSecrets(environ)
	want := []string{"PATH=/bin", "HOME=/home/u", "ANTHROPIC_API_KEY_NOTE=kept"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("WithoutSecrets mismatch (-want +got):\n%s", diff)
	}
	if environ[1] != "ANTHROPIC_API_KEY=sk-test" {
		t.Error("WithoutSecrets changed its input")
	}
}
