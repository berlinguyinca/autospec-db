#!/bin/bash
# The CI gate for autospec-db: a throwaway PostgreSQL cluster, then
# `go vet` / `go build` / `go test -race`, then the anti-vacuous-green guard.
#
# This replaces the TeamCity build `Autospec_AutospecDbCi` ("Postgres instance
# and go test -race"), which did the same three things but obtained its server
# from an Apptainer SIF of the postgres:16 image on quobyte -- itself already a
# stand-in for the GitHub Actions `services: postgres:16` block.
#
# WHY NO CONTAINER HERE. The Woodpecker exec hosts are Slurm jobs on Rocky 9;
# `image:` in .woodpecker.yml names a HOST executable, and there is no docker,
# no sudo and no apt. But the agent image ships the PostgreSQL server binaries
# (initdb, pg_ctl, postgres) and the step runs unprivileged, which is precisely
# what a cluster of one's own needs: postgres refuses to run as root, so the
# thing that blocks a container here is the thing that makes initdb work. One
# `initdb` into a temp directory is cheaper, quieter and easier to reason about
# than any of the three container shapes this check has worn so far.
#
# WHAT THE SUITE CANNOT DO. cmd/autospec-db/integration_test.go does NOT start
# a database; it reads AUTOSPEC_DB_TEST_DSN and calls t.Skip when it is empty.
# So the DSN that this script exports IS the test fixture, and a bug that stops
# it reaching `go test` does not fail -- it SKIPS, and the build goes green over
# zero database coverage. That is what the guard at the bottom exists for, and
# it is the single most load-bearing line in this file.
#
# RUN IT LOCALLY: `bash ops/ci/woodpecker-gates.sh` from a clean checkout, on
# any machine with a Go toolchain and PostgreSQL server binaries installed. It
# takes no Woodpecker-specific input and needs no secret: the superuser password
# is generated per run and never leaves the job.
set -euo pipefail

journal=""
for candidate in "${WOODPECKER_JOURNAL_DIR:-}" /home/wohlgemuth/woodpecker/logs; do
  [ -n "$candidate" ] || continue
  if mkdir -p "$candidate" 2>/dev/null && [ -w "$candidate" ]; then
    journal="$candidate/autospec-db-gates-${CI_COMMIT_SHA:-local}-$(date +%s).log"
    break
  fi
done

SCRATCH=""
PGBIN=""
PGDATA_DIR=""
PGSOCK_DIR=""

# A pg_ctl-started postmaster is an ordinary child of this step, so it lives in
# the Slurm job's cgroup and dies with the allocation. The TeamCity step needed
# a reaper for stale `autospecdb-*` instances because `apptainer instance start`
# daemonises OUTSIDE the job and survived a killed build, holding a port and
# /dev/shm segments until it turned into a red on some later build on the same
# agent. That failure mode does not exist for this shape, so the reaper is
# deliberately absent rather than forgotten.
cleanup() {
  if [ -n "$PGDATA_DIR" ] && [ -n "$PGBIN" ] && [ -s "$PGDATA_DIR/postmaster.pid" ]; then
    # -m immediate, not fast: this is a disposable cluster with nothing to
    # flush, and a hung shutdown here would burn the step's remaining walltime.
    "$PGBIN/pg_ctl" -D "$PGDATA_DIR" -m immediate stop >/dev/null 2>&1 || true
  fi
  [ -n "$SCRATCH" ] && rm -rf "$SCRATCH"
  return 0
}
trap cleanup EXIT INT TERM

# resolve_pgbin finds the server bindir.
#
# It does NOT just use `command -v initdb`. On Debian that name can be
# postgresql-common's pg_wrapper, which dispatches on a CONFIGURED cluster and
# fails in a way that reads like a postgres bug rather than a packaging one when
# there is no cluster and the caller is not the postgres user. Naming the
# versioned bindir explicitly removes that whole class.
resolve_pgbin() {
  local d best=""
  for d in /usr/lib/postgresql/*/bin /usr/pgsql-*/bin; do
    [ -x "$d/initdb" ] && [ -x "$d/pg_ctl" ] && best="$d"
  done
  if [ -z "$best" ]; then
    # Fall back to PATH only if the well-known layouts are absent.
    if command -v initdb >/dev/null 2>&1 && command -v pg_ctl >/dev/null 2>&1; then
      best="$(dirname "$(command -v initdb)")"
    fi
  fi
  if [ -z "$best" ]; then
    echo "FATAL: no PostgreSQL server binaries on this agent." >&2
    echo "Looked in /usr/lib/postgresql/*/bin, /usr/pgsql-*/bin and on PATH." >&2
    echo "The integration tests need a server, not just libpq: initdb and pg_ctl." >&2
    return 1
  fi
  printf '%s' "$best"
}

