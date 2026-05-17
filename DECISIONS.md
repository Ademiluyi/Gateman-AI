# Architecture Decisions & Constraints

## Product thesis: event-only surveillance

GatemanAI's storage primitive is an **AI-summarised event**, not a video clip. A local Gemma watches the camera in real time and writes one JSONL line per event (timestamp + scene description + key-frame JPEG path). Users query the log over WhatsApp — *"what happened today?"*, *"show me the 3pm one."*

This is the load-bearing product decision. Every technical choice below — single-writer JSONL over SQLite, 8k Modelfile pinning, two-Gemma-call inbound workaround, pure-Go motion over OpenCV — exists because the storage and inference unit is an event, not a video frame stream.

Why this matters for the target market:

- **Storage.** Traditional 1080p H.264 surveillance writes ~50GB/day per camera. Event-only writes ~150KB/day (50 events × ~3KB). A microSD lasts forever; cloud upload isn't needed.
- **Retrieval.** "Scrubbing through footage" is the standard UX for finding an event. Event-only collapses retrieval into a WhatsApp message and a natural-language reply.
- **Bandwidth.** WhatsApp-sized messages survive in Lagos / Nairobi / Dhaka bandwidth conditions that defeat live video streaming.

The entrance camera is the wedge. The platform is surveillance for the markets that can't afford Ring, can't trust cloud DVRs, and can't scrub through hours of CCTV.

---

## Hardware Constraint: Raspberry Pi 3 B+

**Device:** Raspberry Pi 3 B+
**RAM:** 1GB LPDDR2
**CPU:** ARM Cortex-A53 quad-core @ 1.4GHz (ARMv8 64-bit)

### The problem

Gemma 4 E2B at 4-bit quantization requires ~1.5GB of RAM for model weights alone — before the OS or application overhead. The Pi 3 B+ has 1GB. It cannot run on-device inference.

### Decision: hybrid edge architecture

The Pi 3 B+ acts as the **edge sensor node**:

- Captures images via the camera module
- Runs continuous software motion detection on the stream (pure-Go frame diff); optional GPIO trigger on pin 17 for a button or PIR
- Exposes presence events over HTTP (long-poll)

A laptop on the same local network acts as the **edge inference node**:

- Runs Gemma 4 E2B via Ollama
- Receives images from the Pi over local HTTP
- Sends WhatsApp messages via OpenClaw

Both nodes operate on the same local network with no cloud AI dependency. The system is offline-capable for inference — no inference data leaves the local network. WhatsApp delivery still uses the user's existing internet connection.

### Why this is still a valid edge story

- No cloud AI calls — Gemma 4 runs locally
- AI inference works on local Wi-Fi only; WhatsApp delivery uses the same internet connection the user already has
- Pi 3 B+ is the realistic prototype target for low-cost surveillance and accessibility devices in markets where flagship hardware isn't available. Production target is Pi 5 8GB (~$80) — collapses the hybrid back to single-device.

### Upgrade path

If a Pi 4 or Pi 5 with 8GB RAM becomes available, inference moves fully on-device with no application code changes — only the Ollama endpoint URL changes from `http://laptop-ip:11434` to `http://localhost:11434`. (4GB Pi 4 is insufficient — Gemma 4 E2B at 8k context needs ~7.8GB.)

---

## Track Selection

**Impact Track:** Digital Equity & Inclusivity
**Special Technology Track:** Ollama

### Rationale

- Deaf accessibility and security for small-business owners in emerging markets are both clear, human equity stories; same product reaches both.
- Ollama exposes a REST API — clean Go integration, no CGo, no cross-compilation issues
- LiteRT (Google AI Edge) was considered but requires CGo, which breaks ARM cross-compilation
- llama.cpp was considered but Ollama abstracts the same functionality with less setup cost

---

## Scoring Priorities

| Criteria | Weight | Implication |
|---|---|---|
| Impact & Vision | 40 pts | Lead with the human story — deaf user, perimeter awareness, WhatsApp |
| Video Pitch & Storytelling | 30 pts | The video carries the narrative. Demo quality matters more than code quality |
| Technical Depth & Execution | 30 pts | Code + writeup prove the demo is real, not faked |

The video is the product. The code proves it works.

---

## Ollama Setup Issues (2026-05-03)

### Problem 1: Ollama version too old for Gemma 4

`ollama pull gemma4:e2b` failed with:

```
Error: pull model manifest: 412:
The model you are attempting to pull requires a newer version of Ollama.
```

