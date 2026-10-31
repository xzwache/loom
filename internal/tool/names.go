package tool

import "strings"

// Names of the built-in tools.
const (
	Agent  = "agent"
	Bash   = "bash"
	Edit   = "edit"
	Glob   = "glob"
	Memory = "memory"
	Read   = "read"
	Skill  = "skill"
	Task   = "task"
	Todo   = "todo"
	Write  = "write"
)

// MainOnly are the tools only the main agent gets: a subagent cannot start
// subagents, change the main todo list or write memory. Subagents get a
// read-only memory tool of their own.
var MainOnly = []string{Agent, Memory, Todo}

// MCPPrefix starts every MCP tool name: mcp__<server>__<tool>.
const MCPPrefix = "mcp__"

// Covers reports whether entry, from a tool list or a permission rule, names
// the tool: the same name, or an MCP server ("mcp__github") for each of its
// tools.
func Covers(entry, name string) bool {
	if entry == name {
		return true
	}
	server, ok := strings.CutPrefix(entry, MCPPrefix)
	return ok && server != "" && !strings.Contains(server, "__") && strings.HasPrefix(name, entry+"__")
}
