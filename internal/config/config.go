// Package config resolves autospec-db connection settings from
// ~/.autospec/db.conf (admin/installer creds) and ~/.autospec/db.env (agent
// DSNs). Environment variables ALWAYS win over file contents.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Conf holds the installer-facing admin settings parsed from db.conf.
type Conf struct {
	Host          string
	Port          string
	Name          string
	AdminUser     string
	AdminPassword string
	SSLMode       string
	EmitPassword  string
	ReadPassword  string
}

// confKeys maps struct fields to their db.conf / environment key names.
var confKeys = []struct {
	key string
	set func(*Conf, string)
}{
	{"DB_HOST", func(c *Conf, v string) { c.Host = v }},
	{"DB_PORT", func(c *Conf, v string) { c.Port = v }},
	{"DB_NAME", func(c *Conf, v string) { c.Name = v }},
	{"DB_ADMIN_USER", func(c *Conf, v string) { c.AdminUser = v }},
	{"DB_ADMIN_PASSWORD", func(c *Conf, v string) { c.AdminPassword = v }},
	{"DB_SSLMODE", func(c *Conf, v string) { c.SSLMode = v }},
	{"EMIT_PASSWORD", func(c *Conf, v string) { c.EmitPassword = v }},
	{"READ_PASSWORD", func(c *Conf, v string) { c.ReadPassword = v }},
}

// Home returns $HOME (respecting an overridden environment).
// Disabled reports whether telemetry is hard-disabled via AUTOSPEC_DB_DISABLE.
// Any non-empty value other than "0" disables. This is the kill switch for
// test harnesses and emergencies: emit and drain exit 0 immediately — no
// config resolution, no network, no spool.
func Disabled() bool {
	v := os.Getenv("AUTOSPEC_DB_DISABLE")
	return v != "" && v != "0"
}

func Home() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// ConfPath is AUTOSPEC_DB_CONF or ~/.autospec/db.conf.
func ConfPath() string {
	if p := os.Getenv("AUTOSPEC_DB_CONF"); p != "" {
		return p
	}
	return filepath.Join(Home(), ".autospec", "db.conf")
}

// EnvPath is ~/.autospec/db.env.
func EnvPath() string {
	return filepath.Join(Home(), ".autospec", "db.env")
}

// SpoolPath is ~/.autospec/db-spool.jsonl.
func SpoolPath() string {
	return filepath.Join(Home(), ".autospec", "db-spool.jsonl")
}

// ParseKV parses KEY=VALUE lines, ignoring blank lines and # comments. Values
// keep their raw text (an optional surrounding pair of quotes is stripped).
func ParseKV(r *bufio.Scanner) map[string]string {
	m := map[string]string{}
	for r.Scan() {
		line := strings.TrimSpace(r.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		k = strings.TrimPrefix(k, "export ")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = unquote(v)
		m[k] = v
	}
	return m
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// parseFile reads KEY=VALUE pairs from path (missing file → empty map).
func parseFile(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return map[string]string{}
	}
	defer f.Close()
	return ParseKV(bufio.NewScanner(f))
}

// LoadConf parses db.conf and layers environment overrides on top. The bool
// reports whether the conf file exists on disk (install uses it to decide
// whether to write the template).
func LoadConf() (*Conf, bool) {
	path := ConfPath()
	_, statErr := os.Stat(path)
	exists := statErr == nil

	m := parseFile(path)
	c := &Conf{}
	for _, ck := range confKeys {
		v := m[ck.key]
		if env := os.Getenv(ck.key); env != "" {
			v = env // env ALWAYS wins
		}
		ck.set(c, v)
	}
	return c, exists
}

// envFileValue returns the value of a variable exported by db.env.
func envFileValue(name string) (string, bool) {
	m := parseFile(EnvPath())
	v, ok := m[name]
	return v, ok
}

// EmitDSN resolves the agent (autospec_emit) DSN: AUTOSPEC_DB_DSN in the
// environment wins, otherwise db.env. The bool is false when neither source
// provides it — the signal that emit must be a silent no-op.
func EmitDSN() (string, bool) {
	if v := os.Getenv("AUTOSPEC_DB_DSN"); v != "" {
		return v, true
	}
	return envFileValue("AUTOSPEC_DB_DSN")
}

// ReadDSN resolves the dashboard (autospec_read) DSN: AUTOSPEC_DB_READ_DSN in
// the environment wins, otherwise db.env.
func ReadDSN() (string, bool) {
	if v := os.Getenv("AUTOSPEC_DB_READ_DSN"); v != "" {
		return v, true
	}
	return envFileValue("AUTOSPEC_DB_READ_DSN")
}
