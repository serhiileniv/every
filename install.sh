#!/bin/sh
# every — installer for Linux (and any Unix where Homebrew isn't the answer).
#
#   curl -fsSL https://raw.githubusercontent.com/serhiileniv/every/main/install.sh | sh
#   ./install.sh [--prefix DIR] [--version vX.Y.Z | --ref BRANCH]
#   ./install.sh --uninstall [--prefix DIR] [--force]
#
# Installs a self-contained tree at PREFIX/lib/every, links PREFIX/bin/every at
# it, and drops the man page and shell completions where the shells look. The
# symlink is what the scheduler records in each systemd unit, so a later
# re-install (upgrade) reaches already-scheduled tasks without touching them.
#
# POSIX sh on purpose: no bashisms, no dependencies beyond a Ruby already on
# the box (2.6+ — the same floor as macOS system Ruby), plus tar and
# curl-or-wget when downloading.

set -eu

REPO="serhiileniv/every"
RUBY_MIN="2.6"

PREFIX=""
REF=""
ACTION="install"
FORCE=0
SRC=""
TMP=""

# ---- output --------------------------------------------------------------
# Color is a hint only: every line reads with the codes stripped. Honors
# NO_COLOR and TERM=dumb, like the CLI itself.
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
# A warning's follow-up line belongs on the same stream as its warning —
# otherwise `2>/dev/null` leaves an instruction with nothing to explain it.
hint() { printf '%s\n' "  $*" >&2; }
die()  { printf '%s\n' "${C_R}every:${C_0} $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<'TXT'
every installer

  install.sh [--prefix DIR] [--version TAG | --ref BRANCH]
  install.sh --uninstall [--prefix DIR] [--force]

  --prefix DIR    where to install (default: ~/.local, or /usr/local as root)
  --version TAG   install a specific release, e.g. --version v0.3.0
  --ref BRANCH    install a branch, e.g. --ref main
  --uninstall     remove the install (tasks and logs are kept)
  --force         uninstall even while tasks are still scheduled
  --help          this
TXT
}

# ---- arguments -----------------------------------------------------------
# Accept both `--flag value` and `--flag=value`, as the CLI does.
while [ $# -gt 0 ]; do
  case "$1" in
    --prefix)    [ $# -ge 2 ] || die "--prefix needs a directory"; PREFIX=$2; shift 2 ;;
    --prefix=*)  PREFIX=${1#--prefix=}; shift ;;
    --version)   [ $# -ge 2 ] || die "--version needs a tag"; REF=$2; shift 2 ;;
    --version=*) REF=${1#--version=}; shift ;;
    --ref)       [ $# -ge 2 ] || die "--ref needs a branch"; REF=$2; shift 2 ;;
    --ref=*)     REF=${1#--ref=}; shift ;;
    --uninstall) ACTION="uninstall"; shift ;;
    --force)     FORCE=1; shift ;;
    -h|--help)   usage; exit 0 ;;
    *)           die "unknown option: $1 (see --help)" ;;
  esac
done

if [ -z "$PREFIX" ]; then
  if [ "$(id -u)" = "0" ]; then PREFIX="/usr/local"; else PREFIX="$HOME/.local"; fi
fi
# Trailing slashes would double up in every path we print.
while :; do
  case "$PREFIX" in */) PREFIX=${PREFIX%/} ;; *) break ;; esac
