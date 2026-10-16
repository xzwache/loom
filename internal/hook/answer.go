package hook

import "github.com/xzwache/loom/internal/event"

// Answer is what a hook may change on an event, beyond Reason and Halt, which
// every event accepts.
type Answer uint8

const (
	// AddsContext gives the model more text.
	AddsContext Answer = 1 << iota
	// Decides allows, asks about or denies the action.
	Decides
	RewritesInput
	RewritesOutput
	// Blocks stops the action, or on Stop keeps the run going.
	Blocks
)

// answers lists what each event accepts. An event that is not listed is a
// notice: hooks can watch it and halt the run, nothing else.
var answers = map[event.Name]Answer{
	event.SessionStart{}.Name():       AddsContext,
	event.UserPromptSubmit{}.Name():   AddsContext | Blocks,
	event.ModelRequest{}.Name():       AddsContext,
	event.SubagentStart{}.Name():      AddsContext,
	event.PreToolUse{}.Name():         Decides | RewritesInput,
	event.PermissionRequest{}.Name():  Decides,
	event.PostToolUse{}.Name():        AddsContext | RewritesOutput | Blocks,
	event.PostToolUseFailure{}.Name(): AddsContext,
	event.PostToolBatch{}.Name():      AddsContext,
	event.PreCompact{}.Name():         AddsContext,
	event.Stop{}.Name():               Blocks,
	event.SubagentStop{}.Name():       Blocks,
}

// field is one part of an Outcome that an event may not accept.
type field struct {
	answer Answer
	name   string
	isSet  func(Outcome) bool
	clear  func(*Outcome)
}

var fields = []field{
	{AddsContext, "additional context", func(o Outcome) bool { return o.AdditionalContext != "" }, func(o *Outcome) { o.AdditionalContext = "" }},
	{Decides, "decision", func(o Outcome) bool { return o.Decision != "" }, func(o *Outcome) { o.Decision = "" }},
	{RewritesInput, "updated input", func(o Outcome) bool { return o.UpdatedInput != nil }, func(o *Outcome) { o.UpdatedInput = nil }},
	{RewritesOutput, "updated output", func(o Outcome) bool { return o.UpdatedOutput != nil }, func(o *Outcome) { o.UpdatedOutput = nil }},
	{Blocks, "block", func(o Outcome) bool { return o.Block }, func(o *Outcome) { o.Block = false }},
}

// accepted clears what the event does not accept and logs each part it drops.
func (p *Pipeline) accepted(name event.Name, o Outcome) Outcome {
	allowed := answers[name]
	for _, f := range fields {
		if allowed&f.answer == 0 && f.isSet(o) {
			f.clear(&o)
			p.log().Warn("hook answer ignored", "event", name, "field", f.name)
		}
	}
	return o
}
