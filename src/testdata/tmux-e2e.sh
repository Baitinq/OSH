#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
socket="osh-e2e-$$"
session=osh-e2e
binary=$(mktemp "${TMPDIR:-/tmp}/osh-e2e.XXXXXX")
tmux=(tmux -L "$socket")
cleanup() { "${tmux[@]}" kill-server 2>/dev/null || true; rm -f "$binary"; }
trap cleanup EXIT

cd "$root"
go test -c -o "$binary" ./internal/ui
"${tmux[@]}" new-session -d -x 60 -y 18 -s "$session" /bin/sh
"${tmux[@]}" set-option -t "$session" remain-on-exit on
"${tmux[@]}" send-keys -t "$session" "OSH_TMUX_HARNESS=1 '$binary' -test.run '^TestTmuxHarness$' -test.count=1" Enter

capture() { "${tmux[@]}" capture-pane -p -t "$session" -S -; }
wait_for() {
  local pattern=$1
  for _ in $(seq 1 200); do capture | grep -q "$pattern" && return 0; sleep .025; done
  echo "timed out waiting for $pattern" >&2; capture >&2; return 1
}
wait_for 'Type a message'

# Stream past the viewport and verify both ends reached native tmux history.
"${tmux[@]}" send-keys -t "$session" -l stream
"${tmux[@]}" send-keys -t "$session" Enter
wait_for 'context 321 tokens'
all=$(capture)
grep -q 'STREAM-LINE-01' <<<"$all"
grep -q 'STREAM-LINE-32' <<<"$all"

# Shift+Enter queues while plain Enter steers; the steer must run first.
"${tmux[@]}" send-keys -t "$session" -l stream
"${tmux[@]}" send-keys -t "$session" Enter
wait_for 'STREAM-LINE-03'
"${tmux[@]}" send-keys -t "$session" -l queued-order
"${tmux[@]}" send-keys -t "$session" S-Enter
"${tmux[@]}" send-keys -t "$session" -l steer-order
"${tmux[@]}" send-keys -t "$session" Enter
wait_for 'ECHO<queued-order>'
all=$(capture)
steer_line=$(grep -n 'ECHO<steer-order>' <<<"$all" | head -1 | cut -d: -f1)
queue_line=$(grep -n 'ECHO<queued-order>' <<<"$all" | head -1 | cut -d: -f1)
[[ -n "$steer_line" && -n "$queue_line" && "$steer_line" -lt "$queue_line" ]]

# tmux's native search and selection can reach finalized transcript output.
"${tmux[@]}" copy-mode -t "$session"
"${tmux[@]}" send-keys -t "$session" -X history-top
"${tmux[@]}" send-keys -t "$session" -X search-forward 'STREAM-LINE-01'
"${tmux[@]}" send-keys -t "$session" -X select-line
"${tmux[@]}" send-keys -t "$session" -X copy-selection-and-cancel
"${tmux[@]}" show-buffer | grep -q 'STREAM-LINE-01'

# Stay scrolled up while another response continues rendering.
"${tmux[@]}" send-keys -t "$session" -l stream
"${tmux[@]}" send-keys -t "$session" Enter
sleep .15
"${tmux[@]}" copy-mode -u -t "$session"
"${tmux[@]}" send-keys -t "$session" -X page-up
scroll_before=$("${tmux[@]}" display-message -p -t "$session" '#{scroll_position}')
sleep .8
scroll_after=$("${tmux[@]}" display-message -p -t "$session" '#{scroll_position}')
[[ "$scroll_before" -gt 0 && "$scroll_after" == "$scroll_before" ]]
"${tmux[@]}" send-keys -t "$session" -X cancel

# Multiline input and hardware cursor positioning.
"${tmux[@]}" send-keys -t "$session" -l alpha
"${tmux[@]}" send-keys -t "$session" C-j
"${tmux[@]}" send-keys -t "$session" -l beta
sleep .05
visible=$("${tmux[@]}" capture-pane -p -t "$session")
grep -q '│ alpha' <<<"$visible"
grep -q '│ beta' <<<"$visible"
"${tmux[@]}" send-keys -t "$session" Enter
wait_for 'ECHO<alpha'

# Tool call, result, and streamed final response share the logical transcript.
"${tmux[@]}" send-keys -t "$session" -l tools
"${tmux[@]}" send-keys -t "$session" Enter
wait_for 'context 654 tokens'
all=$(capture)
grep -q '\$ printf tool-output' <<<"$all"
grep -q '│ tool-output' <<<"$all"
grep -q 'tool turn complete' <<<"$all"

# Cancellation must replace mutable output without leaving the terminal dirty.
"${tmux[@]}" send-keys -t "$session" -l cancel
"${tmux[@]}" send-keys -t "$session" Enter
wait_for 'WAITING-FOR-CANCEL'
"${tmux[@]}" send-keys -t "$session" Escape
wait_for 'Cancelled.'

# Pi-style resize replay must retain the semantic transcript at both widths.
"${tmux[@]}" resize-window -t "$session" -x 36 -y 12
sleep .2
all=$(capture)
[[ $(grep -c 'STREAM-LINE-01' <<<"$all") -eq 3 ]]
[[ $(grep -c 'STREAM-LINE-32' <<<"$all") -eq 3 ]]
grep -q 'Cancelled.' <<<"$all"
"${tmux[@]}" resize-window -t "$session" -x 72 -y 24
sleep .2

# Let the mutable editor become taller than the original live region.
for i in $(seq -w 1 14); do
  "${tmux[@]}" send-keys -t "$session" -l "EDIT-$i"
  [[ "$i" == 14 ]] || "${tmux[@]}" send-keys -t "$session" C-j
done
sleep .1
visible=$("${tmux[@]}" capture-pane -p -t "$session")
grep -q '│ EDIT-14' <<<"$visible"
"${tmux[@]}" send-keys -t "$session" Enter
wait_for 'context 777 tokens'
all=$(capture)
grep -q 'EDIT-01' <<<"$all"
grep -q 'EDIT-14' <<<"$all"

# Exit and prove the shell is usable and the cursor cell was not overwritten.
"${tmux[@]}" send-keys -t "$session" C-c
wait_for '^PASS$'
"${tmux[@]}" send-keys -t "$session" "printf 'TERMINAL-CLEAN\\n'" Enter
wait_for 'TERMINAL-CLEAN'
if capture | grep -q '│  ype a message'; then
  echo 'exit overwrote the input cursor cell' >&2
  exit 1
fi

printf 'tmux e2e passed: streaming, tools, history, scroll anchoring, dynamic multiline input, cancellation, resize, and cleanup\n'
