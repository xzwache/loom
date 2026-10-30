package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (a *Anthropic) SendMessage(ctx context.Context, req Request) (Response, error) {
	params, err := toParams(req)
	if err != nil {
		return Response{}, err
	}
	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return Response{}, &APIError{
				StatusCode: apiErr.StatusCode,
				Type:       string(apiErr.Type()),
				RequestID:  apiErr.RequestID,
				Message:    apiErr.Error(),
			}
		}
		return Response{}, fmt.Errorf("send message: %w", err)
	}
	return fromMessage(msg)
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

func fromMessage(msg *anthropic.Message) (Response, error) {
	resp := Response{
		StopReason: StopReason(msg.StopReason),
		Usage: Usage{
			InputTokens:      int(msg.Usage.InputTokens),
			OutputTokens:     int(msg.Usage.OutputTokens),
			CacheReadTokens:  int(msg.Usage.CacheReadInputTokens),
			CacheWriteTokens: int(msg.Usage.CacheCreationInputTokens),
		},
	}
	switch resp.StopReason {
	case StopEndTurn, StopToolUse, StopMaxTokens, StopSequence, StopPauseTurn, StopRefusal, StopContextWindowExceeded:
	default:
		return Response{}, fmt.Errorf("%w: stop reason %q", ErrUnexpectedResponse, msg.StopReason)
	}
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			resp.Content = append(resp.Content, ModelText(b.Text))
		case anthropic.ToolUseBlock:
			resp.Content = append(resp.Content, ToolUse{ID: b.ID, Name: b.Name, Input: b.Input})
		case anthropic.ThinkingBlock:
			resp.Content = append(resp.Content, Thinking{Thinking: b.Thinking, Signature: b.Signature})
		case anthropic.RedactedThinkingBlock:
			resp.Content = append(resp.Content, RedactedThinking{Data: b.Data})
		default:
			return Response{}, fmt.Errorf("%w: content block %q", ErrUnexpectedResponse, block.Type)
		}
	}
	return resp, nil
}
