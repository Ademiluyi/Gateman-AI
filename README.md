# GatemanAI

**Physical AI for the front door, delivered through WhatsApp.** Built for the [Gemma 4 Good Hackathon](https://www.kaggle.com/competitions/google-gemma-3-hackathon).

When someone arrives at your door, GatemanAI captures a photo, has Gemma 4 describe what it sees, and sends both to your WhatsApp. You can also text the system anytime to ask "who's at the door?" and get a live photo back. No app to install. No cloud AI. No per-message fees. Runs entirely on a Raspberry Pi and a laptop on your local network.

The first user is a deaf person who cannot hear a doorbell. The second is a small-business owner in Lagos who needs to see who's at the gate without leaving the till. The longer arc is physical AI for the 2 billion people who interact with the world primarily through WhatsApp.

For the full motivation, design, and submission writeup see [SUBMISSION.md](./SUBMISSION.md). For architectural decisions and constraints see [DECISIONS.md](./DECISIONS.md).

## Demo

> Doorbell rings → photo arrives in ~5 seconds → Gemma description follows ~30 seconds later.
> User texts "who's at the door?" → live photo arrives in ~15 seconds.

## What you need

- A **Raspberry Pi** with a camera module (any Pi, but 3 B+ is the tested baseline)
- A **laptop** on the same Wi-Fi as the Pi (8GB RAM minimum; 16GB+ recommended)
- A **WhatsApp account** linked via QR code to OpenClaw
- [Ollama](https://ollama.com/) ≥ 0.22.1 with `gemma4:e2b` pulled
- [OpenClaw](https://docs.openclaw.ai/) installed locally
- Go 1.22+ if you're building from source (skip if you're using the [release binaries](#install-from-release))

## Architecture

Three small Go binaries; one runs on the Pi, two run on the laptop.

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

- `picapture` — Pi-side. HTTP server on `:9000` exposing `/capture`, `/doorbell` (long-poll), `/trigger`, `/health`. Watches GPIO17 for button presses with a TTL'd queue so events buffered during a brief outage are delivered when the laptop reconnects.
- `gatemanai` — laptop-side. Long-polls the Pi for doorbell events. On each ring, captures, sends the photo immediately, then runs Gemma in the background and sends the description as a follow-up. Each external call is wrapped in retry-with-backoff so a single Wi-Fi flap doesn't drop the ring.
- `mcpserver` — laptop-side, registered with OpenClaw as an MCP server. Exposes a `capture_door` tool the Gemma agent can call. The tool fetches a photo from the Pi and sends it directly to WhatsApp.

## Install from release

Pre-built binaries are on the [Releases page](../../releases). Download the matching artefact for each host:

- Laptop (macOS or Linux): `gatemanai`, `mcpserver`, `doctor`
- Pi 3 B+ / 32-bit Raspberry Pi OS: `picapture_arm32`
- Pi 4 / 5 with 64-bit OS: `picapture_arm64`

`chmod +x` each one and skip to [Configure](#configure).

## Build from source

```bash
git clone https://github.com/ade/gatemanai
cd gatemanai
make build      # gatemanai, mcpserver, doctor for the host
make cross      # picapture_arm32 + picapture_arm64 for the Pi
make test       # full test suite
```

## Configure

### 1. Pull Gemma and create the 8k variant

```bash
ollama pull gemma4:e2b
ollama show gemma4:e2b --modelfile > /tmp/Modelfile
echo "PARAMETER num_ctx 8192" >> /tmp/Modelfile
ollama create gemma4:e2b-8k -f /tmp/Modelfile
```

The 8k variant caps Gemma's context window at 8,192 tokens, dropping its memory footprint from ~8.9GB to ~7.8GB. Required on 8GB Macs. See `DECISIONS.md` for the full rationale.

### 2. Link WhatsApp through OpenClaw

```bash
openclaw channels login --channel whatsapp
```

Scan the QR with the WhatsApp account that will receive notifications. Then edit `~/.openclaw/openclaw.json`:

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

Replace `+15551234567` with the linked number.

### 3. Register the MCP server with OpenClaw

```bash
openclaw mcp set gatemanai-camera '{
  "command": "/absolute/path/to/mcpserver",
  "args": [],
  "env": {
    "PI_URL": "http://<pi-ip>:9000",
    "OPENCLAW_TO": "+15551234567"
  }
}'
```

### 4. Deploy picapture to the Pi

```bash
scp picapture_arm32 pi@<pi-ip>:~/picapture
ssh pi@<pi-ip> "chmod +x ~/picapture"
```

## Run

On the Pi:

```bash
~/picapture
# logs: "picapture listening on :9000"
```

On the laptop:

```bash
openclaw gateway restart
export OPENCLAW_TO=+15551234567
export PI_URL=http://<pi-ip>:9000
./gatemanai
```

Verify everything is reachable before recording a demo:

```bash
./doctor
```

`doctor` checks the Pi, Ollama, the gemma4:e2b-8k model, OpenClaw, the MCP registration, and the env vars — and prints PASS/FAIL for each.

## Try it

**Outbound push** — fire a doorbell event manually:

```bash
curl http://<pi-ip>:9000/trigger
```

A WhatsApp photo arrives in ~5 seconds; Gemma's description follows as a second message ~30 seconds later.

**Inbound pull** — text your linked WhatsApp number:

```
/new
Who's at the door?
```

A live photo + brief acknowledgment arrive in ~15 seconds. The `/new` resets OpenClaw's session memory; see `DECISIONS.md` for why.

## Repository layout

```
cmd/
  gatemanai/      laptop process: doorbell loop + outbound pipeline
  mcpserver/      MCP stdio server: capture_door tool for the agent
  picapture/      Pi HTTP server: capture, doorbell, trigger, health
  doctor/         pre-flight system health check
  visiontest/     ad-hoc vision + WhatsApp test harness
internal/
  camera/         HTTP capture client (Pi-aware, tested)
  doorbell/       long-poll the Pi for events with backoff (tested)
  vision/         Ollama /api/chat client for Gemma 4
  openclaw/       OpenClaw CLI wrapper for WhatsApp sends
  retry/          generic exponential-backoff helper (tested)
```

## License

MIT — see [LICENSE](./LICENSE).
