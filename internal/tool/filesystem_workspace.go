package tool

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var ErrOutsideWorkspace = errors.New("path is outside the workspace")

const (
	filePerm os.FileMode = 0o644
	dirPerm  os.FileMode = 0o755
)

type FileSystemWorkspace struct {
	root *os.Root
	dirs []string
}

func OpenFileSystemWorkspace(dir string) (*FileSystemWorkspace, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	dirs := []string{dir}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		dirs = append(dirs, real)
	}
	return &FileSystemWorkspace{root: root, dirs: dirs}, nil
}

func (w *FileSystemWorkspace) Dir() string  { return w.dirs[0] }
func (w *FileSystemWorkspace) FS() fs.FS    { return w.root.FS() }
func (w *FileSystemWorkspace) Close() error { return w.root.Close() }

func (w *FileSystemWorkspace) ReadFile(path string) ([]byte, error) {
	rel, err := w.ToRelativePath(path)
	if err != nil {
		return nil, err
	}
	return w.root.ReadFile(rel)
}

func (w *FileSystemWorkspace) WriteFile(path string, data []byte) error {
	rel, err := w.ToRelativePath(path)
	if err != nil {
		return err
	}
	perm := filePerm
	if info, err := w.root.Stat(rel); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", path)
		}
		perm = info.Mode().Perm()
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := w.root.MkdirAll(dir, dirPerm); err != nil {
			return err
		}
	}
	return w.root.WriteFile(rel, data, perm)
}

func (w *FileSystemWorkspace) ToRelativePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	if !filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	for _, dir := range w.dirs {
		rel, err := filepath.Rel(dir, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrOutsideWorkspace, path)
}
