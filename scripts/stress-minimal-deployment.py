#!/usr/bin/env python3
"""Bounded local HTTP stress probe for the minimal deployment (stdlib only).

Requires the existing, healthy localhost Compose stack. Temporarily opens local
signup, creates two disposable accounts/Companies, closes signup, exercises the
API/proxy, and deletes its accounts. Never points at a remote service or runs an
agent/provider. Includes the real 120-second slow-body timeout, so takes minutes.
"""

import concurrent.futures
import datetime
import http.client
import http.cookies
import json
import os
from pathlib import Path
import secrets
import select
import socket
import subprocess
import threading
import time

ROOT = Path(__file__).resolve().parents[1]
COMPOSE = [
    "docker",
    "compose",
    "--env-file",
    "deploy/.env",
    "-f",
    "deploy/minimal/compose.yml",
]
PORT = int(os.environ.get("ZENITH_TEAM_MINIMAL_WEB_PORT", "8080"))
ORIGIN = f"http://localhost:{PORT}"
REPORT = ROOT / "docs/audit/evidence/http-stress.json"
CONCURRENCY = 32


def compose(*args, signup=False):
    env = dict(os.environ, ZENITH_TEAM_ALLOW_SIGNUP=str(signup).lower())
    return subprocess.check_output(COMPOSE + list(args), cwd=ROOT, env=env, text=True)


class Client:
    def __init__(self):
        self.cookies = {}

    def request(self, method, path, payload=None, save_cookies=False):
        headers = {"Origin": ORIGIN, "Content-Type": "application/json"}
        if self.cookies:
            headers["Cookie"] = "; ".join(f"{k}={v}" for k, v in self.cookies.items())
        if "zenith_team_csrf" in self.cookies:
            headers["X-CSRF-Token"] = self.cookies["zenith_team_csrf"]
        body = None if payload is None else json.dumps(payload).encode()
        start = time.monotonic()
        connection = http.client.HTTPConnection("127.0.0.1", PORT, timeout=15)
        try:
            connection.request(method, path, body, headers)
            response = connection.getresponse()
            raw = response.read(1024 * 1024)
            response_headers = dict(response.getheaders())
            if save_cookies:
                for key, value in response.getheaders():
                    if key.lower() == "set-cookie":
                        cookies = http.cookies.SimpleCookie(value)
                        self.cookies.update({k: m.value for k, m in cookies.items()})
            try:
                data = json.loads(raw)
            except (ValueError, UnicodeError):
                data = raw.decode(errors="replace")
            return (
                response.status,
                data,
                response_headers,
                (time.monotonic() - start) * 1000,
            )
        finally:
            connection.close()


def require_status(response, expected, context):
    if response[0] != expected:
        raise RuntimeError(f"{context}: expected {expected}, received {response[0]}")
    return response[1]


def batch(name, total, operation, validate):
    start = time.monotonic()
    statuses, durations, failures = {}, [], []

    def one(index):
        try:
            response = operation(index)
            return response[0], response[3], validate(index, response), None
        except Exception as error:
            return "transportError", 0, False, type(error).__name__

    with concurrent.futures.ThreadPoolExecutor(max_workers=CONCURRENCY) as pool:
        for index, (status, duration, valid, error) in enumerate(
            pool.map(one, range(total))
        ):
            statuses[str(status)] = statuses.get(str(status), 0) + 1
            durations.append(duration)
            if not valid and len(failures) < 10:
                failures.append(
                    {"request": index, "status": status, "errorType": error}
                )
    elapsed = time.monotonic() - start
    durations.sort()

    def percentile(q):
        return round(durations[min(len(durations) - 1, int(len(durations) * q))], 2)

    result = {
        "name": name,
        "requests": total,
        "concurrency": CONCURRENCY,
        "elapsedSeconds": round(elapsed, 3),
        "requestsPerSecond": round(total / elapsed, 2),
        "statuses": statuses,
        "p50Ms": percentile(0.50),
        "p95Ms": percentile(0.95),
        "p99Ms": percentile(0.99),
        "maxMs": round(max(durations), 2),
        "failureSamples": failures,
        "passed": not failures,
    }
    print(json.dumps(result), flush=True)
    return result


