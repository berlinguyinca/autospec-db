// Package db builds pgx connections (from either discrete conf fields or a DSN
// URL) and performs the single wire-contract call, autospec.ingest().
package db

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Params are discrete connection fields (from db.conf). Building a conninfo
// keyword string — with libpq-style escaping — avoids the URL-encoding pitfalls
// of arbitrary admin passwords while still letting pgx derive TLS from sslmode.
type Params struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
	SSLMode  string
	Timeout  time.Duration
}

// escapeConninfo quotes a libpq keyword/value per the conninfo rules: empty or
// whitespace/quote/backslash-bearing values are single-quoted with '\” and
// '\\' escapes. This is NOT URL encoding.
func escapeConninfo(v string) string {
	if v == "" {
		return "''"
	}
	if strings.ContainsAny(v, " '\\\t\r\n") {
		v = strings.ReplaceAll(v, `\`, `\\`)
		v = strings.ReplaceAll(v, `'`, `\'`)
		return "'" + v + "'"
	}
	return v
}

// Connect opens a connection from discrete Params.
func Connect(ctx context.Context, p Params) (*pgx.Conn, error) {
	sslmode := p.SSLMode
	if sslmode == "" {
		sslmode = "prefer"
	}
	kw := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		escapeConninfo(p.Host),
		escapeConninfo(p.Port),
		escapeConninfo(p.User),
		escapeConninfo(p.Password),
		escapeConninfo(p.Database),
		escapeConninfo(sslmode),
	)
	cfg, err := pgx.ParseConfig(kw)
	if err != nil {
		return nil, err
	}
	if p.Timeout > 0 {
		cfg.ConnectTimeout = p.Timeout
	}
	return pgx.ConnectConfig(ctx, cfg)
}

// ConnectDSN opens a connection from a DSN URL (or keyword string).
func ConnectDSN(ctx context.Context, dsn string, timeout time.Duration) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		cfg.ConnectTimeout = timeout
	}
	return pgx.ConnectConfig(ctx, cfg)
}

// Ingest executes the wire contract: SELECT autospec.ingest($1::jsonb). The
// payload is a BOUND parameter — never spliced into SQL text.
func Ingest(ctx context.Context, conn *pgx.Conn, payload []byte) error {
	_, err := conn.Exec(ctx, "select autospec.ingest($1::jsonb)", string(payload))
	return err
}

// IsServerError reports whether err is a server-side data error (the server
// responded with a PgError: bad cast, constraint, etc.) as opposed to a
// connection/timeout error.
func IsServerError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr)
}

// ServerVersion returns the server_version parameter status, if known.
func ServerVersion(conn *pgx.Conn) string {
	return conn.PgConn().ParameterStatus("server_version")
}

// UsesTLS reports whether the connection's underlying transport is TLS.
func UsesTLS(conn *pgx.Conn) bool {
	nc := conn.PgConn().Conn()
	_, ok := nc.(*tls.Conn)
	return ok
}
