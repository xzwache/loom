package subagent

// Type is a kind of subagent, defined by a markdown file: front matter with
// name, description and optional tools and model, then the system prompt.
type Type struct {
	Name        string
	Description string
	// Tools limits the type to these tools; empty means every subagent tool.
	Tools []string
	// Model overrides the subagent model.
	Model  string
	Prompt string
	Path   string
}
