// Package childenv builds the environment of programs loom starts, such as
// hook scripts and MCP servers, without loom's own secrets.
package childenv

import (
	"slices"
	"strings"
)

// Hidden lists the variables child programs never see.
var Hidden = []string{"ANTHROPIC_API_KEY"}

// WithoutSecrets returns environ (as from os.Environ) minus the Hidden variables.
func WithoutSecrets(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(Hidden, name)
	})
}
