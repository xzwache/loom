package frontend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/llm"
)

// builtin is in /help order.
func builtin() []Command {
	return []Command{
		{Name: "help", Description: "list the commands", run: runHelp},
		{Name: "compact", Description: "compact the conversation now", run: runCompact},
		{Name: "clear", Description: "end this session and start a fresh one", run: runClear},
		{Name: "exit", Description: "end the session", run: runExit},
	}
}

func runHelp(_ context.Context, s *Session, _ string) (llm.Usage, error) {
	var b strings.Builder
	for _, c := range s.commands {
		fmt.Fprintf(&b, "/%s  %s\n", c.Name, c.Description)
	}
	s.notify(Notice{Kind: NoticeCommand, Message: strings.TrimSuffix(b.String(), "\n")})
	return llm.Usage{}, nil
}

func runCompact(ctx context.Context, s *Session, _ string) (llm.Usage, error) {
	res, changed, err := s.conversation.Compact(ctx)
	switch {
	case errors.Is(err, agent.ErrNoCompaction):
		s.notify(Notice{Kind: NoticeCommand, Message: "compaction is turned off in the config"})
		return llm.Usage{}, nil
	case err != nil:
		return llm.Usage{}, err
	case res.Stop == agent.StopHalted:
		s.notify(Notice{Kind: NoticeCommand, Message: "compaction stopped by a hook: " + res.Detail})
	case !changed:
		s.notify(Notice{Kind: NoticeCommand, Message: "nothing to compact"})
	}
	return res.Usage, nil
}

func runClear(ctx context.Context, s *Session, _ string) (llm.Usage, error) {
	return s.restart(ctx, EndClear)
}

func runExit(ctx context.Context, s *Session, _ string) (llm.Usage, error) {
	return s.end(ctx, EndExit), nil
}
