#!/bin/sh
# every — installer for Linux and macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/serhiileniv/every/main/install.sh | sh
#   ./install.sh [--prefix DIR] [--version X.Y.Z]
#   ./install.sh --uninstall [--prefix DIR] [--force]
#
# Downloads one static binary for this platform, verifies it against the
# release checksums, and drops it at PREFIX/bin/every with the man page and
# shell completions where the shells look.
#
# There is no runtime to find, no version of anything to check, and no tree to
# copy. Half of the previous installer existed to locate a Ruby >= 2.6 and
# stage a library beside it; that is what the rewrite deleted.
#
# The installed path is what the scheduler records in every unit it writes, so
# a later re-install (upgrade) lands on the same path and reaches
# already-scheduled tasks without touching them.
#
# POSIX sh on purpose: no bashisms, and nothing required beyond tar and
# curl-or-wget.

set -eu

REPO="serhiileniv/every"

PREFIX=""
VERSION=""
ACTION="install"
FORCE=0
TMP=""
LOCAL_BIN=""

# ---- output --------------------------------------------------------------
# Color is a hint only: every line reads with the codes stripped. Honors
# NO_COLOR, and stays quiet when stdout is not a terminal.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  C_G=$(printf '\033[32m'); C_Y=$(printf '\033[33m')
  C_R=$(printf '\033[31m'); C_D=$(printf '\033[2m'); C_0=$(printf '\033[0m')
else
  C_G=""; C_Y=""; C_R=""; C_D=""; C_0=""
fi

say()  { printf '%s\n' "$*"; }
step() { printf '%s\n' "${C_D}$*${C_0}"; }
ok()   { printf '%s\n' "${C_G}✓${C_0} $*"; }
warn() { printf '%s\n' "${C_Y}!${C_0} $*" >&2; }
die()  { printf '%s\n' "${C_R}✗${C_0} $*" >&2; exit 1; }
hint() { printf '%s\n' "  $*" >&2; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<EOF
every installer

  --prefix DIR     install root (default: ~/.local, or /usr/local as root)
  --version X.Y.Z  install a specific release (default: the latest)
  --uninstall      remove an install
  --force          uninstall even while tasks are still scheduled
  -h, --help       this
EOF
  exit 0
}

# ---- arguments -----------------------------------------------------------
# Both --flag value and --flag=value, because people type both.
while [ $# -gt 0 ]; do
  case "$1" in
    --prefix)    PREFIX="${2:?--prefix needs a directory}"; shift 2 ;;
    --prefix=*)  PREFIX="${1#*=}"; shift ;;
    --version)   VERSION="${2:?--version needs a release}"; shift 2 ;;
    --version=*) VERSION="${1#*=}"; shift ;;
    --uninstall) ACTION="uninstall"; shift ;;
    --force)     FORCE=1; shift ;;
    -h|--help)   usage ;;
    *)           die "unknown option: $1 (try --help)" ;;
  esac
done

if [ -z "$PREFIX" ]; then
  if [ "$(id -u)" = "0" ]; then PREFIX="/usr/local"; else PREFIX="$HOME/.local"; fi
fi
# A quoted --prefix '~/opt' reaches us with the tilde unexpanded, because the
# shell only expands it unquoted. Expand it ourselves rather than creating a
# directory literally named "~".
# shellcheck disable=SC2088  # the tilde here is a case PATTERN, not a path
case "$PREFIX" in
  "~") PREFIX="$HOME" ;;
  "~/"*) PREFIX="$HOME/${PREFIX#\~/}" ;;
esac

BINDIR="$PREFIX/bin"
BINARY="$BINDIR/every"
MANDIR="$PREFIX/share/man/man1"
MANIFEST="$PREFIX/share/every/.install-manifest"

