package frontend

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
)

// State is what is happening now; a UI that connects mid-session starts
// from it.
type State struct {
	Tools     []Tool // open calls, oldest first
	Plan      []event.TodoItem
	Subagents []event.Agent
	Usage     llm.Usage // since Open
}

type Tool struct {
	ID     string
	Name   string
	Agent  event.Agent
	Status ToolStatus
	Input  json.RawMessage
}

// state is the live State, guarded by Session.mu.
type state struct {
	tools     []Tool
	plan      []event.TodoItem
	subagents []event.Agent
	usage     llm.Usage
}

func (s *state) snapshot() State {
	return State{
		Tools:     slices.Clone(s.tools),
		Plan:      slices.Clone(s.plan),
		Subagents: slices.Clone(s.subagents),
		Usage:     s.usage,
	}
}

func (s *state) getToolIndex(id string) int {
	return slices.IndexFunc(s.tools, func(t Tool) bool { return t.ID == id })
}

func (s *state) addTool(t ToolPending, from event.Agent) error {
	if s.getToolIndex(t.ID) >= 0 {
		return fmt.Errorf("tool %s is already open", t.ID)
	}
	s.tools = append(s.tools, Tool{ID: t.ID, Name: t.Name, Agent: from, Status: StatusPending})
	return nil
}

func (s *state) startTool(t ToolStarted) error {
	i := s.getToolIndex(t.ID)
	if i < 0 {
		return fmt.Errorf("tool %s started without being pending", t.ID)
	}
	if s.tools[i].Status != StatusPending {
		return fmt.Errorf("tool %s started twice", t.ID)
	}
	s.tools[i].Status, s.tools[i].Input = StatusRunning, t.Input
	return nil
}

func (s *state) setStatus(id string, status ToolStatus) error {
	i := s.getToolIndex(id)
	if i < 0 {
		return fmt.Errorf("tool %s is not open", id)
	}
	s.tools[i].Status = status
	return nil
}

func (s *state) finishTool(id string) error {
	i := s.getToolIndex(id)
	if i < 0 {
		return fmt.Errorf("tool %s finished, but it is not open", id)
	}
	s.tools = slices.Delete(s.tools, i, i+1)
	return nil
}

func (s *state) addSubagent(a SubagentStarted) error {
	if slices.ContainsFunc(s.subagents, func(o event.Agent) bool { return o.ID == a.ID }) {
		return fmt.Errorf("subagent %s started twice", a.ID)
	}
	s.subagents = append(s.subagents, event.Agent{ID: a.ID, Type: a.Type})
	return nil
}

func (s *state) removeSubagent(id string) error {
	i := slices.IndexFunc(s.subagents, func(o event.Agent) bool { return o.ID == id })
	if i < 0 {
		return fmt.Errorf("subagent %s stopped, but it is not running", id)
	}
	s.subagents = slices.Delete(s.subagents, i, i+1)
	return nil
}
