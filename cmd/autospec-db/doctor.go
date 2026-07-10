package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	autospecdb "github.com/berlinguyinca/autospec-db"
	"github.com/berlinguyinca/autospec-db/internal/config"
	"github.com/berlinguyinca/autospec-db/internal/db"
	"github.com/berlinguyinca/autospec-db/internal/migrate"
	"github.com/berlinguyinca/autospec-db/internal/spool"
	"github.com/jackc/pgx/v5"
)

// cmdDoctor reports OK/FAIL diagnostics and exits 1 if any check FAILs.
func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	anyFail := false
	ok := func(label, detail string) { fmt.Printf("OK    %-22s %s\n", label, detail) }
	fail := func(label, detail string) { fmt.Printf("FAIL  %-22s %s\n", label, detail); anyFail = true }
	info := func(label, detail string) { fmt.Printf("      %-22s %s\n", label, detail) }

	// config files
	conf, confExists := config.LoadConf()
	if confExists {
		ok("db.conf", config.ConfPath())
	} else {
		fail("db.conf", "missing at "+config.ConfPath())
	}
	if _, err := os.Stat(config.EnvPath()); err == nil {
		ok("db.env", config.EnvPath())
	} else {
		fail("db.env", "missing at "+config.EnvPath())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	// admin connectivity + server facts
	var adminConn *pgx.Conn
	if confExists && conf.AdminPassword != "" {
		c, err := db.Connect(ctx, adminParams(conf, conf.Name, 5*time.Second))
		if err != nil {
			fail("admin connect", err.Error())
		} else {
			adminConn = c
			defer adminConn.Close(context.Background())
			ok("admin connect", conf.AdminUser+"@"+conf.Host+":"+conf.Port+"/"+conf.Name)
			info("server version", db.ServerVersion(adminConn))
			if db.UsesTLS(adminConn) {
				info("tls", "in use")
			} else {
				info("tls", "not in use")
			}
		}
	} else {
		fail("admin connect", "no usable admin credentials in db.conf")
	}

	// emit connectivity
	if dsn, present := config.EmitDSN(); present {
		c, err := db.ConnectDSN(ctx, dsn, 3*time.Second)
		if err != nil {
			fail("emit connect", err.Error())
		} else {
			ok("emit connect", "autospec_emit reachable")
			c.Close(context.Background())
		}
	} else {
		fail("emit connect", "no AUTOSPEC_DB_DSN")
	}

	// read connectivity
	if dsn, present := config.ReadDSN(); present {
		c, err := db.ConnectDSN(ctx, dsn, 3*time.Second)
		if err != nil {
			fail("read connect", err.Error())
		} else {
			ok("read connect", "autospec_read reachable")
			c.Close(context.Background())
		}
	} else {
		fail("read connect", "no AUTOSPEC_DB_READ_DSN")
	}

	// migration status
	embedded, _ := migrate.Embedded(autospecdb.Migrations)
	if adminConn != nil {
		var pending []string
		for _, name := range embedded {
			var seen int
			err := adminConn.QueryRow(ctx,
				"select 1 from autospec.schema_migrations where filename = $1", name).Scan(&seen)
			if err != nil || seen != 1 {
				pending = append(pending, name)
			}
		}
		if len(pending) == 0 {
			ok("migrations", fmt.Sprintf("%d applied, 0 pending", len(embedded)))
		} else {
			fail("migrations", fmt.Sprintf("pending: %v", pending))
		}
	} else {
		info("migrations", fmt.Sprintf("%d embedded (server unreachable)", len(embedded)))
	}

	// spool
	sp := spool.Default(config.SpoolPath())
	size, lines, _ := sp.Stat()
	info("spool", fmt.Sprintf("%s: %d bytes, %d line(s)", sp.Path(), size, lines))

	if anyFail {
		return 1
	}
	return 0
}