def slow_request(kind):
    start = time.monotonic()
    data = b""
    with socket.create_connection(("127.0.0.1", PORT), timeout=5) as connection:
        connection.settimeout(135)
        if kind == "headers":
            request = b"GET / HTTP/1.1\r\nHost: localhost\r\nX-Slow: "
            min_seconds, max_seconds = 4, 10
        else:
            request = (
                f"POST /api/v1/auth/login HTTP/1.1\r\nHost: localhost:{PORT}\r\n"
                f"Origin: {ORIGIN}\r\nContent-Type: application/json\r\n"
                "Content-Length: 64\r\nConnection: close\r\n\r\n{"
            ).encode()
            min_seconds, max_seconds = (110, 130) if kind == "trickleBody" else (55, 70)
        connection.sendall(request)
        try:
            if kind == "trickleBody":
                while time.monotonic() - start < 130:
                    readable, _, _ = select.select([connection], [], [], 15)
                    if readable:
                        break
                    connection.sendall(b" ")
            data = connection.recv(8192)
        except ConnectionResetError:
            pass
    elapsed = time.monotonic() - start
    line = data.split(b"\r\n", 1)[0].decode(errors="replace")
    # Some listeners close silently; others return a timeout/client-error status.
    status = int(line.split()[1]) if line.startswith("HTTP/") else None
    return {
        "kind": kind,
        "elapsedSeconds": round(elapsed, 3),
        "status": status,
        "passed": min_seconds <= elapsed <= max_seconds
        and status in (None, 400, 408, 499, 502, 504),
    }


def container_state():
    ids = compose("ps", "-q", "server", "web").split()
    rows = json.loads(subprocess.check_output(["docker", "inspect", *ids], text=True))
    return {
        row["Name"].lstrip("/"): {
            "image": row["Image"],
            "restartCount": row["RestartCount"],
            "oomKilled": row["State"]["OOMKilled"],
            "health": row["State"].get("Health", {}).get("Status"),
        }
        for row in rows
    }


