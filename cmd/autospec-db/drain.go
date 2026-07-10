package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/berlinguyinca/autospec-db/internal/config"
	"github.com/berlinguyinca/autospec-db/internal/db"
	"github.com/berlinguyinca/autospec-db/internal/spool"
)

// cmdDrain explicitly replays the spool. It exits 0 even when the database is
// down: kept lines simply stay for the next attempt.
func cmdDrain(args []string) int {
	fs := flag.NewFlagSet("drain", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if config.Disabled() {
		fmt.Println("autospec-db: disabled via AUTOSPEC_DB_DISABLE; nothing drained")
		return 0
	}

	sp := spool.Default(config.SpoolPath())

	dsn, ok := config.EmitDSN()
	if !ok {
		fmt.Println("autospec-db: no DSN configured (AUTOSPEC_DB_DSN unset and db.env absent); nothing to drain")
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := db.ConnectDSN(ctx, dsn, 8*time.Second)
	if err != nil {
		size, lines, _ := sp.Stat()
		fmt.Printf("replayed=0 dropped=0 kept=%d (database unreachable; %d bytes spooled)\n", lines, size)
		return 0
	}
	defer conn.Close(context.Background())

	res, err := sp.Drain(func(l []byte) error {
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dcancel()
		ierr := db.Ingest(dctx, conn, l)
		if ierr == nil {
			return nil
		}
		if db.IsServerError(ierr) {
			return spool.Poison(ierr)
		}
		return ierr
	}, 0, time.Time{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "autospec-db drain: %v\n", err)
	}
	fmt.Printf("replayed=%d dropped=%d kept=%d\n", res.Replayed, res.Dropped, res.Kept)
	return 0
}
