package codexsync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func validRelative(rel string) bool {
	return rel != "." && fs.ValidPath(rel) && !strings.ContainsAny(rel, "\\:\x00")
}

// Validate even absent destinations, resolving their nearest existing parent.
// This prevents source/destination aliases from turning a render into a prune
// of the source or putting its insurance inside a directory being replaced.
func validateConfig(cfg Config) error {
	homes := []string{cfg.ClaudeHome, cfg.AgentsHome, cfg.CodexHome, cfg.BackupRoot}
	resolved := make([]string, len(homes))
	for i, home := range homes {
		if !filepath.IsAbs(home) || filepath.Clean(home) != home || home == string(filepath.Separator) {
			return fmt.Errorf("home must be an absolute, clean non-root path: %q", home)
		}
		p := home
		var tail []string
		for {
			r, err := filepath.EvalSymlinks(p)
			if err == nil {
				for j := len(tail) - 1; j >= 0; j-- {
					r = filepath.Join(r, tail[j])
				}
				resolved[i] = r
				break
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			tail = append(tail, filepath.Base(p))
			p = filepath.Dir(p)
		}
	}
	for i, a := range resolved {
		for j := i + 1; j < len(resolved); j++ {
			b := resolved[j]
			if a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator)) {
				return fmt.Errorf("homes must not overlap: %s and %s", homes[i], homes[j])
			}
		}
	}
	if info, err := os.Stat(cfg.ClaudeHome); err != nil {
		return fmt.Errorf("read Claude home: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("Claude home is not a directory: %s", cfg.ClaudeHome)
	}
	return nil
}

// Refuse destination links, including intermediate directories. Reading through
// one and then writing the same path would change its source in another home.
func validateTarget(root, rel string) error {
	if !validRelative(rel) {
		return fmt.Errorf("invalid destination path %q", rel)
	}
	p := root
	parts := append([]string{""}, strings.Split(rel, "/")...)
	for i, part := range parts {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination is a symlink: %s; move it aside before applying", p)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("destination parent is not a directory: %s", p)
		}
	}
	return nil
}

func readRegular(target string) ([]byte, fs.FileMode, bool, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("expected a regular file: %s (%s)", target, info.Mode())
	}
	body, err := os.ReadFile(target)
	return body, info.Mode().Perm(), true, err
}

func atomicWrite(target string, body []byte, mode fs.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".codexsync-*")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	_, writeErr := file.Write(body)
	chmodErr := file.Chmod(mode)
	closeErr := file.Close()
	if err := errors.Join(writeErr, chmodErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), target)
}

func removeEmptyParents(root, dir string) error {
	for dir != root {
		err := os.Remove(dir)
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		dir = filepath.Dir(dir)
	}
	return nil
}
