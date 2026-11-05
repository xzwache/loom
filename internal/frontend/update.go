package frontend

import (
	"encoding/json"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
)

// Update is one thing that happened. Agent is zero for the main agent.
type Update struct {
	Agent event.Agent
	Body  Body
}

// Body is closed: a UI handles it with a type switch.
type Body interface {
	// apply records the update in the live state, or says why it does not fit.
	apply(s *state, from event.Agent) error
}

type TextDelta struct {
	Text string
}

type ThinkingDelta struct {
	Thinking string
}

// ToolPending is a call the model is still writing: not checked, not run.
type ToolPending struct {
	ID   string
	Name string
}

// ToolStarted is a call that passed its checks and is running.
type ToolStarted struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolFinished: Output is the result, the error, or why it did not run.
type ToolFinished struct {
	ID     string
	Status ToolStatus
	Output string
}

type ToolStatus string

const (
	StatusPending          ToolStatus = "pending"
	StatusAwaitingApproval ToolStatus = "awaiting_approval"
	StatusRunning          ToolStatus = "running"
	StatusDone             ToolStatus = "done"
	StatusFailed           ToolStatus = "failed"
	StatusDenied           ToolStatus = "denied"
	StatusSkipped          ToolStatus = "skipped"
	StatusCancelled        ToolStatus = "cancelled"
)

type PlanChanged struct {
	Items []event.TodoItem
}

type SubagentStarted struct {
	ID   string
	Type string
}

type SubagentStopped struct {
	ID   string
	Type string
}

type Compacted struct {
	TokensBefore int
	TokensAfter  int
}

// Usage is one model call.
type Usage struct {
	Usage llm.Usage
}

type Notice struct {
	Kind    string
	Message string
}

// SessionStarted comes at Open and after /clear.
type SessionStarted struct {
	Source string
	Model  string
}

// SessionEnded: after EndExit a UI quits.
type SessionEnded struct {
	Reason EndReason
}

// TurnDone ends a prompt. Usage includes the subagents.
type TurnDone struct {
	Stop  agent.StopReason
	Usage llm.Usage
	Err   error
}

func (TextDelta) apply(*state, event.Agent) error         { return nil }
func (ThinkingDelta) apply(*state, event.Agent) error     { return nil }
func (Compacted) apply(*state, event.Agent) error         { return nil }
func (Notice) apply(*state, event.Agent) error            { return nil }
func (SessionStarted) apply(*state, event.Agent) error    { return nil }
func (SessionEnded) apply(*state, event.Agent) error      { return nil }
func (TurnDone) apply(*state, event.Agent) error          { return nil }
func (u Usage) apply(s *state, _ event.Agent) error       { s.usage = s.usage.Add(u.Usage); return nil }
func (p PlanChanged) apply(s *state, _ event.Agent) error { s.plan = p.Items; return nil }

func (t ToolPending) apply(s *state, from event.Agent) error  { return s.addTool(t, from) }
func (t ToolStarted) apply(s *state, _ event.Agent) error     { return s.startTool(t) }
func (t ToolFinished) apply(s *state, _ event.Agent) error    { return s.finishTool(t.ID) }
func (a SubagentStarted) apply(s *state, _ event.Agent) error { return s.addSubagent(a) }
func (a SubagentStopped) apply(s *state, _ event.Agent) error { return s.removeSubagent(a.ID) }

// bodyOf is false for events a UI does not need.
func bodyOf(e event.Event) (Body, bool) {
	switch e := e.(type) {
	case event.ModelTextDelta:
		return TextDelta{Text: e.Text}, true
	case event.ModelThinkingDelta:
		return ThinkingDelta{Thinking: e.Thinking}, true
	case event.ModelToolUse:
		return ToolPending{ID: e.ToolUseID, Name: e.ToolName}, true
	case event.ToolRunning:
		return ToolStarted{ID: e.ToolUseID, Name: e.ToolName, Input: e.ToolInput}, true
	case event.PostToolUse:
		return ToolFinished{ID: e.ToolUseID, Status: StatusDone, Output: e.ToolResponse}, true
	case event.PostToolUseFailure:
		return ToolFinished{ID: e.ToolUseID, Status: StatusFailed, Output: e.Error}, true
	case event.PermissionDenied:
		return ToolFinished{ID: e.ToolUseID, Status: StatusDenied, Output: e.Reason}, true
	case event.TodoUpdated:
		return PlanChanged{Items: e.Items}, true
	case event.SubagentStart:
		return SubagentStarted{ID: e.AgentID, Type: e.AgentType}, true
	case event.SubagentStop:
		return SubagentStopped{ID: e.AgentID, Type: e.AgentType}, true
	case event.PostCompact:
		return Compacted{TokensBefore: e.TokensBefore, TokensAfter: e.TokensAfter}, true
	case event.ModelResponse:
		return Usage{Usage: e.Usage}, true
	case event.SessionStart:
		return SessionStarted{Source: e.Source, Model: e.Model}, true
	case event.SessionEnd:
		return SessionEnded{Reason: EndReason(e.Reason)}, true
	case event.Notification:
		return Notice{Kind: e.Kind, Message: e.Message}, true
	case event.MCPServerFailed:
		return Notice{Kind: "mcp_server_failed", Message: e.Server + ": " + e.Error}, true
	default:
		return nil, false
	}
}
