package tool

// Names of the built-in tools.
const (
	Agent = "agent"
	Bash  = "bash"
	Edit  = "edit"
	Glob  = "glob"
	Read  = "read"
	Todo  = "todo"
	Write = "write"
)

// MainOnly are the tools only the main agent gets: a subagent cannot start
// subagents or change the main todo list.
var MainOnly = []string{Agent, Todo}
