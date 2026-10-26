// Command agent-events collects model request events from local coding agent
// logs (Claude Code, Codex, OpenCode, Antigravity) and serves a web UI to
// browse and analyze them.
package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

//go:embed web
var webFiles embed.FS

func defaultDataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "agent-events")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "agent-events")
}

func openBrowser(url string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		log.Printf("open browser: %v", err)
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8790", "listen address; a free port is used when it is taken")
	dataDir := flag.String("data", defaultDataDir(), "directory for collected events, custom prices and the models.dev cache")
	noOpen := flag.Bool("no-open", false, "do not open the browser")
	rebuild := flag.Bool("rebuild", false, "discard collected events and parse every source again; custom prices are kept")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	store, err := openStore(filepath.Join(*dataDir, "events.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if *rebuild {
		if err := store.Reset(); err != nil {
			log.Fatal(err)
		}
	}
	pricer, err := newPricer(store, filepath.Join(*dataDir, "models-dev.json"))
	if err != nil {
		log.Fatal(err)
	}
	app, err := newApp(store, pricer, *dataDir)
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		host, _, _ := net.SplitHostPort(*addr)
		if listener, err = net.Listen("tcp", net.JoinHostPort(host, "0")); err != nil {
			log.Fatal(err)
		}
	}
	url := "http://" + listener.Addr().String()
	log.Printf("agent-events serving %s (data: %s)", url, *dataDir)
	go app.Watch()
	if pricer.Stale() {
		go func() {
			if err := app.RefreshPrices(context.Background()); err != nil {
				log.Printf("models.dev: %v", err)
			}
		}()
	}
	if !*noOpen {
		openBrowser(url)
	}
	web, _ := fs.Sub(webFiles, "web")
	log.Fatal(http.Serve(listener, app.routes(web)))
}
