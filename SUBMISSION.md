# GatemanAI
 
## Premise
 
GatemanAI is built for the half of the world flagship AI ignores. Lagos, Nairobi, Dhaka, São Paulo, places with WhatsApp-shaped infrastructure, $50-phone realities, and intermittent power and internet. Every architectural choice in this project comes from constraints those places impose.
 
Most people don't have a monthly phone plan, so cloud AI is a non-starter. Phones are mid-tier, so on-device flagship AI is also out. Data cost and storage mean apps don't get installed; an APK won't reach them. Internet drops, so cloud-dependent systems are brittle. Household hardware budget is closer to $50 than $700.
 
Every choice here follows from those constraints: WhatsApp instead of a custom app, WhatsApp Business API for distribution, local Gemma on an edge device already in the home, a model variant tuned for 8GB-RAM consumer hardware, a $35 Pi 3 B+ as the prototype sensor with a clean upgrade path to a single Raspberry Pi 5 or better in production.
 
GatemanAI compresses video surveillance into queryable events. A local AI watches the camera in real time, summarises what it sees as text plus a key frame, and persists those summaries as an event log the user queries over WhatsApp. Storage drops from gigabytes a day to kilobytes. Retrieval becomes a conversation, not a video scrubber. Bandwidth becomes a WhatsApp message.
 
This project is shaped by the place, not by the model.
 
## Motivation: one platform, multiple communities
 
GatemanAI gives you environmental awareness: the ability to know who has entered a space you care about, without watching live video. The first deployment is one camera at an entrance. The architecture extends to any perimeter (a gate, a fence, a roof line, a shop floor) and to multiple cameras feeding a single event log.
 
### The bakery manager
 
In the community around Yabatech Polytechnic in Lagos, gun violence is a painful reality. The school's gate was locked down for over a year, only to see another incident shortly after reopening. A community bakery manager I spoke with described the daily fear of walking around the school back gate area. Armed forces guard the school; civilians around it have no protection. CCTV and smart cameras are either too expensive or require infrastructure outside their reality.
 
### The deaf user
 
Growing up, I always wondered how my mum's deaf friend would raise a kid like me. For deaf residents and parents, "silent risks" are common: not knowing when someone enters their environment, when a child is sneaking out or in danger, or when a stranger is at the gate. Standard solutions assume flagship phones and broadband most people don't have. GatemanAI turns presence into awareness through the messaging tool people already use, with a snapshot and description of who is there, not just a buzz or a flash.
 
Different communities, same unmet need: security for one, accessibility for the other. Same architecture, same messages, same hardware.
 
## System Architecture (for the hackathon)
 
Hybrid edge, three layers:
 
- **Perception.** Raspberry Pi 3 B+ with a camera module. Pure-Go frame-difference motion detection on the stream (no OpenCV); optional GPIO listener for button or PIR.
- **Cognition.** Gemma 4 E2B via Ollama on a connected laptop. Vision plus function-calling.
- **Distribution.** WhatsApp delivery. OpenClaw powers the prototype for rapid development; production migrates to the official Meta WhatsApp Cloud API for compliance and stability.
### Operational modes
 
**Push.** On a presence event the camera host captures a frame and sends it via WhatsApp immediately. In parallel, Gemma analyses the scene and sends a descriptive follow-up.
 
**Pull.** Users message "who is outside?" for a live frame, or "what happened in the last hour?" for a retrospective. Gemma routes between three MCP tools (live capture, recent-event listing, and photo retrieval by event ID) to either grab a fresh frame or browse the past 7 days.
 
### MCP over WhatsApp
 
The pull flow reveals a useful pattern: with an MCP-speaking agent and a WhatsApp gateway, any function-callable tool becomes reachable from any phone through an interface people already know. No new app, no account creation. The Meta WhatsApp Cloud API handles delivery compliantly at scale.
 
### Integration surface: works with cameras already on the wall
 
The capture path is a thin HTTP contract (/capture, /presence); picapture is one implementation running on the Pi. A second binary, rtspsource, implements the same contract against any RTSP stream, designed as a drop-in for existing IP cameras without requiring new hardware. Point gatemanai at either source via PI_URL and the rest of the pipeline runs unchanged.
GatemanAI becomes the alerting and accessibility layer on top of cameras people already own. The existing recording keeps running; we add the WhatsApp push, the AI description, and the "what happened today?" retrospective the existing app cannot answer.

The Go codebase compiles to a small set of binaries: picapture (cross-compiled for the Pi), rtspsource, gatemanai, mcpserver, doctor, allow. Every external call uses context.Context with timeouts and backoff retry so a Wi-Fi flap doesn't lose an event.
 
