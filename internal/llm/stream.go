package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
)

// StreamEvent is one piece of a reply as a provider streams it. A provider
// translates its own wire events into these; Collect builds the Response.
// The set is closed: consumers handle it with a type switch.
//
// A valid stream is, for each content block in index order, a BlockStart,
// any deltas for that block, and a BlockStop; then one MessageStop. Blocks
// may be open at the same time, but each starts with the next index.
type StreamEvent interface {
	// apply adds the event to the reply being built, or reports why it
	// breaks the order above.
	apply(c *collector) error
}

// BlockStart opens the content block at Index. Block is empty Text,
// Thinking, or a ToolUse with ID and Name; RedactedThinking comes whole.
type BlockStart struct {
	Index int
	Block Block
}

type TextDelta struct {
	Index int
	Text  string
}

type ThinkingDelta struct {
	Index    int
	Thinking string
}

type SignatureDelta struct {
	Index     int
	Signature string
}

// ToolInputDelta is a piece of a tool's input JSON; the pieces only parse
// once the block stops.
type ToolInputDelta struct {
	Index int
	JSON  string
}

type BlockStop struct {
	Index int
}

// MessageStop ends the reply, with the usage for the whole request.
type MessageStop struct {
	StopReason StopReason
	Usage      Usage
}

func (e BlockStart) apply(collector *collector) error { return collector.start(e) }
func (e TextDelta) apply(collector *collector) error {
	return collector.appendTo(e.Index, e.Text, isText)
}

func (e ThinkingDelta) apply(collector *collector) error {
	return collector.appendTo(e.Index, e.Thinking, isThinking)
}

func (e ToolInputDelta) apply(collector *collector) error {
	return collector.appendTo(e.Index, e.JSON, isToolUse)
}

func (e SignatureDelta) apply(collector *collector) error {
	return collector.sign(e.Index, e.Signature)
}
func (e BlockStop) apply(collector *collector) error   { return collector.stop(e.Index) }
func (e MessageStop) apply(collector *collector) error { return collector.finish(e) }

// Send streams a request and returns the whole reply, for callers that do
// not show it as it comes.
func Send(ctx context.Context, m Model, req Request) (Response, error) {
	return Collect(m.Stream(ctx, req), nil)
}

// Collect builds the Response from a stream. onEvent, if not nil, sees each
// event after it has been checked. A stream that breaks the order above, or
// ends without a MessageStop, is ErrUnexpectedResponse; an error from the
// stream itself is returned as it is.
func Collect(stream iter.Seq2[StreamEvent, error], onEvent func(StreamEvent)) (Response, error) {
	var c collector
	for ev, err := range stream {
		if err != nil {
			return Response{}, err
		}
		if err := c.add(ev); err != nil {
			return Response{}, fmt.Errorf("%w: %w", ErrUnexpectedResponse, err)
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
	if !c.stopped {
		return Response{}, fmt.Errorf("%w: stream ended before the message stopped", ErrUnexpectedResponse)
	}
	return c.resp, nil
}

type collector struct {
	resp    Response
	blocks  []partBlock
	stopped bool
}

// partBlock is a content block still being streamed.
type partBlock struct {
	block Block
	text  []byte // text, thinking or tool input JSON so far
	sig   string
	open  bool
	// badInput is a tool input that did not parse.
	badInput bool
}

func (c *collector) add(ev StreamEvent) error {
	if c.stopped {
		return fmt.Errorf("%T after the message stopped", ev)
	}
	return ev.apply(c)
}

func (c *collector) start(ev BlockStart) error {
	if ev.Index != len(c.blocks) {
		return fmt.Errorf("block %d started, want block %d next", ev.Index, len(c.blocks))
	}
	switch ev.Block.(type) {
	case Text, Thinking, ToolUse, RedactedThinking:
	default:
		return fmt.Errorf("block %d: cannot stream a %T", ev.Index, ev.Block)
	}
	c.blocks = append(c.blocks, partBlock{block: ev.Block, open: true})
	return nil
}

func (c *collector) open(index int, kind func(Block) bool) (*partBlock, error) {
	if index < 0 || index >= len(c.blocks) || !c.blocks[index].open {
		return nil, fmt.Errorf("delta for block %d, which is not open", index)
	}
	b := &c.blocks[index]
	if !kind(b.block) {
		return nil, fmt.Errorf("block %d is a %T and takes no such delta", index, b.block)
	}
	return b, nil
}

func (c *collector) appendTo(index int, s string, kind func(Block) bool) error {
	b, err := c.open(index, kind)
	if err != nil {
		return err
	}
	b.text = append(b.text, s...)
	return nil
}

func (c *collector) sign(index int, sig string) error {
	b, err := c.open(index, isThinking)
	if err != nil {
		return err
	}
	b.sig += sig
	return nil
}

func (c *collector) stop(index int) error {
	if index < 0 || index >= len(c.blocks) || !c.blocks[index].open {
		return fmt.Errorf("stop for block %d, which is not open", index)
	}
	b := &c.blocks[index]
	b.open = false
	switch blk := b.block.(type) {
	case Text:
		b.block = ModelText(string(b.text))
	case Thinking:
		b.block = Thinking{Thinking: string(b.text), Signature: b.sig}
	case ToolUse:
		input := json.RawMessage(b.text)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		if !json.Valid(input) {
			b.badInput, input = true, json.RawMessage("{}")
		}
		blk.Input = input
		b.block = blk
	}
	b.text = nil
	return nil
}

func (c *collector) finish(ev MessageStop) error {
	for i, b := range c.blocks {
		if b.open {
			return fmt.Errorf("message stopped with block %d still open", i)
		}
		// A reply cut off at max_tokens can end inside a tool's input. The
		// stop reason reports the cut; the input becomes {} so the history
		// stays valid to send.
		if b.badInput && ev.StopReason != StopMaxTokens {
			use := b.block.(ToolUse)
			return fmt.Errorf("tool %s (%s): input is not valid JSON", use.Name, use.ID)
		}
	}
	switch ev.StopReason {
	case StopEndTurn, StopToolUse, StopMaxTokens, StopSequence, StopPauseTurn, StopRefusal, StopContextWindowExceeded:
	default:
		return fmt.Errorf("stop reason %q", ev.StopReason)
	}
	c.stopped = true
	c.resp.StopReason, c.resp.Usage = ev.StopReason, ev.Usage
	for _, b := range c.blocks {
		c.resp.Content = append(c.resp.Content, b.block)
	}
	return nil
}

func isText(b Block) bool     { _, ok := b.(Text); return ok }
func isThinking(b Block) bool { _, ok := b.(Thinking); return ok }
func isToolUse(b Block) bool  { _, ok := b.(ToolUse); return ok }
