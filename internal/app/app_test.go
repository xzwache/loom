package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm/llmtest"
	"github.com/xzwache/loom/internal/session"
)

func TestBuildAndRun(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "test-model"
	dirs := Dirs{Home: t.TempDir(), User: t.TempDir(), State: t.TempDir()}
	opts := Options{Workdir: t.TempDir(), Dirs: dirs, Model: llmtest.NewScripted(llmtest.TextReply("hi"))}

	a, err := Build(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	res, err := a.Run(context.Background(), "say hi")
	if err != nil || res.Stop != agent.StopDone || res.Text != "hi" {
		t.Fatalf("Run = %+v, %v", res, err)
	}

	saved, err := session.Latest(context.Background(), a.store)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != a.Session.ID || len(saved.History.Messages()) != 2 {
		t.Errorf("saved session %s with %d messages, want %s with 2", saved.ID, len(saved.History.Messages()), a.Session.ID)
	}
}

func TestBuildFailsOnMissingWorkdir(t *testing.T) {
	opts := Options{Workdir: "/does/not/exist", Model: llmtest.NewScripted()}
	if _, err := Build(context.Background(), config.Default(), opts); err == nil {
		t.Error("Build succeeded without a workspace")
	}
}

func TestBuildIgnoresFailingNoticeHooks(t *testing.T) {
	workdir := t.TempDir()
	writeFile(t, filepath.Join(workdir, ProjectDir, "skills", "release", "SKILL.md"), "---\nname: release\ndescription: Cut a release.\n---\nTag.\n")
	writeFile(t, filepath.Join(workdir, "AGENTS.md"), "Be brief.\n")
	cfg := config.Default()
	cfg.Model = "test-model"
	opts := Options{Workdir: workdir, Dirs: Dirs{User: t.TempDir(), State: t.TempDir()}, Model: llmtest.NewScripted()}

	b := newBuilder(cfg, opts)
	var failed []event.Name
	b.app.hooks.OnAll(hook.Func(func(_ context.Context, e event.Event) (hook.Outcome, error) {
		failed = append(failed, e.Name())
		return hook.Outcome{}, errors.New("broken hook")
	}))
	a, err := b.build(context.Background())
	if err != nil {
		t.Fatalf("build failed because of a notice hook: %v", err)
	}
	a.Close()
	for _, want := range []event.Name{event.SkillLoaded{}.Name(), event.InstructionsLoaded{}.Name()} {
		if !slices.Contains(failed, want) {
			t.Errorf("the failing hook did not see %s; saw %v", want, failed)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
