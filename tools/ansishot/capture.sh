#!/usr/bin/env bash
# capture.sh — regenerate reviewed PTY captures for the `camp setup` scene.
# Each run uses the development harness `internal/setupui/scenerun`, which drives
# the real setupui.Model/setupui.Run flow inside a real PTY with scripted input.
# This exercises the same presentation model/compositor/terminal lifecycle as
# setup, but only in Development-mode scenerun and only via scripted input.
# It does not run ProductionLifecycle operations or any machine/provider
# mutation/configuration.
#
# Outputs:
#   .scene-captures/                 scratch (.raw/.ansi/.keys, gitignored)
#   docs/assets/setup-scene/*.png    tracked review captures
#
# Usage: tools/ansishot/capture.sh   (from the repository root)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
SCRATCH=.scene-captures
OUT=docs/assets/setup-scene
mkdir -p "$SCRATCH" "$OUT"

BIN="$SCRATCH/scenerun"
go build -o "$BIN" ./internal/setupui/scenerun

PTY="python3 tools/ansishot/ptycap.py"
GRID="python3 tools/ansishot/vtgrid.py"
SHOT="python3 tools/ansishot/ansishot.py"

# Submit the form: enter through all five fields. Key lines are written
# verbatim; ptycap.py decodes \r, \t, \x03, \x1b itself.
SUBMIT=('key \r' 'key \r' 'key \r' 'key \r' 'key \r')

# cap NAME COLS ROWS [GRID_COLS GRID_ROWS] KEYLINE... -- CMD...
# GRID_* override the replay grid when the PTY is resized mid-capture.
cap() {
  local name=$1 cols=$2 rows=$3; shift 3
  local gcols=$cols grows=$rows
  if [[ $1 =~ ^[0-9]+$ ]]; then gcols=$1 grows=$2; shift 2; fi
  : > "$SCRATCH/$name.keys"
  while [[ $1 != -- ]]; do
    printf '%s\n' "$1" >> "$SCRATCH/$name.keys"
    shift
  done
  shift
  $PTY --cols "$cols" --rows "$rows" --send-file "$SCRATCH/$name.keys" -- "$@" > "$SCRATCH/$name.raw"
  $GRID --cols "$gcols" --rows "$grows" "$SCRATCH/$name.raw" "$SCRATCH/$name.ansi"
  $SHOT "$SCRATCH/$name.ansi" "$OUT/$name.png" --cols "$gcols" --rows "$grows"
}

# 1–2. First prompt (configure) at 80×24 and 120×40.
cap configure-80x24  80  24 'sleep 0.8' -- "$BIN" -mode ready
cap configure-120x40 120 40 'sleep 0.8' -- "$BIN" -mode ready

# 3. Partially completed provisioning (toolchain done, runtime active) at
#    120×40. The hold pipeline stays at RUNTIME; the capture ends while the
#    program is still inside the alternate screen.
cap progress-120x40 120 40 "${SUBMIT[@]}" 'sleep 1.5' -- "$BIN" -mode hold

# 4. Ready at 120×40.
cap ready-120x40 120 40 "${SUBMIT[@]}" 'sleep 3.0' -- "$BIN" -mode ready

# 4b. Ready at 80×24 (compact): the help line must own the last row.
cap ready-80x24 80 24 "${SUBMIT[@]}" 'sleep 3.0' -- "$BIN" -mode ready

# 5. Failure at 120×40.
cap failure-120x40 120 40 "${SUBMIT[@]}" 'sleep 2.0' -- "$BIN" -mode failure

# 6. Ready at 160×48.
cap ready-160x48 160 48 "${SUBMIT[@]}" 'sleep 3.0' -- "$BIN" -mode ready

# 7. Live resize: type into the source field at 120×40, then resize the PTY to
#    160×48. The captured frame (replayed on a 160×48 grid) must show the
#    typed value retained in the recomposed scene.
cap resize-120x40-to-160x48 120 40 160 48 \
  'sleep 0.5' 'key /älpha dir' 'sleep 0.5' 'resize 160 48' 'sleep 1.0' \
  -- "$BIN" -mode ready

# 8. Cancellation restores the shell: run inside an interactive bash, ctrl+c
#    during provisioning, and capture the main screen with the prompt back.
cap cancel-restored-shell 120 40 \
  'key PS1="camp$ "\r' 'sleep 0.4' "key $BIN -mode hold\r" 'sleep 1.2' \
  "${SUBMIT[@]}" 'sleep 1.5' 'key \x03' 'sleep 0.8' \
  -- bash --norc --noprofile -i

echo "captures written to $OUT/"

python3 tools/ansishot/test_vtgrid.py
