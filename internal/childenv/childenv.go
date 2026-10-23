// Package childenv builds the environment of programs loom starts: bash
// commands, hook scripts and MCP servers. Variables that look like secrets
// are left out unless the user passes them by name; loom's own API key is
// never passed.
package childenv

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// DefaultHide matches common secret names. Matching ignores case.
var DefaultHide = []string{
	"*_API_KEY", "*_APIKEY", "*_ACCESS_KEY", "*_PRIVATE_KEY",
	"*_TOKEN", "*_SECRET", "*_SECRET_*", "*_PASSWORD", "*_PASSWD", "*_CREDENTIALS",
}

// alwaysHidden are loom's own secrets; Pass cannot expose them.
var alwaysHidden = []string{"ANTHROPIC_API_KEY"}

type Filter struct {
	Hide []string `json:"hide,omitempty"`
	Pass []string `json:"pass,omitempty"`
}

func (f Filter) Validate() error {
	var errs []error
	for _, p := range f.Hide {
		if _, err := path.Match(p, ""); p == "" || err != nil {
			errs = append(errs, fmt.Errorf("child_env.hide: %q is not a valid pattern", p))
		}
	}
	for _, name := range f.Pass {
		switch {
		case name == "" || strings.ContainsAny(name, "=*?["):
			errs = append(errs, fmt.Errorf("child_env.pass: %q is not a variable name", name))
		case slices.ContainsFunc(alwaysHidden, func(h string) bool { return strings.EqualFold(h, name) }):
			errs = append(errs, fmt.Errorf("child_env.pass: %s is loom's own secret and is never passed", name))
		}
	}
	return errors.Join(errs...)
}

// NeverPassed reports loom's own secrets, which no child process may get,
// not even through an MCP server's ${VAR}.
func NeverPassed(name string) bool {
	return slices.Contains(alwaysHidden, strings.ToUpper(name))
}

func (f Filter) Hides(name string) bool {
	if NeverPassed(name) {
		return true
	}
	upper := strings.ToUpper(name)
	if slices.ContainsFunc(f.Pass, func(p string) bool { return strings.EqualFold(p, name) }) {
		return false
	}
	for _, p := range slices.Concat(DefaultHide, f.Hide) {
		if ok, _ := path.Match(strings.ToUpper(p), upper); ok {
			return true
		}
	}
	return false
}

// Apply returns environ (as from os.Environ) without the hidden variables.
func (f Filter) Apply(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return f.Hides(name)
	})
}

// Hidden lists the names in environ that Apply leaves out, for the log.
func (f Filter) Hidden(environ []string) []string {
	var names []string
	for _, kv := range environ {
		if name, _, _ := strings.Cut(kv, "="); f.Hides(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
