// Package spool is the local, flock-guarded overflow buffer for events that
// could not be ingested (database unreachable). Telemetry is lossy-by-design:
// on overflow the oldest lines are dropped; runs are never blocked.
package spool

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// DefaultMaxBytes is the spool size cap when AUTOSPEC_DB_SPOOL_MAX_BYTES is unset.
const DefaultMaxBytes int64 = 10 * 1024 * 1024

// PoisonError marks an ingest failure where the server responded with a data
// error (bad cast / constraint). Such a line is dropped on drain rather than
// retained — it can never succeed and must not wedge the spool.
type PoisonError struct{ Err error }

func (e *PoisonError) Error() string {
	if e.Err == nil {
		return "poison payload"
	}
	return "poison payload: " + e.Err.Error()
}

func (e *PoisonError) Unwrap() error { return e.Err }

// Poison wraps err so Drain treats the line as unrecoverable (drop it).
func Poison(err error) error { return &PoisonError{Err: err} }

func isPoison(err error) bool {
	var p *PoisonError
	return errors.As(err, &p)
}

// Result reports the outcome of a Drain pass.
type Result struct {
	Replayed int // successfully ingested and removed
	Dropped  int // poison lines removed without ingest
	Kept     int // lines still in the spool afterwards
}

// Spool is a handle to a single spool file.
type Spool struct {
	path     string
	maxBytes int64
}

// New returns a spool at path with the given size cap.
func New(path string, maxBytes int64) *Spool {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Spool{path: path, maxBytes: maxBytes}
}

// Default returns the spool at spoolPath, honoring AUTOSPEC_DB_SPOOL_MAX_BYTES.
func Default(spoolPath string) *Spool {
	max := DefaultMaxBytes
	if v := os.Getenv("AUTOSPEC_DB_SPOOL_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			max = n
		}
	}
	return New(spoolPath, max)
}

// Path returns the spool file path.
func (s *Spool) Path() string { return s.path }

// lock acquires an exclusive advisory lock via a sidecar .lock file, so the
// data file can be atomically replaced (temp+rename) without invalidating the
// lock. Returns an unlock func.
func (s *Spool) lock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// readLines returns the non-empty lines currently on disk.
func (s *Spool) readLines() ([][]byte, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return splitLines(data), nil
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	for _, ln := range bytes.Split(data, []byte{'\n'}) {
		if len(ln) > 0 {
			out = append(out, ln)
		}
	}
	return out
}

func totalBytes(lines [][]byte) int64 {
	var n int64
	for _, ln := range lines {
		n += int64(len(ln)) + 1 // + newline
	}
	return n
}

// writeAtomic replaces the spool file with lines via a temp file + rename,
// holding the caller's lock (on the sidecar) throughout.
func (s *Spool) writeAtomic(lines [][]byte) error {
	var buf bytes.Buffer
	for _, ln := range lines {
		buf.Write(ln)
		buf.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".db-spool-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// Append adds line to the spool under an exclusive lock. On overflow past the
// size cap, the OLDEST lines are dropped first (the just-appended line is kept).
func (s *Spool) Append(line []byte) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()

	lines, err := s.readLines()
	if err != nil {
		return err
	}
	lines = append(lines, bytes.TrimRight(line, "\n"))
	for len(lines) > 1 && totalBytes(lines) > s.maxBytes {
		lines = lines[1:]
	}
	return s.writeAtomic(lines)
}

// Drain replays spool lines through ingest under an exclusive lock. Per line:
// nil error → replayed (removed); PoisonError → dropped (removed); any other
// error → the line is kept and draining stops (connection error). Processing
// halts at maxLines processed (>0) or once deadline passes (non-zero); the
// remaining lines are kept. The file is atomically rewritten with the kept set.
func (s *Spool) Drain(ingest func([]byte) error, maxLines int, deadline time.Time) (Result, error) {
	unlock, err := s.lock()
	if err != nil {
		return Result{}, err
	}
	defer unlock()

	lines, err := s.readLines()
	if err != nil {
		return Result{}, err
	}

	var res Result
	var kept [][]byte
	stop := false
	for _, ln := range lines {
		if stop {
			kept = append(kept, ln)
			continue
		}
		if maxLines > 0 && res.Replayed+res.Dropped >= maxLines {
			stop = true
			kept = append(kept, ln)
			continue
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			stop = true
			kept = append(kept, ln)
			continue
		}
		switch err := ingest(ln); {
		case err == nil:
			res.Replayed++
		case isPoison(err):
			res.Dropped++
		default:
			// connection error: keep this line and everything after it
			stop = true
			kept = append(kept, ln)
		}
	}
	res.Kept = len(kept)
	if werr := s.writeAtomic(kept); werr != nil {
		return res, werr
	}
	return res, nil
}

// Stat returns the spool's on-disk byte size and line count.
func (s *Spool) Stat() (size int64, lines int, err error) {
	info, statErr := os.Stat(s.path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return 0, 0, nil
		}
		return 0, 0, statErr
	}
	ls, rerr := s.readLines()
	if rerr != nil {
		return info.Size(), 0, rerr
	}
	return info.Size(), len(ls), nil
}
