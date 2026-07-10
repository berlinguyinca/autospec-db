package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	autospecdb "github.com/berlinguyinca/autospec-db"
	"github.com/berlinguyinca/autospec-db/internal/config"
	"github.com/berlinguyinca/autospec-db/internal/db"
	"github.com/berlinguyinca/autospec-db/internal/migrate"
	"github.com/berlinguyinca/autospec-db/internal/payload"
	"github.com/jackc/pgx/v5"
)

// confTemplate is written verbatim on first run — byte-for-byte the template
// the shell-era install.sh emitted (same keys, same comments).
const confTemplate = `# ~/.autospec/db.conf — autospec-db connection settings. Keep chmod 600.
#
# Admin credentials are used ONLY by the installer (schema, migrations,
# roles). Agents use the generated ~/.autospec/db.env instead and can never
# read or modify telemetry.
DB_HOST=localhost
DB_PORT=5432
DB_NAME=autospec
DB_ADMIN_USER=postgres
DB_ADMIN_PASSWORD=
# require | prefer | disable  (use require for anything non-local)
DB_SSLMODE=prefer

# Generated on first successful install — do not edit:
`

var dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func say(format string, a ...any)  { fmt.Printf("autospec-db: "+format+"\n", a...) }
func serr(format string, a ...any) { fmt.Fprintf(os.Stderr, "autospec-db: ERROR: "+format+"\n", a...) }

func cmdInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	confPath := config.ConfPath()

	// ── 1. config file ──────────────────────────────────────────────────
	if _, err := os.Stat(confPath); err != nil {
		if mkErr := os.MkdirAll(filepath.Dir(confPath), 0o700); mkErr != nil {
			serr("cannot create %s: %v", filepath.Dir(confPath), mkErr)
			return 1
		}
		if wErr := os.WriteFile(confPath, []byte(confTemplate), 0o600); wErr != nil {
			serr("cannot write %s: %v", confPath, wErr)
			return 1
		}
		say("wrote config template: %s", confPath)
		say("→ edit it (at minimum DB_HOST + DB_ADMIN_PASSWORD), then re-run the same install line.")
		return 1
	}

	// ── 2. parse + validate ─────────────────────────────────────────────
	conf, _ := config.LoadConf()
	if conf.Host == "" {
		serr("DB_HOST missing in %s", confPath)
		return 1
	}
	if conf.Port == "" {
		serr("DB_PORT missing in %s", confPath)
		return 1
	}
	if conf.Name == "" {
		serr("DB_NAME missing in %s", confPath)
		return 1
	}
	if conf.AdminUser == "" {
		serr("DB_ADMIN_USER missing in %s", confPath)
		return 1
	}
	if conf.AdminPassword == "" {
		serr("DB_ADMIN_PASSWORD is empty in %s — set it and re-run", confPath)
		return 1
	}
	if conf.SSLMode == "" {
		conf.SSLMode = "prefer"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── 3. connectivity + create-database-if-missing ────────────────────
	conn, err := db.Connect(ctx, adminParams(conf, conf.Name, 5*time.Second))
	if err != nil {
		say("database '%s' not reachable — trying to create it", conf.Name)
		if !dbNameRe.MatchString(conf.Name) {
			serr("DB_NAME must match [A-Za-z0-9_]+ (got: %s)", conf.Name)
			return 1
		}
		maint, mErr := db.Connect(ctx, adminParams(conf, "postgres", 5*time.Second))
		if mErr != nil {
			serr("cannot reach Postgres at %s:%s as %s (checked dbs: %s, postgres)",
				conf.Host, conf.Port, conf.AdminUser, conf.Name)
			return 1
		}
		var exists int
		qErr := maint.QueryRow(ctx, "select 1 from pg_database where datname = $1", conf.Name).Scan(&exists)
		if qErr != nil && qErr != pgx.ErrNoRows {
			maint.Close(context.Background())
			serr("could not check for database %s: %v", conf.Name, qErr)
			return 1
		}
		if exists != 1 {
			// Identifier — cannot be a bound parameter; validated above.
			if _, cErr := maint.Exec(ctx, "create database "+conf.Name); cErr != nil {
				maint.Close(context.Background())
				serr("could not create database %s: %v", conf.Name, cErr)
				return 1
			}
			say("created database %s", conf.Name)
		}
		maint.Close(context.Background())

		conn, err = db.Connect(ctx, adminParams(conf, conf.Name, 5*time.Second))
		if err != nil {
			serr("database %s still not reachable after create: %v", conf.Name, err)
			return 1
		}
	}
	defer conn.Close(context.Background())

	// ── 4. schema + migrations ──────────────────────────────────────────
	applied, err := migrate.Apply(ctx, conn, autospecdb.Migrations)
	if err != nil {
		serr("migrations failed: %v", err)
		return 1
	}
	say("migrations: %d applied, %d already present", len(applied), migrationCount()-len(applied))

	// ── 5. roles (passwords generated once, persisted in db.conf) ────────
	emitPw := conf.EmitPassword
	if emitPw == "" {
		emitPw = genSecret()
		if aErr := appendConf(confPath, "EMIT_PASSWORD", emitPw); aErr != nil {
			serr("could not persist EMIT_PASSWORD: %v", aErr)
			return 1
		}
	}
	readPw := conf.ReadPassword
	if readPw == "" {
		readPw = genSecret()
		if aErr := appendConf(confPath, "READ_PASSWORD", readPw); aErr != nil {
			serr("could not persist READ_PASSWORD: %v", aErr)
			return 1
		}
	}
	if err := convergeRoles(ctx, conn, emitPw, readPw); err != nil {
		serr("role convergence failed: %v", err)
		return 1
	}
	say("roles converged (autospec_emit: ingest-only, autospec_read: select-only)")

	// ── 6. agent env file ───────────────────────────────────────────────
	if err := writeEnvFile(config.EnvPath(), conf, emitPw, readPw); err != nil {
		serr("could not write %s: %v", config.EnvPath(), err)
		return 1
	}

	// ── 7. verification round-trip through the agent role ───────────────
	if err := probeEmit(ctx, conf, emitPw); err != nil {
		serr("verification emit through autospec_emit failed: %v", err)
		return 1
	}
	say("verification event ingested through the agent role")

	// ── 8. summary ──────────────────────────────────────────────────────
	envPath := config.EnvPath()
	say("DONE. Agents on this machine are configured via %s", envPath)
	say("→ other machines: copy %s there (scp %s host:~/.autospec/)", envPath, envPath)
	say("→ dashboards: use AUTOSPEC_DB_READ_DSN from %s (Grafana/Metabase)", envPath)
	return 0
}

