// Package event defines the lifecycle events that hooks and the trace receive.
// Names and JSON fields follow Claude Code hooks where an equivalent exists.
package event

import (
	"encoding/json"

	"github.com/xzwache/loom/internal/llm"
)

// SchemaVersion changes whenever an event's name or fields change incompatibly.
const SchemaVersion = 1

type Name string

type Event interface {
	Name() Name
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
	Total int `json:"total"`
	Done  int `json:"done"`
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

type MemoryWrite struct {
	Path string `json:"path"`
}

// Subagents and tasks

type SubagentStart struct {
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

type SubagentStop struct {
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
	Stop      string `json:"stop"`
}

type TaskCreated struct {
	TaskID  string `json:"task_id"`
	Subject string `json:"subject"`
}

type TaskCompleted struct {
	TaskID string `json:"task_id"`
}

// Compaction

type PreCompact struct {
	Trigger string `json:"trigger"`
	Tokens  int    `json:"tokens"`
}

type PostCompact struct {
	Trigger      string `json:"trigger"`
	TokensBefore int    `json:"tokens_before"`
	TokensAfter  int    `json:"tokens_after"`
}

// Display

type Notification struct {
	Kind    string `json:"notification_type"`
	Message string `json:"message"`
}

type MessageDisplay struct {
	Text string `json:"text"`
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
func (ConfigChange) Name() Name       { return "ConfigChange" }
func (FileChanged) Name() Name        { return "FileChanged" }
func (CwdChanged) Name() Name         { return "CwdChanged" }