done
case "$PREFIX" in
  /*) ;;
  ~*) PREFIX="$HOME${PREFIX#\~}" ;;
  *)  PREFIX="$(pwd)/$PREFIX" ;;
esac

LIBDIR="$PREFIX/lib/every"
BINLINK="$PREFIX/bin/every"
MANDIR="$PREFIX/share/man/man1"
MANIFEST="$LIBDIR/.install-manifest"

# A prefix inside $HOME is a single-user install: completions belong in the
# per-user dirs the shells read, not in the prefix nobody has configured.
case "$PREFIX" in
  "$HOME"|"$HOME"/*) USER_INSTALL=1 ;;
  *)                 USER_INSTALL=0 ;;
esac

XDG_DATA="${XDG_DATA_HOME:-$HOME/.local/share}"
XDG_CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}"
UNIT_DIR="$XDG_CONFIG/systemd/user"

if [ "$USER_INSTALL" = "1" ]; then
  BASH_COMP="$XDG_DATA/bash-completion/completions/every"
  FISH_COMP="$XDG_CONFIG/fish/completions/every.fish"
else
  BASH_COMP="$PREFIX/share/bash-completion/completions/every"
  FISH_COMP="$PREFIX/share/fish/vendor_completions.d/every.fish"
fi
ZSH_COMP="$PREFIX/share/zsh/site-functions/_every"

# ---- uninstall -----------------------------------------------------------

uninstall() {
  [ -f "$LIBDIR/bin/every" ] ||
    die "no every install found at $PREFIX — pass --prefix DIR if you installed elsewhere"

  # Removing the launcher out from under live timers is exactly the silent
  # breakage every exists to kill: units keep firing and every run dies.
  scheduled=""
  for t in "$UNIT_DIR"/every-*.timer; do
    [ -e "$t" ] || continue
    n=${t##*/every-}; scheduled="$scheduled ${n%.timer}"
  done
  if [ -n "$scheduled" ] && [ "$FORCE" != "1" ]; then
    say "Still scheduled:$scheduled"
    say ""
    say "Remove them first, so no timer is left pointing at a deleted every:"
    for n in $scheduled; do say "  every rm $n"; done
    say ""
    die "nothing removed (use --force to uninstall anyway)"
  fi
  [ -n "$scheduled" ] && warn "leaving live timers behind:$scheduled"

  if [ -f "$MANIFEST" ]; then
    while IFS= read -r p; do
      [ -n "$p" ] || continue
      [ -e "$p" ] || [ -L "$p" ] || continue
      rm -f "$p" && step "removed $p"
    done < "$MANIFEST"
  else
    # Pre-manifest install (or a hand-edited one): fall back to the defaults.
    for p in "$BINLINK" "$MANDIR/every.1" "$BASH_COMP" "$ZSH_COMP" "$FISH_COMP"; do
      [ -e "$p" ] || [ -L "$p" ] || continue
      rm -f "$p" && step "removed $p"
    done
  fi

  rm -rf "$LIBDIR"
  step "removed $LIBDIR"
  ok "every uninstalled from $PREFIX"
  say ""
  say "Tasks and logs were kept. To remove those too:"
  say "  rm -rf ${EVERY_HOME:-$XDG_DATA/every}"
}

# ---- source: local checkout, or download ---------------------------------

