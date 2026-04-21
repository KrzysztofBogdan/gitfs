#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

mkdir -p "$INSTALL_DIR"
go build -o "$INSTALL_DIR/gfs" ./cmd/gitfs

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "warning: $INSTALL_DIR is not on \$PATH" >&2 ;;
esac

echo "installed: $INSTALL_DIR/gfs"
