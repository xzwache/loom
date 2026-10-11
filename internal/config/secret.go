package config

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive string such as an API key. Formatting, logging and
// JSON encoding print a placeholder; only Reveal returns the value.
type Secret string

var _ slog.LogValuer = Secret("")

func (s Secret) Reveal() string               { return string(s) }
func (s Secret) String() string               { return s.mask() }
func (s Secret) GoString() string             { return s.mask() }
func (s Secret) LogValue() slog.Value         { return slog.StringValue(s.mask()) }
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.mask()), nil }

func (s Secret) mask() string {
	if s == "" {
		return ""
	}
	return redacted
}
