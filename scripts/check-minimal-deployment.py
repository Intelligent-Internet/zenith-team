#!/usr/bin/env python3
"""Read-only checks of the running private deployment; stdlib only."""
import json
import os
import subprocess
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ["docker", "compose", "--env-file", "deploy/.env", "-f", "deploy/minimal/compose.yml"]


def docker_json(*args):
    return json.loads(subprocess.check_output(["docker", *args], cwd=ROOT, text=True))


def check_http(path, status, expected=None, method="GET"):
    port = int(os.environ.get("ZENITH_TEAM_MINIMAL_WEB_PORT", "8080"))
    request = urllib.request.Request(f"http://127.0.0.1:{port}{path}", method=method)
    try:
        response = urllib.request.urlopen(request, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read(65536).decode("utf-8", errors="replace")
        if response.status != status or (expected is not None and body != expected):
            raise SystemExit(f"HTTP policy failed for {path}: status {response.status}")


for service in ("server", "web", "postgres", "redis", "storage"):
    cid = subprocess.check_output(COMPOSE + ["ps", "-q", service], cwd=ROOT, text=True).strip()
    if not cid:
        raise SystemExit(f"Service is not running: {service}")
    container = docker_json("inspect", cid)[0]
    host = container["HostConfig"]
    user = container["Config"]["User"].split(":")[0]
    if user in ("", "root", "0"):
        raise SystemExit(f"Service runs as root: {service}")
    if not host["ReadonlyRootfs"] or host["CapDrop"] != ["ALL"]:
        raise SystemExit(f"Missing filesystem/capability restriction: {service}")
    if "no-new-privileges:true" not in host["SecurityOpt"]:
        raise SystemExit(f"Missing privilege restriction: {service}")
    if not host["PidsLimit"] or host["PidsLimit"] <= 0 or host["Memory"] <= 0 or host["NanoCpus"] <= 0:
        raise SystemExit(f"Missing resource limit: {service}")
    published = host["PortBindings"] or {}
    if service != "web" and published:
        raise SystemExit(f"Unexpected published port: {service}")
    if service == "web":
        if set(published) != {"8080/tcp"} or any(p["HostIp"] != "127.0.0.1" for p in published["8080/tcp"]):
            raise SystemExit("Web port is not exclusively loopback-bound")
    networks = container["NetworkSettings"]["Networks"]
    network_configs = [docker_json("network", "inspect", n)[0] for n in networks]
    if service != "web" and not all(n["Internal"] for n in network_configs):
        raise SystemExit(f"Unexpected external network: {service}")
    if service == "web" and any(n["Name"].endswith("_state") for n in network_configs):
        raise SystemExit("Web can reach the data network")

check_http("/", 200)
check_http("/api/v1/auth/signup", 403, "Registration is closed", "POST")
check_http("/api/v1/auth/%73ignup", 403, "Registration is closed", "POST")
for prefix in ("public", "internal", "webhooks", "social/oauth", "auth/google"):
    check_http(f"/api/v1/{prefix}/probe", 404, "Unavailable in this deployment")
print("Minimal deployment: HTTP policies, non-root/read-only/capability/resource limits, ports and networks passed.")
