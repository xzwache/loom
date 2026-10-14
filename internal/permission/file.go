package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// fileGuard guards read, edit and write calls. Read(spec), Edit(spec) and
// Write(spec) rules match the file's path.
type fileGuard struct {
	policy *Policy
	ws     workspace
	tool   string
	access Access
}

func (g fileGuard) Decide(input json.RawMessage) Verdict {
	in, err := decodeInput[struct {
		Path string `json:"file_path"`
	}](input)
	if err != nil {
		return denied(err.Error())
	}
	return g.policy.evaluate(g.tool, g.access, g.ws.file(in.Path))
}

// workspace resolves paths against the workspace root.
type workspace struct {
	dir     string
	realDir string
	home    string
}

func newWorkspace(dir, home string) workspace {
	ws := workspace{dir: filepath.Clean(dir), home: home}
	ws.realDir = ws.dir
	if real, err := filepath.EvalSymlinks(ws.dir); err == nil {
		ws.realDir = real
	}
	return ws
}

// fileTarget is a file a call reaches: the path as given and, when it is a
// symlink, the file it points to.
type fileTarget struct {
	forms []filePath
	home  string
}

func (f fileTarget) fitsSome(r Rule) bool {
	for _, form := range f.forms {
		if r.matchPath(form, f.home) {
			return true
		}
	}
	return false
}

func (f fileTarget) fitsEvery(r Rule) bool {
	for _, form := range f.forms {
		if !r.matchPath(form, f.home) {
			return false
		}
	}
	return true
}

type filePath struct {
	abs     string
	rel     string
	outside bool
}

// file returns the target for name, relative to the workspace.
func (ws workspace) file(name string) fileTarget {
	abs := name
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(ws.dir, abs)
	}
	abs = filepath.Clean(abs)
	forms := []filePath{newFilePath(abs, ws.dir)}
	if real := realPath(abs); real != abs {
		forms = append(forms, newFilePath(real, ws.realDir))
	}
	return fileTarget{forms: forms, home: ws.home}
}

func newFilePath(abs, dir string) filePath {
	rel, err := filepath.Rel(dir, abs)
	outside := err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
	return filePath{abs: filepath.ToSlash(abs), rel: filepath.ToSlash(rel), outside: outside}
}

// realPath resolves symlinks in abs. For a file that does not exist yet it
// resolves the directory, and for a dangling symlink the link itself, since
// writing through it creates its target.
func realPath(abs string) string {
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	dir := filepath.Dir(abs)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	target, err := os.Readlink(abs)
	if err != nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return filepath.Clean(target)
}
