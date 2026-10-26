// Package collect finds the logs and databases that coding agents keep
// locally and parses the model requests recorded in them.
package collect

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

// Source is what a collector parses at once: a log file, a database, or a
// directory whose files only make sense together. Size and modification time
// detect changes.
type Source struct {
	Agent string
	Path  string
	Size  int64
	MTime int64
}

// Collector finds one agent's sources and parses them into events.
type Collector struct {
	Agent string
	List  func() ([]Source, error)
	Parse func(path string) ([]event.Event, error)
}

// All returns a collector for every supported agent.
func All() []Collector {
	return []Collector{
		claude(), codex(), openCode(), kilo(), antigravity(), gemini(), qwen(), copilot(), amp(),
		codebuff(), droid(), goose(), grok(), hermes(), kimi(), openClaw(), pi(), zcode(),
	}
}

func homeDir(elem ...string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, filepath.Join(elem...))
}

func xdgDataDir(elem ...string) string {
	return filepath.Join(cmp.Or(os.Getenv("XDG_DATA_HOME"), homeDir(".local", "share")), filepath.Join(elem...))
}

// roots returns the comma-separated paths of an environment variable, or the
// defaults when it is unset.
func roots(env string, defaults ...string) []string {
	value := strings.TrimSpace(os.Getenv(env))
	if value == "" {
		return defaults
	}
	var paths []string
	for _, path := range strings.Split(value, ",") {
		if path = strings.TrimSpace(path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// under joins elem to each root.
func under(roots []string, elem ...string) []string {
	paths := make([]string, len(roots))
	for i, root := range roots {
		paths[i] = filepath.Join(root, filepath.Join(elem...))
	}
	return paths
}

func withSuffix(suffixes ...string) func(path string) bool {
	return func(path string) bool {
		for _, suffix := range suffixes {
			if strings.HasSuffix(path, suffix) {
				return true
			}
		}
		return false
	}
}

func named(name string) func(path string) bool {
	return func(path string) bool { return filepath.Base(path) == name }
}

// walk calls fn for every regular file below the roots that match accepts.
// Missing roots are skipped.
func walk(roots []string, accept func(path string) bool, fn func(path string, info fs.FileInfo)) error {
	seen := map[string]bool{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if !entry.Type().IsRegular() || seen[path] || !accept(path) {
				return nil
			}
			seen[path] = true
			if info, err := entry.Info(); err == nil {
				fn(path, info)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// files lists every file below the roots that match accepts as its own source.
func files(agent string, roots []string, accept func(path string) bool) ([]Source, error) {
	var sources []Source
	err := walk(roots, accept, func(path string, info fs.FileInfo) {
		sources = append(sources, Source{Agent: agent, Path: path, Size: info.Size(), MTime: info.ModTime().UnixNano()})
	})
	return sources, err
}

// databases lists the SQLite databases that exist; their signature includes
// the write-ahead log, where recent writes live.
func databases(agent string, paths ...string) ([]Source, error) {
	var sources []Source
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if info.IsDir() {
			continue
		}
		source := Source{Agent: agent, Path: path, Size: info.Size(), MTime: info.ModTime().UnixNano()}
		if wal, err := os.Stat(path + "-wal"); err == nil {
			source.Size += wal.Size()
			source.MTime = max(source.MTime, wal.ModTime().UnixNano())
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// globDatabases lists the databases matching any of the patterns.
func globDatabases(agent string, patterns ...string) ([]Source, error) {
	var paths []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		paths = append(paths, matches...)
	}
	return databases(agent, paths...)
}
