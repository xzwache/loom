package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type AnthropicOptions struct {
	APIKey     string
	BaseURL    string
	Timeout    time.Duration
	MaxRetries int
}

type Anthropic struct {
	client anthropic.Client
}

var _ Model = (*Anthropic)(nil)

func NewAnthropic(opts AnthropicOptions) *Anthropic {
	var reqOpts []option.RequestOption
	if opts.APIKey != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(opts.BaseURL))
	}
	if opts.Timeout > 0 {
		reqOpts = append(reqOpts, option.WithRequestTimeout(opts.Timeout))
	}
	reqOpts = append(reqOpts, option.WithMaxRetries(opts.MaxRetries))
	return &Anthropic{client: anthropic.NewClient(reqOpts...)}
}

func (a *Anthropic) Stream(ctx context.Context, req Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		params, err := toParams(req)
		if err != nil {
			yield(nil, err)
			return
		}
		stream := a.client.Messages.NewStreaming(ctx, params)
		defer stream.Close()

		var translator streamTranslator
		for stream.Next() {
			ev, ok, err := translator.translate(stream.Current())
			if err != nil {
				yield(nil, err)
				return
			}
			if ok && !yield(ev, nil) {
				return
			}
		}
		if err := stream.Err(); err != nil {
			yield(nil, apiError(err))
		}
	}
}

// apiError turns the SDK's error for a non-2xx response into an APIError.
// The SDK retries only before a stream starts; an error mid-stream is final.
func apiError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return &APIError{
			StatusCode: apiErr.StatusCode,
			Type:       string(apiErr.Type()),
			RequestID:  apiErr.RequestID,
			Message:    apiErr.Error(),
		}
	}
	return fmt.Errorf("stream message: %w", err)
}

// streamTranslator turns Anthropic's server events into StreamEvents. Usage
// comes in two parts: input tokens in message_start, the final counts in
// message_delta; MessageStop carries both.
type streamTranslator struct {
	usage Usage
	stop  StopReason
}

// translate reports false for events with no StreamEvent of their own.
func (t *streamTranslator) translate(ev anthropic.MessageStreamEventUnion) (StreamEvent, bool, error) {
	switch e := ev.AsAny().(type) {
	case anthropic.MessageStartEvent:
		u := e.Message.Usage
		t.usage = Usage{
			InputTokens:      int(u.InputTokens),
			OutputTokens:     int(u.OutputTokens),
			CacheReadTokens:  int(u.CacheReadInputTokens),
			CacheWriteTokens: int(u.CacheCreationInputTokens),
		}
		return nil, false, nil
	case anthropic.ContentBlockStartEvent:
		block, err := startBlock(e)
		return BlockStart{Index: int(e.Index), Block: block}, err == nil, err
	case anthropic.ContentBlockDeltaEvent:
		delta, err := blockDelta(e)
		return delta, err == nil, err
	case anthropic.ContentBlockStopEvent:
		return BlockStop{Index: int(e.Index)}, true, nil
	case anthropic.MessageDeltaEvent:
		t.stop = StopReason(e.Delta.StopReason)
		// The counts here are cumulative; input ones may be absent.
		u := e.Usage
		t.usage.OutputTokens = int(u.OutputTokens)
		if u.InputTokens > 0 {
			t.usage.InputTokens = int(u.InputTokens)
		}
		if u.CacheReadInputTokens > 0 {
			t.usage.CacheReadTokens = int(u.CacheReadInputTokens)
		}
		if u.CacheCreationInputTokens > 0 {
			t.usage.CacheWriteTokens = int(u.CacheCreationInputTokens)
		}
		return nil, false, nil
	case anthropic.MessageStopEvent:
		return MessageStop{StopReason: t.stop, Usage: t.usage}, true, nil
	default:
		return nil, false, fmt.Errorf("%w: stream event %q", ErrUnexpectedResponse, ev.Type)
	}
}

func startBlock(e anthropic.ContentBlockStartEvent) (Block, error) {
	switch b := e.ContentBlock.AsAny().(type) {
	case anthropic.TextBlock:
		return Text{}, nil
	case anthropic.ThinkingBlock:
		return Thinking{}, nil
	case anthropic.RedactedThinkingBlock:
		return RedactedThinking{Data: b.Data}, nil
	case anthropic.ToolUseBlock:
		return ToolUse{ID: b.ID, Name: b.Name}, nil
	default:
		return nil, fmt.Errorf("%w: content block %q", ErrUnexpectedResponse, e.ContentBlock.Type)
	}
}

func blockDelta(e anthropic.ContentBlockDeltaEvent) (StreamEvent, error) {
	i := int(e.Index)
	switch d := e.Delta.AsAny().(type) {
	case anthropic.TextDelta:
		return TextDelta{Index: i, Text: d.Text}, nil
	case anthropic.ThinkingDelta:
		return ThinkingDelta{Index: i, Thinking: d.Thinking}, nil
	case anthropic.SignatureDelta:
		return SignatureDelta{Index: i, Signature: d.Signature}, nil
	case anthropic.InputJSONDelta:
		return ToolInputDelta{Index: i, JSON: d.PartialJSON}, nil
	default:
		return nil, fmt.Errorf("%w: content delta %q", ErrUnexpectedResponse, e.Delta.Type)
	}
}

func toParams(req Request) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		Model:     req.Model,
		MaxTokens: int64(req.MaxTokens),
		// Caches the whole prefix up to the last block on every turn.
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	for _, m := range req.Messages {
		mp, err := toMessageParam(m)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		params.Messages = append(params.Messages, mp)
	}
	for _, t := range req.Tools {
		tp, err := toToolParam(t)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return params, nil
}

func toMessageParam(m Message) (anthropic.MessageParam, error) {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Content))
	for _, b := range m.Content {
		switch b := b.(type) {
		case Text:
			blocks = append(blocks, anthropic.NewTextBlock(b.Sent()))
		case ToolUse:
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, input, b.Name))
		case ToolResult:
			blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolUseID, b.Content, b.IsError))
		case Thinking:
			blocks = append(blocks, anthropic.NewThinkingBlock(b.Signature, b.Thinking))
		case RedactedThinking:
			blocks = append(blocks, anthropic.NewRedactedThinkingBlock(b.Data))
		default:
			return anthropic.MessageParam{}, fmt.Errorf("unsupported block type %T", b)
		}
	}
	switch m.Role {
	case RoleUser:
		return anthropic.NewUserMessage(blocks...), nil
	case RoleAssistant:
		return anthropic.NewAssistantMessage(blocks...), nil
	default:
		return anthropic.MessageParam{}, fmt.Errorf("unsupported role %q", m.Role)
	}
}

func toToolParam(t ToolSpec) (anthropic.ToolParam, error) {
	var schema map[string]any
	if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
		return anthropic.ToolParam{}, fmt.Errorf("tool %s: parse input schema: %w", t.Name, err)
	}
	in := anthropic.ToolInputSchemaParam{Properties: schema["properties"]}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				in.Required = append(in.Required, s)
			}
		}
	}
	delete(schema, "type")
	delete(schema, "properties")
	delete(schema, "required")
	if len(schema) > 0 {
		in.ExtraFields = schema
	}
	return anthropic.ToolParam{
		Name:        t.Name,
		Description: anthropic.String(t.Description),
		InputSchema: in,
	}, nil
}
