package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Unsetenv("AUTOSPEC_DB_CONF")
	// clear any env overrides that could bleed in
	for _, k := range []string{"DB_HOST", "DB_PORT", "DB_NAME", "DB_ADMIN_USER",
		"DB_ADMIN_PASSWORD", "DB_SSLMODE", "EMIT_PASSWORD", "READ_PASSWORD",
		"AUTOSPEC_DB_DSN", "AUTOSPEC_DB_READ_DSN"} {
		os.Unsetenv(k)
	}
	return home
}

func TestLoadConfParsesFile(t *testing.T) {
	home := setupHome(t)
	writeFile(t, filepath.Join(home, ".autospec", "db.conf"), `# comment
DB_HOST=db.example.com
DB_PORT=5433
DB_NAME=autospec
DB_ADMIN_USER=admin
DB_ADMIN_PASSWORD=s3cret with spaces
DB_SSLMODE=require
EMIT_PASSWORD=abc123
`)
	c, exists := LoadConf()
	if !exists {
		t.Fatal("exists = false, want true")
	}
	if c.Host != "db.example.com" {
		t.Errorf("Host = %q", c.Host)
	}
	if c.Port != "5433" {
		t.Errorf("Port = %q", c.Port)
	}
	if c.AdminPassword != "s3cret with spaces" {
		t.Errorf("AdminPassword = %q", c.AdminPassword)
	}
	if c.SSLMode != "require" {
		t.Errorf("SSLMode = %q", c.SSLMode)
	}
	if c.EmitPassword != "abc123" {
		t.Errorf("EmitPassword = %q", c.EmitPassword)
	}
}

func TestLoadConfEnvWins(t *testing.T) {
	home := setupHome(t)
	writeFile(t, filepath.Join(home, ".autospec", "db.conf"), "DB_HOST=fromfile\nDB_ADMIN_PASSWORD=filepw\n")
	t.Setenv("DB_HOST", "fromenv")
	t.Setenv("DB_ADMIN_PASSWORD", "envpw")
	c, _ := LoadConf()
	if c.Host != "fromenv" {
		t.Errorf("env should win: Host = %q", c.Host)
	}
	if c.AdminPassword != "envpw" {
		t.Errorf("env should win: AdminPassword = %q", c.AdminPassword)
	}
}

func TestLoadConfMissingFile(t *testing.T) {
	setupHome(t)
	_, exists := LoadConf()
	if exists {
		t.Error("exists = true for missing file")
	}
}

func TestEmitDSNEnvWins(t *testing.T) {
	home := setupHome(t)
	writeFile(t, filepath.Join(home, ".autospec", "db.env"),
		`export AUTOSPEC_DB_DSN="postgresql://file"`+"\n")
	t.Setenv("AUTOSPEC_DB_DSN", "postgresql://env")
	dsn, ok := EmitDSN()
	if !ok || dsn != "postgresql://env" {
		t.Errorf("EmitDSN = %q, %v; want env value", dsn, ok)
	}
}

func TestEmitDSNFromEnvFile(t *testing.T) {
	home := setupHome(t)
	writeFile(t, filepath.Join(home, ".autospec", "db.env"),
		`export AUTOSPEC_DB_DSN="postgresql://autospec_emit:pw@h:5432/autospec?sslmode=require"`+"\n"+
			`export AUTOSPEC_DB_READ_DSN="postgresql://autospec_read:pw@h:5432/autospec"`+"\n")
	dsn, ok := EmitDSN()
	if !ok {
		t.Fatal("EmitDSN ok = false")
	}
	if dsn != "postgresql://autospec_emit:pw@h:5432/autospec?sslmode=require" {
		t.Errorf("EmitDSN = %q", dsn)
	}
	rdsn, rok := ReadDSN()
	if !rok || rdsn != "postgresql://autospec_read:pw@h:5432/autospec" {
		t.Errorf("ReadDSN = %q, %v", rdsn, rok)
	}
}

func TestEmitDSNAbsent(t *testing.T) {
	setupHome(t)
	if _, ok := EmitDSN(); ok {
		t.Error("EmitDSN ok = true with no env and no db.env")
	}
}

func TestConfPathOverride(t *testing.T) {
	setupHome(t)
	t.Setenv("AUTOSPEC_DB_CONF", "/custom/path/db.conf")
	if ConfPath() != "/custom/path/db.conf" {
		t.Errorf("ConfPath = %q", ConfPath())
	}
}

func TestDisabled(t *testing.T) {
	t.Setenv("AUTOSPEC_DB_DISABLE", "")
	if Disabled() {
		t.Error("empty should not disable")
	}
	t.Setenv("AUTOSPEC_DB_DISABLE", "0")
	if Disabled() {
		t.Error("0 should not disable")
	}
	t.Setenv("AUTOSPEC_DB_DISABLE", "1")
	if !Disabled() {
		t.Error("1 should disable")
	}
	t.Setenv("AUTOSPEC_DB_DISABLE", "true")
	if !Disabled() {
		t.Error("true should disable")
	}
}
