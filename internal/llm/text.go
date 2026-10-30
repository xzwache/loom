package llm

import (
	"errors"
	"fmt"
)

// Origin is who wrote a text block. One user message can hold both the
// user's words and loom's notes, since the API takes one user message per
// turn, so the origin is kept per block.
type Origin string

const (
	OriginUser  Origin = "user"
	OriginModel Origin = "model"
	// OriginSystem is text loom added: never the user's words. Its Kind
	// says what it is for.
	OriginSystem Origin = "system"
)

// Kind is what a system text is for.
type Kind string

const (
	KindHookContext Kind = "hook_context"
	KindMemoryHint  Kind = "memory_hint"
	KindSummary     Kind = "summary"
)

func UserText(s string) Text           { return Text{Text: s, Origin: OriginUser} }
func ModelText(s string) Text          { return Text{Text: s, Origin: OriginModel} }
func SystemText(k Kind, s string) Text { return Text{Text: s, Origin: OriginSystem, Kind: k} }

// Sent is the text as the model gets it: a system text is wrapped in a
// loom-note tag, so the model can tell it from the user's words.
func (t Text) Sent() string {
	if t.Origin != OriginSystem {
		return t.Text
	}
	return fmt.Sprintf("<loom-note kind=%q>\n%s\n</loom-note>", t.Kind, t.Text)
}

// check reports a text whose origin does not fit the message role.
func (t Text) check(role Role) error {
	switch {
	case t.Origin == "":
		return errors.New("text has no origin")
	case role == RoleAssistant && t.Origin != OriginModel:
		return fmt.Errorf("assistant text with origin %q", t.Origin)
	case role == RoleUser && t.Origin == OriginModel:
		return errors.New("user message with model text")
	case t.Origin == OriginSystem && t.Kind == "":
		return errors.New("system text has no kind")
	case t.Origin != OriginSystem && t.Kind != "":
		return fmt.Errorf("%s text with kind %q", t.Origin, t.Kind)
	}
	return nil
}

// Check reports a text block whose origin is missing or does not fit the
// message's role.
func (m Message) Check() error {
	for i, b := range m.Content {
		if t, ok := b.(Text); ok {
			if err := t.check(m.Role); err != nil {
				return fmt.Errorf("block %d: %w", i, err)
			}
		}
	}
	return nil
}

// Texts returns the message's text blocks from the given origins.
func (m Message) Texts(origins ...Origin) []Text {
	var out []Text
	for _, b := range m.Content {
		if t, ok := b.(Text); ok {
			for _, o := range origins {
				if t.Origin == o {
					out = append(out, t)
					break
				}
			}
		}
	}
	return out
}
