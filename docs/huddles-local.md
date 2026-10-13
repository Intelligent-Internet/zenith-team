# Huddles and voice Agents on your machine

Run calls, screen sharing and a voice Agent entirely locally: a LiveKit media
server in Docker, the Go API, the app, and the Python voice worker. No LiveKit
Cloud account or public tunnel is needed. The only external service is OpenAI,
and only for the voice Agent.

Production uses LiveKit Cloud and a deployed worker instead; that setup is in
[`../voice-agent/README.md`](../voice-agent/README.md#livekit-cloud-managed-media-and-voice-worker).

```text
 browser / desktop app ──WebRTC──▶ LiveKit (Docker, :7880) ◀──WebRTC── voice worker (Python)
          │                              │ webhook                        │
          └────────── REST ─────────▶ Go API (:8788) ◀── claim/heartbeat ─┘
```

## Prerequisites

- The [quickstart](quickstart.md) prerequisites, with **Docker**.
- **[uv](https://docs.astral.sh/uv/)** for the voice worker.
- For the voice Agent only: an **OpenAI API key** whose project can use
  `gpt-live-1`. Calls between people need no key.

## 1. Start the services

Each long-running command gets its own terminal.

Huddles are off by default. Turn them on once, in a file git ignores:

```bash
echo "HUDDLES_ENABLED='true'" >> docker/dev.local.env
```

Then start the stack as usual:

```bash
make infra-up       # PostgreSQL, Redis, Silo, and LiveKit (ports 7880-7882) while Huddles are on
make server         # Go API on :8788: docker/dev.env, then docker/dev.local.env
make frontend       # web app on :5173  (or: cd frontend && pnpm electron:dev)
```

`docker/dev.env` already points the API at this LiveKit and turns voice Agents
on with a development workload secret. `HUDDLES_ENABLED` is a server setting:
when it is false, the API ignores every LiveKit and voice Agent setting, and the
app hides the Huddle button.

Using `make server-local` instead? Copy the `LIVEKIT_*` and `HUDDLE_AI_*` lines
from `docker/dev.env` into `server/.env`.

## 2. Try a call between two people

Sign up two accounts in the same Company — for example one in the browser and
one in a private window or the desktop app. Open a channel, choose **Huddle**,
then **Start huddle**; the other account is called. Camera and screen sharing
work the same way. If this works, media and the LiveKit webhook are set up.

## 3. Run the voice worker

```bash
make voice-setup    # creates voice-agent/.env from .env.example and installs dependencies
```

Open `voice-agent/.env` and set `OPENAI_API_KEY`. Every other value already
matches the local stack. Then check the setup and start the worker:

```bash
make voice-doctor   # checks the file, LiveKit and its keys, the API, the shared secret
make voice-start    # foreground; keep this terminal for its logs
```

`make voice-status` and `make voice-stop` inspect and stop it from another terminal.

## 4. Invite a voice Agent into a call

1. Pick an Agent. Voice is on by default; **Agents** → the Agent → **Voice**
   changes its voice or turns it off.
2. Start a Huddle and join it.
3. **Invite Agent** → **AI budget**: as a Company administrator, set a monthly
   allowance above $1 (the default of $0 keeps voice Agents off).
4. **Invite Agent** again, choose the Agent and confirm. It joins, greets once,
   and listens. Ask it to leave, then open its transcript card in the call chat.

Real calls spend from your OpenAI project; the Company allowance caps what one
Company can use.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| `make infra-up` fails: port 7880 is already allocated | Another LiveKit is running, often a native `livekit-server`. Run only one. |
| No Huddle button | `HUDDLES_ENABLED='true'` is missing from `docker/dev.local.env`, or the API was not restarted after adding it. |
| A call never connects, or stays "Connecting" | `make livekit-logs`. LiveKit advertises `127.0.0.1`, so only this machine can join (see below). |
| People join but in-call chat is refused | The webhook did not reach the API: is `make server` running on :8788? |
| Invite Agent shows unavailable or budget errors | The Company allowance is $0 or below $1, or voice is off for that Agent. |
| The Agent never joins | `make voice-doctor`, then the worker's terminal. A `secret` mismatch is reported there. |
| Screen sharing does nothing on macOS | Allow **Screen & System Audio Recording** for the app that launched Electron (your terminal in development). |

## Notes

- **Other devices.** ICE advertises `127.0.0.1`, so phones or other machines
  cannot reach media. To test across your LAN, change `--node-ip` in the
  `livekit` service of `docker/compose.yml` to this machine's LAN address, and
  set `LIVEKIT_URL` to that address too; the API hands it to clients.
- **Native LiveKit instead of Docker.** Install `livekit-server` (for example
  `brew install livekit`) and run
  `livekit-server --config docker/livekit.dev.yaml` after `make livekit-down`
  (both use the same ports). Same keys and webhook.
- **Stopping.** `make livekit-down` stops LiveKit; `make infra-down` stops it
  with the rest of the stack.
- **Renamed settings.** `LIVEKIT_*` and `HUDDLE_AI_*` were `BOARDLY_LIVEKIT_*`
  and `BOARDLY_HUDDLE_AI_*`. The old names still work; when both are set, the
  new one wins. The API also accepts a `ws(s)://` `LIVEKIT_URL`, so it can share
  one value with the voice worker.
- All keys and secrets above are for development only. Never reuse them.