def main():
    subprocess.run(
        ["python3", "scripts/check-minimal-deployment.py"], cwd=ROOT, check=True
    )
    report = {
        "startedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "target": ORIGIN,
        "scope": "local disposable fixtures; no provider execution",
        "initial": container_state(),
        "batches": [],
        "samples": [],
    }
    stop_sampling = threading.Event()

    def sample():
        ids = compose("ps", "-q", "server", "web").split()
        while not stop_sampling.is_set():
            try:
                output = subprocess.check_output(
                    ["docker", "stats", "--no-stream", "--format", "{{json .}}", *ids],
                    text=True,
                    timeout=10,
                )
                report["samples"].append(
                    {
                        "elapsedSeconds": round(time.monotonic() - began, 2),
                        "containers": [
                            json.loads(line) for line in output.splitlines()
                        ],
                    }
                )
            except (subprocess.SubprocessError, ValueError) as error:
                report.setdefault("samplingErrors", []).append(type(error).__name__)
            stop_sampling.wait(2)

    fixtures = []
    began = time.monotonic()
    sampler = threading.Thread(target=sample, daemon=True)
    try:
        compose("up", "-d", "--no-deps", "--wait", "web", signup=True)
        for _ in range(2):
            client = Client()
            suffix = secrets.token_hex(6)
            credentials = {
                "email": f"stress-{suffix}@example.test",
                "password": secrets.token_urlsafe(32),
                "client": "web",
            }
            require_status(
                client.request(
                    "POST",
                    "/api/v1/auth/signup",
                    {**credentials, "displayName": "Stress Fixture"},
                    True,
                ),
                201,
                "signup",
            )
            fixture = {"client": client, "credentials": credentials}
            fixtures.append(fixture)
            company = require_status(
                client.request(
                    "POST",
                    "/api/v1/companies",
                    {
                        "name": "Temporary stress fixture",
                        "slug": f"stress-{suffix}",
                        "taskPrefix": "STR",
                        "businessContext": "Disposable local stress probe; no agent execution.",
                    },
                ),
                201,
                "company create",
            )
            fixture["companyId"] = company["id"]
        compose("up", "-d", "--no-deps", "--wait", "web")
        report["before"] = container_state()
        sampler.start()
        anonymous = Client()
        denied = [
            ("/api/v1/auth/signup", 403, "Registration is closed"),
            ("/api/v1/auth/%73ignup", 403, "Registration is closed"),
            ("/api/v1/public/probe", 404, "Unavailable in this deployment"),
            ("/api/v1/internal/probe", 404, "Unavailable in this deployment"),
            ("/api/v1/auth/google/web/start", 404, "Unavailable in this deployment"),
            ("/api/v1/webhooks/probe", 404, "Unavailable in this deployment"),
        ]
        with concurrent.futures.ThreadPoolExecutor(max_workers=12) as slow_pool:
            slow = [
                slow_pool.submit(slow_request, kind)
                for kind in ["headers"] * 4 + ["idleBody"] * 4 + ["trickleBody"] * 4
            ]
            report["batches"].append(
                batch(
                    "proxy-denials",
                    1200,
                    lambda i: anonymous.request("POST", denied[i % len(denied)][0], {}),
                    lambda i, r: (
                        r[0] == denied[i % len(denied)][1]
                        and r[1] == denied[i % len(denied)][2]
                    ),
                )
            )
            report["batches"].append(
                batch(
                    "authenticated-company-reads",
                    1600,
                    lambda i: fixtures[i % 2]["client"].request(
                        "GET", "/api/v1/companies"
                    ),
                    lambda i, r: (
                        r[0] == 200
                        and {c["id"] for c in r[1]["items"]}
                        == {fixtures[i % 2]["companyId"]}
                    ),
                )
            )
            report["batches"].append(
                batch(
                    "cross-company-access-denials",
                    800,
                    lambda i: fixtures[i % 2]["client"].request(
                        "GET", "/api/v1/companies/" + fixtures[1 - i % 2]["companyId"]
                    ),
                    lambda i, r: r[0] in (403, 404),
                )
            )

            def login_load(index):
                payload = dict(
                    fixtures[0]["credentials"], password="wrong-stress-password"
                )
                if index % 2:
                    payload["email"] = "absent-stress-fixture@example.test"
                return anonymous.request("POST", "/api/v1/auth/login", payload)

            def valid_login(index, response):
                if response[0] == 401:
                    return response[1].get("code") == "invalid_credentials"
                return (
                    response[0] == 503
                    and response[1].get("code") == "authentication_busy"
                    and any(
                        k.lower() == "retry-after" and v == "1"
                        for k, v in response[2].items()
                    )
                )

            report["batches"].append(
                batch("password-overload", 2400, login_load, valid_login)
            )

            def mixed_load(index):
                time.sleep(
                    0.25
                )  # Bounded sustained traffic, approximately 128 requests/s.
                if index % 4 == 0:
                    return login_load(index)
                if index % 4 == 1:
                    return fixtures[index % 2]["client"].request(
                        "GET", "/api/v1/companies"
                    )
                if index % 4 == 2:
                    return fixtures[0]["client"].request(
                        "GET", "/api/v1/companies/" + fixtures[1]["companyId"]
                    )
                return anonymous.request("POST", denied[0][0], {})

            def valid_mixed(index, response):
                if index % 4 == 0:
                    return valid_login(index, response)
                if index % 4 == 1:
                    return response[0] == 200 and {
                        c["id"] for c in response[1]["items"]
                    } == {fixtures[index % 2]["companyId"]}
                if index % 4 == 2:
                    return response[0] in (403, 404)
                return response[0] == 403 and response[1] == "Registration is closed"

            report["batches"].append(
                batch("paced-mixed-traffic", 3200, mixed_load, valid_mixed)
            )
            for fixture in fixtures:
                require_status(
                    fixture["client"].request(
                        "POST", "/api/v1/auth/login", fixture["credentials"], True
                    ),
                    200,
                    "login recovery",
                )
            report["loginRecoveryPassed"] = True
            report["slowConnections"] = [future.result() for future in slow]
        report["batches"].append(
            batch(
                "post-load-company-reads",
                800,
                lambda i: fixtures[i % 2]["client"].request("GET", "/api/v1/companies"),
                lambda i, r: (
                    r[0] == 200
                    and {c["id"] for c in r[1]["items"]}
                    == {fixtures[i % 2]["companyId"]}
                ),
            )
        )
        report["after"] = container_state()
        report["containersRecovered"] = all(
            not row["oomKilled"]
            and row["health"] == "healthy"
            and row["restartCount"] == report["before"][name]["restartCount"]
            for name, row in report["after"].items()
        )
    except Exception as error:
        report["failureType"] = type(error).__name__
        report["failureMessage"] = (
            str(error)
            if isinstance(error, RuntimeError)
            else "See local execution output; no response bodies or credentials recorded."
        )
        raise
    finally:
        stop_sampling.set()
        if sampler.is_alive():
            sampler.join(timeout=15)
        cleanup = []
        for fixture in fixtures:
            try:
                response = fixture["client"].request(
                    "DELETE", "/api/v1/me", {"confirmation": "DELETE"}
                )
                cleanup.append(response[0] == 204)
            except Exception:
                cleanup.append(False)
        report["fixtureCleanupPassed"] = bool(cleanup) and all(cleanup)
        compose("up", "-d", "--no-deps", "--wait", "web")
        report["signupClosed"] = (
            anonymous.request("POST", "/api/v1/auth/signup", {})[0] == 403
            if "anonymous" in locals()
            else False
        )
        report["elapsedSeconds"] = round(time.monotonic() - began, 2)
        report["passed"] = (
            len(report["batches"]) == 6
            and all(b["passed"] for b in report["batches"])
            and all(c["passed"] for c in report.get("slowConnections", []))
            and len(report.get("slowConnections", [])) == 12
            and report.get("loginRecoveryPassed", False)
            and report.get("containersRecovered", False)
            and report["fixtureCleanupPassed"]
            and report["signupClosed"]
        )
        REPORT.parent.mkdir(parents=True, exist_ok=True)
        REPORT.write_text(json.dumps(report, indent=2) + "\n")
        print(
            f"Stress result: {'PASS' if report['passed'] else 'FAIL'}; report: {REPORT}",
            flush=True,
        )
    if not report["passed"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
