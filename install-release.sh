#!/usr/bin/env bash
# Installs the latest gfs release for the current user, or updates it:
#   curl -fsSL https://raw.githubusercontent.com/KrzysztofBogdan/gitfs/master/install-release.sh | bash
# Linux and macOS, amd64 and arm64. The binary goes to ~/.local/bin/gfs (override with INSTALL_DIR).
set -euo pipefail

repo="https://github.com/KrzysztofBogdan/gitfs"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

fail() {
  echo "install-release.sh: $1" >&2
  exit 1
}

# the archive of this machine: gfs_<os>_<arch>.tar.gz, as .goreleaser.yaml names it
asset_name() {
  local os arch
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "no gfs release for $(uname -s); on Windows see $repo#windows" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "no gfs release for $(uname -m); build it from source, see $repo#build-from-source" ;;
  esac
  echo "gfs_${os}_${arch}.tar.gz"
}

# the latest release's tag, from where github.com/.../releases/latest redirects
latest_tag() {
  local url
  url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$repo/releases/latest")"
  case "$url" in
    */tag/v*) echo "${url##*/tag/}" ;;
    *) fail "found no gfs release at $repo/releases/latest (it led to $url)" ;;
  esac
}

sha256_of() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1" | cut -d ' ' -f 1
    return
  fi
  shasum -a 256 "$1" | cut -d ' ' -f 1
}

# downloads asset $2 of release $1 into folder $3 and checks it against the release's checksums.txt
download() {
  local expected
  echo "downloading $2 ($1)"
  curl -fsSL -o "$3/$2" "$repo/releases/download/$1/$2"
  curl -fsSL -o "$3/checksums.txt" "$repo/releases/download/$1/checksums.txt"
  expected="$(awk -v name="$2" '$2 == name || $2 == "*" name { print $1 }' "$3/checksums.txt")"
  [ -n "$expected" ] || fail "checksums.txt of $1 has no line for $2"
  [ "$(sha256_of "$3/$2")" = "$expected" ] || fail "$2 does not match its checksum in checksums.txt of $1"
}

main() {
  local asset tag work
  asset="$(asset_name)"
  tag="$(latest_tag)"
  work="$(mktemp -d)"
  # expanded now: the trap runs after main, when its local work is gone
  trap "rm -rf '$work'" EXIT
  download "$tag" "$asset" "$work"
  tar -xzf "$work/$asset" -C "$work" gfs
  mkdir -p "$INSTALL_DIR"
  # a rename replaces a running gfs without touching it
  install -m 755 "$work/gfs" "$INSTALL_DIR/gfs.new"
  mv -f "$INSTALL_DIR/gfs.new" "$INSTALL_DIR/gfs"
  echo "installed $INSTALL_DIR/gfs ($tag)"
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) echo "warning: $INSTALL_DIR is not on \$PATH; add it to run gfs" >&2 ;;
  esac
}

# called last, so a download cut short runs nothing
main "$@"