# A prefix inside $HOME is a single-user install: completions belong in the
# per-user dirs the shells read, not in a prefix nobody has configured.
case "$PREFIX" in
  "$HOME"|"$HOME"/*) USER_INSTALL=1 ;;
  *)                 USER_INSTALL=0 ;;
esac

XDG_DATA="${XDG_DATA_HOME:-$HOME/.local/share}"
XDG_CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}"
UNIT_DIR="$XDG_CONFIG/systemd/user"
AGENT_DIR="$HOME/Library/LaunchAgents"

if [ "$USER_INSTALL" = "1" ]; then
  BASH_COMP="$XDG_DATA/bash-completion/completions/every"
  FISH_COMP="$XDG_CONFIG/fish/completions/every.fish"
else
  BASH_COMP="$PREFIX/share/bash-completion/completions/every"
  FISH_COMP="$PREFIX/share/fish/vendor_completions.d/every.fish"
fi
ZSH_COMP="$PREFIX/share/zsh/site-functions/_every"

# ---- platform ------------------------------------------------------------
# Matches the archive names pinned in .goreleaser.yaml. Changing either
# without the other breaks every download.
detect_platform() {
  os=$(uname -s)
  arch=$(uname -m)
  case "$os" in
    Darwin) OS="darwin" ;;
    Linux)  OS="linux" ;;
    MINGW*|MSYS*|CYGWIN*)
      die "native Windows uses install.ps1 — run: powershell -ExecutionPolicy Bypass -File install.ps1" ;;
    *) die "$os isn't a supported platform — every schedules through launchd or systemd" ;;
  esac
  case "$arch" in
    x86_64|amd64)  ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) die "unsupported architecture: $arch (every ships amd64 and arm64)" ;;
  esac
  # macOS releases ship one universal binary rather than a slice per arch.
  if [ "$OS" = "darwin" ]; then ARCH="all"; fi
}

# ---- uninstall -----------------------------------------------------------

uninstall() {
  [ -f "$BINARY" ] ||
    die "no every install found at $PREFIX — pass --prefix DIR if you installed elsewhere"

  # Removing the launcher out from under live tasks is exactly the silent
  # breakage every exists to kill: the scheduler keeps firing and every run
  # dies. Checked on both platforms -- the previous installer looked only for
  # systemd timers, which left macOS unguarded.
  scheduled=""
  for t in "$UNIT_DIR"/every-*.timer; do
    [ -e "$t" ] || continue
    n=${t##*/every-}; scheduled="$scheduled ${n%.timer}"
  done
  for p in "$AGENT_DIR"/com.every.*.plist; do
    [ -e "$p" ] || continue
    n=${p##*/com.every.}; scheduled="$scheduled ${n%.plist}"
  done
  if [ -n "$scheduled" ] && [ "$FORCE" != "1" ]; then
    say "Still scheduled:$scheduled"
    say ""
    say "Remove them first, so nothing is left pointing at a deleted every:"
    for n in $scheduled; do say "  every rm $n"; done
    say ""
    die "nothing removed (use --force to uninstall anyway)"
  fi
  [ -n "$scheduled" ] && warn "leaving live tasks behind:$scheduled"

  if [ -f "$MANIFEST" ]; then
    while IFS= read -r p; do
      [ -n "$p" ] || continue
      [ -e "$p" ] || [ -L "$p" ] || continue
      rm -f "$p" && step "removed $p"
    done < "$MANIFEST"
    rm -f "$MANIFEST"
  else
    # Pre-manifest install (or a hand-edited one): fall back to the defaults.
    for p in "$BINARY" "$MANDIR/every.1" "$BASH_COMP" "$ZSH_COMP" "$FISH_COMP"; do
      [ -e "$p" ] || [ -L "$p" ] || continue
      rm -f "$p" && step "removed $p"
    done
  fi

  # A 0.3.x install kept its tree here; remove it so an upgrade-then-uninstall
  # does not strand a Ruby library nobody can account for.
  if [ -d "$PREFIX/lib/every" ]; then
    rm -rf "$PREFIX/lib/every"
    step "removed $PREFIX/lib/every (from a pre-0.4 install)"
  fi

  ok "every uninstalled from $PREFIX"
  say ""
  say "Tasks and logs were kept. To remove those too:"
  say "  rm -rf ${EVERY_HOME:-$XDG_DATA/every}"
}

# ---- download ------------------------------------------------------------

