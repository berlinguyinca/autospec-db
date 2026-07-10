package payload

import (
	"os"
	"strconv"
	"testing"
)

func clearSessionEnv(t *testing.T) {
	t.Helper()
	for _, k := range sessionEnvChain {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestBuildAutoFields(t *testing.T) {
	m := Build("heartbeat", nil)
	if m["schema"] != Schema {
		t.Errorf("schema = %q, want %q", m["schema"], Schema)
	}
	if m["kind"] != "heartbeat" {
		t.Errorf("kind = %q, want heartbeat", m["kind"])
	}
	if m["event_uuid"] == "" {
		t.Error("event_uuid empty")
	}
	if len(m["event_uuid"]) != 36 {
		t.Errorf("event_uuid not a uuid4: %q", m["event_uuid"])
	}
	if m["ts"] == "" {
		t.Error("ts empty")
	}
}

func TestBuildEventUUIDUnique(t *testing.T) {
	a := Build("heartbeat", nil)["event_uuid"]
	b := Build("heartbeat", nil)["event_uuid"]
	if a == b {
		t.Errorf("event_uuid not unique across calls: %q", a)
	}
}

func TestBuildKVOverride(t *testing.T) {
	m := Build("session.step", []string{"repo=owner/name", "step=finalize", "kind=OVERRIDDEN"})
	if m["repo"] != "owner/name" {
		t.Errorf("repo = %q", m["repo"])
	}
	if m["step"] != "finalize" {
		t.Errorf("step = %q", m["step"])
	}
	// a k=v may override an auto field
	if m["kind"] != "OVERRIDDEN" {
		t.Errorf("kind override failed: %q", m["kind"])
	}
}

func TestBuildLaterArgWins(t *testing.T) {
	m := Build("x", []string{"a=1", "a=2", "a=3"})
	if m["a"] != "3" {
		t.Errorf("later arg should win: a = %q", m["a"])
	}
}

func TestBuildRawValues(t *testing.T) {
	raw := `title with "quotes" and $$ and \backslash`
	m := Build("feature.described", []string{"detail=" + raw})
	if m["detail"] != raw {
		t.Errorf("value not preserved raw: %q", m["detail"])
	}
}

func TestBuildIgnoresArgsWithoutEquals(t *testing.T) {
	m := Build("x", []string{"noequals", "k=v"})
	if _, ok := m["noequals"]; ok {
		t.Error("arg without '=' should be ignored")
	}
	if m["k"] != "v" {
		t.Error("valid arg lost")
	}
}

func TestSessionIDChainOrder(t *testing.T) {
	clearSessionEnv(t)
	t.Setenv("CODEX_SESSION_ID", "codex")
	if got := SessionID(); got != "codex" {
		t.Errorf("SessionID = %q, want codex", got)
	}
	t.Setenv("OPENCODE_SESSION_ID", "opencode")
	if got := SessionID(); got != "opencode" {
		t.Errorf("SessionID = %q, want opencode", got)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude")
	if got := SessionID(); got != "claude" {
		t.Errorf("SessionID = %q, want claude", got)
	}
	t.Setenv("AUTOSPEC_SESSION_ID", "autospec")
	if got := SessionID(); got != "autospec" {
		t.Errorf("SessionID = %q, want autospec (highest priority)", got)
	}
}

func TestSessionIDFallbackPID(t *testing.T) {
	clearSessionEnv(t)
	want := "pid-" + strconv.Itoa(os.Getppid())
	if got := SessionID(); got != want {
		t.Errorf("SessionID fallback = %q, want %q", got, want)
	}
}

func TestBuildJSONValid(t *testing.T) {
	b, err := BuildJSON("heartbeat", []string{"repo=a/b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 || b[0] != '{' {
		t.Errorf("not a JSON object: %s", b)
	}
}