# alloc_port asks the kernel for a free ephemeral port.
#
# Several agents share a compute node (the agent job asks for 8 of a node's 24
# CPUs precisely so three can), and nothing here has a network namespace of its
# own, so a hardcoded 5432 is a collision waiting for the second concurrent
# pipeline. The TeamCity step learned this the same way.
alloc_port() {
  python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'
}

# start_pg initialises and starts a cluster on a fresh port, returning with
# PGPORT set. The caller retries it once: alloc_port releases the port before
# postgres rebinds it, so another process on the node can take it in between.
start_pg() {
  local attempt="$1" pwfile
  PGDATA_DIR="$SCRATCH/pg$attempt/data"
  PGSOCK_DIR="$SCRATCH/pg$attempt/run"
  mkdir -p "$PGDATA_DIR" "$PGSOCK_DIR"
  chmod 700 "$PGDATA_DIR"

  pwfile="$SCRATCH/pg$attempt/pw"
  ( umask 077; printf '%s\n' "$PGPASSWORD_GEN" > "$pwfile" )

  # --auth-host=scram-sha-256, not trust: TestIntegrationEmitRoleBlastRadius
  # creates autospec_emit with a password and RECONNECTS over TCP as that role
  # to prove it cannot read the corpus. Under trust the password would be
  # ignored and the test would still pass, but it would have stopped testing
  # the credential path the installer actually generates.
  #
  # --locale=C and LC_ALL=C, because the agent's environment is NOT the agent
  # image's. The Slurm job runs on Rocky 9 and exports its LANG (en_US.UTF-8)
  # into a Debian container that does not have that locale generated, and
  # initdb -- which inherits it -- dies with "invalid locale settings; check
  # LANG and LC_* environment variables" before writing any server log. C
  # collation is the right default for a disposable test cluster anyway: it is
  # the one ordering that does not drift with the container's glibc.
  if ! LC_ALL=C LANG=C "$PGBIN/initdb" -D "$PGDATA_DIR" -U postgres \
    --auth-local=trust --auth-host=scram-sha-256 \
    --pwfile="$pwfile" --encoding=UTF8 --locale=C --no-sync >/dev/null; then
      rm -f "$pwfile"
      echo "initdb failed in $PGDATA_DIR" >&2
      return 1
  fi
  rm -f "$pwfile"

  # -k: a unix socket in our own scratch dir. The default /var/run/postgresql
  # is not writable here, and putting it in the Woodpecker workspace risks the
  # ~107-character sockaddr_un limit on a deep checkout path.
  # fsync=off: a cluster that is deleted at the end of the step.
  LC_ALL=C LANG=C "$PGBIN/pg_ctl" -D "$PGDATA_DIR" -l "$SCRATCH/pg$attempt/postgres.log" -w -t 60 \
    -o "-p $PGPORT -k $PGSOCK_DIR -c listen_addresses=127.0.0.1 -c fsync=off -c full_page_writes=off" \
    start >/dev/null 2>&1 || {
      echo "postgres failed to start on port $PGPORT; server log follows:" >&2
      cat "$SCRATCH/pg$attempt/postgres.log" >&2 2>/dev/null || echo "(no server log)" >&2
      return 1
    }

  local i=1
  while [ "$i" -le 30 ]; do
    if "$PGBIN/pg_isready" -h 127.0.0.1 -p "$PGPORT" -U postgres >/dev/null 2>&1; then
      # Over the UNIX SOCKET (--auth-local=trust), not 127.0.0.1.
      #
      # The TCP path is scram-sha-256 by deliberate choice above, so createdb
      # there prompts for a password on a terminal that has none -- and it does
      # not fail, it BLOCKS, reprinting "Password:" until the step's walltime
      # runs out. `</dev/null` on every client call keeps a future auth change
      # from turning into a 25-minute hang instead of an error.
      "$PGBIN/createdb" -h "$PGSOCK_DIR" -p "$PGPORT" -U postgres "$POSTGRES_DB" </dev/null || return 1
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  echo "postgres did not become ready on port $PGPORT; server log follows:" >&2
  cat "$SCRATCH/pg$attempt/postgres.log" >&2 2>/dev/null || echo "(no server log)" >&2
  return 1
}

