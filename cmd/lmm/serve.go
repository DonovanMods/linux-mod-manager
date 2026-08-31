package main

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"

	"github.com/spf13/cobra"
)

var (
	serveAddr   string
	serveNoOpen bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the local web UI",
	Long: `Start a local HTTP server with a browser-based UI over the same mod
database and profiles the CLI uses.

Binds to 127.0.0.1:7420 by default - local-only, no authentication in this
release. Binding a non-loopback address prints a warning: anyone who can
reach that address can drive lmm exactly as you can.

Opens the default browser automatically unless --no-open is given.

Examples:
  lmm serve
  lmm serve --addr 127.0.0.1:8080
  lmm serve --no-open`,
	Args: cobra.NoArgs,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", "127.0.0.1:7420", "address to listen on (host:port)")
	serveCmd.Flags().BoolVar(&serveNoOpen, "no-open", false, "don't open a browser automatically")
	rootCmd.AddCommand(serveCmd)
}

// serveReady, when non-nil, is called once the server is actively listening
// (after the startup message is printed, before Serve blocks). Test-only
// seam - default no-op, same pattern as stdoutColorCapable's var above in
// root.go.
var serveReady = func(addr string) {}

func runServe(cmd *cobra.Command, _ []string) error {
	return withService(cmd, func(ctx context.Context, svc *core.Service) error {
		return doServe(ctx, cmd, svc)
	})
}

// doServe binds and runs the serve HTTP server until ctx is cancelled. It
// prints the non-loopback warning up front, then the listening URL, then
// blocks in srv.Serve, which drains in-flight requests with a bounded grace
// period once ctx is cancelled (see internal/serve's serveGraceful).
func doServe(ctx context.Context, cmd *cobra.Command, svc *core.Service) error {
	if err := warnIfNonLoopback(cmd, serveAddr); err != nil {
		return err
	}

	srv := serve.New(ctx, svc, svc.Logger(), serve.Options{Addr: serveAddr})
	addr, err := srv.Listen()
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	url := fmt.Sprintf("http://%s/", addr)
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "lmm serve listening on %s\n", url); err != nil {
		return fmt.Errorf("writing startup message: %w", err)
	}
	if !serveNoOpen {
		openBrowser(url)
	}
	serveReady(addr.String())

	return srv.Serve(ctx)
}

// warnIfNonLoopback prints the loud, undismissable warning
// (docs/plans/2026-08-30-serve-design.md §Security) when addr is not
// loopback-only: there is no authentication in this release, so anything
// reachable from outside this machine is a real exposure.
func warnIfNonLoopback(cmd *cobra.Command, addr string) error {
	loopback, err := serve.IsLoopbackAddr(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if !loopback {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(),
			"%s lmm serve is bound to %s, which is reachable from outside this machine. There is no authentication in this release - anyone who can reach it can drive lmm exactly as you can. Only do this on a trusted network.\n",
			colorRed("WARNING:"), addr)
		return err
	}
	return nil
}

// openBrowser best-effort opens url in the user's default browser via
// xdg-open. Errors are ignored deliberately: a missing/failing xdg-open
// must never prevent `lmm serve` from starting - it just means the user
// opens the printed URL themselves. Not exercised by tests (no display in
// CI/sandboxes to actually open).
func openBrowser(url string) {
	_ = exec.Command("xdg-open", url).Start()
}
