#!/bin/sh
# install.sh — fetch and install the latest (or pinned) grid-cli release.
#
#   curl -fsSL https://raw.githubusercontent.com/anshul-kh/grid-cli/main/install.sh | sh
#
# Env overrides:
#   GRID_INSTALL_VERSION   pin to a release tag (e.g. "v1.2.3"); default: latest
#   GRID_INSTALL_DIR       force the install directory; default: auto-detected below
#   GRID_INSTALL_METHOD    auto | archive | source | rpm (default: auto)
#
#     auto     use the release archive; build from source when this release has
#              no archive for the host os/arch (or the download 404s)
#     archive  release archive only — fail if it is missing
#     source   clone the tag and `go build` it (needs git + go)
#     rpm      install the Fedora/RHEL RPM via dnf; requires root
#
# This script never prompts for or invokes root/sudo on the archive and source
# paths. If neither /usr/local/bin nor ~/.local/bin is writable, it fails with
# instructions instead of escalating privileges silently. The rpm method is an
# explicit opt-in and must be run as root, because dnf installs system-wide.
set -eu

REPO="anshul-kh/grid-cli"
PROJECT="grid-cli" # .goreleaser.yaml's project_name — the archive's name prefix
BINARY="grid"
MODULE="github.com/anshul-kh/grid-cli"
METHOD="${GRID_INSTALL_METHOD:-auto}"

log() { printf '%s\n' "$*" >&2; }
die() {
	log "error: $*"
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "'$1' is required but not found on PATH"
}

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

# Only used to print the right "install the build dependencies" hint. The
# archive path works identically on every distro.
detect_distro() {
	[ -r /etc/os-release ] || {
		echo unknown
		return
	}
	# shellcheck disable=SC1091
	. /etc/os-release
	case "${ID:-}${ID_LIKE:-}" in
	*fedora* | *rhel* | *centos* | *rocky* | *alma* | *suse* | *opensuse*) echo fedora ;;
	*debian* | *ubuntu*) echo debian ;;
	*arch*) echo arch ;;
	*) echo unknown ;;
	esac
}

install_deps_hint() {
	case "$(detect_distro)" in
	fedora) log "  Fedora / RHEL: sudo dnf install -y golang git" ;;
	debian) log "  Debian / Ubuntu: sudo apt-get install -y golang-go git" ;;
	arch) log "  Arch: sudo pacman -S --needed go git" ;;
	*) log "  install a Go 1.27+ toolchain and git from your package manager" ;;
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

verify_checksum() {
	file="$1"
	name="$2"
	checksums="$3"
	want=$(grep " ${name}\$" "$checksums" | awk '{print $1}')
	[ -n "$want" ] || die "no checksum entry for $name in checksums.txt"
	got=$(sha256_of "$file")
	[ "$want" = "$got" ] || die "checksum mismatch for $name: expected $want, got $got"
	log "Checksum verified."
}

fetch_release_file() {
	url="$1"
	out="$2"
	curl -fsSL -o "$out" "$url"
}

# install_into_dir installs an already-built/downloaded binary and prints the
# PATH hint. Shared by the archive and source paths so both behave the same.
install_into_dir() {
	src_binary="$1"

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

	install -m 0755 "$src_binary" "$install_dir/$BINARY" 2>/dev/null ||
		cp "$src_binary" "$install_dir/$BINARY"
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
}

build_from_source() {
	commit=$(git -C "$workdir/src" rev-parse --short HEAD)
	date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	log "Building $BINARY $tag from source (commit $commit)..."
	(
		cd "$workdir/src" &&
			CGO_ENABLED=0 go build -trimpath \
				-ldflags "-s -w \
					-X ${MODULE}/internal/buildinfo.Version=${version} \
					-X ${MODULE}/internal/buildinfo.Commit=${commit} \
					-X ${MODULE}/internal/buildinfo.Date=${date}" \
				-o "$workdir/$BINARY" .
	) || die "go build failed (Go 1.27+ is required; see the hints above)"
	install_into_dir "$workdir/$BINARY"
}

source_fallback() {
	reason="$1"
	log ""
	log "$reason"
	log "Falling back to a source build."

	if ! command -v git >/dev/null 2>&1 || ! command -v go >/dev/null 2>&1; then
		log "error: building from source needs git and a Go 1.27+ toolchain."
		install_deps_hint
		die "install those, or pin a release that ships an archive: GRID_INSTALL_VERSION=<tag>"
	fi

	git clone --depth 1 --branch "$tag" "https://github.com/${REPO}" "$workdir/src" >/dev/null 2>&1 ||
		die "could not clone ${REPO} at ${tag}"
	build_from_source
}

install_rpm() {
	[ "$(id -u)" -eq 0 ] || die \
		"the rpm method installs a system package and must run as root:
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sudo GRID_INSTALL_METHOD=rpm sh
Use the default archive method if you do not want a system-wide install."

	command -v dnf >/dev/null 2>&1 || command -v rpm >/dev/null 2>&1 ||
		die "no dnf/rpm on this host — the rpm method is for Fedora/RHEL family systems"

	rpm_asset="${PROJECT}_${version}_${os}_${arch}.rpm"
	base_url="https://github.com/${REPO}/releases/download/${tag}"
	log "Installing grid-cli ${tag} from RPM (${os}/${arch})..."
	fetch_release_file "$base_url/$rpm_asset" "$workdir/$rpm_asset" ||
		die "could not download $base_url/$rpm_asset (does release $tag ship an RPM for ${arch}?)"
	fetch_release_file "$base_url/checksums.txt" "$workdir/checksums.txt" ||
		die "could not download checksums.txt for $tag"
	verify_checksum "$workdir/$rpm_asset" "$rpm_asset" "$workdir/checksums.txt"

	if command -v dnf >/dev/null 2>&1; then
		dnf install -y "$workdir/$rpm_asset"
	else
		rpm -Uvh "$workdir/$rpm_asset"
	fi
	log "Installed grid-cli via RPM; verify with: $BINARY --version"
}

os=$(detect_os)
arch=$(detect_arch)
tag=$(resolve_tag)
version=${tag#v}

case "$METHOD" in
auto | archive | source | rpm) ;;
*) die "unknown GRID_INSTALL_METHOD '$METHOD' (expected auto, archive, source or rpm)" ;;
esac

workdir=$(mktemp -d) || die "could not create a temp directory"
trap 'rm -rf "$workdir"' EXIT

if [ "$METHOD" = "rpm" ]; then
	install_rpm
	exit 0
fi

if [ "$METHOD" = "source" ]; then
	source_fallback "GRID_INSTALL_METHOD=source."
	exit 0
fi

need curl
need tar

asset="${PROJECT}_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases/download/${tag}"

log "Installing grid-cli ${tag} (${os}/${arch})..."

if ! fetch_release_file "$base_url/$asset" "$workdir/$asset"; then
	if [ "$METHOD" = "archive" ]; then
		die "could not download $base_url/$asset (does release $tag have a ${os}/${arch} build?)"
	fi
	source_fallback "Release $tag has no ${os}/${arch} archive (download failed)."
	exit 0
fi

fetch_release_file "$base_url/checksums.txt" "$workdir/checksums.txt" ||
	die "could not download checksums.txt for $tag"

# Verify the checksum before anything in the archive is ever executed. A
# mismatch is never a reason to fall back to a source build.
verify_checksum "$workdir/$asset" "$asset" "$workdir/checksums.txt"

tar -xzf "$workdir/$asset" -C "$workdir" "$BINARY" ||
	die "could not extract $BINARY from $asset"

install_into_dir "$workdir/$BINARY"
