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
	"github.com/jackc/pgx/v5"
)

// cmdMigrate applies embedded migrations. With --dsn it uses that DSN,
// otherwise the admin credentials from db.conf.
func cmdMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dsn := fs.String("dsn", "", "admin DSN (default: db.conf admin credentials)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var conn *pgx.Conn
	var err error
	if *dsn != "" {
		conn, err = db.ConnectDSN(ctx, *dsn, 5*time.Second)
	} else {
		conf, exists := config.LoadConf()
		if !exists {
			fmt.Fprintf(os.Stderr, "autospec-db migrate: no --dsn and no config at %s\n", config.ConfPath())
			return 1
		}
		if conf.AdminPassword == "" {
			fmt.Fprintf(os.Stderr, "autospec-db migrate: DB_ADMIN_PASSWORD empty in %s\n", config.ConfPath())
			return 1
		}
		conn, err = db.Connect(ctx, adminParams(conf, conf.Name, 5*time.Second))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "autospec-db migrate: connect: %v\n", err)
		return 1
	}
	defer conn.Close(context.Background())

	applied, err := migrate.Apply(ctx, conn, autospecdb.Migrations)
	if err != nil {
		fmt.Fprintf(os.Stderr, "autospec-db migrate: %v\n", err)
		return 1
	}
	if len(applied) == 0 {
		fmt.Println("autospec-db: migrations up to date (nothing applied)")
	} else {
		for _, name := range applied {
			fmt.Printf("applied %s\n", name)
		}
		fmt.Printf("autospec-db: %d migration(s) applied\n", len(applied))
	}
	return 0
}

// adminParams builds discrete connection Params from admin conf for dbname.
func adminParams(conf *config.Conf, dbname string, timeout time.Duration) db.Params {
	return db.Params{
		Host:     conf.Host,
		Port:     conf.Port,
		User:     conf.AdminUser,
		Password: conf.AdminPassword,
		Database: dbname,
		SSLMode:  conf.SSLMode,
		Timeout:  timeout,
	}
}