cleanup() { if [ -n "$TMP" ]; then rm -rf "$TMP"; fi; }

fetch() {
  if   have curl; then curl -fsSL "$1"
  elif have wget; then wget -qO- "$1"
  else die "need curl or wget to download every"
  fi
}

script_dir() {
  case "$0" in
    */*) (cd "$(dirname "$0")" && pwd) ;;
    *)   pwd ;;
  esac
}

# A checkout with a Go toolchain builds what is in front of it. That is what
# makes `sh install.sh` in a working tree test the code being changed, rather
# than silently installing the last release over it.
try_local_build() {
  here=$(script_dir)
  [ -z "$VERSION" ] || return 1
  [ -f "$0" ] || return 1
  [ -f "$here/go.mod" ] && [ -d "$here/cmd/every" ] || return 1
  have go || return 1

  TMP=$(mktemp -d 2>/dev/null || mktemp -d -t every) || die "couldn't create a temp dir"
  trap cleanup EXIT HUP INT TERM
  step "building from $here"
  ( cd "$here" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$TMP/every" ./cmd/every ) ||
    die "go build failed"
  LOCAL_BIN="$TMP/every"
  SRCDIR="$here"
  return 0
}

resolve_version() {
  [ -n "$VERSION" ] && { VERSION="${VERSION#v}"; return 0; }
  VERSION=$(fetch "https://api.github.com/repos/$REPO/releases/latest" |
              sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
              head -1) || true
  [ -n "$VERSION" ] ||
    die "couldn't resolve the latest release (rate-limited or offline) — retry with --version X.Y.Z"
  VERSION="${VERSION#v}"
}

# Verify the archive against the release's checksums.txt. A truncated or
# tampered download must fail loudly rather than install a broken binary.
verify_checksum() {
  archive="$1"; name="$2"
  sums="$TMP/checksums.txt"
  if ! fetch "https://github.com/$REPO/releases/download/v$VERSION/checksums.txt" > "$sums" 2>/dev/null; then
    warn "no checksums.txt for v$VERSION — skipping verification"
    return 0
  fi
  expected=$(sed -n "s/^\([0-9a-f]\{64\}\)  *$name\$/\1/p" "$sums" | head -1)
  if [ -z "$expected" ]; then
    warn "$name is not listed in checksums.txt — skipping verification"
    return 0
  fi
  if   have sha256sum; then actual=$(sha256sum "$archive" | cut -d' ' -f1)
  elif have shasum;    then actual=$(shasum -a 256 "$archive" | cut -d' ' -f1)
  else warn "no sha256sum or shasum — skipping verification"; return 0
  fi
  [ "$actual" = "$expected" ] ||
    die "checksum mismatch for $name
  expected $expected
  got      $actual
This is either a corrupted download or a tampered release. Nothing was installed."
  step "checksum verified"
}

download() {
  have tar || die "tar not found — needed to unpack the release"
  TMP=$(mktemp -d 2>/dev/null || mktemp -d -t every) || die "couldn't create a temp dir"
  trap cleanup EXIT HUP INT TERM

  resolve_version
  name="every_${VERSION}_${OS}_${ARCH}.tar.gz"
  url="https://github.com/$REPO/releases/download/v$VERSION/$name"

  step "downloading v$VERSION ($OS/$ARCH)"
  fetch "$url" > "$TMP/$name" || die "download failed: $url
If v$VERSION predates 0.4.0 it has no binaries — those releases installed from source."
  verify_checksum "$TMP/$name" "$name"

  mkdir -p "$TMP/x"
  tar -xzf "$TMP/$name" -C "$TMP/x" || die "couldn't unpack $name"
  [ -f "$TMP/x/every" ] || die "unexpected archive layout for $name"
  chmod 755 "$TMP/x/every"
  LOCAL_BIN="$TMP/x/every"
  SRCDIR="$TMP/x"
}

# ---- install -------------------------------------------------------------

put() {  # put <src-file> <dest-file>, recording it for uninstall
  [ -f "$1" ] || return 0
  mkdir -p "$(dirname "$2")"
  cp "$1" "$2"
  chmod 644 "$2"
  printf '%s\n' "$2" >> "$MANIFEST"
  step "installed $2"
}

install_all() {
  # Fail before anything is written, not halfway through a copy.
  no_write="can't write to $PREFIX — re-run with sudo, or install for yourself: --prefix ~/.local"
  mkdir -p "$BINDIR" 2>/dev/null || die "$no_write"
  mkdir -p "$(dirname "$MANIFEST")" 2>/dev/null || die "$no_write"

  # Write beside the target and rename over it. Replacing a running binary
  # in place is what breaks a task that fires mid-upgrade; a rename is atomic,
  # so a scheduled run sees either the old file or the new one.
  tmpbin="$BINDIR/.every.new.$$"
  cp "$LOCAL_BIN" "$tmpbin" || die "$no_write"
  chmod 755 "$tmpbin"
  mv "$tmpbin" "$BINARY" || die "$no_write"
  printf '%s\n' "$BINARY" > "$MANIFEST"   # first entry: truncates any old list
  step "installed $BINARY"

  put "$SRCDIR/man/every.1" "$MANDIR/every.1"
  put "$SRCDIR/completions/every.bash" "$BASH_COMP"
  put "$SRCDIR/completions/_every" "$ZSH_COMP"
  put "$SRCDIR/completions/every.fish" "$FISH_COMP"

  # A 0.3.x install left a Ruby tree and a symlink here. The binary has just
  # replaced the symlink; the tree is now unreferenced.
  if [ -d "$PREFIX/lib/every" ]; then
    rm -rf "$PREFIX/lib/every"
    step "removed $PREFIX/lib/every (no longer needed)"
  fi
}

# ---- what's left for the user --------------------------------------------

report() {
  # Running what we just installed is the smoke test: it proves the binary
  # landed, is executable, and runs on this machine.
  if ! version=$("$BINARY" version 2>&1); then
    die "installed, but '$BINARY version' failed:
$version"
  fi
  version=$(printf '%s\n' "$version" | head -1)
  [ -n "$version" ] || die "installed, but '$BINARY version' printed nothing"

  say ""
  ok "$version → $PREFIX"
  say ""

  case ":$PATH:" in
    *":$BINDIR:"*) ;;
    *)
      warn "$BINDIR isn't on your PATH. Add it:"
      hint "echo 'export PATH=\"$BINDIR:\$PATH\"' >> ~/.profile" ;;
  esac

  if [ "$USER_INSTALL" = "1" ] && [ -f "$ZSH_COMP" ]; then
    case "${SHELL:-}" in
      *zsh)
        warn "zsh completions need that dir on your fpath — add before compinit:"
        hint "fpath=($PREFIX/share/zsh/site-functions \$fpath)" ;;
    esac
  fi

  # Linux only: launchd is the macOS backend, so systemctl is not expected
  # there and telling a Mac user "systemd not found" is wrong and alarming.
  if [ "$(uname -s)" = "Linux" ]; then
    if ! have systemctl; then
      warn "systemd not found — every schedules through systemd user timers."
    elif ! systemctl --user show-environment >/dev/null 2>&1; then
      warn "no user systemd session here (bare container, or plain SSH?) —"
      hint "'every doctor' will say more once you're in a real login session."
    elif ! loginctl show-user "${USER:-$(id -un)}" -p Linger 2>/dev/null | grep -q "Linger=yes"; then
      warn "timers stop at logout unless you enable lingering:"
      hint "  loginctl enable-linger ${USER:-$(id -un)}"
    fi
  fi

  say ""
  say "  every day 9am -- 'echo it ran'   schedule something"
  say "  every list                       did it run?"
  say "  every doctor                     why isn't it running?"
}

# ---- go ------------------------------------------------------------------

if [ "$ACTION" = "uninstall" ]; then
  uninstall
  exit 0
fi

detect_platform
if [ "$(uname -s)" = "Darwin" ]; then
  warn "on macOS the Homebrew tap is the better path (it upgrades in place):"
  warn "  brew tap serhiileniv/tap && brew install every"
fi

if ! try_local_build; then
  download
fi
install_all
report
