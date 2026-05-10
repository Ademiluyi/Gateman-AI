#!/usr/bin/env bash
# demo-logs.sh — multiplexes every log surface GatemanAI uses into one
# terminal so a demo recording can show the system "thinking" in real time.
#
# Usage:
#   ./scripts/demo-logs.sh [PI_USER@PI_IP]
#
# Streams:
#   - mcpserver  ← /tmp/gatemanai-mcpserver.log  (laptop, inbound tool calls)
#   - openclaw   ← `openclaw logs --follow`      (laptop, WhatsApp activity)
#   - ollama     ← ~/.ollama/logs/server.log     (laptop, Gemma inference)
#   - picapture  ← ssh remote `tail -f`          (Pi, capture + doorbell)
#
# Each line is prefixed with a colour-coded source tag so the operator (or
# viewer of the demo recording) can see at a glance which subsystem fired.

set -u

PI_TARGET="${1:-gateman-ai@192.168.0.119}"
MCP_LOG="/tmp/gatemanai-mcpserver.log"
OLLAMA_LOG="$HOME/.ollama/logs/server.log"

# ANSI colours
C_RESET=$'\e[0m'
C_MCP=$'\e[36m'      # cyan
C_OPEN=$'\e[35m'     # magenta
C_OLL=$'\e[33m'      # yellow
C_PI=$'\e[32m'       # green
C_DIM=$'\e[2m'

prefix() {
  local tag="$1" colour="$2"
  while IFS= read -r line; do
    printf '%b[%s]%b %s\n' "$colour" "$tag" "$C_RESET" "$line"
  done
}

# Ensure the mcpserver log exists so tail -f doesn't error before the first run.
touch "$MCP_LOG"

echo "${C_DIM}demo-logs.sh: tailing 4 log surfaces (Ctrl-C to exit)${C_RESET}"
echo "${C_DIM}  mcpserver:  $MCP_LOG${C_RESET}"
echo "${C_DIM}  openclaw:   openclaw logs --follow${C_RESET}"
echo "${C_DIM}  ollama:     $OLLAMA_LOG${C_RESET}"
echo "${C_DIM}  picapture:  ssh $PI_TARGET tail -f /tmp/picapture.log${C_RESET}"
echo

# Background tails. All inherit our stdout, so Ctrl-C kills them with us.
(tail -F "$MCP_LOG"     2>/dev/null | prefix MCP      "$C_MCP")  &
(openclaw logs --follow 2>/dev/null | prefix OPENCLAW "$C_OPEN") &
(tail -F "$OLLAMA_LOG"  2>/dev/null | prefix OLLAMA   "$C_OLL")  &
(ssh -o LogLevel=ERROR "$PI_TARGET" 'tail -F /tmp/picapture.log 2>/dev/null || journalctl -fu picapture 2>/dev/null' \
                                    | prefix PI       "$C_PI")   &

wait