script_dir() {
  case "$0" in
    */*) (cd "$(dirname "$0")" && pwd) ;;
    *)   pwd ;;
  esac
}

cleanup() {
  if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
}

download() {
  have tar || die "tar not found — needed to unpack the release"
  TMP=$(mktemp -d 2>/dev/null || mktemp -d -t every) ||
    die "couldn't create a temp dir"
  trap cleanup EXIT HUP INT TERM

  case "$REF" in
    "")
      REF=$(fetch "https://api.github.com/repos/$REPO/releases/latest" |
              sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
              head -1) || true
      [ -n "$REF" ] ||
        die "couldn't resolve the latest release (rate-limited or offline) — retry with --version vX.Y.Z"
      url="https://github.com/$REPO/archive/refs/tags/$REF.tar.gz" ;;
    v[0-9]*)
      url="https://github.com/$REPO/archive/refs/tags/$REF.tar.gz" ;;
    [0-9]*.[0-9]*)
      # `--version 0.3.0` means the v0.3.0 tag, not a branch called 0.3.0.
      REF="v$REF"
      url="https://github.com/$REPO/archive/refs/tags/$REF.tar.gz" ;;
    *)
      url="https://github.com/$REPO/archive/refs/heads/$REF.tar.gz" ;;
  esac

  step "downloading $REF"
  fetch "$url" > "$TMP/every.tar.gz" || die "download failed: $url"
  tar -xzf "$TMP/every.tar.gz" -C "$TMP" || die "couldn't unpack $REF"

  # GitHub tarballs unpack to every-<ref>/.
  for d in "$TMP"/every-*; do
    [ -d "$d" ] || continue
    SRC=$d
  done
  if [ -z "$SRC" ] || [ ! -f "$SRC/lib/every.rb" ]; then
    die "unexpected tarball layout for $REF"
  fi
}

fetch() {
  if   have curl; then curl -fsSL "$1"
  elif have wget; then wget -qO- "$1"
  else die "need curl or wget to download every"
  fi
}

resolve_source() {
  here=$(script_dir)
  # An explicit --version/--ref always wins: asking for v0.1.3 from inside a
  # checkout must install v0.1.3, not whatever is sitting next to the script.
  # Otherwise `[ -f "$0" ]` tells a `./install.sh` run apart from `curl | sh`,
  # where $0 is the shell's own name and there is no checkout to install from.
  if [ -z "$REF" ] && [ -f "$0" ] && [ -f "$here/lib/every.rb" ] && [ -f "$here/bin/every" ]; then
    SRC=$here
    step "installing from $SRC"
  else
    download
  fi
}

# ---- preflight -----------------------------------------------------------

preflight() {
  have ruby || die "ruby not found — every runs on Ruby $RUBY_MIN+, and installs nothing else.
  Debian/Ubuntu:  sudo apt install ruby
  Fedora/RHEL:    sudo dnf install ruby
  Arch:           sudo pacman -S ruby
  openSUSE:       sudo zypper install ruby
  Alpine:         sudo apk add ruby"

  ruby -e 'exit((RUBY_VERSION.split(".").map { |p| p.to_i } <=> [2, 6]) >= 0 ? 0 : 1)' ||
    die "ruby $(ruby -e 'print RUBY_VERSION') is too old — every needs $RUBY_MIN+"

  case "$(uname -s)" in
    Darwin)
      warn "on macOS the Homebrew tap is the better path (it upgrades in place):"
      warn "  brew tap serhiileniv/tap && brew install every" ;;
    Linux) ;;
    MINGW*|MSYS*|CYGWIN*)
      die "native Windows uses install.ps1 — run: powershell -ExecutionPolicy Bypass -File install.ps1" ;;
    *) warn "$(uname -s) isn't a supported platform — every schedules through launchd or systemd" ;;
  esac

  # Fail before anything is written, not halfway through a cp.
  no_write="can't write to $PREFIX — re-run with sudo, or install for yourself: --prefix ~/.local"
  mkdir -p "$PREFIX/bin" "$PREFIX/lib" 2>/dev/null || die "$no_write"
  if [ ! -w "$PREFIX/bin" ] || [ ! -w "$PREFIX/lib" ]; then
    die "$no_write"
  fi
}

# ---- install -------------------------------------------------------------

put() {  # put <src-file> <dest-file>, recording it for uninstall
  mkdir -p "$(dirname "$2")"
  cp "$1" "$2"
  chmod 644 "$2"
  printf '%s\n' "$2" >> "$MANIFEST"
  step "installed $2"
}

install_tree() {
  # Stage beside the target and swap with renames, so an interrupted install
  # can't leave a half-copied tree where a working one used to be.
  staging="$PREFIX/lib/every.new.$$"
  previous="$PREFIX/lib/every.old.$$"
  rm -rf "$staging" "$previous"
  mkdir -p "$staging"
  cp -R "$SRC/bin" "$staging/bin"
  cp -R "$SRC/lib" "$staging/lib"
  chmod 755 "$staging/bin/every"

  if [ -d "$LIBDIR" ]; then mv "$LIBDIR" "$previous"; fi
  mv "$staging" "$LIBDIR"
  rm -rf "$previous"
  step "installed $LIBDIR"

  # bin/every resolves its own realpath to find lib/, so the symlink works —
  # and staying a symlink is what lets an upgrade land without rewriting units.
  mkdir -p "$PREFIX/bin"
  rm -f "$BINLINK"
  ln -s "$LIBDIR/bin/every" "$BINLINK"
  printf '%s\n' "$BINLINK" > "$MANIFEST"   # first entry: truncates any old list
  step "linked $BINLINK"

  [ -f "$SRC/man/every.1" ] && put "$SRC/man/every.1" "$MANDIR/every.1"
  [ -f "$SRC/completions/every.bash" ] && put "$SRC/completions/every.bash" "$BASH_COMP"
  [ -f "$SRC/completions/_every" ] && put "$SRC/completions/_every" "$ZSH_COMP"
  [ -f "$SRC/completions/every.fish" ] && put "$SRC/completions/every.fish" "$FISH_COMP"
  return 0
}

# What's left for the user to do — printed only when it actually applies.
report() {
  # Running the thing we just installed is the smoke test: it proves the
  # symlink resolves, lib/ was found, and this Ruby can load it.
  if ! version=$("$BINLINK" version 2>&1); then
    die "installed, but '$BINLINK version' failed:
$version"
  fi
  version=$(printf '%s\n' "$version" | head -1)
  [ -n "$version" ] || die "installed, but '$BINLINK version' printed nothing"

  say ""
  ok "$version → $PREFIX"
  say ""

  case ":$PATH:" in
    *":$PREFIX/bin:"*) ;;
    *)
      warn "$PREFIX/bin isn't on your PATH. Add it:"
      hint "  echo 'export PATH=\"$PREFIX/bin:\$PATH\"' >> ~/.profile" ;;
  esac

  if [ "$USER_INSTALL" = "1" ] && [ -f "$ZSH_COMP" ]; then
    case "${SHELL:-}" in
      *zsh)
        warn "zsh completions need that dir on your fpath — add before compinit:"
        hint "  fpath=($PREFIX/share/zsh/site-functions \$fpath)" ;;
    esac
  fi

  if ! have systemctl; then
    warn "systemd not found — every schedules through systemd user timers."
  elif ! systemctl --user show-environment >/dev/null 2>&1; then
    warn "no user systemd session here (bare container, or plain SSH?) —"
    hint "'every doctor' will say more once you're in a real login session."
  elif ! loginctl show-user "${USER:-$(id -un)}" -p Linger 2>/dev/null | grep -q "Linger=yes"; then
    warn "timers stop at logout unless you enable lingering:"
    hint "  loginctl enable-linger ${USER:-$(id -un)}"
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

preflight
resolve_source
install_tree
report
