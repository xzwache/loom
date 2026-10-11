package config

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive string such as an API key. Formatting, logging and
// JSON encoding print a placeholder; only Reveal returns the value.
type Secret string

var _ slog.LogValuer = Secret("")

// Reveal returns the secret value. Call it only where the value is sent to
// its intended destination.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer.
func (s Secret) String() string { return s.mask() }

// GoString implements fmt.GoStringer, which covers the %#v verb.
func (s Secret) GoString() string { return s.mask() }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.mask()) }

// MarshalText implements encoding.TextMarshaler, which encoding/json uses.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.mask()), nil }

func (s Secret) mask() string {
	if s == "" {
		return ""
	}
	return redacted
}