func migrationCount() int {
	names, _ := migrate.Embedded(autospecdb.Migrations)
	return len(names)
}

// genSecret returns 20 crypto-random bytes as 40 hex characters.
func genSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b)
}

// appendConf appends KEY=VALUE to the conf file (matching the shell installer).
func appendConf(path, key, value string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s=%s\n", key, value)
	return err
}

// convergeRoles ports roles/bootstrap-roles.sql: create-if-absent, set
// passwords via a two-step format()+execute (never string-concatenated in Go),
// then apply the static least-privilege grants.
func convergeRoles(ctx context.Context, conn *pgx.Conn, emitPw, readPw string) error {
	const createRoles = `
do $$
begin
    if not exists (select from pg_roles where rolname = 'autospec_emit') then
        create role autospec_emit login;
    end if;
    if not exists (select from pg_roles where rolname = 'autospec_read') then
        create role autospec_read login;
    end if;
end
$$;`
	if _, err := conn.Exec(ctx, createRoles); err != nil {
		return fmt.Errorf("create roles: %w", err)
	}

	if err := setRolePassword(ctx, conn, "autospec_emit", emitPw); err != nil {
		return err
	}
	if err := setRolePassword(ctx, conn, "autospec_read", readPw); err != nil {
		return err
	}

	grants := []string{
		"grant usage on schema autospec to autospec_emit",
		"grant execute on function autospec.ingest(jsonb) to autospec_emit",
		"grant usage on schema autospec to autospec_read",
		"grant select on all tables in schema autospec to autospec_read",
		"alter default privileges in schema autospec grant select on tables to autospec_read",
		"grant execute on function autospec.stalled_sessions(interval) to autospec_read",
	}
	for _, g := range grants {
		if _, err := conn.Exec(ctx, g); err != nil {
			return fmt.Errorf("grant (%s): %w", g, err)
		}
	}
	return nil
}

// setRolePassword builds the ALTER ROLE statement server-side via format('%L')
// so the password is safely quoted, then executes the returned string. The
// role name is a validated literal; only the password crosses as a parameter.
func setRolePassword(ctx context.Context, conn *pgx.Conn, role, pw string) error {
	stmt := fmt.Sprintf("select format('ALTER ROLE %s LOGIN PASSWORD %%L', $1::text)", role)
	var altered string
	if err := conn.QueryRow(ctx, stmt, pw).Scan(&altered); err != nil {
		return fmt.Errorf("format alter for %s: %w", role, err)
	}
	if _, err := conn.Exec(ctx, altered); err != nil {
		return fmt.Errorf("set password for %s: %w", role, err)
	}
	return nil
}

// writeEnvFile writes ~/.autospec/db.env (chmod 600) with encoded DSN URLs.
func writeEnvFile(path string, conf *config.Conf, emitPw, readPw string) error {
	emitDSN := buildDSN("autospec_emit", emitPw, conf)
	readDSN := buildDSN("autospec_read", readPw, conf)
	body := "# Generated by autospec-db install — source this (autospec does it\n" +
		"# automatically). Copy this file to ~/.autospec/db.env on your other machines.\n" +
		fmt.Sprintf("export AUTOSPEC_DB_DSN=%q\n", emitDSN) +
		fmt.Sprintf("export AUTOSPEC_DB_READ_DSN=%q\n", readDSN)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

// buildDSN uses url.UserPassword so generated credentials are properly encoded.
func buildDSN(user, pw string, conf *config.Conf) string {
	q := url.Values{}
	q.Set("sslmode", conf.SSLMode)
	q.Set("connect_timeout", "2")
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(user, pw),
		Host:     conf.Host + ":" + conf.Port,
		Path:     "/" + conf.Name,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// probeEmit ingests a session.terminal/install-verified event THROUGH the emit
// role (which cannot read anything back — that is the design).
func probeEmit(ctx context.Context, conf *config.Conf, emitPw string) error {
	pconn, err := db.Connect(ctx, db.Params{
		Host:     conf.Host,
		Port:     conf.Port,
		User:     "autospec_emit",
		Password: emitPw,
		Database: conf.Name,
		SSLMode:  conf.SSLMode,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		return err
	}
	defer pconn.Close(context.Background())

	line, err := payload.BuildJSON("session.terminal", []string{
		"session_id=install-probe",
		"outcome=install-verified",
	})
	if err != nil {
		return err
	}
	return db.Ingest(ctx, pconn, line)
}
