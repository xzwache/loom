package memory

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

var (
	//go:embed extract.md
	extractPrompt string
	//go:embed select.md
	selectPrompt string
	//go:embed consolidate.md
	consolidatePrompt string
)

const (
	maxExtracted = 5
	// maxKnown caps the existing memories sent to extraction, in bytes.
	maxKnown = 20_000
	// maxConsolidate caps the memories sent to consolidation, in bytes. Over
	// it, consolidation fails rather than work on a part of them.
	maxConsolidate = 100_000
)

// Helper makes the model calls memory needs besides the agent's own:
// extraction at the end of a session, selection when the list does not fit
// the prompt, and consolidation when there are many memories. A cheap model
// does well here.
type Helper struct {
	Model     llm.Model
	ModelID   string
	MaxTokens int
	// MaxInput caps the conversation sent to extraction, in bytes; over it,
	// the middle is cut. 0 means no cap.
	MaxInput int
}

// ask sends one request and decodes the JSON object in the answer into v.
func (h Helper) ask(ctx context.Context, system, text string, v any) (llm.Usage, error) {
	resp, err := llm.Send(ctx, h.Model, llm.Request{
		Model:     h.ModelID,
		System:    system,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.UserText(text)}}},
		MaxTokens: h.MaxTokens,
	})
	if err != nil {
		return llm.Usage{}, err
	}
	answer := responseText(resp.Content)
	start, end := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	if start < 0 || end < start {
		return resp.Usage, errors.New("the model did not answer with JSON")
	}
	if err := json.Unmarshal([]byte(answer[start:end+1]), v); err != nil {
		return resp.Usage, fmt.Errorf("read the model's answer: %w", err)
	}
	return resp.Usage, nil
}

type Extracted struct {
	Memories []Memory
	// Rejected are candidates that failed validation, with why.
	Rejected []error
	Usage    llm.Usage
}

