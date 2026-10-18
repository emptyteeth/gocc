// Command gocc-cli controls Chromecast devices on the local network, and
// serves its interface to a browser instead of drawing a window.
//
//	gocc-cli --iface wlan0 --listen 127.0.0.1:8097
//	gocc-cli --listen :8097 --basic-auth alice:s3cret
//
// It finds devices, plays the streams of a playlist on the one chosen, and
// sets what it plays and how loud, exactly as the desktop app gocc does.
// The difference is where the interface is: this one is a web page, so it
// runs headless, on a server, or behind an ssh tunnel.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/emptyteeth/gocc/cast"
	"github.com/emptyteeth/gocc/core"
	"github.com/emptyteeth/gocc/internal/version"
	"github.com/emptyteeth/gocc/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("gocc-cli: %v", err)
	}
}

func run() error {
	var (
		listen      = flag.String("listen", "127.0.0.1:8097", "address and port to serve the interface on, as 127.0.0.1:8097 or :8097")
		ifaceName   = flag.String("iface", "", "network interface to discover devices on, as wlan0 (default: every interface)")
		basicAuth   = flag.String("basic-auth", "", "ask browsers for HTTP basic authentication, as user:password (default: none)")
		dataDir     = flag.String("data-dir", "", "directory holding playlist.txt (default: the user's config directory for gocc)")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("gocc-cli", version.String())
		return nil
	}
	if flag.NArg() > 0 {
		flag.Usage()
		return fmt.Errorf("unexpected argument %q", flag.Arg(0))
	}

	username, password, err := parseBasicAuth(*basicAuth)
	if err != nil {
		return err
	}

	var iface *net.Interface
	if *ifaceName != "" {
		iface, err = cast.InterfaceByName(*ifaceName)
		if err != nil {
			return fmt.Errorf("network interface %q: %w", *ifaceName, err)
		}
	}

	dir := *dataDir
	if dir == "" {
		dir = defaultDataDir()
	}

	engine, err := core.New(core.Options{DataDir: dir, Iface: iface})
	if err != nil {
		return err
	}
	defer engine.Close()

	server := web.New(engine, web.Options{Username: username, Password: password})

	// Discovery runs for as long as the process does, and stops with it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine.Start(ctx)

	logStartup(*listen, *ifaceName, username != "", engine.PlaylistPath())

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// A browser watching the state holds its connection open, so
		// there is no write timeout to cut it off.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	// Serve, and stop when the process is asked to.
	done := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}

	fmt.Println("gocc-cli: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return <-done
}

// usage prints what the command does and its flags.
func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprint(out, `gocc-cli controls Chromecast devices on the local network, and serves
its interface to a browser instead of drawing a window.

Usage:
  gocc-cli [flags]

Examples:
  gocc-cli --iface wlan0 --listen 127.0.0.1:8097
  gocc-cli --listen :8097 --basic-auth alice:s3cret

Flags:
`)
	flag.PrintDefaults()
}

// parseBasicAuth reads a user:password pair. An empty value turns
// authentication off.
func parseBasicAuth(value string) (username, password string, err error) {
	if value == "" {
		return "", "", nil
	}
	username, password, ok := strings.Cut(value, ":")
	if !ok {
		return "", "", fmt.Errorf("--basic-auth needs a user:password pair, as alice:s3cret")
	}
	if username == "" {
		return "", "", fmt.Errorf("--basic-auth needs a user before the colon")
	}
	return username, password, nil
}

// defaultDataDir is where the playlist is kept, which both this and the
// desktop app read, so that they share one list.
func defaultDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "gocc")
	}
	return "."
}

// logStartup says what the server is doing, and warns about the one
// mistake this command makes easy: serving a network's devices to anyone
// who can reach the address.
func logStartup(listen, iface string, authed bool, playlist string) {
	where := listen
	if strings.HasPrefix(listen, ":") {
		where = "every interface" + listen
	}
	log.Printf("serving on %s", where)
	if iface != "" {
		log.Printf("discovering devices on %s", iface)
	} else {
		log.Printf("discovering devices on every interface")
	}
	log.Printf("playlist: %s", playlist)

	if authed {
		return
	}
	if host, _, err := net.SplitHostPort(listen); err == nil && !isLoopback(host) {
		log.Printf("WARNING: %s is reachable from the network and no --basic-auth is set, "+
			"so anyone who can reach it can control your devices", where)
	}
}

// isLoopback reports whether a listen address is reachable from this
// machine alone. An empty host means every interface, which is not.
func isLoopback(host string) bool {
	switch host {
	case "":
		return false
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
