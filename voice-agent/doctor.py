"""Checks a local voice setup before a real call. Prints no secret values."""

import base64
import hashlib
import hmac
import json
import sys
import time
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit, urlunsplit
from urllib.request import ProxyHandler, Request, build_opener

ROOT = Path(__file__).resolve().parent
REQUIRED = (
    "LIVEKIT_URL",
    "LIVEKIT_API_KEY",
    "LIVEKIT_API_SECRET",
    "OPENAI_API_KEY",
    "ZENITH_API_URL",
    "HUDDLE_AI_WORKLOAD_SECRET",
)
# Loopback services must never go through a configured HTTP proxy.
OPENER = build_opener(ProxyHandler({}))


# Old names still accepted for these settings.
LEGACY = {"HUDDLE_AI_WORKLOAD_SECRET": "BOARDLY_HUDDLE_AI_WORKLOAD_SECRET"}


def read_env(path):
    values = {}
    for raw in path.read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip().strip("'\"")
    for name, old in LEGACY.items():
        if not values.get(name) and values.get(old):
            values[name] = values[old]
    return values


def http_url(livekit_url):
    parts = urlsplit(livekit_url)
    scheme = {"ws": "http", "wss": "https"}.get(parts.scheme, parts.scheme)
    return urlunsplit((scheme, parts.netloc, "", "", ""))


def admin_token(api_key, api_secret, now=None):
    """A short-lived LiveKit token that may only list rooms."""
    issued = int(now if now is not None else time.time())

    def encode(value):
        raw = json.dumps(value, separators=(",", ":")).encode()
        return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()

    signing_input = (
        encode({"alg": "HS256", "typ": "JWT"})
        + "."
        + encode(
            {
                "iss": api_key,
                "nbf": issued,
                "exp": issued + 60,
                "video": {"roomList": True},
            }
        )
    )
    signature = hmac.new(
        api_secret.encode(), signing_input.encode(), hashlib.sha256
    ).digest()
    return (
        signing_input + "." + base64.urlsafe_b64encode(signature).rstrip(b"=").decode()
    )


def request(url, *, data=None, headers=None):
    with OPENER.open(
        Request(url, data=data, headers=headers or {}), timeout=3
    ) as response:
        return response.status


def check_livekit(env):
    base = http_url(env["LIVEKIT_URL"])
    try:
        request(base + "/")
    except (HTTPError, URLError, OSError) as error:
        return (
            False,
            f"LiveKit is not reachable at {base} ({reason(error)}). Set HUDDLES_ENABLED='true' in docker/dev.local.env, then run `make infra-up`.",
        )
    try:
        request(
            base + "/twirp/livekit.RoomService/ListRooms",
            data=b"{}",
            headers={
                "Authorization": "Bearer "
                + admin_token(env["LIVEKIT_API_KEY"], env["LIVEKIT_API_SECRET"]),
                "Content-Type": "application/json",
            },
        )
    except HTTPError as error:
        if error.code == 401:
            return (
                False,
                "LiveKit rejected LIVEKIT_API_KEY/LIVEKIT_API_SECRET; they must match the LiveKit config.",
            )
        return False, f"LiveKit room API answered {error.code}."
    except (URLError, OSError) as error:
        return False, f"LiveKit room API failed ({reason(error)})."
    return True, f"LiveKit is up at {base} and accepts these keys."


def check_api(env):
    base = env["ZENITH_API_URL"].rstrip("/")
    try:
        request(base + "/readyz")
    except (HTTPError, URLError, OSError) as error:
        return (
            False,
            f"The API is not ready at {base} ({reason(error)}). Run `make server`.",
        )
    return True, f"The API is ready at {base}."


def check_workload_secret(env, dev_env_path):
    """With `make server`, the API reads docker/dev.env; the secrets must match."""
    if urlsplit(env["ZENITH_API_URL"]).hostname not in {"127.0.0.1", "localhost"}:
        return (
            True,
            "Remote API: make sure it uses the same HUDDLE_AI_WORKLOAD_SECRET.",
        )
    if not dev_env_path.exists():
        return True, "No docker/dev.env to compare the workload secret with."
    expected = read_env(dev_env_path).get("HUDDLE_AI_WORKLOAD_SECRET", "")
    if expected and expected != env["HUDDLE_AI_WORKLOAD_SECRET"]:
        return False, (
            "HUDDLE_AI_WORKLOAD_SECRET differs from docker/dev.env. "
            "Fine for `make server-local` if server/.env matches; otherwise copy it."
        )
    return True, "The workload secret matches docker/dev.env."


def reason(error):
    return getattr(error, "reason", None) or error.__class__.__name__


def run(env_path, dev_env_path):
    if not env_path.exists():
        return [(False, f"{env_path.name} is missing. Run `make voice-setup`.")]
    env = read_env(env_path)
    missing = [key for key in REQUIRED if not env.get(key)]
    if missing:
        return [(False, f"Set {', '.join(missing)} in {env_path.name}.")]
    return [
        (True, f"{env_path.name} has every required value."),
        check_livekit(env),
        check_api(env),
        check_workload_secret(env, dev_env_path),
    ]


def main():
    results = run(ROOT / ".env", ROOT.parent / "docker" / "dev.env")
    for ok, message in results:
        print(("✔ " if ok else "✘ ") + message)
    healthy = all(ok for ok, _ in results)
    print(
        "Ready: run `make voice-start`."
        if healthy
        else "Fix the items above, then run this again."
    )
    return 0 if healthy else 1


if __name__ == "__main__":
    sys.exit(main())
