// Command autospec-db provisions and talks to the central telemetry Postgres.
//
// Subcommands: install, migrate, emit, drain, sessions, doctor, version.
package main

import (
	"fmt"
	"os"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `autospec-db — central telemetry database tool

usage: autospec-db <command> [flags]

commands:
  install     provision database, migrations, roles, and ~/.autospec/db.env
  migrate     apply embedded SQL migrations   [--dsn <dsn>]
  emit        emit one event (fire-and-forget, never blocks a run)
  drain       replay the local spool into the database
  sessions    list sessions                   [--stalled] [--threshold 5m]
  doctor      diagnose configuration and connectivity
  version     print the binary version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "install":
		os.Exit(cmdInstall(args))
	case "migrate":
		os.Exit(cmdMigrate(args))
	case "emit":
		// emit owns its own exit discipline (always 0 on the telemetry path).
		cmdEmit(args)
		os.Exit(0)
	case "drain":
		os.Exit(cmdDrain(args))
	case "sessions":
		os.Exit(cmdSessions(args))
	case "doctor":
		os.Exit(cmdDoctor(args))
	case "version", "--version", "-v":
		fmt.Println(version)
		os.Exit(0)
	case "help", "-h", "--help":
		fmt.Print(usage)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "autospec-db: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}