The installed version was 0.18.0 (from Homebrew). Gemma 4 requires 0.22.1+.

### Problem 2: Installer couldn't replace the running app

Downloading the new `.dmg` from ollama.com and dragging to Applications failed with "item is in use" — the old Ollama was still running as a background process.

### Resolution

1. `killall ollama` to stop the running process
2. `sudo rm /opt/homebrew/bin/ollama` to delete the old Homebrew binary
3. Open the new Ollama from Applications
4. Verify: `ollama --version` reports `0.22.1`

---

## Inference Laptop RAM Constraint (8GB)

The development laptop has 8GB of RAM. Gemma 4 E2B at the default 131,072-token context allocates an 8.9GB KV cache, which exceeds physical memory. Running inference at the default context forced macOS to swap to disk: latency rose to ~80 seconds, the laptop froze, and the WhatsApp Web socket timed out (status 408) before Gemma's reply was ready.

### Fix: custom `gemma4:e2b-8k` Modelfile

A scene description never needs more than a few hundred tokens of context. Capping `num_ctx` at 8,192 drops the KV cache footprint enough to bring memory usage to ~7.8GB.

```
ollama show gemma4:e2b --modelfile > Modelfile
echo "PARAMETER num_ctx 8192" >> Modelfile
ollama create gemma4:e2b-8k -f Modelfile
```

The variant shares the same weights as the upstream model — quality is unchanged. The trade-off is no support for very long multi-turn conversations, which we don't need.

This is a deployment-shaped decision, not a research one. The default Ollama configuration assumes a developer machine with abundant RAM. On the kind of consumer hardware GatemanAI targets — modest laptops in homes and small businesses — that default is wrong. A single Modelfile parameter, no retraining, takes the system from "crashes during inference" to "runs reliably on 8GB."

---

## Inbound Pull Required Two Concurrent Gemma Calls

The natural inbound flow ("user texts → describe + reply") requires two Gemma calls back-to-back: one for the vision tool, one for the agent's text reply. Even with the 8k variant, two calls in quick succession crashed the OpenClaw gateway under memory pressure on 8GB hardware.

### Fix: drop Gemma vision from the inbound path

The inbound live-capture tool sends the photo directly via the OpenClaw CLI and returns a tiny string (`"Photo sent."`). The agent's reply turn is now nearly free — it has nothing to reason about. The user gets the photo (the actually useful thing for a deaf user) and a one-line acknowledgment.

On a 16GB+ machine this constraint disappears and the description path can be re-enabled with a single config flip.

---

## Outbound Pipelining for Perceived Latency

Gemma 4 E2B vision inference takes ~30–80 seconds depending on cold/warm state. A naive `capture → describe → send` flow makes the user wait the full duration before they know what's happening — bad UX, bad demo.

### Fix: split the outbound flow into two messages

Each presence event runs in its own goroutine that:

1. Captures the photo from the Pi (~3 seconds)
2. Sends the photo to WhatsApp immediately (~3 seconds)
3. Runs Gemma in the same goroutine
4. Sends the description as a follow-up message when ready

The user sees the photo in seconds and the description arrives when it's done.

---

## Runtime Findings (2026-05-07)

Three constraints surfaced during end-to-end integration testing. None require code changes; all matter for operations and the writeup.

### 1. OpenClaw session state persists across gateway restarts

`channels.whatsapp.dmHistoryLimit` and `channels.whatsapp.historyLimit` only control inbound message buffering, not the agent's session context. OpenClaw persists session state to `~/.openclaw/agents/main/sessions/sessions.json`, which survives `openclaw gateway restart`. After several inbound queries, Gemma starts reusing cached context and short-circuits the tool call — replying "Photo sent." without actually invoking the live-capture tool.

The reliable workaround is to text `/new` before each test query. For the demo, this is acceptable. A production fix would require a custom OpenClaw plugin that scopes sessions per-message, or a wrapper that auto-issues `/new` before each inbound turn.

### 2. OpenClaw overrides the Modelfile's `num_ctx`

The custom `gemma4:e2b-8k` variant declares `PARAMETER num_ctx 8192` in its Modelfile. Ollama respects this when called directly (`ollama run gemma4:e2b-8k` loads at 8192 / 7.8GB). However, when OpenClaw issues a chat request, it sends its own `num_ctx` in the request body, and Ollama reloads the model at the requested context. `ollama ps` after an OpenClaw call shows `gemma4:e2b-8k` running at 131072 / 8.9GB.

