# Zenith Team Hosted Frontend Proxy

Cloudflare Pages deploys these functions with the static frontend bundle when the
Pages project root is `frontend/`.

Required production environment variables:

- `ZENITH_TEAM_API_ORIGIN`: Zenith Team API origin, for example `https://api.example.com`.

Routes:

- `/api/*` is proxied to `ZENITH_TEAM_API_ORIGIN`.
- `../public/_routes.json` is copied into the Vite build output and makes `/api`,
  and `/api/*` explicit Function invocation routes. The native WebSocket upgrade at
  `/api/v1/ws` uses the same API proxy; no client SSE route is exposed.

Do not use Cloudflare Pages `_redirects` for these backend routes. Cloudflare Pages
only supports relative URL proxying there, while Zenith Team needs same-origin public
routes to reach the separate Zenith Team API origin.