## Challenges
 
### Hardware iteration: from Pi-only to hybrid
 
The original plan ran everything on the Pi. Gemma 4 E2B at 4-bit quantisation needs ~1.5GB of weights; the Pi 3 B+ has 1GB. The model wouldn't load. That forced the split: Pi as the body, laptop on the same Wi-Fi as the brain. Most design choices downstream (the long-poll, the MCP integration, the 8k Modelfile) follow from it. Production deployment on a Pi 5 8GB collapses the hybrid back to a single device.
 
### 8GB laptop RAM and the `gemma4:e2b-8k` Modelfile
 
Ollama loads Gemma 4 E2B with a 131k-token context. `ollama ps` reports 8.9GB on an 8GB laptop, larger than physical memory. The system swapped, froze, and the WhatsApp socket timed out. Fix: a custom Modelfile pinning `num_ctx 8192`. Same weights, smaller KV cache, memory drops to ~7.8GB, inference finishes inside the WhatsApp keepalive window. Ollama's default assumes a developer machine, not the consumer hardware GatemanAI targets.
 
### Inbound pull required two consecutive Gemma calls
 
The natural inbound flow needs two Gemma calls back-to-back: vision tool plus agent reply. Two in succession crashed the gateway under 8GB pressure. Fix: the live-capture tool sends the photo directly via the WhatsApp delivery layer and returns "Photo sent." so the agent's reply is nearly free. The user gets the photo and a one-line ack. On a 16GB+ machine the description path is one config flip.
 
### Outbound pipelining for perceived latency
 
Gemma 4 E2B vision takes ~30 to 80 seconds. A naive capture, describe, send flow makes the user wait the full duration. Fix: each event runs in its own goroutine that sends the photo immediately (~3s), then runs Gemma and sends the description as a follow-up.
 
### Hardware sourcing and the pivot to motion detection
 
I had planned a momentary push button; the top two Lagos electronics stores stocked neither. That forced a useful pivot: software motion detection on the camera stream, pure-Go frame differencing with an inner-region crop to ignore wind, branches, and sun glare. No new hardware, no OpenCV/CGO. A PIR sensor remains a drop-in option (`GPIO_TRIGGER_EDGE=rising`).
 
### Retrospective queries
 
Push only answers "what is happening now." Real awareness also needs "what happened earlier": the bakery owner who stepped out, the deaf user who wakes up and wants the overnight. Events persist to `~/.gatemanai/events.jsonl` with description and JPEG. Two MCP tools let users ask "what happened in the last hour" then "show me the 3pm one." A janitor purges entries older than 7 days.
 
### Engineering for unstable infrastructure
 
Lagos means engineering for power and ISP failures: every capture/vision call wraps in exponential-backoff retry; the poll loop doubles its sleep on error (1s to 30s) and resets instantly on recovery; a timestamped event queue (cap 10, 5-min TTL) prevents drops during blips without surfacing stale events.
 
### WhatsApp delivery layer
 
The prototype uses OpenClaw for rapid local iteration without API approval delays. Production deployment migrates to the official Meta WhatsApp Cloud API, a messaging layer swap that leaves the Go architecture, Gemma integration, and MCP tooling unchanged. This removes platform risk and positions GatemanAI for compliant commercial deployment.
 
## Personal Story
 
The Pi powering GatemanAI came back with me from Austin. A former manager gave it to me when it was no longer needed. I spent two years at Eagle Eye Networks on security camera infrastructure and saw exactly what enterprise surveillance couldn't reach. Back in Lagos, that gap was impossible to ignore.
 
WhatsApp was always there. OpenClaw revealed it as an agentic interface for physical AI. The pattern holds in production via the official Meta WhatsApp Cloud API.
 
## From wedge to platform
 
The entrance camera is the wedge. The platform is agentic surveillance for markets traditional security ignores. Three things make this buildable now:
 
- **Local vision-language models got small enough.** Gemma 4 E2B fits 8GB hardware. The trend continues; next year's E2B will be sharper and faster on the same memory.
- **WhatsApp is the distribution layer.** The Meta WhatsApp Cloud API makes any tool reachable from any phone through the messaging app people already use, compliantly and at scale.
- **Pi 5 8GB  or higher.** Single-device production deployment is cost-feasible.
What's next: multi-camera deployments (a small shop with cameras on the entrance, the till, the back exit), richer event classification beyond pixel diff (loitering, vehicle stopped, unfamiliar face), a two-tier alert system that separates the fast motion ping from the slower scene description, and outdoor enclosures with PoE or solar for perimeter use.
 
At scale, these deployments generate data from urban environments missing from current datasets, closing a vision gap for the Global Majority.