// candidate is a memory as the model writes it in JSON.
type candidate struct {
	Scope       Scope  `json:"scope"`
	Name        string `json:"name"`
	Type        Type   `json:"type"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

func (c candidate) memory() Memory {
	return Memory{Scope: c.Scope, Name: c.Name, Type: c.Type, Description: strings.TrimSpace(c.Description), Content: c.Content}
}

// Extract picks memories from a finished session. It sees only the user's
// and the agent's text: tool output is where text from files and web pages
// lands, and that must not become a memory unchecked. A conversation without
// text costs no call.
func (h Helper) Extract(ctx context.Context, messages []llm.Message, known []Memory) (Extracted, error) {
	conversation := conversationText(messages)
	if conversation == "" {
		return Extracted{}, nil
	}
	if h.MaxInput > 0 {
		conversation = tool.Truncate(conversation, h.MaxInput)
	}
	var answer struct {
		Memories []candidate `json:"memories"`
	}
	text := "Existing memories:\n\n" + tool.Truncate(knownText(known), maxKnown) + "\n\nSession:\n\n" + conversation
	usage, err := h.ask(ctx, extractPrompt, text, &answer)
	out := Extracted{Usage: usage}
	if err != nil {
		return out, err
	}
	for i, c := range answer.Memories {
		m := c.memory()
		switch err := m.Validate(); {
		case i >= maxExtracted:
			out.Rejected = append(out.Rejected, fmt.Errorf("memory %q: more than %d in one session", m.Name, maxExtracted))
		case err != nil:
			out.Rejected = append(out.Rejected, fmt.Errorf("memory %q: %w", m.Name, err))
		default:
			out.Memories = append(out.Memories, m)
		}
	}
	return out, nil
}

// Select picks at most limit of candidates that may help with request, by
// name and description only.
func (h Helper) Select(ctx context.Context, request string, candidates []Memory, limit int) ([]Memory, llm.Usage, error) {
	if len(candidates) == 0 || strings.TrimSpace(request) == "" {
		return nil, llm.Usage{}, nil
	}
	var list strings.Builder
	for i, m := range candidates {
		fmt.Fprintf(&list, "%d. %s\n", i, m.Line())
	}
	var answer struct {
		Pick []int `json:"pick"`
	}
	text := fmt.Sprintf("Pick at most %d.\n\nMemories:\n%s\nRequest:\n%s", limit, list.String(), request)
	usage, err := h.ask(ctx, selectPrompt, text, &answer)
	if err != nil {
		return nil, usage, err
	}
	var picked []Memory
	seen := map[int]bool{}
	for _, i := range answer.Pick {
		if i < 0 || i >= len(candidates) {
			return nil, usage, fmt.Errorf("the model picked memory %d of %d", i, len(candidates))
		}
		if !seen[i] && len(picked) < limit {
			seen[i] = true
			picked = append(picked, candidates[i])
		}
	}
	return picked, usage, nil
}

// Plan is a consolidation: memories to write (merged or corrected) and
// memories to delete.
type Plan struct {
	Write  []Memory
	Delete []Key
}

type Key struct {
	Scope Scope  `json:"scope"`
	Name  string `json:"name"`
}

func (p Plan) Empty() bool { return len(p.Write) == 0 && len(p.Delete) == 0 }

// Consolidate asks for a plan that merges duplicates and drops what is stale
// or wrong. The plan is checked before it is returned: every write is valid,
// every delete names an existing memory, nothing is both written and deleted,
// and at most half of the memories go away, so one bad answer cannot wipe
// the memory.
func (h Helper) Consolidate(ctx context.Context, mems []Memory) (Plan, llm.Usage, error) {
	text := knownText(mems)
	if len(text) > maxConsolidate {
		return Plan{}, llm.Usage{}, fmt.Errorf("memories are %d bytes, more than the %d one consolidation takes", len(text), maxConsolidate)
	}
	var answer struct {
		Write  []candidate `json:"write"`
		Delete []Key       `json:"delete"`
	}
	usage, err := h.ask(ctx, consolidatePrompt, "Memories:\n\n"+text, &answer)
	if err != nil {
		return Plan{}, usage, err
	}
	plan := Plan{Delete: answer.Delete}
	for _, c := range answer.Write {
		plan.Write = append(plan.Write, c.memory())
	}
	return plan, usage, checkPlan(plan, mems)
}

func checkPlan(plan Plan, mems []Memory) error {
	exists := map[Key]bool{}
	for _, m := range mems {
		exists[Key{m.Scope, m.Name}] = true
	}
	written := map[Key]bool{}
	var errs []error
	for _, m := range plan.Write {
		if err := m.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("write %q: %w", m.Name, err))
		}
		written[Key{m.Scope, m.Name}] = true
	}
	deleted := map[Key]bool{}
	for _, k := range plan.Delete {
		switch {
		case !exists[k]:
			errs = append(errs, fmt.Errorf("delete %s/%s: no such memory", k.Scope, k.Name))
		case written[k]:
			errs = append(errs, fmt.Errorf("%s/%s is both written and deleted", k.Scope, k.Name))
		}
		deleted[k] = true
	}
	if after := len(mems) - len(deleted); after < (len(mems)+1)/2 {
		errs = append(errs, fmt.Errorf("the plan deletes %d of %d memories; at most half may go", len(deleted), len(mems)))
	}
	if len(errs) > 0 {
		return fmt.Errorf("consolidation plan rejected: %w", errors.Join(errs...))
	}
	return nil
}

func conversationText(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		// System text (hook context, memory hints, summaries) is not what
		// the user or the agent said.
		for _, t := range m.Texts(llm.OriginUser, llm.OriginModel) {
			if text := strings.TrimSpace(t.Text); text != "" {
				fmt.Fprintf(&b, "[%s] %s\n\n", t.Origin, text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func knownText(mems []Memory) string {
	if len(mems) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, m := range mems {
		fmt.Fprintf(&b, "%s/%s (%s): %s\n%s\n\n", m.Scope, m.Name, m.Type, m.Description, m.Content)
	}
	return strings.TrimSpace(b.String())
}

func responseText(content []llm.Block) string {
	var parts []string
	for _, b := range content {
		if t, ok := b.(llm.Text); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}
