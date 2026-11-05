package frontend

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/llm"
)

type Command struct {
	Name        string
	Description string
	// run reports through updates; args is the text after the name.
	run func(ctx context.Context, s *Session, args string) (llm.Usage, error)
}

// commandName rejects "/usr/bin is empty": that is a prompt.
var commandName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

const NoticeCommand = "command"

func parseCommand(text string) (name, args string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(text), "/")
	if !found {
		return "", "", false
	}
	name, args, _ = strings.Cut(rest, " ")
	if !commandName.MatchString(name) {
		return "", "", false
	}
	return name, strings.TrimSpace(args), true
}

// runCommand never sends an unknown command to the model.
func (s *Session) runCommand(ctx context.Context, name, args string) (agent.Result, error) {
	i := slices.IndexFunc(s.commands, func(c Command) bool { return c.Name == name })
	if i < 0 {
		notice := Notice{Kind: NoticeCommand, Message: fmt.Sprintf("unknown command /%s ; /help lists the commands", name)}
		s.notify(notice)
		return agent.Result{Stop: agent.StopDone}, nil
	}
	used, err := s.commands[i].run(ctx, s, args)
	return agent.Result{Stop: agent.StopDone, Usage: used}, err
}
