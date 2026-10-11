package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretNeverPrints(t *testing.T) {
	const value = "sk-ant-very-secret"
	s := Secret(value)
	cfg := Config{Model: "m", APIKey: s}

	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("start", "key", s)
	jsonOut, err := json.Marshal(struct{ Key Secret }{s})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	outputs := map[string]string{
		"%v":       fmt.Sprintf("%v", s),
		"%#v":      fmt.Sprintf("%#v", s),
		"%+v cfg":  fmt.Sprintf("%+v", cfg),
		"slog":     logs.String(),
		"json":     string(jsonOut),
		"cfg json": mustJSON(t, cfg),
	}
	for name, out := range outputs {
		if strings.Contains(out, value) {
			t.Errorf("%s output leaks the secret: %s", name, out)
		}
	}
	if got := s.Reveal(); got != value {
		t.Errorf("Reveal() = %q, want %q", got, value)
	}
}

func TestEmptySecretPrintsEmpty(t *testing.T) {
	if got := Secret("").String(); got != "" {
		t.Errorf(`Secret("").String() = %q, want ""`, got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}
