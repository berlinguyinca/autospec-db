#!/usr/bin/env bash
# autospec-db one-line installer — thin bootstrap.
#
#   curl -fsSL https://raw.githubusercontent.com/berlinguyinca/autospec-db/main/install.sh | bash
#
# Downloads the prebuilt static `autospec-db` binary for this OS/arch to
# ~/.autospec/bin/autospec-db and hands off to `autospec-db install`, which
# does the real work (config template on first run; create-db-if-missing,
# idempotent migrations, role convergence, and ~/.autospec/db.env after).
#
# Env overrides:
#   AUTOSPEC_DB_BINARY=<path>   use this binary, skip the download entirely
#   AUTOSPEC_DB_VERSION=vX.Y.Z  pin a release (default: latest)
#
# bash 3.2 compatible.
set -eu

REPO="berlinguyinca/autospec-db"
BINDIR="$HOME/.autospec/bin"
BIN="$BINDIR/autospec-db"

say() { printf 'autospec-db: %s\n' "$*"; }
die() { printf 'autospec-db: ERROR: %s\n' "$*" >&2; exit 1; }

# ── binary resolution ────────────────────────────────────────────────────────
# Case 1: explicit override (dev / CI).
if [ -n "${AUTOSPEC_DB_BINARY:-}" ]; then
    [ -x "$AUTOSPEC_DB_BINARY" ] || die "AUTOSPEC_DB_BINARY not executable: $AUTOSPEC_DB_BINARY"
    exec "$AUTOSPEC_DB_BINARY" install "$@"
fi

# Case 2: download the release asset for this platform.
os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
    Darwin) os="darwin" ;;
    Linux)  os="linux" ;;
    *)      die "unsupported OS: $os (build from source: go install github.com/$REPO/cmd/autospec-db@latest)" ;;
esac
case "$arch" in
    x86_64|amd64)  arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *)             die "unsupported arch: $arch" ;;
esac

version="${AUTOSPEC_DB_VERSION:-}"
if [ -z "$version" ]; then
    command -v curl >/dev/null 2>&1 || die "curl is required to discover the latest release"
    # jq-free: pull the tag from the latest-release API.
    version="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
        | grep '"tag_name"' | head -1 | sed 's/.*"tag_name"[^"]*"\([^"]*\)".*/\1/')"
    [ -n "$version" ] || die "no release found — check https://github.com/$REPO/releases (or set AUTOSPEC_DB_VERSION)"
fi

# goreleaser strips the leading v from {{ .Version }} in the archive name.
ver_nov="${version#v}"
asset="autospec-db_${ver_nov}_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/download/$version/$asset"

command -v curl >/dev/null 2>&1 || die "curl is required to download the release"
mkdir -p "$BINDIR"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "downloading $asset ($version)"
curl -fsSL "$url" -o "$tmp/asdb.tar.gz" \
    || die "could not download $url — check https://github.com/$REPO/releases"
tar -xzf "$tmp/asdb.tar.gz" -C "$tmp" \
    || die "could not extract $asset"

# The archive contains the binary named autospec-db.
if [ -f "$tmp/autospec-db" ]; then
    mv "$tmp/autospec-db" "$BIN"
else
    found="$(find "$tmp" -type f -name autospec-db | head -1)"
    [ -n "$found" ] || die "autospec-db binary not found in $asset"
    mv "$found" "$BIN"
fi
chmod +x "$BIN"
say "installed binary: $BIN"

exec "$BIN" install "$@"
