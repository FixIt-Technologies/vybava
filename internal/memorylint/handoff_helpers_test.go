package memorylint_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func fmtSprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// writeDeep writes a file, creating the nested handoff-home layout on the way.
func writeDeep(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
