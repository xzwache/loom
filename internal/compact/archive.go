package compact

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/xzwache/loom/internal/event"
)

// Archive keeps offloaded tool output on disk, inside the workspace, so the
// model can read it back with the read tool:
//
//	<Root>/<Dir>/.gitignore            "*": never committed, in any repository
//	<Root>/<Dir>/<session>/<id>.txt    output of tool use <id>
//	<Root>/<Dir>/<session>/<agent>/... output of a subagent's tool uses
type Archive struct {
	// Root is the workspace.
	Root string
	// Dir is the archive's folder in Root, as the model sees it, e.g.
	// .loom/outputs.
	Dir     string
	Session string
	// Redact removes secrets loom knows of, such as its API key, before
	// output is written.
	Redact func(string) string
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func safeName(s string) string { return cmp.Or(unsafeChars.ReplaceAllString(s, "_"), "_") }

// Path is where the output of a tool use is kept, as the model sees it. A
// subagent's outputs go to its own folder.
func (a Archive) Path(ctx context.Context, toolUseID string) string {
	dir := filepath.Join(a.Dir, safeName(a.Session))
	if sub, ok := event.AgentFrom(ctx); ok {
		dir = filepath.Join(dir, safeName(sub.ID))
	}
	return filepath.Join(dir, safeName(toolUseID)+".txt")
}

func (a Archive) Save(ctx context.Context, toolUseID, output string) error {
	if err := a.ignoreInGit(); err != nil {
		return err
	}
	path := filepath.Join(a.Root, a.Path(ctx, toolUseID))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if a.Redact != nil {
		output = a.Redact(output)
	}
	return os.WriteFile(path, []byte(output), 0o600)
}

// ignoreInGit writes a .gitignore that ignores the whole archive, so offloaded
// output, which may hold secrets, is never committed.
func (a Archive) ignoreInGit() error {
	dir := filepath.Join(a.Root, a.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte("*\n"), 0o600)
}

// Prune deletes the folders of other sessions not changed for maxAge.
func (a Archive) Prune(maxAge time.Duration, now time.Time) error {
	dir := filepath.Join(a.Root, a.Dir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || e.Name() == safeName(a.Session) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		errs = append(errs, os.RemoveAll(filepath.Join(dir, e.Name())))
	}
	return errors.Join(errs...)
}
