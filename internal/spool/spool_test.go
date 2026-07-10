package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newSpool(t *testing.T, maxBytes int64) *Spool {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "db-spool.jsonl"), maxBytes)
}

func TestAppendAndStat(t *testing.T) {
	s := newSpool(t, DefaultMaxBytes)
	for i := 0; i < 3; i++ {
		if err := s.Append([]byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	_, lines, err := s.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if lines != 3 {
		t.Errorf("lines = %d, want 3", lines)
	}
}

func TestAppendCapDropsOldest(t *testing.T) {
	// cap tuned so only ~2 of the 5 lines fit
	s := newSpool(t, 24)
	for i := 0; i < 5; i++ {
		if err := s.Append([]byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := s.readLines()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 || len(lines) >= 5 {
		t.Fatalf("expected oldest dropped, got %d lines", len(lines))
	}
	// newest line must survive
	last := string(lines[len(lines)-1])
	if last != `{"n":4}` {
		t.Errorf("newest line lost: last = %q", last)
	}
	// oldest must be gone
	if string(lines[0]) == `{"n":0}` {
		t.Error("oldest line should have been dropped")
	}
}

func TestDrainAllSuccess(t *testing.T) {
	s := newSpool(t, DefaultMaxBytes)
	for i := 0; i < 4; i++ {
		s.Append([]byte(fmt.Sprintf(`{"n":%d}`, i)))
	}
	var seen int
	res, err := s.Drain(func(l []byte) error { seen++; return nil }, 0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Replayed != 4 || res.Kept != 0 || res.Dropped != 0 {
		t.Errorf("res = %+v, want replayed=4 kept=0 dropped=0", res)
	}
	if seen != 4 {
		t.Errorf("ingest called %d times", seen)
	}
	_, lines, _ := s.Stat()
	if lines != 0 {
		t.Errorf("spool not emptied: %d lines", lines)
	}
}

func TestDrainPoisonDropped(t *testing.T) {
	s := newSpool(t, DefaultMaxBytes)
	s.Append([]byte(`{"n":0}`))
	s.Append([]byte(`{"bad":1}`))
	s.Append([]byte(`{"n":2}`))
	res, err := s.Drain(func(l []byte) error {
		if string(l) == `{"bad":1}` {
			return Poison(errors.New("bad cast"))
		}
		return nil
	}, 0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Replayed != 2 || res.Dropped != 1 || res.Kept != 0 {
		t.Errorf("res = %+v, want replayed=2 dropped=1 kept=0", res)
	}
}

func TestDrainConnErrorKeepsAndStops(t *testing.T) {
	s := newSpool(t, DefaultMaxBytes)
	for i := 0; i < 4; i++ {
		s.Append([]byte(fmt.Sprintf(`{"n":%d}`, i)))
	}
	calls := 0
	res, err := s.Drain(func(l []byte) error {
		calls++
		if calls == 2 {
			return errors.New("connection refused") // not poison
		}
		return nil
	}, 0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// line 1 replayed, line 2 fails (kept + stop), lines 3,4 kept untried
	if res.Replayed != 1 || res.Kept != 3 {
		t.Errorf("res = %+v, want replayed=1 kept=3", res)
	}
	if calls != 2 {
		t.Errorf("ingest should stop after conn error: calls = %d", calls)
	}
	_, lines, _ := s.Stat()
	if lines != 3 {
		t.Errorf("expected 3 lines retained, got %d", lines)
	}
}

func TestDrainMaxLines(t *testing.T) {
	s := newSpool(t, DefaultMaxBytes)
	for i := 0; i < 5; i++ {
		s.Append([]byte(fmt.Sprintf(`{"n":%d}`, i)))
	}
	res, err := s.Drain(func(l []byte) error { return nil }, 2, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Replayed != 2 || res.Kept != 3 {
		t.Errorf("res = %+v, want replayed=2 kept=3", res)
	}
}

func TestConcurrentAppends(t *testing.T) {
	s := newSpool(t, DefaultMaxBytes)
	const workers = 8
	const each = 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := s.Append([]byte(fmt.Sprintf(`{"w":%d,"i":%d}`, w, i))); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	_, lines, err := s.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if lines != workers*each {
		t.Errorf("lines = %d, want %d (lost writes under concurrency)", lines, workers*each)
	}
}

func TestDefaultHonorsEnvCap(t *testing.T) {
	t.Setenv("AUTOSPEC_DB_SPOOL_MAX_BYTES", "512")
	s := Default(filepath.Join(t.TempDir(), "s.jsonl"))
	if s.maxBytes != 512 {
		t.Errorf("maxBytes = %d, want 512", s.maxBytes)
	}
}

func TestAppendCreatesDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deep", "db-spool.jsonl")
	s := New(path, DefaultMaxBytes)
	if err := s.Append([]byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("spool file not created: %v", err)
	}
}
