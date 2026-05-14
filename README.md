# GatemanAI

**Physical AI for the front door, delivered through WhatsApp.** Built for the [Gemma 4 Good Hackathon](https://www.kaggle.com/competitions/google-gemma-3-hackathon).

When someone arrives at your door, GatemanAI captures a photo, has Gemma 4 describe what it sees, and sends both to your WhatsApp. You can also text the system anytime to ask "who's at the door?" and get a live photo back. No app to install. No cloud AI. No per-message fees. Runs entirely on a Raspberry Pi and a laptop on your local network.

The first user is a deaf person who cannot hear a doorbell. The second is a small-business owner in Lagos who needs to see who's at the gate without leaving the till. The longer arc is physical AI for the 2 billion people who interact with the world primarily through WhatsApp.

For the full motivation, design, and submission writeup see [SUBMISSION.md](./SUBMISSION.md). For architectural decisions and constraints see [DECISIONS.md](./DECISIONS.md).

## Demo

> Motion at the door → photo arrives in ~5 seconds → Gemma description follows ~30 seconds later.
> User texts "who's at the door?" → live photo arrives in ~15 seconds.
> User texts "what happened in the last hour?" → Gemma reads the event log and replies.

## What you need

- A **camera source.** Either a **Raspberry Pi** with a camera module (any Pi; 3 B+ is the tested baseline) **or** any **RTSP camera** you already own — Hikvision, Dahua, ONVIF IP cam, ESP32-CAM. See [Use an existing camera](#use-an-existing-camera-rtsphikvisiondahua) below.
- A **laptop** on the same Wi-Fi as the camera (8GB RAM minimum; 16GB+ recommended)
- A **WhatsApp account** linked via QR code to OpenClaw
- [Ollama](https://ollama.com/) ≥ 0.22.1 with `gemma4:e2b` pulled
- [OpenClaw](https://docs.openclaw.ai/) installed locally
- `ffmpeg` on the laptop **only if** you're using the RTSP source
- Go 1.22+ if you're building from source (skip if you're using the [release binaries](#install-from-release))

## Architecture

A small set of Go binaries. One runs on the camera host (Pi or laptop), the rest run on the laptop.

```
PUSH (motion / GPIO / manual trigger → notification)
[Pi camera or RTSP cam]
        │ motion detected
        ▼
[picapture | rtspsource] ─HTTP─► [gatemanai on laptop]
                                          │
                                  ┌───────┴────────┐
                                  ▼ (immediate)    ▼ (background)
                        [openclaw send photo]  [Gemma describe]
                                  │                │
                                  ▼                ▼
                          [WhatsApp: photo]  [WhatsApp: description]

PULL (user texts → live photo OR retrospective)
[User texts WhatsApp] → [OpenClaw] → [Gemma 4 E2B]
                                          │  (function call)
                                          ▼
                                   [mcpserver]
                                          │
                ┌─────────────────────────┼─────────────────────────┐
                ▼                         ▼                         ▼
       [capture_door]            [recent_events]              [send_photo]
        live photo            JSONL event log read      retrieve a stored event
```

- `picapture` — Pi-side. HTTP server on `:9000` exposing `/capture`, `/presence` (long-poll), `/trigger`, `/health`. Runs software motion detection on the camera stream and an optional GPIO listener (button or PIR via `GPIO_TRIGGER_EDGE`).
- `rtspsource` — laptop-side. **Drop-in replacement for `picapture` against any RTSP camera.** Same HTTP contract; pulls frames via `ffmpeg`. Lets you use GatemanAI on a Hikvision DVR or any IP cam you already own — no Pi required.
- `gatemanai` — laptop-side. Long-polls the camera source for presence events. On each event: captures, sends the photo immediately, then runs Gemma in the background and sends the description as a follow-up. Persists every event to `~/.gatemanai/events.jsonl` with a 7-day janitor. Each external call is retry-with-backoff so a single Wi-Fi flap doesn't drop the event.
- `mcpserver` — laptop-side, registered with OpenClaw as an MCP server. Exposes three tools to the Gemma agent: `capture_door` (live photo), `recent_events` (windowed listing of past events), and `send_photo` (retrieve the JPEG for a specific event ID).
- `doctor` — pre-flight health check (camera reachable, Ollama up, model loaded, gateway running, MCP registered, env vars set).
- `allow` — operator helper: adds a tester's number to both `channels.whatsapp.allowFrom` and the MCP server's `OPENCLAW_TO` env in one command.

## Install from release

Pre-built binaries are on the [Releases page](../../releases). Download the matching artefact for each host:

- Laptop (macOS or Linux): `gatemanai`, `mcpserver`, `doctor`, `allow`, `rtspsource` (only needed if you're using an RTSP camera)
- Pi 3 B+ / 32-bit Raspberry Pi OS: `picapture_arm32`
- Pi 4 / 5 with 64-bit OS: `picapture_arm64`

`chmod +x` each one and skip to [Configure](#configure).

## Build from source

```bash
git clone https://github.com/ade/gatemanai
cd gatemanai
make build      # gatemanai, mcpserver, doctor, allow, rtspsource for the host
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

## Use an existing camera (RTSP/Hikvision/Dahua)

If you already own a CCTV DVR or IP camera that exposes RTSP, skip the Pi entirely and run `rtspsource` on the laptop instead. It implements the same HTTP contract as `picapture`, so the rest of the system doesn't know the difference.

```bash
brew install ffmpeg                # or: apt install ffmpeg
RTSP_URL="rtsp://admin:pass@192.168.0.50:554/Streaming/Channels/101" ./rtspsource
```

Then point `gatemanai` at the local source instead of the Pi:

```bash
export PI_URL=http://127.0.0.1:9000
./gatemanai
```

Same motion detection, same WhatsApp pipeline, same MCP retrospective. The DVR keeps recording untouched.

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

**Outbound push** — fire a presence event manually (or just walk in front of the camera and let motion detection fire it):

```bash
curl http://<pi-ip>:9000/trigger
```

A WhatsApp photo arrives in ~5 seconds; Gemma's description follows as a second message ~30 seconds later.

**Inbound pull — live** — text your linked WhatsApp number:

```
/new
Who's at the door?
```

A live photo + brief acknowledgment arrive in ~15 seconds. The `/new` resets OpenClaw's session memory; see `DECISIONS.md` for why.

**Inbound pull — retrospective** — same WhatsApp thread:

```
/new
What happened in the last hour?
```

Gemma reads the event log and replies with a list of recent events (timestamp + scene description). Follow up with "show me the 3pm one" and `send_photo` retrieves the JPEG.

## Repository layout

```
cmd/
  gatemanai/      laptop process: presence loop + outbound pipeline + event store
  mcpserver/      MCP stdio server: capture_door, recent_events, send_photo
  picapture/      Pi HTTP server: capture, presence (long-poll), trigger, health
  rtspsource/     drop-in replacement for picapture against any RTSP camera
  doctor/         pre-flight system health check
  allow/          operator helper: sync allowFrom + MCP env in one command
  visiontest/     ad-hoc vision + WhatsApp test harness
internal/
  camera/         HTTP capture client (tested)
  presence/       long-poll the camera source with adaptive backoff (tested)
  motion/         pure-Go frame-differencing motion detector (tested)
  events/         JSONL event store + 7-day janitor (tested)
  vision/         Ollama /api/chat client for Gemma 4
  openclaw/       OpenClaw CLI wrapper for WhatsApp sends (tested)
  audio/          (reserved for audio hooks)
  retry/          generic exponential-backoff helper (tested)
```

## License

MIT — see [LICENSE](./LICENSE).
