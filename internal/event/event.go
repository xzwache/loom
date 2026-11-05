// Package event defines the lifecycle events that hooks and the trace receive.
// Names and JSON fields follow Claude Code hooks where an equivalent exists.
package event

import (
	"context"
	"encoding/json"

	"github.com/xzwache/loom/internal/llm"
)

// SchemaVersion changes whenever an event's name or fields change incompatibly.
const SchemaVersion = 1

type Name string

type Event interface {
	Name() Name
}

// Agent names the subagent whose run fires an event.
type Agent struct {
	ID   string
	Type string
}

type agentKey struct{}

func WithAgent(ctx context.Context, a Agent) context.Context {
	return context.WithValue(ctx, agentKey{}, a)
}

// AgentFrom reports the subagent firing events with ctx; false means the main agent.
func AgentFrom(ctx context.Context) (Agent, bool) {
	a, ok := ctx.Value(agentKey{}).(Agent)
	return a, ok
}

type ToolCall struct {
	ToolUseID string          `json:"tool_use_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

type ToolOutcome struct {
	ToolUseID string `json:"tool_use_id"`
	ToolName  string `json:"tool_name"`
	IsError   bool   `json:"is_error"`
}

// Session

type SessionStart struct {
	Source string `json:"source"`
	Model  string `json:"model"`
	Cwd    string `json:"cwd"`
}

type SessionEnd struct {
	Reason string `json:"reason"`
}

// Turn

type UserPromptSubmit struct {
	Prompt string `json:"prompt"`
}

type Stop struct {
	StopHookActive       bool   `json:"stop_hook_active"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

type StopFailure struct {
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

// Model

type ModelRequest struct {
	Turn         int    `json:"turn"`
	Model        string `json:"model"`
	MessageCount int    `json:"message_count"`
	ToolCount    int    `json:"tool_count"`
}

type ModelResponse struct {
	Turn       int            `json:"turn"`
	StopReason llm.StopReason `json:"stop_reason"`
	Usage      llm.Usage      `json:"usage"`
	Text       string         `json:"text,omitempty"`
	ToolUses   []ToolCall     `json:"tool_uses,omitempty"`
}

type PreModelSwitch struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type PostModelSwitch struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Budget

type BudgetExceeded struct {
	Budget string `json:"budget"`
	Limit  int64  `json:"limit"`
	Used   int64  `json:"used"`
}

// Tools

type PreToolUse struct {
	ToolCall
}

type PostToolUse struct {
	ToolCall
	ToolResponse string `json:"tool_response"`
}

type PostToolUseFailure struct {
	ToolCall
	Error string `json:"error"`
}

type PostToolBatch struct {
	Results []ToolOutcome `json:"results"`
}

// Permissions

type PermissionRequest struct {
	ToolCall
}

type PermissionDenied struct {
	ToolCall
	Reason string `json:"reason"`
}

// Prompt, todo, skills, memory

type InstructionsLoaded struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

type TodoUpdated struct {
	Total int        `json:"total"`
	Done  int        `json:"done"`
	Items []TodoItem `json:"items"`
}

type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type SkillLoaded struct {
	Skill string `json:"skill"`
	Path  string `json:"path"`
}

type SkillUsed struct {
	Skill string `json:"skill"`
}

type MemoryRead struct {
	Path string `json:"path"`
}

// MemoryWrite fires when a memory is saved or removed. Action is create,
// update or delete; By is the model (the memory tool), or extraction or
// consolidation at the end of a session.
type MemoryWrite struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	By     string `json:"by"`
}

// Subagents and tasks

type SubagentStart struct {
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// SubagentStop fires when a subagent finishes, in place of Stop. Blocking it
// makes the subagent keep working.
type SubagentStop struct {
	AgentID              string `json:"agent_id"`
	AgentType            string `json:"agent_type"`
	StopHookActive       bool   `json:"stop_hook_active"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

type TaskCreated struct {
	TaskID  string `json:"task_id"`
	Subject string `json:"subject"`
}

// TaskCompleted fires when a task is completed; Owner is the agent that had
// claimed it.
type TaskCompleted struct {
	TaskID  string `json:"task_id"`
	Subject string `json:"subject"`
	Owner   string `json:"owner"`
}

// Compaction

type PreCompact struct {
	Trigger string `json:"trigger"`
	Tokens  int    `json:"tokens"`
}

type PostCompact struct {
	Trigger      string `json:"trigger"`
	Strategy     string `json:"strategy"`
	TokensBefore int    `json:"tokens_before"`
	TokensAfter  int    `json:"tokens_after"`
	// Failed counts parts the strategy had to leave, such as outputs it
	// could not save; Error is the first reason.
	Failed int    `json:"failed,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Display

type Notification struct {
	Kind    string `json:"notification_type"`
	Message string `json:"message"`
}

type MessageDisplay struct {
	Text string `json:"text"`
}

// Shown events go to UIs only (hook.Pipeline.Show): hooks and the trace never
// see them, and config cannot name them.

// ModelTextDelta is a piece of the model's answer as it streams.
type ModelTextDelta struct {
	Text string `json:"text"`
}

type ModelThinkingDelta struct {
	Thinking string `json:"thinking"`
}

// ModelToolUse fires when the model starts writing a tool call; its input is
// still streaming.
type ModelToolUse struct {
	ToolUseID string `json:"tool_use_id"`
	ToolName  string `json:"tool_name"`
}

// ToolRunning fires when a tool call has passed its checks and starts.
type ToolRunning struct {
	ToolCall
}

// Environment

type ConfigChange struct {
	Path string `json:"path"`
}

type FileChanged struct {
	Path string `json:"path"`
}

type CwdChanged struct {
	OldCwd string `json:"old_cwd"`
	NewCwd string `json:"new_cwd"`
}

// MCP servers. Source is "user" or "project"; none of them carries a
// server's command, URL, env or headers, which may hold secrets.

type MCPServerConnected struct {
	Server    string   `json:"server"`
	Source    string   `json:"source"`
	Transport string   `json:"transport"` // "stdio" or "http"
	Tools     []string `json:"tools"`     // registered names, e.g. mcp__github__create_issue
}

// MCPServerSkipped fires for a server loom chose not to start, such as a
// project server missing from mcp.enabled_project_servers.
type MCPServerSkipped struct {
	Server string `json:"server"`
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// MCPServerFailed fires when a server could not start. Stage "config" is a
// bad entry or a missing ${VAR}; "connect" is the start, handshake or tool list.
type MCPServerFailed struct {
	Server string `json:"server"`
	Source string `json:"source"`
	Stage  string `json:"stage"`
	Error  string `json:"error"`
}

var catalog = []Event{
	SessionStart{},
	SessionEnd{},
	UserPromptSubmit{},
	Stop{},
	StopFailure{},
	ModelRequest{},
	ModelResponse{},
	PreModelSwitch{},
	PostModelSwitch{},
	BudgetExceeded{},
	PreToolUse{},
	PostToolUse{},
	PostToolUseFailure{},
	PostToolBatch{},
	PermissionRequest{},
	PermissionDenied{},
	InstructionsLoaded{},
	TodoUpdated{},
	SkillLoaded{},
	SkillUsed{},
	MemoryRead{},
	MemoryWrite{},
	SubagentStart{},
	SubagentStop{},
	TaskCreated{},
	TaskCompleted{},
	PreCompact{},
	PostCompact{},
	Notification{},
	MessageDisplay{},
	ConfigChange{},
	FileChanged{},
	CwdChanged{},
	MCPServerConnected{},
	MCPServerSkipped{},
	MCPServerFailed{},
}

func Known(name Name) bool {
	for _, e := range catalog {
		if e.Name() == name {
			return true
		}
	}
	return false
}

// Tool returns the tool call an event is about, if any.
func Tool(e Event) (ToolCall, bool) {
	switch ev := e.(type) {
	case PreToolUse:
		return ev.ToolCall, true
	case PostToolUse:
		return ev.ToolCall, true
	case PostToolUseFailure:
		return ev.ToolCall, true
	case PermissionRequest:
		return ev.ToolCall, true
	case PermissionDenied:
		return ev.ToolCall, true
	default:
		return ToolCall{}, false
	}
}

func (SessionStart) Name() Name       { return "SessionStart" }
func (SessionEnd) Name() Name         { return "SessionEnd" }
func (UserPromptSubmit) Name() Name   { return "UserPromptSubmit" }
func (Stop) Name() Name               { return "Stop" }
func (StopFailure) Name() Name        { return "StopFailure" }
func (ModelRequest) Name() Name       { return "ModelRequest" }
func (ModelResponse) Name() Name      { return "ModelResponse" }
func (PreModelSwitch) Name() Name     { return "PreModelSwitch" }
func (PostModelSwitch) Name() Name    { return "PostModelSwitch" }
func (BudgetExceeded) Name() Name     { return "BudgetExceeded" }
func (PreToolUse) Name() Name         { return "PreToolUse" }
func (PostToolUse) Name() Name        { return "PostToolUse" }
func (PostToolUseFailure) Name() Name { return "PostToolUseFailure" }
func (PostToolBatch) Name() Name      { return "PostToolBatch" }
func (PermissionRequest) Name() Name  { return "PermissionRequest" }
func (PermissionDenied) Name() Name   { return "PermissionDenied" }
func (InstructionsLoaded) Name() Name { return "InstructionsLoaded" }
func (TodoUpdated) Name() Name        { return "TodoUpdated" }
func (SkillLoaded) Name() Name        { return "SkillLoaded" }
func (SkillUsed) Name() Name          { return "SkillUsed" }
func (MemoryRead) Name() Name         { return "MemoryRead" }
func (MemoryWrite) Name() Name        { return "MemoryWrite" }
func (SubagentStart) Name() Name      { return "SubagentStart" }
func (SubagentStop) Name() Name       { return "SubagentStop" }
func (TaskCreated) Name() Name        { return "TaskCreated" }
func (TaskCompleted) Name() Name      { return "TaskCompleted" }
func (PreCompact) Name() Name         { return "PreCompact" }
func (PostCompact) Name() Name        { return "PostCompact" }
func (Notification) Name() Name       { return "Notification" }
func (MessageDisplay) Name() Name     { return "MessageDisplay" }
func (ModelTextDelta) Name() Name     { return "ModelTextDelta" }
func (ModelThinkingDelta) Name() Name { return "ModelThinkingDelta" }
func (ModelToolUse) Name() Name       { return "ModelToolUse" }
func (ToolRunning) Name() Name        { return "ToolRunning" }
func (ConfigChange) Name() Name       { return "ConfigChange" }
func (FileChanged) Name() Name        { return "FileChanged" }
func (CwdChanged) Name() Name         { return "CwdChanged" }
func (MCPServerConnected) Name() Name { return "MCPServerConnected" }
func (MCPServerSkipped) Name() Name   { return "MCPServerSkipped" }
func (MCPServerFailed) Name() Name    { return "MCPServerFailed" }
