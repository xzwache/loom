package mcp

import (
	"errors"
	"fmt"
	"regexp"
)

// Transports a server can use.
const (
	Stdio = "stdio"
	HTTP  = "http"
)

type ServerConfig struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (c ServerConfig) Transport() string {
	if c.Type == "" {
		return Stdio
	}
	return c.Type
}

func (c ServerConfig) validate() error {
	switch c.Transport() {
	case Stdio:
		if c.Command == "" {
			return errors.New("stdio server needs command")
		}
	case HTTP:
		if c.URL == "" {
			return errors.New("http server needs url")
		}
	default:
		return fmt.Errorf("unknown type %q (want %s or %s)", c.Type, Stdio, HTTP)
	}
	return nil
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// Env is what ${VAR} in a server config may read. Refused, when set, names
// variables a server from the given source may not read, even with a default.
type Env struct {
	Lookup  func(name string) (string, bool)
	Refused func(source Source, name string) bool
}

func (c ServerConfig) expand(env Env, source Source) (ServerConfig, error) {
	var missing, refused []string
	sub := func(s string) string {
		return envRef.ReplaceAllStringFunc(s, func(ref string) string {
			m := envRef.FindStringSubmatch(ref)
			name, hasDefault, def := m[1], m[2] != "", m[3]
			if env.Refused != nil && env.Refused(source, name) {
				refused = append(refused, name)
				return ""
			}
			if v, ok := env.Lookup(name); ok && (v != "" || !hasDefault) {
				return v
			}
			if hasDefault {
				return def
			}
			missing = append(missing, name)
			return ""
		})
	}
	out := ServerConfig{Type: c.Type, Command: sub(c.Command), URL: sub(c.URL)}
	for _, a := range c.Args {
		out.Args = append(out.Args, sub(a))
	}
	out.Env = subValues(c.Env, sub)
	out.Headers = subValues(c.Headers, sub)
	var errs []error
	if len(refused) > 0 {
		errs = append(errs, fmt.Errorf("environment variables not given to %s servers: %v", source, refused))
	}
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("environment variables not set: %v", missing))
	}
	if err := errors.Join(errs...); err != nil {
		return ServerConfig{}, err
	}
	return out, nil
}

func subValues(m map[string]string, sub func(string) string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = sub(v)
	}
	return out
}
