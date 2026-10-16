package compact

import "github.com/xzwache/loom/internal/llm"

// charsPerToken is a rough average for English text and code.
const charsPerToken = 4

// Estimate guesses how many tokens messages take. It is only used next to a
// size the API counted: to add messages appended since, or to subtract what a
// strategy removed. The system prompt and tools never need estimating.
func Estimate(messages []llm.Message) int {
	chars := 0
	for _, m := range messages {
		for _, b := range m.Content {
			chars += blockChars(b)
		}
	}
	return chars / charsPerToken
}

func blockChars(b llm.Block) int {
	switch b := b.(type) {
	case llm.Text:
		return len(b.Text)
	case llm.ToolUse:
		return len(b.Input)
	case llm.ToolResult:
		return len(b.Content)
	case llm.Thinking:
		return len(b.Thinking)
	default:
		return 0
	}
}

// ContextSize is how many tokens a response's request and output take: the
// size of the context the next request starts from.
func ContextSize(u llm.Usage) int {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens + u.OutputTokens
}
