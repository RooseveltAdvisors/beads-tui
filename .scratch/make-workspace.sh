#!/bin/sh
# Create an ISOLATED synthetic bd workspace at $1 with database name $2 and prefix $3.
# All data is synthetic; never touches ~/.beads or /opt/ra/firstmate/.beads.
set -e
DIR="$1"; DB="$2"; PREFIX="$3"
mkdir -p "$DIR/.beads"
chmod 700 "$DIR/.beads"
cat > "$DIR/.beads/metadata.json" <<META
{
  "database": "dolt",
  "backend": "dolt",
  "dolt_mode": "embedded",
  "dolt_database": "$DB",
  "project_id": "11111111-2222-3333-4444-555555555555"
}
META
BEADS_DIR="$PWD/$DIR/.beads" bd init --prefix "$PREFIX" --quiet --stealth 2>&1 | grep -v "beads.role\|git config" || true
