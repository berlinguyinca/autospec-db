package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	autospecdb "github.com/berlinguyinca/autospec-db"
	"github.com/berlinguyinca/autospec-db/internal/config"
	"github.com/berlinguyinca/autospec-db/internal/db"
	"github.com/berlinguyinca/autospec-db/internal/migrate"
	"github.com/berlinguyinca/autospec-db/internal/payload"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// testDSN returns AUTOSPEC_DB_TEST_DSN or skips the test.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("AUTOSPEC_DB_TEST_DSN")
	if dsn == "" {
		t.Skip("AUTOSPEC_DB_TEST_DSN not set; skipping integration test")
	}
	return dsn
}

func adminConn(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := db.ConnectDSN(ctx, testDSN(t), 5*time.Second)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

// resetSchema drops the autospec schema (and its schema_migrations) for a clean slate.
func resetSchema(t *testing.T, c *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	if _, err := c.Exec(ctx, "drop schema if exists autospec cascade"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
}

func TestIntegrationMigrateIdempotency(t *testing.T) {
	c := adminConn(t)
	resetSchema(t, c)
	ctx := context.Background()

	applied, err := migrate.Apply(ctx, c, autospecdb.Migrations)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if len(applied) != 2 {
		t.Fatalf("first apply = %v, want 2 migrations", applied)
	}
	applied2, err := migrate.Apply(ctx, c, autospecdb.Migrations)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(applied2) != 0 {
		t.Errorf("second apply = %v, want 0 (idempotent)", applied2)
	}
}

// TestIntegrationShellEraParity pre-seeds the tracking table and objects the way
// the shell-era scripts/apply.sh would, then asserts migrate applies nothing.
func TestIntegrationShellEraParity(t *testing.T) {
	c := adminConn(t)
	resetSchema(t, c)
	ctx := context.Background()

	// Bootstrap the tracking table exactly like apply.sh, then run each SQL file
	// and record its basename — simulating a server provisioned by the old shell.
	_, err := c.Exec(ctx, `
		create schema if not exists autospec;
		create table if not exists autospec.schema_migrations (
			filename   text primary key,
			applied_at timestamptz not null default now()
		);`)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	for _, name := range []string{"001_events_raw.sql", "002_views.sql"} {
		sqlBytes, rerr := autospecdb.Migrations.ReadFile("migrations/" + name)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, eerr := c.Exec(ctx, string(sqlBytes)); eerr != nil {
			t.Fatalf("exec %s: %v", name, eerr)
		}
		if _, ierr := c.Exec(ctx,
			"insert into autospec.schema_migrations(filename) values ($1)", name); ierr != nil {
			t.Fatalf("record %s: %v", name, ierr)
		}
	}

	applied, err := migrate.Apply(ctx, c, autospecdb.Migrations)
	if err != nil {
		t.Fatalf("apply on shell-era server: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("shell-era parity broken: migrate applied %v, want nothing", applied)
	}
}

func TestIntegrationIngestDedup(t *testing.T) {
	c := adminConn(t)
	resetSchema(t, c)
	ctx := context.Background()
	if _, err := migrate.Apply(ctx, c, autospecdb.Migrations); err != nil {
		t.Fatal(err)
	}

	id := uuid.NewString()
	line := []byte(`{"schema":"autospec.events.v1","event_uuid":"` + id + `","kind":"heartbeat","session_id":"s1"}`)
	for i := 0; i < 3; i++ {
		if err := db.Ingest(ctx, c, line); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}
	var n int
	if err := c.QueryRow(ctx,
		"select count(*) from autospec.events_raw where payload->>'event_uuid' = $1", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("dedup failed: %d rows for one event_uuid, want 1", n)
	}
}

func TestIntegrationEmitRoleBlastRadius(t *testing.T) {
	c := adminConn(t)
	resetSchema(t, c)
	ctx := context.Background()
	if _, err := migrate.Apply(ctx, c, autospecdb.Migrations); err != nil {
		t.Fatal(err)
	}

	emitPw := "emit_" + uuid.NewString()
	readPw := "read_" + uuid.NewString()
	if err := convergeRoles(ctx, c, emitPw, readPw); err != nil {
		t.Fatalf("convergeRoles: %v", err)
	}

	p := dsnParams(t, testDSN(t))
	p.User = "autospec_emit"
	p.Password = emitPw
	p.Timeout = 5 * time.Second
	emitConn, err := db.Connect(ctx, p)
	if err != nil {
		t.Fatalf("emit connect: %v", err)
	}
	defer emitConn.Close(context.Background())

	// emit role can ingest
	line, _ := payload.BuildJSON("heartbeat", []string{"session_id=blast"})
	if err := db.Ingest(ctx, emitConn, line); err != nil {
		t.Fatalf("emit role ingest should succeed: %v", err)
	}
	// emit role CANNOT read the corpus
	var x int
	if err := emitConn.QueryRow(ctx, "select count(*) from autospec.events_raw").Scan(&x); err == nil {
		t.Error("emit role SELECT on events_raw should be denied, but succeeded")
	}
}

func TestIntegrationStalledExcludesTerminal(t *testing.T) {
	c := adminConn(t)
	resetSchema(t, c)
	ctx := context.Background()
	if _, err := migrate.Apply(ctx, c, autospecdb.Migrations); err != nil {
		t.Fatal(err)
	}

	insertOld := func(sessionID, kind string) {
		_, err := c.Exec(ctx,
			"insert into autospec.events_raw(received_at, payload) values (now() - interval '1 hour', $1::jsonb)",
			`{"schema":"autospec.events.v1","event_uuid":"`+uuid.NewString()+
				`","kind":"`+kind+`","session_id":"`+sessionID+`","host":"h"}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertOld("stalled-sess", "heartbeat")
	insertOld("done-sess", "heartbeat")
	insertOld("done-sess", "session.terminal")

	rows, err := c.Query(ctx, "select session_id from autospec.stalled_sessions($1::interval)", "1 second")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			t.Fatal(err)
		}
		seen[sid] = true
	}
	if !seen["stalled-sess"] {
		t.Error("stalled-sess should be flagged stalled")
	}
	if seen["done-sess"] {
		t.Error("done-sess is terminal and must be excluded")
	}
}

func TestIntegrationInstallEndToEnd(t *testing.T) {
	c := adminConn(t)
	resetSchema(t, c)

	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Unsetenv("AUTOSPEC_DB_CONF")
	for _, k := range []string{"AUTOSPEC_DB_DSN", "AUTOSPEC_DB_READ_DSN"} {
		os.Unsetenv(k)
	}

	p := dsnParams(t, testDSN(t))
	confDir := filepath.Join(home, ".autospec")
	if err := os.MkdirAll(confDir, 0o700); err != nil {
		t.Fatal(err)
	}
	confBody := "DB_HOST=" + p.Host + "\n" +
		"DB_PORT=" + p.Port + "\n" +
		"DB_NAME=" + p.Database + "\n" +
		"DB_ADMIN_USER=" + p.User + "\n" +
		"DB_ADMIN_PASSWORD=" + p.Password + "\n" +
		"DB_SSLMODE=" + p.SSLMode + "\n"
	if err := os.WriteFile(filepath.Join(confDir, "db.conf"), []byte(confBody), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := cmdInstall(nil); code != 0 {
		t.Fatalf("install exit code = %d, want 0", code)
	}

	// db.env created
	if _, err := os.Stat(config.EnvPath()); err != nil {
		t.Errorf("db.env not written: %v", err)
	}
	// probe event landed
	ctx := context.Background()
	var n int
	if err := c.QueryRow(ctx,
		"select count(*) from autospec.events_raw where payload->>'session_id' = 'install-probe'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Error("install-probe verification event not found")
	}
	// resolved emit DSN works end-to-end
	emitDSN, ok := config.EmitDSN()
	if !ok {
		t.Fatal("EmitDSN not resolvable after install")
	}
	emitConn, err := db.ConnectDSN(ctx, emitDSN, 5*time.Second)
	if err != nil {
		t.Fatalf("connect via generated emit DSN: %v", err)
	}
	defer emitConn.Close(context.Background())
	line, _ := payload.BuildJSON("heartbeat", []string{"session_id=post-install"})
	if err := db.Ingest(ctx, emitConn, line); err != nil {
		t.Errorf("emit via generated DSN failed: %v", err)
	}

	// second install run is idempotent
	if code := cmdInstall(nil); code != 0 {
		t.Errorf("second install exit code = %d, want 0", code)
	}
}

// dsnParams decomposes a DSN URL into db.Params for role-scoped reconnects.
func dsnParams(t *testing.T, dsn string) db.Params {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	pw, _ := u.User.Password()
	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "prefer"
	}
	return db.Params{
		Host:     host,
		Port:     port,
		User:     u.User.Username(),
		Password: pw,
		Database: strings.TrimPrefix(u.Path, "/"),
		SSLMode:  sslmode,
	}
}
