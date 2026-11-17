package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haowang02/agent-events/internal/event"
)

func eventsOnly(result Result, err error) ([]event.Event, error) {
	return result.Events, err
}

// writeFile writes lines to name below a temporary directory and returns its
// path.

func writeFile(t *testing.T, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
