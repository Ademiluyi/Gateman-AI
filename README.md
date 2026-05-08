# GatemanAI

Physical AI for the front door. Built for the [Gemma 4 Good Hackathon](https://www.kaggle.com/competitions/google-gemma-3-hackathon).

When someone arrives at your door, GatemanAI captures a photo, has Gemma 4 describe what it sees, and sends both to your WhatsApp. You can also text the system anytime to ask "who's at the door?" and it will reply with a live photo. No app to install, no cloud AI, no per-message fees — runs entirely on a Raspberry Pi and a laptop on your local network.

The first user is a deaf person who cannot hear a doorbell. The longer arc is physical AI for the 2 billion people who interact with the world primarily through WhatsApp.

For the full motivation, design, and submission writeup, see [SUBMISSION.md](./SUBMISSION.md). For architectural decisions and constraints, see [DECISIONS.md](./DECISIONS.md).

## Architecture

```
PUSH (doorbell rings → notification)
[GPIO17 button] → [picapture on Pi] ─HTTP─► [gatemanai on laptop]
                                                    │
                                            ┌───────┴────────┐
                                            ▼ (immediate)    ▼ (background)
                                  [openclaw send photo]  [Gemma describe]
                                            │                │
                                            ▼                ▼
                                    [WhatsApp: photo]  [openclaw send text]
                                                              │
                                                              ▼
                                                    [WhatsApp: description]

PULL (user texts → photo)
[User texts WhatsApp] → [OpenClaw] → [Gemma 4 E2B]
                                           │  (function call)
                                           ▼
                                   [MCP: capture_door]
                                           │
                                           ▼
                              [mcpserver on laptop]
                                           │
                              ┌────────────┴────────────┐
                              ▼                         ▼
                      [HTTP fetch from Pi]   [openclaw send photo direct]
                                                        │
                                                        ▼
                                              [WhatsApp: photo]
```

Three Go binaries:

- `picapture` — runs on the Pi. HTTP server on `:9000` exposing `/capture`, `/doorbell` (long-poll), `/trigger`, `/health`. Watches GPIO17 for button presses.
- `gatemanai` — runs on the laptop. Long-polls the Pi for doorbell events. On each ring, captures a photo, sends it immediately to WhatsApp, then runs Gemma in the background and sends the description as a follow-up.
- `mcpserver` — runs on the laptop, registered with OpenClaw as an MCP server. Exposes a `capture_door` tool that the OpenClaw agent (Gemma) can call. The tool fetches a photo from the Pi and sends it directly to WhatsApp.

## Requirements

**Pi (sensor node):**
- Raspberry Pi 3 B+ or newer running Raspberry Pi OS
- Camera module (libcamera/rpicam compatible)
- GPIO17 wired to a doorbell button (active-low) — optional; `/trigger` works for testing

**Laptop (inference node):**
- macOS or Linux on the same Wi-Fi as the Pi
- 8GB RAM minimum; 16GB+ recommended
- [Ollama](https://ollama.com/) ≥ 0.22.1 with `gemma4:e2b` pulled
- [OpenClaw](https://docs.openclaw.ai/) installed and a WhatsApp account linked
- Go 1.22+

## Setup

### 1. Pull the model and create the 8k variant

```bash
ollama pull gemma4:e2b
ollama show gemma4:e2b --modelfile > /tmp/Modelfile
echo "PARAMETER num_ctx 8192" >> /tmp/Modelfile
ollama create gemma4:e2b-8k -f /tmp/Modelfile
```

The 8k variant caps the model's context at 8,192 tokens, dropping its memory footprint from ~8.9GB to ~7.8GB. Required for 8GB Macs. See `DECISIONS.md` for context on this trade-off.

### 2. Configure OpenClaw

Link WhatsApp:

```bash
openclaw channels login --channel whatsapp
```

Set the agent's model and system prompt. Edit `~/.openclaw/openclaw.json`:

```json
{
  "channels": {
    "whatsapp": {
      "enabled": true,
      "dmPolicy": "allowlist",
      "allowFrom": ["+15551234567"],
      "dmHistoryLimit": 0,
      "historyLimit": 0
    }
  },
  "agents": {
    "defaults": {
      "model": "ollama/gemma4:e2b-8k",
      "systemPromptOverride": "You are GatemanAI — a WhatsApp-native AI assistant for a smart door camera. You have one tool: capture_door. It captures a live photo AND sends it directly via WhatsApp. When the user asks about the door, who is there, or what is outside, immediately call capture_door. After it returns, reply with five words or fewer like 'Photo sent.' For unrelated messages, respond in one short sentence."
    }
  }
}
```

Replace `+15551234567` with your linked WhatsApp number.

### 3. Register the MCP server

```bash
openclaw mcp set gatemanai-camera '{
  "command": "/absolute/path/to/Gemma-4-Good/mcpserver",
  "args": [],
  "env": {
    "PI_URL": "http://<pi-ip>:9000",
    "OPENCLAW_TO": "+15551234567"
  }
}'
```

### 4. Build and deploy

```bash
# Build mcpserver and gatemanai for the laptop
go build -o ./mcpserver ./cmd/mcpserver
go build -o ./gatemanai ./cmd/gatemanai

# Cross-compile picapture for the Pi (Pi 3 B+ runs ARMv7)
GOOS=linux GOARCH=arm GOARM=7 go build -o ./picapture_arm32 ./cmd/picapture

# Copy to the Pi
scp ./picapture_arm32 pi@<pi-ip>:~/picapture
ssh pi@<pi-ip> "chmod +x ~/picapture"
```

### 5. Run

On the Pi:
```bash
~/picapture
# logs: "picapture listening on :9000" + "Doorbell listening on GPIO17"
```

On the laptop:
```bash
openclaw gateway restart
export OPENCLAW_TO=+15551234567
export PI_URL=http://<pi-ip>:9000
./gatemanai
```

That's it. Two test scenarios:

**Outbound push** — fire a doorbell event manually:
```bash
curl http://<pi-ip>:9000/trigger
```

You should receive a WhatsApp photo within ~5 seconds and a Gemma-generated description ~30–90 seconds later.

**Inbound pull** — text your linked WhatsApp number:
```
/new
Who's at the door?
```

You should receive a live photo + acknowledgment within ~15–20 seconds. The `/new` resets OpenClaw's session memory between queries; see `DECISIONS.md` for why.

## Development

```bash
# Vet, build, test all packages
go build ./...

# End-to-end vision test (skips the doorbell, exercises capture → Gemma → WhatsApp)
OPENCLAW_TO=+15551234567 go run ./cmd/visiontest /path/to/test.jpg
```

## Repository layout

```
cmd/
  gatemanai/      laptop process: doorbell loop + outbound pipeline
  mcpserver/      MCP stdio server: capture_door tool for the agent
  picapture/      Pi HTTP server: capture, doorbell, trigger, health
  visiontest/     ad-hoc vision + WhatsApp test
internal/
  camera/         HTTP capture client (Pi-aware)
  doorbell/       long-poll the Pi for events
  vision/         Ollama /api/chat client for Gemma 4
  openclaw/       OpenClaw CLI wrapper for WhatsApp sends
```

## License

MIT.
