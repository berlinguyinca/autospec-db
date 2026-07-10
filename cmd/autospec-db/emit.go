package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/berlinguyinca/autospec-db/internal/config"
	"github.com/berlinguyinca/autospec-db/internal/db"
	"github.com/berlinguyinca/autospec-db/internal/payload"
	"github.com/berlinguyinca/autospec-db/internal/spool"
)

// cmdEmit is THE hot path. Contract: it must NEVER block a run, NEVER fail a
// caller, and NEVER print the DSN. Any database trouble degrades to the local
// spool. The top-level recover guarantees exit 0 even on an unexpected panic.
func cmdEmit(args []string) {
	defer func() { _ = recover() }()

	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	verbose := fs.Bool("v", false, "print the event payload to stderr (debugging)")
	if err := fs.Parse(args); err != nil {
		return
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "autospec-db emit: missing <kind>")
		return
	}
	kind, kvs := rest[0], rest[1:]

	line, err := payload.BuildJSON(kind, kvs)
	if err != nil {
		return
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "%s\n", line)
	}

	// Optionality contract: no DSN configured → silent no-op.
	dsn, ok := config.EmitDSN()
	if !ok {
		return
	}

	sp := spool.Default(config.SpoolPath())

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	conn, err := db.ConnectDSN(ctx, dsn, 2*time.Second)
	if err != nil {
		_ = sp.Append(line) // best-effort; failure to spool is still exit 0
		return
	}
	defer conn.Close(context.Background())

	if err := db.Ingest(ctx, conn, line); err != nil {
		_ = sp.Append(line)
		return
	}

	// Success: opportunistically drain the spool over the same connection,
	// bounded and best-effort. Errors are ignored.
	drainDeadline := time.Now().Add(5 * time.Second)
	_, _ = sp.Drain(func(l []byte) error {
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer dcancel()
		ierr := db.Ingest(dctx, conn, l)
		if ierr == nil {
			return nil
		}
		if db.IsServerError(ierr) {
			return spool.Poison(ierr) // drop the poison line
		}
		return ierr // connection error: keep and stop
	}, 200, drainDeadline)
}
