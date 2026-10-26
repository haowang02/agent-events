// Command agent-events collects model request events from the local logs of
// coding agents and serves a web UI to browse and analyze them.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/haowang02/agent-events/internal/pricing"
	"github.com/haowang02/agent-events/internal/server"
	"github.com/haowang02/agent-events/internal/store"
	"github.com/haowang02/agent-events/web"
)

// version is set at release builds.
var version = "dev"

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
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	events, err := store.Open(filepath.Join(*dataDir, "events.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer events.Close()
	if *rebuild {
		if err := events.Reset(); err != nil {
			log.Fatal(err)
		}
	}
	custom, err := events.CustomPrices()
	if err != nil {
		log.Fatal(err)
	}
	pricer := pricing.NewPricer(filepath.Join(*dataDir, "models-dev.json"), custom)
	srv, err := server.New(events, pricer, web.Files, *dataDir)
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
	log.Printf("agent-events %s serving %s (data: %s)", version, url, *dataDir)
	go srv.Watch()
	if pricer.Stale() {
		go func() {
			if err := srv.RefreshPrices(context.Background()); err != nil {
				log.Printf("models.dev: %v", err)
			}
		}()
	}
	if !*noOpen {
		openBrowser(url)
	}
	log.Fatal(http.Serve(listener, srv.Handler()))
}
