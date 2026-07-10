// Package payload builds autospec.events.v1 JSON event objects.
package payload

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Schema is the literal wire-contract identifier every event carries.
const Schema = "autospec.events.v1"

// sessionEnvChain is the harness session-id fallback order.
var sessionEnvChain = []string{
	"AUTOSPEC_SESSION_ID",
	"CLAUDE_CODE_SESSION_ID",
	"OPENCODE_SESSION_ID",
	"CODEX_SESSION_ID",
}

// SessionID resolves the session identity from the harness env chain, falling
// back to pid-<ppid> when no harness variable is set.
func SessionID() string {
	for _, k := range sessionEnvChain {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "pid-" + strconv.Itoa(os.Getppid())
}

// shortHost returns AUTOSPEC_DB_HOST_LABEL when set (site/machine
// disambiguation for generic hostnames), else the hostname truncated at the
// first dot (hostname -s).
func shortHost() string {
	if l := os.Getenv("AUTOSPEC_DB_HOST_LABEL"); l != "" {
		return l
	}
	h, _ := os.Hostname()
	if i := strings.IndexByte(h, '.'); i >= 0 {
		h = h[:i]
	}
	return h
}

// Build assembles an event object. The auto fields (schema, event_uuid, kind,
// ts, session_id, host) are set first; caller key=value args are merged on top
// in order (later args win, and a k=v may override an auto field). Args without
// a '=' are ignored.
func Build(kind string, args []string) map[string]string {
	m := map[string]string{
		"schema":     Schema,
		"event_uuid": uuid.NewString(),
		"kind":       kind,
		"ts":         time.Now().UTC().Format(time.RFC3339),
		"session_id": SessionID(),
		"host":       shortHost(),
	}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			continue
		}
		m[k] = v
	}
	return m
}

// BuildJSON builds an event and marshals it to a compact JSON line.
func BuildJSON(kind string, args []string) ([]byte, error) {
	return json.Marshal(Build(kind, args))
}
