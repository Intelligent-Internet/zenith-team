# Roadmap

Where Zenith Team is going next, and where you can help.

**Last reviewed:** 2026-10-21 · **Next review:** with the next minor release, and at least once a month · **Owners:** the maintainers ([CODEOWNERS](.github/CODEOWNERS))

This is our direction, not a promise. Items have no dates, and plans change as
we learn. Status follows the `main` branch; packaged versions are on
[Releases](https://github.com/Intelligent-Internet/zenith-team/releases).

**Status:** ✅ shipped · 🚧 in progress · 📋 planned · 💭 exploring (design first)

Each item gets one tracking issue with the `roadmap` label. That issue holds
the details and the discussion; this file stays one line per item.

## Where things stand

Zenith Team is pre-1.0. The core works today. Some parts need extra setup, and
a few are still early.

| Area | Today |
| --- | --- |
| Companies, team roles and Project access | Available |
| Agents, tasks, runs and chat: task chats, direct messages, channels and threads | Available |
| Company Plan, Projects with pages and canvas, Company Wiki, Whiteboards | Available |
| Workflows catalog and Schedules | Available |
| Approvals, questions and the activity feed | Available |
| Analytics and usage | Early: cost covers only some models |
| Company Mail and email sign-in codes | Needs an email provider (Resend today) |
| Huddles: calls, AI voice and notes | Needs LiveKit; the AI parts need an OpenAI key |
| Google sign-in, social accounts, publishing outputs | Needs your own provider apps |
| Zenith Studio tools | Needs the hosted Studio service |
| Desktop app | Early: macOS and Linux, built from source |
| `zen` CLI and daemon | macOS and Linux; Windows is planned |
| Agent sandbox that keeps sign-in files away from agents | macOS; Linux and Windows are planned |
| Self-hosting | Docker Compose |

## Priorities

In order. When two items compete, the higher theme wins.

1. **Self-hosting that is easy and safe to run.** A shared server needs the
   controls an operator expects.
2. **Every platform.** Windows, a Linux sandbox, and a desktop app anyone can
   install.
3. **Runs and chat you can rely on.** Clear runtime status, safe workspaces,
   and chat that survives a dropped connection.
4. **A good place to contribute.** Rendered docs, checks that catch mistakes,
   and clear starter tasks.
5. **New ways to work.** Notifications, wider search, chat platforms and
   single sign-on.

## Now

The first work after the open-source launch.

- 📋 **Desktop app that connects to any server.** Choose your server on first launch, install from GitHub Releases, and get updates from Releases. `area:desktop` · `help wanted`
- 📋 **`zen` and the daemon on Windows.** Three Unix-only calls block the Windows build today; fix them, then add a Windows build to CI. `area:cli` · `help wanted`
- 📋 **Operator controls.** A setting to turn off public sign-up, an admin command to create users and reset passwords without email, and sign-in rate limits. `area:server` · `area:self-host` · `help wanted`
- 📋 **Documentation site.** Render the docs and check every link in CI. `area:docs` · `help wanted`
- 📋 **Email through your own mail server.** Send sign-in codes, invitations and password resets over SMTP, not only through Resend. `area:server` · `help wanted`

## Next

Planned for the next releases. Most need a short design note in the issue
before code.

- 📋 **Agent sandbox on Linux.** Agents on Linux run without the sign-in-file protection macOS has. `area:daemon` · `help wanted`
- 📋 **Honest runtime status.** Show a computer as ready only when its agent CLI is signed in and able to run, not just online. `area:daemon` · `area:web`
- 📋 **Chat that survives reconnects.** Safe message retries and catch-up after a dropped connection. `area:web` · `area:server`
- 📋 **Safer workspaces.** Each run gets its own files, and continuing earlier work never mixes in another run's files. `area:daemon` · `needs-design`
- 📋 **Notifications in the browser and by email.** `area:web` · `area:server` · `help wanted`
- 📋 **Search across everything.** Wiki pages, Projects, files and Agents, not just tasks and chats. `area:server` · `area:web` · `help wanted`
- 📋 **Sessions and devices.** See where you are signed in, and sign out everywhere. `area:server` · `area:web`
- 📋 **Diagnostics.** Copyable request IDs in error messages, opt-in crash reports sent to your own server, and a `zen doctor` bundle to attach to an issue. `area:server` · `area:cli` · `area:desktop`
- 📋 **Backups and moving servers.** Backup and restore commands for the Compose stack, then company export and import. `area:self-host` · `help wanted`
- 📋 **Huddles in the self-host stack.** LiveKit and the voice worker in Docker Compose, and a published voice image. `area:self-host` · `help wanted`
- 📋 **Real costs in Analytics.** Cost for every model, and how long each run took. `area:server` · `area:web`

## Later

Things we want, with open design questions. Start with a
[Discussion](https://github.com/Intelligent-Internet/zenith-team/discussions)
before any code. `needs-design`

- 💭 **Team acceptance of finished work.** Several people review and accept a task's result together.
- 💭 **One list of everything waiting on you.** Approvals, questions and reviews in one place.
- 💭 **Publishing app results.** Share an app an Agent built, through a safe preview.
- 💭 **Versioned files as task inputs.** Pin the exact version of a file a task should use.
- 💭 **Wiki suggestions from Agents.** Agents propose changes, and people review them.
- 💭 **What each Agent can see.** Clear limits on which chats and shared work an Agent reads.
- 💭 **Chat platforms.** Talk to your Agents from Telegram first, then Slack.
- 💭 **Feature switches for operators.** Turn features off on your server without a rebuild.
- 💭 **Single sign-on and two-step sign-in.** OIDC or SAML, SCIM, and MFA.
- 💭 **Workflows you write and share.** Company-made Workflows, and contributing to the catalog from this repository.
- 💭 **Every language and every user.** Translations, an accessibility pass, and a JavaScript linter.
- 💭 **Agent sandbox on Windows.**

## Not planned for now

Ask in a Discussion if you need one of these. Strong demand can change this
list.

- **More agent CLIs in core.** Zenith Team runs Claude Code and Codex. Adding another CLI needs a maintainer decision first.
- **Billing and payments.** The open-source project has no billing.
- **Zenith Studio itself.** The Studio tools connect to a hosted service. Studio is not part of this repository.

## Recently shipped

In the first public version.

- ✅ **Self-hosting with Docker Compose**, with a guide and generated secrets.
- ✅ **Workflows**: 145 ready-made Workflows you can run.
- ✅ **Schedules** for recurring work.
- ✅ **Company Wiki** with weighted search and Agent authorship.
- ✅ **Projects** with pages, a canvas and presentations, and **Whiteboards** made in chat.
- ✅ **Team roles and per-person Project access.**
- ✅ **Huddle notes** with recaps, when Huddles are set up.
- ✅ **Analytics** and **custom Skills**.

The pull request that ships an item moves its line here. Lines older than two
releases move to the release notes.

## How to help

- **Pick a starter task.** Look for
  [`good first issue`](https://github.com/Intelligent-Internet/zenith-team/labels/good%20first%20issue)
  and
  [`help wanted`](https://github.com/Intelligent-Internet/zenith-team/labels/help%20wanted).
- **Claim it.** Comment on the issue, and a maintainer assigns it to you. If an
  assigned issue is quiet for 14 days, it goes back to the pool.
- **Bring a bigger idea.** Open a
  [Discussion](https://github.com/Intelligent-Internet/zenith-team/discussions)
  or a [feature request](https://github.com/Intelligent-Internet/zenith-team/issues/new/choose).
  When maintainers agree, they label the issue `roadmap` and add it here.
- **Agree the direction before you build a feature.** See
  [CONTRIBUTING.md](CONTRIBUTING.md).
- **Improve this file.** Pull requests that fix a stale line or propose a
  change are welcome.

Areas: `area:server` · `area:web` · `area:desktop` · `area:cli` ·
`area:daemon` · `area:self-host` · `area:docs`
