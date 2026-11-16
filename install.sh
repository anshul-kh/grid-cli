#!/bin/sh
# install.sh — fetch and install the latest (or pinned) grid-cli release.
#
#   curl -fsSL https://raw.githubusercontent.com/anshul-kh/grid-cli/main/install.sh | sh
#
# Env overrides:
#   GRID_INSTALL_VERSION   pin to a release tag (e.g. "v1.2.3"); default: latest
#   GRID_INSTALL_DIR       force the install directory; default: auto-detected below
#
# This script never prompts for or invokes root/sudo. If neither
# /usr/local/bin nor ~/.local/bin is writable, it fails with instructions
# instead of escalating privileges silently.
set -eu

REPO="anshul-kh/grid-cli"
PROJECT="grid-cli" # .goreleaser.yaml's project_name — the archive's name prefix
BINARY="grid"

log() { printf '%s\n' "$*" >&2; }
die() {
	log "error: $*"
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "'$1' is required but not found on PATH"
}

need curl
need tar

detect_os() {
	case "$(uname -s)" in
	Linux) echo linux ;;
	Darwin) echo darwin ;;
	*) die "unsupported OS: $(uname -s) (Windows users: run install.ps1 instead)" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo amd64 ;;
	arm64 | aarch64) echo arm64 ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "need 'sha256sum' or 'shasum' to verify the download"
	fi
}

# resolve_tag prints the release tag to install: GRID_INSTALL_VERSION if set,
# otherwise GitHub's "latest" redirect (avoids an unauthenticated API call
# that would otherwise count against GitHub's stricter anonymous rate limit).
resolve_tag() {
	if [ -n "${GRID_INSTALL_VERSION:-}" ]; then
		echo "$GRID_INSTALL_VERSION"
		return
	fi
	effective_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
		"https://github.com/${REPO}/releases/latest") ||
		die "could not resolve the latest release (network issue?)"
	case "$effective_url" in
	*/tag/*) tag=$(echo "$effective_url" | sed 's#.*/tag/##') ;;
	*) die "no releases found at https://github.com/${REPO}/releases (got $effective_url)" ;;
	esac
	[ -n "$tag" ] || die "could not parse a release tag from $effective_url"
	echo "$tag"
}

os=$(detect_os)
arch=$(detect_arch)
tag=$(resolve_tag)
version=${tag#v}
asset="${PROJECT}_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases/download/${tag}"

log "Installing grid-cli ${tag} (${os}/${arch})..."

workdir=$(mktemp -d) || die "could not create a temp directory"
trap 'rm -rf "$workdir"' EXIT

curl -fsSL -o "$workdir/$asset" "$base_url/$asset" ||
	die "could not download $base_url/$asset (does release $tag have a ${os}/${arch} build?)"
curl -fsSL -o "$workdir/checksums.txt" "$base_url/checksums.txt" ||
	die "could not download checksums.txt for $tag"

# Verify the checksum before anything in the archive is ever executed.
want=$(grep " ${asset}\$" "$workdir/checksums.txt" | awk '{print $1}')
[ -n "$want" ] || die "no checksum entry for $asset in checksums.txt"
got=$(sha256_of "$workdir/$asset")
[ "$want" = "$got" ] || die "checksum mismatch for $asset: expected $want, got $got"
log "Checksum verified."

tar -xzf "$workdir/$asset" -C "$workdir" "$BINARY" ||
	die "could not extract $BINARY from $asset"

# Pick an install directory without ever escalating privileges. Prefer the
# conventional system path if we can already write to it; otherwise fall
# back to a per-user directory we can create ourselves.
if [ -n "${GRID_INSTALL_DIR:-}" ]; then
	install_dir="$GRID_INSTALL_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
	install_dir="/usr/local/bin"
else
	install_dir="${HOME}/.local/bin"
fi

if [ ! -d "$install_dir" ]; then
	mkdir -p "$install_dir" 2>/dev/null || die \
		"cannot create $install_dir and /usr/local/bin is not writable.
Re-run with an install directory you own, e.g.:
  GRID_INSTALL_DIR=\$HOME/bin sh install.sh
This script never uses sudo — if you want $BINARY in /usr/local/bin, create
it yourself first (e.g. 'sudo mkdir -p /usr/local/bin && sudo chown \$(whoami) /usr/local/bin')."
fi
[ -w "$install_dir" ] || die \
	"$install_dir is not writable by $(whoami).
This script never uses sudo. Either fix permissions on $install_dir yourself,
or point it elsewhere: GRID_INSTALL_DIR=\$HOME/bin sh install.sh"

install -m 0755 "$workdir/$BINARY" "$install_dir/$BINARY" 2>/dev/null ||
	cp "$workdir/$BINARY" "$install_dir/$BINARY"
chmod 0755 "$install_dir/$BINARY"

log "Installed $install_dir/$BINARY"

case ":$PATH:" in
*":$install_dir:"*) ;;
*)
	log ""
	log "$install_dir is not on your PATH. Add it, e.g.:"
	log "  export PATH=\"$install_dir:\$PATH\""
	;;
esac

"$install_dir/$BINARY" --version >&2 || true
