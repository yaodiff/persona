// Command persona-projects serves team documents and manages their membership and revisions.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/teamdocs"
)

const usage = `Usage:
  persona-projects serve [-listen 127.0.0.1:18082]
  persona-projects grant PROJECT MEMBER reader|writer|approver
  persona-projects revoke PROJECT MEMBER
  persona-projects publish [member flags] -expected N -file PATH PROJECT DOCUMENT
  persona-projects read [member flags] -revision N PROJECT DOCUMENT
  persona-projects approve [member flags] -revision N -sha256 DIGEST PROJECT DOCUMENT

Member flags: -url SERVICE_URL and -token-file PATH, defaulting to TEAM_PROJECT_URL and TEAM_PROJECT_TOKEN_FILE.
Publish the first revision with -expected 0. Publish and approve print a JSON receipt including the revision and SHA-256.
Read writes the verified document bytes to stdout. Approve requires the lowercase SHA-256 of the reviewed revision.

For serve, grant and revoke, set TEAM_PROJECT_DATABASE_URL to an explicit PostgreSQL URL naming a dedicated team database.
Serve binds a literal loopback IP. For remote clients, place a TLS reverse proxy in front of it.
Grant writes a new project bearer to stdout and replaces any previous bearer for that member.
Revoke removes the member's access to the project.`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" || len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		fmt.Println(usage)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var listenAddr netip.AddrPort
	switch args[0] {
	case "publish", "read", "approve":
		return runMemberCommand(ctx, args, os.Stdout)
	case "serve":
		f := flag.NewFlagSet("serve", flag.ContinueOnError)
		f.SetOutput(io.Discard)
		address := f.String("listen", "127.0.0.1:18082", "literal loopback IP and port")
		if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
			return errors.New(usage)
		}
		var err error
		listenAddr, err = netip.ParseAddrPort(*address)
		if err != nil || !listenAddr.Addr().IsLoopback() {
			return errors.New("listen must be a literal loopback IP and port")
		}
	case "grant":
		if len(args) != 4 || (args[3] != "reader" && args[3] != "writer" && args[3] != "approver") {
			return errors.New(usage)
		}
	case "revoke":
		if len(args) != 3 {
			return errors.New(usage)
		}
	default:
		return errors.New(usage)
	}

	setup, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cfg, err := teamDatabase(os.Getenv("TEAM_PROJECT_DATABASE_URL"))
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(setup, cfg)
	if err != nil {
		return errors.New("cannot open the team database")
	}
	defer pool.Close()
	store := &teamdocs.Store{Pool: pool}
	if err := store.Init(setup); err != nil {
		return errors.New("cannot initialize the team database")
	}
	switch args[0] {
	case "grant":
		token, err := store.Grant(setup, args[1], args[2], args[3])
		if err != nil {
			return memberError(err)
		}
		_, err = fmt.Fprintln(os.Stdout, token)
		return err
	case "revoke":
		return memberError(store.Revoke(setup, args[1], args[2]))
	}
	cancel()
	ln, err := osproc.Listen("tcp", listenAddr.String())
	if err != nil {
		return errors.New("cannot listen on the requested loopback address")
	}
	fmt.Fprintln(os.Stderr, "Listening on", ln.Addr())
	srv := &http.Server{Handler: teamdocs.Handler(store), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case <-done:
		return errors.New("project HTTP listener stopped unexpectedly")
	case <-ctx.Done():
		drain, end := context.WithTimeout(context.Background(), 10*time.Second)
		defer end()
		if err := srv.Shutdown(drain); err != nil {
			_ = srv.Close()
			return errors.New("project HTTP shutdown timed out")
		}
		return nil
	}
}

func teamDatabase(raw string) (*pgxpool.Config, error) {
	invalid := errors.New("set TEAM_PROJECT_DATABASE_URL to a PostgreSQL URL with a database name")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Fragment != "" {
		return nil, invalid
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return nil, invalid
	}
	config, err := pgxpool.ParseConfig(raw)
	if err != nil || config.ConnConfig.Database != name {
		return nil, invalid
	}
	return config, nil
}

func memberError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, teamdocs.ErrInvalid):
		return errors.New("invalid project, member or role")
	case errors.Is(err, teamdocs.ErrNotFound):
		return errors.New("membership not found")
	default:
		return errors.New("membership update failed")
	}
}
