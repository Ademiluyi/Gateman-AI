# GatemanAI

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)

**Physical AI for environmental awareness, delivered through WhatsApp.** Built for the [Gemma 4 Good Hackathon](https://www.kaggle.com/competitions/gemma-4-good-hackathon).

GatemanAI compresses video surveillance into queryable events. A local AI watches your camera in real time, summarises what it sees as text + a key frame, and persists those summaries as an event log you can query over WhatsApp. Storage drops from gigabytes a day to kilobytes. Retrieval becomes a conversation, not a video scrubber. No app to install. No cloud AI inference. No per-message fees. Runs locally on a Raspberry Pi or any RTSP camera plus a laptop on the same network.

The first use case is a deaf person who can't hear someone arriving. The second is a small-business owner in Lagos who needs to know who's outside the shop without leaving the till. The longer arc is agentic surveillance for the millions of people who use WhatsApp daily — the markets traditional CCTV and cloud doorbells don't reach.

For the full motivation, design, and submission writeup see [SUBMISSION.md](./SUBMISSION.md). For architectural decisions and constraints see [DECISIONS.md](./DECISIONS.md).

## Demo

> Motion at the entrance → photo arrives in ~5 seconds → Gemma description follows seconds later.
> User texts "who's outside?" → live photo arrives in ~15 seconds.
> User texts "what happened in the last hour?" → Gemma reads the event log and replies.

## What you need

- A **camera source.** Either a **Raspberry Pi** with a camera module (Pi 3 B+ is the tested baseline; Pi 4 / 5 also work). The Pi binary auto-detects the still-capture tool: `rpicam-still` (Bookworm), `libcamera-still` (Bullseye), or `raspistill` (Buster).
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
- `doctor` — pre-flight health check. Runs 8 verifications: camera reachable, camera serves a real JPEG (magic-bytes), Ollama reachable, Ollama has the pinned model, OpenClaw config pins the agent to the same model (catches config drift), OpenClaw gateway running, MCP server registered, `OPENCLAW_TO` set.
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
      "systemPromptOverride": "You are GatemanAI — a WhatsApp-native AI assistant for a perimeter camera. You have one tool: capture_door (legacy name; it captures from whatever camera you've configured). It takes a live photo AND sends it directly via WhatsApp. When the user asks who is outside, who is at the entrance, or what is happening, immediately call the tool. After it returns, reply with five words or fewer like 'Photo sent.' For unrelated messages, respond in one short sentence."
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

`doctor` runs 8 checks (camera reachable, camera serves a real JPEG, Ollama reachable, model loaded, OpenClaw config pins the same model, gateway running, MCP registered, `OPENCLAW_TO` set) and prints PASS/FAIL for each. Non-zero exit on any failure.

## Try it

**Outbound push** — fire a presence event manually (or just walk in front of the camera and let motion detection fire it):

```bash
curl http://<pi-ip>:9000/trigger
```

A WhatsApp photo arrives in ~5 seconds; Gemma's description follows as a second message ~30 seconds later.

**Inbound pull — live** — text your linked WhatsApp number:

```
/new
Who's outside?
```

A live photo + brief acknowledgment arrive in ~15 seconds. The `/new` resets OpenClaw's session memory; see `DECISIONS.md` for why.

**Inbound pull — retrospective** — same WhatsApp thread:

```
/new
What happened in the last hour?
```

Gemma reads the event log and replies with a list of recent events (timestamp + scene description). Follow up with "show me the 3pm one" and `send_photo` retrieves the JPEG.

## Tuning motion detection

Motion detection runs a frame-difference loop with a configurable threshold. The default `MOTION_THRESHOLD=10` is what's been validated end-to-end on the developer's indoor scene (mixed lighting, ~70KB JPEG frames). Your camera's noise floor will differ — too sensitive and you get spurious fires from sensor noise and lighting flicker; too conservative and real movement gets ignored.

Override with env vars at startup. None of these need a rebuild:

| Env | Default | Notes |
|---|---|---|
| `MOTION_THRESHOLD` | `10` | Mean absolute pixel difference (0–255). Raise if your scene has noisy lighting; lower for stable scenes where you want subtle movement. |
| `MOTION_INTERVAL_MS` | `3000` | Capture interval in milliseconds. Lower = snappier, more CPU. |
| `MOTION_COOLDOWN_S` | `30` | Quiet window after a fire. Prevents one person walking past from generating a flood. |
| `MOTION_EDGE_CROP` | `0.05` | Ignore the outer N% of the frame (wind on branches, sun glare on lens housing). |
| `MOTION_ENABLED` | `true` | Set `false` to disable motion entirely (use `/trigger` or GPIO instead). |

To find the right threshold for your scene, sit still in front of the camera for ~30s with `MOTION_THRESHOLD=1` and watch `tail -f /tmp/picapture.log` — the diff numbers tell you your noise floor. Pick a threshold a few points above it.

## Privacy

GatemanAI is local-first. All AI inference runs on the user's laptop; photos and Gemma's descriptions never leave the local network except as the WhatsApp messages the user explicitly opts into by linking their account.

Event history (timestamps, descriptions, and JPEGs) is persisted to `~/.gatemanai/` for **7 days** so retrospective queries ("what happened today?") can answer. A janitor goroutine purges anything older every hour. The operator can shorten or disable retention by editing the constant in `cmd/gatemanai/main.go`, or wipe history at any time with `rm -rf ~/.gatemanai`.

## Repository layout

```
cmd/
  gatemanai/      laptop process: presence loop + outbound pipeline + event store
  mcpserver/      MCP stdio server: capture_door, recent_events, send_photo
  picapture/      Pi HTTP server: capture, presence (long-poll), trigger, health
                  (auto-detects rpicam-still / libcamera-still / raspistill)
  rtspsource/     drop-in replacement for picapture against any RTSP camera
  doctor/         pre-flight system health check (8 checks)
  allow/          operator helper: sync allowFrom + MCP env in one command
internal/
  camera/         HTTP capture client (tested)
  presence/       long-poll the camera source with adaptive backoff (tested)
  motion/         pure-Go frame-differencing motion detector (tested)
  events/         JSONL event store + 7-day janitor (tested)
  vision/         Ollama /api/chat client for Gemma 4 (tested)
  openclaw/       OpenClaw CLI wrapper for WhatsApp sends (tested)
  retry/          generic exponential-backoff helper (tested)
```

## License

MIT — see [LICENSE](./LICENSE).