main() {
  echo "commit:  ${CI_COMMIT_SHA:-<local>}"
  step() { echo; echo "=== $* ==="; }

  step "toolchain"
  if ! command -v go >/dev/null 2>&1; then
    for d in /usr/local/go/bin /opt/go/bin "${GOROOT:-}/bin" /usr/lib/golang/bin; do
      if [ -n "$d" ] && [ -x "$d/go" ]; then PATH="$d:$PATH"; export PATH; break; fi
    done
  fi
  if ! command -v go >/dev/null 2>&1; then
    echo "FATAL: no Go toolchain on this agent." >&2
    echo "Looked on PATH and in: /usr/local/go/bin /opt/go/bin \$GOROOT/bin /usr/lib/golang/bin" >&2
    exit 1
  fi
  go version
  if ! command -v python3 >/dev/null 2>&1; then
    echo "FATAL: python3 is not on this agent's PATH (needed to allocate a free port)." >&2
    exit 1
  fi
  PGBIN="$(resolve_pgbin)"
  "$PGBIN/postgres" --version

  step "throwaway postgres"
  SCRATCH="$(mktemp -d /tmp/autospec-db-ci-XXXXXX)"
  POSTGRES_DB=autospec
  # Per-run, never persisted, never printed. There is no Woodpecker secret
  # behind this build precisely because nothing outside the step needs it.
  PGPASSWORD_GEN="$(python3 -c 'import secrets;print(secrets.token_hex(16))')"
  PGPORT="$(alloc_port)"
  if ! start_pg 1; then
    echo "retrying postgres startup on a fresh port" >&2
    "$PGBIN/pg_ctl" -D "$PGDATA_DIR" -m immediate stop >/dev/null 2>&1 || true
    PGPORT="$(alloc_port)"
    start_pg 2 || exit 1
  fi
  # host:port only. The DSN carries the generated password and is never echoed.
  echo "postgres ready on 127.0.0.1:$PGPORT (database $POSTGRES_DB)"

  AUTOSPEC_DB_TEST_DSN="postgresql://postgres:${PGPASSWORD_GEN}@127.0.0.1:${PGPORT}/${POSTGRES_DB}?sslmode=disable"
  export AUTOSPEC_DB_TEST_DSN

  step "go vet"
  go vet ./...

  step "go build"
  go build ./...

  step "go test -race"
  # -v and a log file rather than a pipe: the guard below has to read the
  # per-test PASS/SKIP lines, which only -v emits.
  set +e
  go test -race -v ./... > "$SCRATCH/gotest.log" 2>&1
  gotest_rc=$?
  set -e
  cat "$SCRATCH/gotest.log"
  [ "$gotest_rc" -eq 0 ] || exit "$gotest_rc"

  step "anti-vacuous-green guard"
  # cmd/autospec-db/integration_test.go calls t.Skip when AUTOSPEC_DB_TEST_DSN
  # is empty, so every way this script can fail to hand the DSN to `go test`
  # -- a typo, a lost export, a postgres that started and then died -- produces
  # a PASSING suite that touched no database at all. Assert the integration
  # tests really ran.
  local expected
  expected=$(grep -cE '^func TestIntegration' cmd/autospec-db/integration_test.go || true)
  # Derived, not hardcoded, so a seventh integration test raises the floor with
  # it. Floored at the six that existed when this gate was written, so gutting
  # the file cannot also lower the bar it is checked against.
  if [ "$expected" -lt 6 ]; then
    echo "FATAL: found only $expected TestIntegration* definitions, expected at least 6." >&2
    echo "Either the discovery grep is wrong or integration coverage was removed." >&2
    exit 1
  fi
  skipped=$(grep -cE '^--- SKIP: TestIntegration' "$SCRATCH/gotest.log" || true)
  passed=$(grep -cE '^--- PASS: TestIntegration' "$SCRATCH/gotest.log" || true)
  echo "integration tests: defined=$expected passed=$passed skipped=$skipped"
  if [ "$skipped" -ne 0 ]; then
    echo "FAIL: $skipped TestIntegration* were SKIPPED -- AUTOSPEC_DB_TEST_DSN did not reach go test" >&2
    exit 1
  fi
  if [ "$passed" -lt "$expected" ]; then
    echo "FAIL: only $passed TestIntegration* passed, expected $expected" >&2
    exit 1
  fi

  echo
  echo "GATES PASSED"
}

# The run goes through a PIPELINE, not `exec > >(tee ...)`.
#
# Process substitution does not make the shell wait for the reader: a script
# that fails in seconds exits before tee drains its pipe, and this agent then
# records nothing at all -- losing exactly the runs that need explaining. A
# pipeline is waited on. PIPESTATUS carries the body's status past tee, which
# would otherwise mask it with its own.
if [ -n "$journal" ]; then
  echo "journal: $journal"
  main 2>&1 | tee -a "$journal"
  exit "${PIPESTATUS[0]}"
fi
main
