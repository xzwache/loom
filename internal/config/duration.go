package config

import (
	"fmt"
	"time"
)

// Duration is a time.Duration written as a string such as "90s" or "10m" in
// config files.
type Duration time.Duration

func (d Duration) Std() time.Duration           { return time.Duration(d) }
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", b, err)
	}
	*d = Duration(v)
	return nil
}
