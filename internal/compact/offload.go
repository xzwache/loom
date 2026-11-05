package compact

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/xzwache/loom/internal/llm"
)

// offloadedPrefix starts every marker, so an offloaded result is never
// offloaded again, which would overwrite its file with the marker.
const offloadedPrefix = "[output offloaded to save context"

func offloadedOutput(path, output string) string {
	lines := strings.Count(output, "\n") + 1
	return fmt.Sprintf("%s: %d lines, %d bytes, in %s; read it if you need it]", offloadedPrefix, lines, len(output), path)
}

// OffloadOutputs moves tool output older than the last KeepTurns turns out of
// the history and into the archive, leaving a marker with the file's path.
// Every tool use keeps its result, so the history stays valid, and no model
// call is needed. Output that cannot be saved stays where it is.
type OffloadOutputs struct {
	KeepTurns int
	// MinTokens is the least an offload must free. A smaller one is skipped:
	// the summary that has to follow would replace it anyway.
	MinTokens int
	Archive   Archive
}

func (OffloadOutputs) Name() string { return "offload_outputs" }

// offload is one tool result to move out of the history.
type offload struct {
	message, block int
	result         llm.ToolResult
	marker         string
}

func (o OffloadOutputs) Compact(ctx context.Context, in Input) (Result, error) {
	found := o.findToolResults(ctx, in.Messages)
	if gain(found) < o.MinTokens {
		return Result{Messages: in.Messages}, nil
	}
	out := slices.Clone(in.Messages)
	res := Result{}
	for _, f := range found {
		if err := o.Archive.Save(ctx, f.result.ToolUseID, f.result.Content); err != nil {
			res.Failed++
			if res.FailErr == nil {
				res.FailErr = err
			}
			continue
		}
		replaceContent(out, f)
	}
	res.Messages = out
	return res, nil
}

// findToolResults lists the tool results to offload: those before the last KeepTurns
// turns, not offloaded yet, and longer than their marker.
func (o OffloadOutputs) findToolResults(ctx context.Context, messages []llm.Message) []offload {
	var found []offload
	for i, m := range messages[:turnStart(messages, o.KeepTurns)] {
		for j, b := range m.Content {
			r, ok := b.(llm.ToolResult)
			if !ok || strings.HasPrefix(r.Content, offloadedPrefix) {
				continue
			}
			marker := offloadedOutput(o.Archive.Path(ctx, r.ToolUseID), r.Content)
			if len(r.Content) > len(marker) {
				found = append(found, offload{message: i, block: j, result: r, marker: marker})
			}
		}
	}
	return found
}

func gain(found []offload) int {
	chars := 0
	for _, f := range found {
		chars += len(f.result.Content) - len(f.marker)
	}
	return chars / charsPerToken
}

// replaceContent puts f's marker in place of its output. The message's
// content is copied first, so the input history is never changed.
func replaceContent(messages []llm.Message, f offload) {
	content := slices.Clone(messages[f.message].Content)
	r := f.result
	r.Content = f.marker
	content[f.block] = r
	messages[f.message].Content = content
}

// turnStart returns the index of the first message of the last n turns. A
// turn is an assistant message and the tool results that follow it.
func turnStart(messages []llm.Message, n int) int {
	if n <= 0 {
		return len(messages)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleAssistant {
			n--
			if n == 0 {
				return i
			}
		}
	}
	return 0
}
