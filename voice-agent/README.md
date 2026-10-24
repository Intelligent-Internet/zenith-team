# Zenith Team voice worker

The voice worker lets an Agent join a Huddle (a Zenith Team call), listen and
talk. It is a small Python service, also called Helia, built on
[LiveKit Agents](https://docs.livekit.io/agents/). It is optional: calls
between people work without it.

- Speech uses OpenAI's realtime model `gpt-live-1`, with the invited Agent's
  chosen voice.
- Harder questions are handed to `gpt-5.6-luna` through the Responses API.
- Dependencies are pinned in `pyproject.toml` and `uv.lock`
  (`livekit-agents[openai]==1.8.3`).

A voice session gets no daemon, no Run, no Connector, no web tool and no chat
history from before the invitation. What it can read is listed under
[What a voice Agent can do](#what-a-voice-agent-can-do).

## How it fits together

```text
 browser / desktop app ──WebRTC──▶ LiveKit ◀──WebRTC── voice worker (this folder)
          │                          │ signed webhook          │
          └──────── REST ───────▶ Zenith Team API ◀── claim / heartbeat / tools
                                                      (workload secret)
```

1. People join a Huddle through your LiveKit server. LiveKit tells the API who
   joined, through a signed webhook to `/api/v1/internal/livekit/webhook`.
2. A person invites an Agent. The API asks LiveKit to start a job named
   `zenith-helia` in that room. The job's only metadata is an attempt id.
3. The worker claims that attempt from the API with the shared workload
   secret (`/api/v1/internal/huddle-ai/<attempt>/…`), then heartbeats while it
   talks. The API decides, on every claim and heartbeat, whether the Agent may
   still be there.

## What you need

- **A LiveKit server** that browsers and the worker can reach: your own
  `livekit-server`, or LiveKit Cloud.
- **An OpenAI API key** whose project can use `gpt-live-1` and
  `gpt-5.6-luna`. Real calls spend from this project.
- **The Zenith Team API**, reachable from the worker, and from LiveKit for the
  webhook. In production that is your HTTPS address.
- **Python 3.13 and [uv](https://docs.astral.sh/uv/)** to run from source, or
  Docker to run the image.

## Configure the server

Set these on the Zenith Team server (`deploy/.env` for a self-hosted install).
They are described in the
[environment variables](../docs/content/docs/self-hosting/environment-variables.mdx#huddles-voice-rooms)
page.

| Variable | Value |
| --- | --- |
| `HUDDLES_ENABLED` | `true` (the default) |
| `LIVEKIT_URL` | Your LiveKit server, such as `wss://livekit.example.com` |
| `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET` | A LiveKit key pair |
| `HUDDLE_AI_ENABLED` | `true` to allow voice Agents |
| `HUDDLE_AI_WORKLOAD_SECRET` | At least 32 random bytes, shared with the worker (`openssl rand -hex 32`) |

Then point LiveKit's webhook at
`https://<your server>/api/v1/internal/livekit/webhook`, signed with the same
API key the server uses. A different key makes every join fail. For a
self-hosted `livekit-server`, `docker/livekit.docker.yaml` shows the webhook
setting.

## Configure the worker

The worker reads its settings from the environment:

| Variable | Value |
| --- | --- |
| `LIVEKIT_URL`, `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET` | The same LiveKit server and key pair |
| `OPENAI_API_KEY` | Your OpenAI key |
| `ZENITH_API_URL` | The Zenith Team API address, such as `https://zenith.example.com` |
| `HUDDLE_AI_WORKLOAD_SECRET` | The same secret as the server |

The worker also accepts the older name `ZENITH_TEAM_HUDDLE_AI_WORKLOAD_SECRET`.
Keep real values in your secret manager or shell. Never commit them, and never
reuse the development values from `.env.example` outside your own machine.

## Run it on your machine

The whole stack can run on one machine, with LiveKit in Docker and no public
address. [docs/huddles-local.md](../docs/huddles-local.md) is the full guide.
In short, from the repository root:

```sh
echo "HUDDLES_ENABLED='true'" >> docker/dev.local.env   # once
make infra-up        # includes LiveKit; then `make server` and `make frontend`
make voice-setup     # creates voice-agent/.env; set OPENAI_API_KEY in it
make voice-doctor    # checks LiveKit, its keys, the API and the shared secret
make voice-start     # runs the worker in this terminal
```

`voice-agent/.env.example` already matches `docker/dev.env` and the local
LiveKit config, so only the OpenAI key needs filling in.

### Start, check and stop the local worker

```sh
make voice-start   # foreground; keep this terminal open for the logs
make voice-status  # from another terminal; no model call, no secrets
make voice-stop    # ask this checkout's worker to shut down cleanly
```

- `voice-start` never runs in the background. A lock per user and checkout
  stops a second local worker from starting, and port 8081 must be free.
- `voice-status` shows the worker's process id, the Git revision it started
  from, its terminal, and its latest job and room.
- `voice-stop` asks the worker, over a private socket, to stop. It waits up to
  45 seconds for calls to close and usage to be reported. It never kills a
  process by name or port. Avoid a second `Ctrl-C` while it cleans up.

The worker keeps only small control files under `/tmp/zenith-voice-*`. No
tokens, speech, prompts or product data are saved there. Stopping the worker
ends the voice sessions, not the human call.

## Run it in production

Production runs `python agent.py start`, which drains on shutdown: after
`SIGTERM` it takes no new calls and gives running calls up to 65 minutes to
finish. Make sure your platform allows that long a shutdown, or expect a
visible interruption and a recovery. The health check is `GET /` on port 8081.

### Docker

Build from the repository root. `voice-agent/.dockerignore` keeps the build
context to the files the image needs.

```sh
docker build -t zenith-helia:0.1.0 voice-agent
docker run --env-file <your env file> -p 8081:8081 zenith-helia:0.1.0
```

CI builds and tests this image on every change to `voice-agent/`. Each
release publishes it to the project's private Artifact Registry, beside the
server image and tagged like it; nothing deploys it.

### Kubernetes

`deploy/voice-agent/deployment.yaml` is a starting point. It asks for 2 CPUs
and 4 GiB per pod, takes at most two rooms per process, runs as a non-root
user, and allows a 3,935-second shutdown. Its replica count is zero, so
nothing runs until you scale it up. Before you do:

1. Push the image to your registry and put its digest in the manifest.
2. Create a Secret named `zenith-helia` that holds the worker variables above.
3. Turn on `HUDDLE_AI_ENABLED` on the server and give a Company an AI budget.

### LiveKit Cloud

LiveKit Cloud can host the worker for you. Install the
[LiveKit CLI](https://docs.livekit.io/home/cli/) (`lk`), create the secrets
file `.env.cloud` described below, and, from `voice-agent/`:

```sh
lk agent create --project <your-livekit-project> --secrets-file .env.cloud
lk agent deploy --project <your-livekit-project>
lk agent status --project <your-livekit-project>
lk agent logs   --project <your-livekit-project>
```

`lk agent create` writes `livekit.toml`, which names your LiveKit project and
agent. That file is yours, so git ignores it. LiveKit Cloud supplies
`LIVEKIT_URL`, `LIVEKIT_API_KEY` and `LIVEKIT_API_SECRET` itself, so the
secrets file holds only the values below. Git already ignores `.env.*` files
here; keep the file readable only by you (mode `600`).

```dotenv
OPENAI_API_KEY=<your OpenAI key>
HUDDLE_AI_WORKLOAD_SECRET=<the same secret as the server>
ZENITH_API_URL=https://zenith.example.com
```

After you change the secrets file, run
`lk agent update-secrets --project <your-livekit-project> --secrets-file .env.cloud`.
That restarts the hosted worker. Stopping `lk agent logs` does not stop it,
and `make voice-stop` does not manage it.

The API must be reachable from LiveKit Cloud. For a short test with an API
that is not public, `docker/ngrok.voice.yaml` is an example tunnel policy that
lets through only the voice routes and the webhook. Use a real HTTPS address
for anything beyond a test.

## Turn voice on for a Company

1. **Voice per Agent.** While the server has `HUDDLE_AI_ENABLED=true`, voice
   is on for every Agent unless someone turns it off under
   **Agents → the Agent → Voice**. That tab also sets the speaking voice and
   how the Agent takes part. The Agent's shared instructions supply its
   persona.
2. **A budget.** A Company administrator sets a monthly allowance in US
   dollars under **Invite Agent → AI budget**. The default is zero, which keeps
   voice Agents off. Calls between people are never affected.
3. **The invitation.** After a person has joined the call, the person who
   started it or a Company administrator chooses **Invite Agent**, picks an
   Agent and confirms. **Allow shared Company context** decides whether the
   Agent may read Company information (on by default). **Remove** the Agent to
   end its part in the call.

The Agent greets once, then listens. Messages from before the invitation are
not shared with it. Each invitation freezes the Agent's name, look,
instructions and voice settings; edits apply to the next invitation.

## What a voice Agent can do

- **Leave** when asked (`leave_meeting`).
- **Read shared Company context**, when the invitation allows it:
  `get_company_context`, `list_projects`, `search_tasks` and `get_task`. Only
  the shared Company profile, active Projects and Task conversations are
  visible. Private chats, run transcripts, workspace memory and credentials
  are not. The API checks the invitation, the Agent, who is in the call and
  the budget on every read, and limits how many reads a turn and a call may
  make.
- **Propose work** (`propose_task_work`). A proposal appears under
  **Work proposals** beside the call chat. Nothing happens until a person
  reviews it and chooses **Confirm and create Task**, which saves a Task in
  Backlog without starting a Run.
- **Report on submitted work** (`get_work_status`, `get_work_result`). It can
  read a confirmed proposal's status and the verified final answer of its
  finished Run, never the Run's transcript. It may also mention, briefly and
  at a quiet moment, that submitted work needs a person or has a result.

When the Agent leaves, a **Transcript** card appears in the call chat. It is
built from the voice model's own transcript events; no separate speech-to-text
runs and no audio is kept. The last words can be missing if the call ends
abruptly; the card says so.

## Authority and recovery

- A claim takes one attempt at a time; a repeated job cannot open a second
  model session.
- The worker renews a 30-second lease every five seconds. A watchdog stops a
  stuck process before the lease runs out. The API, not the LiveKit SDK,
  decides when to reconnect, and stops after five failures.
- Only admitted people's microphones reach the model. Cameras, screen shares
  and other AI audio never do. People can talk while the Agent speaks.
- Removing the Agent, turning voice off, revoking a membership or running out
  of budget stops the session within its lease. The human call carries on.

## Costs

The API reserves money before a voice session starts, in steps of 10⁻¹⁰ US
dollars. It holds the remaining voice time plus $1 for reasoning, so a new
call needs at least $1 plus one second of voice in the budget. The price table
lives in `server/cmd/server/app.go`: $0.05 per minute of `gpt-live-1`, and
$0.20 / $0.02 / $1.20 per million input / cached / output `gpt-5.6-luna`
tokens. If prices or models change, update that table and the worker's
reasoning guard in `agent.py` together.

Each session reports its seconds and tokens; replays are not counted twice.
The recorded figures are estimates for the budget, not your OpenAI invoice.
Set a spending limit on the OpenAI project as well.

## Check your change

```sh
make voice-gate   # ruff lint, ruff format check and the Python tests (needs uv)
```

The Python tests use local fakes and never call a paid model. The server side
of voice work has PostgreSQL-backed tests too:

```sh
cd server
go run ./tests/testpostgres go test -race -tags=integration \
  ./internal/task ./internal/conversation ./internal/handler \
  -run 'Test(SubmitHuddle|Huddle)' -count=1
```

A real call is still the only check of audio quality, timing and model
access. It spends from your OpenAI project and the Company budget.

## Independent AI notes

The worker also accepts `notes:<attemptId>` dispatches. These are silent
capture connections, separate from an invited voice Agent. Update the server
and the worker together, and update the worker before you turn the feature on:
an older worker cannot serve notes.

Server settings (off by default):

```dotenv
HUDDLE_AI_ENABLED=true
HUDDLE_AI_WORKLOAD_SECRET=...   # the same 32+ byte secret as the worker
HUDDLE_NOTES_RECAP_API_KEY=...  # optional: the server's own key for recaps
HUDDLE_NOTES_RECAP_MODEL=...    # a model that supports Responses JSON output
```

Huddles and LiveKit must be set up too. The worker uses its own
`OPENAI_API_KEY` for transcription, and that key stays in the worker.
`HUDDLE_AI_ENABLED` turns on both voice invitations and independent notes.

Notes are on by default for each Company, with 30-day retention and 100
audio-stream hours per month. Admins change these limits in the advanced notes
settings, and a policy an admin already saved keeps its values. A Huddle keeps
the capture mode it started with, so start a new Huddle after you change
settings.

When someone stops notes, the worker saves what it still holds and the app
shows "Saving final words" for at most 20 seconds. Retries never extend that
limit or duplicate text. Very short last words are padded with silence, so
nothing is read from later microphone audio. If the provider times out or
access is revoked, the notes are marked incomplete. Ending the call or losing a
microphone can lose the last few words. No audio is written to disk.

Automated tests use made-up transcripts, so check a release with a real call:
notes with people only; inviting and removing a voice Agent while notes keep
running; stop and restart; a worker disconnect; ending the call; editing a
recap with sources; and creating a confirmed task. Check audio quality and
overlapping speakers too.
