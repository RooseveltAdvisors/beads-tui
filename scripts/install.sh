#!/usr/bin/env bash
# Build beads-tui from a git rev and install to ~/.local/bin with proof.
# Usage: scripts/install.sh [rev]   (default: origin/main)
#
# The verification runs against the INSTALLED file, after the install, so a
# wrong merge, a clobbered build, or a concurrent overwrite cannot pass.
set -Eeuo pipefail

REV="${1:-origin/main}"
DEST="$HOME/.local/bin/beads-tui"
TMP="$(mktemp /tmp/beads-tui-install.XXXXXX)"
trap 'rm -f "$TMP"' EXIT

git fetch -q origin
SHA="$(git rev-parse --verify "$REV^{commit}")" || { echo "✗ unknown rev: $REV" >&2; exit 1; }

echo "▶ building $REV (${SHA:0:12})"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$SHA" -o "$TMP" ./cmd/beads-tui

echo "▶ installing atomically to $DEST"
chmod +x "$TMP"
mv -f "$TMP" "$DEST" # mv, not cp: a running popup keeps its old inode, the file swaps cleanly
trap - EXIT

echo "▶ verifying INSTALLED binary"
GOT="$("$DEST" --version | awk '{print $2}')"
if [ "$GOT" != "$SHA" ]; then
  echo "✗ FAIL: installed reports '$GOT', expected '$SHA' (overwritten mid-install or wrong rev)" >&2
  exit 1
fi
printf '%s  %s  %s  %s\n' "$(sha256sum "$DEST" | cut -d' ' -f1)" "$SHA" "$(date -u +%FT%TZ)" "${USER:-unknown}" > "$DEST.manifest"
echo "✓ installed $DEST @ ${SHA:0:12} (manifest: $DEST.manifest)"
"$DEST" --version
