package main

import (
	"bufio"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Source is one file an agent writes requests to. Size and modification time
// detect changes; SQLite sources also include their write-ahead log.
type Source struct {
	Agent string
	Path  string
	Size  int64
	MTime int64
}

type Collector struct {
	Agent string
	List  func() ([]Source, error)
	Parse func(path string) ([]Event, error)
}

func collectors() []*Collector {
	home, _ := os.UserHomeDir()
	env := func(name, fallback string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		return fallback
	}
	claudeRoot := env("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	codexRoot := env("CODEX_HOME", filepath.Join(home, ".codex"))
	opencodeRoot := filepath.Join(env("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "opencode")
	antigravityRoot := filepath.Join(home, ".gemini", "antigravity-cli")
	return []*Collector{
		{
			Agent: "claude",
			List:  func() ([]Source, error) { return listFiles("claude", ".jsonl", filepath.Join(claudeRoot, "projects")) },
			Parse: parseClaudeFile,
		},
		{
			Agent: "codex",
			List: func() ([]Source, error) {
				return listFiles("codex", ".jsonl", filepath.Join(codexRoot, "sessions"), filepath.Join(codexRoot, "archived_sessions"))
			},
			Parse: parseCodexFile,
		},
		{
			Agent: "opencode",
			List:  func() ([]Source, error) { return listDatabases("opencode", filepath.Join(opencodeRoot, "opencode.db")) },
			Parse: parseOpenCodeDatabase,
		},
		{
			Agent: "antigravity",
			List: func() ([]Source, error) {
				paths, err := filepath.Glob(filepath.Join(antigravityRoot, "conversations", "*.db"))
				if err != nil {
					return nil, err
				}
				return listDatabases("antigravity", paths...)
			},
			Parse: parseAntigravityDatabase,
		},
	}
}

func listFiles(agent, extension string, roots ...string) ([]Source, error) {
	var sources []Source
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), extension) {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			sources = append(sources, Source{Agent: agent, Path: path, Size: info.Size(), MTime: info.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return sources, nil
}

func listDatabases(agent string, paths ...string) ([]Source, error) {
	var sources []Source
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
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

// readLines calls fn for each line, including lines longer than the buffer.
func readLines(path string, fn func(line []byte)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long := append([]byte(nil), line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = reader.ReadSlice('\n')
				long = append(long, line...)
			}
			line = long
		}
		if len(line) > 0 {
			fn(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func openReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func parseTimestamp(value string) int64 {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return at.UnixMilli()
}
