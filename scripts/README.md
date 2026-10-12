# `scripts/` — local debugging helpers

Local-only debugging guidance. Diagnostic output is intentionally unredacted
and can contain workspace paths or provider configuration, so it must stay on
your machine; no process wrapper scripts remain in this directory.

## Debugging a run

The daemon is the thing that executes agents, so most debugging goes through
the `zen` CLI rather than a script:

```bash
cd server && go run ./cmd/zen daemon start --foreground
```

Running in the foreground prints the daemon's own log to your terminal:
registration, claims, prepare, provider launch, and terminal reporting. The
other useful subcommands:

| Command | Purpose |
| --- | --- |
| `zen daemon status --output json` | Registration and heartbeat state |
| `zen daemon logs -f` | Follow the rotating daemon log |
| `zen daemon probe-runtimes` | What provider CLIs were found, their versions, and how they were resolved |
| `zen runtime list` | What the server thinks is registered and online |

Requirements: Go and the selected provider CLI on your PATH. Codex also
requires a local `codex login`; authentication is read from `$CODEX_HOME`,
defaulting to `~/.codex`.

## Desktop debugging

`make debug-app` builds the current `zen` and `boardly-mcp` binaries and
starts the Electron app with its Desktop-owned daemon environment. The daemon
is detached, so inspect it with the bundled CLI and the exact `BOARDLY_HOME`
shown by Desktop diagnostics. For example:

```bash
BOARDLY_HOME=/path/from/desktop/diagnostics zen daemon logs -f
```

The retired process shim and its Delve/log wrappers have no replacement: they
depended on Electron owning the daemon as a foreground child, which is not the
current lifecycle.
