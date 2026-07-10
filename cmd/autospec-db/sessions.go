package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/berlinguyinca/autospec-db/internal/config"
	"github.com/berlinguyinca/autospec-db/internal/db"
)

// cmdSessions prints the sessions grid (or the stalled subset) as an aligned
// text table using the read DSN.
func cmdSessions(args []string) int {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stalled := fs.Bool("stalled", false, "only sessions silent past the threshold")
	threshold := fs.String("threshold", "5m", "stall threshold (Go duration, e.g. 5m)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dsn, ok := config.ReadDSN()
	if !ok {
		fmt.Fprintln(os.Stderr, "autospec-db sessions: no read DSN (set AUTOSPEC_DB_READ_DSN or configure db.env)")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := db.ConnectDSN(ctx, dsn, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "autospec-db sessions: connect: %v\n", err)
		return 1
	}
	defer conn.Close(context.Background())

	const cols = `session_id, host, repo, last_step, last_seen_at, is_terminal, is_parked`
	var query string
	var qargs []any
	if *stalled {
		d, perr := time.ParseDuration(*threshold)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "autospec-db sessions: bad --threshold: %v\n", perr)
			return 2
		}
		query = "select " + cols + " from autospec.stalled_sessions($1::interval)"
		qargs = []any{fmt.Sprintf("%d seconds", int64(d.Seconds()))}
	} else {
		query = "select " + cols + " from autospec.sessions order by last_seen_at desc"
	}

	rows, err := conn.Query(ctx, query, qargs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "autospec-db sessions: query: %v\n", err)
		return 1
	}
	defer rows.Close()

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION_ID\tHOST\tREPO\tLAST_STEP\tLAST_SEEN_AT\tSTATE")
	n := 0
	for rows.Next() {
		var sid, host, repo, step *string
		var lastSeen *time.Time
		var isTerminal, isParked bool
		if err := rows.Scan(&sid, &host, &repo, &step, &lastSeen, &isTerminal, &isParked); err != nil {
			fmt.Fprintf(os.Stderr, "autospec-db sessions: scan: %v\n", err)
			return 1
		}
		state := "active"
		switch {
		case isTerminal:
			state = "terminal"
		case isParked:
			state = "parked"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			deref(sid), deref(host), deref(repo), deref(step), tsString(lastSeen), state)
		n++
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "autospec-db sessions: rows: %v\n", err)
		return 1
	}
	tw.Flush()
	if n == 0 {
		fmt.Println("(no sessions)")
	}
	return 0
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

func tsString(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
