# Security Policy

## Supported versions

| Version | Supported |
| --- | --- |
| Latest release | Yes |
| Older releases | No |

Fixes go into the next release. Please upgrade to the latest release before
you report a problem.

## Report a vulnerability

Report security problems privately through GitHub:

**<https://github.com/Intelligent-Internet/zenith-team/security/advisories/new>**

Do not open a public issue, discussion or pull request for a security problem.

Please include:

- the affected part (server, web app, desktop app, daemon or CLI) and the
  version or commit
- the steps to reproduce it
- a working proof of concept
- what an attacker could do with it
- any idea you have for a fix

Test only against your own installation. Do not access or change data that is
not yours.

## What happens next

- We aim to confirm we got your report within **3 business days**.
- We check the problem and keep you updated in the private advisory.
- We agree on a disclosure date with you once a fix is ready.
- We credit you in the advisory, unless you prefer not to be named.

## How Zenith Team draws its security boundary

Knowing this model helps you tell a real vulnerability from expected behavior.

**The server is the boundary.** One server hosts many companies. Its
permission checks decide who can see and change what. These are
vulnerabilities:

- reading or changing another company's data
- doing something your role or project access does not allow
- signing in as someone else, or keeping access after it was taken away
- using a run's token for more than that run is allowed to do

**The daemon runs as you, on your machine.** It starts agents with your own
agent CLI and your own sign-in. An agent can do what you can do on that
machine. That is by design, not a vulnerability. Each run gets its own home
folder and a token that only works for that run.

For self-hosted installs, the operator is responsible for the host, the
network, TLS and the secrets in `deploy/.env`. The
[security model](docs/content/docs/self-hosting/security-model.mdx) explains
what the server protects and what it leaves to you.

## Out of scope

- self-XSS, where you can only attack yourself
- missing security headers without a demonstrated impact
- scanner output without a working proof of concept
- problems that need a compromised machine, or admin access you already have
- an agent doing something its own user could already do on that machine