The 8k variant still serves a purpose — direct calls from our Go code (the `mcpserver` and `gatemanai` vision path) honor the 8192 cap. But the OpenClaw agent calls do not. The system therefore runs at 8.9GB on an 8GB laptop — narrow operating envelope, but functional once stable. A real fix would require OpenClaw configuration to set or limit per-call `num_ctx`, which is not currently exposed in `~/.openclaw/openclaw.json`.

### 3. Unified-model decision and dual-model failure mode

The first integration architecture used two models: full `gemma4:e2b` for vision (assumed to need a larger context to fit image embeddings) and `gemma4:e2b-8k` for the agent's text reasoning. This collapsed under 8GB RAM. Each path switch forced Ollama to evict and reload — tens of seconds of thrashing per transition — and put the gateway in a fragile state that crashed mid-inference. The agent then responded with hallucinated text ("I do not have access to that information") because tool registration was lost across the restart.

A direct curl test against `gemma4:e2b-8k` returned a full vision description in 42 seconds, disproving the "vision needs full context" assumption. Gemma 4's SigLIP-based vision encoder produces a fixed ~256 visual tokens per image regardless of input resolution; this fits comfortably in 8,192. Unifying both vision and agent on `gemma4:e2b-8k` keeps a single model resident, eliminates swap thrashing, and makes the system stable enough to demo.

The premature dual-model optimization cost roughly half a day to diagnose and unwind. Lesson: test the cheaper assumption first.

---

## Software motion detection over OpenCV

### What we chose

Pure-Go frame differencing in `internal/motion`. Decode two JPEGs via `image/jpeg`, downsample to an 80px-wide grid, compute mean absolute pixel difference over the inner region (5% edge crop), threshold. ~120 lines, zero external dependencies, no CGO.

### Why not OpenCV

OpenCV gives proper algorithms — `BackgroundSubtractorMOG2`, contour detection, person-vs-cat classification, shadow rejection — that we don't currently need. The costs are real:

1. **CGO breaks cross-compile.** `gocv` requires CGO and the OpenCV C++ libs. Today we cross-compile from Mac to Pi ARMv7 in one line (`GOOS=linux GOARCH=arm GOARM=7 go build`). Adding CGO means either installing an `arm-linux-gnueabihf-gcc` toolchain on the Mac or building directly on the Pi with `libopencv-dev` (~200MB of apt dependencies, no longer self-contained).
2. **Gemma is the smart layer.** Our motion detector only has to answer "did the scene change?" — Gemma decides whether the change is worth telling the user about. If the diff fires on a shadow or a passing cloud, Gemma sees the frame and either describes nothing meaningful or returns empty; we already skip the description send in that case. Crude motion + smart classification is a clean division of labour.
3. **Dependency risk against deadline.** Five days to ship. Every new dependency is half a day of "why won't this linker work."

If we later need person detection vs animal detection, occupancy heatmaps, or face-aware ROIs, OpenCV earns its weight then. For now: pure stdlib.

### Edge crop

A subtle but real detail: the diff loop ignores the outer 5% of pixels on each side (configurable via `MOTION_EDGE_CROP`). Wind-blown branches, sun glare on the lens housing, and curtain movement disproportionately live on the frame margins. Cropping them drops false positives meaningfully without affecting sensitivity to anything happening in the monitored area.

---

## Retrospective queries: JSON lines over SQLite

### What we chose

`~/.gatemanai/events.jsonl` — one JSON line per event with `{id, timestamp, description, photo_path}`. JPEGs stored alongside in `photos/<id>.jpg`. `gatemanai` is the sole writer; `mcpserver` reads it to serve two new MCP tools (`recent_events`, `send_photo`). A janitor in `gatemanai` purges entries older than 7 days every hour.

### Why not SQLite

Volumes are too small to justify a database. At an active entrance of 50 events/day × 7-day retention = 350 records, ~25MB of photos and ~90KB of metadata. SQLite would buy us indexed search and FTS, neither of which we use today — `recent_events` does a linear scan of a file that's never more than ~300KB. SQLite would also pull in CGO (`mattn/go-sqlite3`), reopening the cross-compile pain we resolved when we picked pure-Go motion detection. A `modernc.org/sqlite` pure-Go fork exists but adds ~3MB to the binary size with no current benefit.

### Single-writer invariant

`gatemanai` is the only process that writes the log. `mcpserver` is read-only and may have multiple instances if OpenClaw re-spawns it. The single-writer constraint is what makes the append-only design safe: writers never collide with each other, and readers tolerate concurrent appends because each event is a single line that's fully flushed before the next.
