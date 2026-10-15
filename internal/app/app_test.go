package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/hook/command"
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
	skillDir := filepath.Join(workdir, ProjectDir, "skills", "release")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: release\ndescription: Cut a release.\n---\nTag.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Model = "test-model"
	broken := []command.Matcher{{Hooks: []command.Handler{{Type: "command", Command: "echo '{bad'"}}}}
	cfg.Hooks = command.Settings{"SkillLoaded": broken, "InstructionsLoaded": broken}
	opts := Options{Workdir: workdir, Dirs: Dirs{User: t.TempDir(), State: t.TempDir()}, Model: llmtest.NewScripted()}

	a, err := Build(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Build failed because of a notice hook: %v", err)
	}
	a.Close()
}
