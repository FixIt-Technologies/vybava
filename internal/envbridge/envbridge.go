// Package envbridge transfers an explicit environment through a private,
// short-lived Unix socket. It never persists values or executes shell code.
package envbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const maxBytes = 64 * 1024

var keyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func decode(r io.Reader, value any) error {
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil || len(b) > maxBytes || json.Unmarshal(b, value) != nil {
		return errors.New("invalid or oversized environment message (value omitted)")
	}
	return nil
}

func selectKeys(values map[string]string, keys []string) (map[string]string, error) {
	if len(keys) == 0 || len(keys) > 128 {
		return nil, errors.New("name between 1 and 128 environment keys")
	}
	selected := make(map[string]string, len(keys))
	for _, key := range keys {
		value, found := values[key]
		if !keyPattern.MatchString(key) || !found || strings.ContainsRune(value, 0) {
			return nil, errors.New("invalid or unavailable environment key (value omitted)")
		}
		if _, duplicate := selected[key]; duplicate {
			return nil, errors.New("duplicate environment key")
		}
		selected[key] = value
	}
	return selected, nil
}

func Input(r io.Reader, keys []string) (map[string]string, error) {
	var values map[string]string
	if err := decode(r, &values); err != nil {
		return nil, err
	}
	return selectKeys(values, keys)
}

func privateParent(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("socket path must be absolute")
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return errors.New("create a private socket directory first (mode 0700)")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("socket directory must be owned by this user and private (mode 0700)")
	}
	return nil
}

// Serve requires a caller-owned deadline, capped at ten minutes.
func Serve(ctx context.Context, path string, values map[string]string, ready func() error) (result error) {
	deadline, bounded := ctx.Deadline()
	if !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Minute {
		return errors.New("server requires a deadline within ten minutes")
	}
	if err := privateParent(path); err != nil {
		return err
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	retained, err := selectKeys(values, keys)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("socket path already exists or cannot be inspected; refusing to replace it")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen on private socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	owned, err := os.Lstat(path)
	if err != nil {
		listener.Close()
		return errors.New("cannot inspect created socket")
	}
	defer func() {
		listener.Close()
		current, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("inspect socket for cleanup: %w", err))
		} else if err == nil && os.SameFile(owned, current) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove owned socket: %w", err))
			}
		}
	}()
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("restrict socket access: %w", err)
	}
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-done:
		}
	}()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept private socket: %w", err)
		}
		limit := time.Now().Add(2 * time.Second)
		if deadline.Before(limit) {
			limit = deadline
		}
		if err := conn.SetDeadline(limit); err != nil {
			conn.Close()
			return fmt.Errorf("bound socket request: %w", err)
		}
		var request struct {
			Keys []string `json:"keys"`
		}
		err = decode(conn, &request)
		selected, selectionErr := selectKeys(retained, request.Keys)
		// A client may disconnect or exceed its deadline while a response is
		// written. Those per-client failures must not terminate the server.
		if err == nil && selectionErr == nil && ctx.Err() == nil {
			_ = json.NewEncoder(conn).Encode(selected)
		} else {
			_, _ = io.WriteString(conn, `{"error":"request refused"}`)
		}
		conn.Close()
	}
}

func Read(ctx context.Context, path string, keys []string) (map[string]string, error) {
	if err := privateParent(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private environment socket is unavailable")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, errors.New("cannot connect to environment socket")
	}
	defer conn.Close()
	limit := time.Now().Add(3 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(limit) {
		limit = deadline
	}
	if err := conn.SetDeadline(limit); err != nil {
		return nil, errors.New("cannot bound environment request")
	}
	if err := json.NewEncoder(conn).Encode(struct {
		Keys []string `json:"keys"`
	}{keys}); err != nil {
		return nil, errors.New("cannot request environment keys")
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		return nil, errors.New("cannot complete environment request")
	}
	var response map[string]string
	if err := decode(conn, &response); err != nil {
		return nil, err
	}
	selected, err := selectKeys(response, keys)
	if err != nil || len(response) != len(selected) {
		return nil, errors.New("environment request refused")
	}
	return selected, nil
}

func ShellExports(values map[string]string) (string, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	if _, err := selectKeys(values, keys); err != nil {
		return "", err
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "export %s='%s'\n", key, strings.ReplaceAll(values[key], "'", "'\\''"))
	}
	return b.String(), nil
}
