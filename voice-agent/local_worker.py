"""Local-only worker ownership. No credentials or synchronized product state live here."""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import stat
import subprocess
import threading
import time
from urllib.error import URLError
from urllib.request import ProxyHandler, build_opener


class WorkerBusy(RuntimeError):
    pass


def runtime_directory():
    # Keep Unix socket paths short on macOS, and isolate users and checkouts.
    root = str(Path(__file__).resolve().parent)
    key = hashlib.sha256(root.encode()).hexdigest()[:16]
    return Path("/tmp") / f"zenith-voice-{os.getuid()}-{key}"


def code_revision():
    try:
        return subprocess.check_output(
            ["git", "describe", "--always", "--dirty"],
            cwd=Path(__file__).parent,
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=2,
        ).strip()
    except (OSError, subprocess.SubprocessError):
        return "unknown"


def require_free_port(port=8081):
    with socket.socket() as probe:
        probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            probe.bind(("127.0.0.1", port))
        except OSError:
            raise WorkerBusy(
                f"Local voice port {port} is already in use or unavailable. "
                f"Inspect: lsof -nP -iTCP:{port} -sTCP:LISTEN. "
                "Stop the old worker in its owning terminal; no process was stopped."
            ) from None


def prepare_directory(directory):
    directory.mkdir(mode=0o700, exist_ok=True)
    info = directory.lstat()
    if (
        not stat.S_ISDIR(info.st_mode)
        or info.st_uid != os.getuid()
        or stat.S_IMODE(info.st_mode) != 0o700
    ):
        raise RuntimeError(
            "Local voice control directory must be owned by you, mode 0700"
        )


class LocalWorker:
    def __init__(self, directory=None, *, revision=None, stop=None):
        self.directory = directory or runtime_directory()
        self.pid = os.getpid()
        self.stop = stop or (lambda: os.kill(self.pid, signal.SIGTERM))
        self.lock_fd = None
        self.listener = None
        self.thread = None
        self.closed = threading.Event()
        self.mutex = threading.Lock()
        try:
            terminal = os.ttyname(0)
        except OSError:
            terminal = "no controlling terminal"
        self.snapshot = {
            "pid": self.pid,
            "revision": revision or code_revision(),
            "terminal": terminal,
            "tmuxPane": os.environ.get("TMUX_PANE", ""),
            "startedAt": time.time(),
            "state": "starting",
            "workerId": "unregistered",
            "jobs": [],
            "checkedAt": None,
        }

    def __enter__(self):
        prepare_directory(self.directory)
        self.lock_fd = os.open(
            self.directory / "worker.lock",
            os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW,
            0o600,
        )
        try:
            fcntl.flock(self.lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            os.close(self.lock_fd)
            self.lock_fd = None
            raise WorkerBusy(
                "Voice worker already running; use make voice-status"
            ) from None
        try:
            path = self.directory / "control.sock"
            # Only the lock owner can remove a stale endpoint.
            path.unlink(missing_ok=True)
            self.listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self.listener.bind(str(path))
            self.listener.listen(4)
            self.listener.settimeout(0.2)
            self.thread = threading.Thread(target=self.serve, daemon=True)
            self.thread.start()
            return self
        except BaseException:
            self.__exit__(None, None, None)
            raise

    def __exit__(self, *_):
        self.closed.set()
        if self.thread:
            self.thread.join(timeout=2)
        if self.listener:
            self.listener.close()
            (self.directory / "control.sock").unlink(missing_ok=True)
        if self.lock_fd is not None:
            os.close(self.lock_fd)
            self.lock_fd = None
        # Never unlink the lock inode: waiters must always lock the same file.

    def load(self, server):
        jobs = [
            {
                "jobId": info.job.id,
                "attemptId": info.job.metadata,
                "room": info.job.room.name,
            }
            for info in server.active_jobs
        ]
        with self.mutex:
            self.snapshot.update(workerId=server.id, jobs=jobs, checkedAt=time.time())
            if self.snapshot["state"] != "stopping":
                self.snapshot["state"] = "draining" if server.draining else "running"
        return len(jobs) / 2

    def serve(self):
        while not self.closed.is_set():
            try:
                connection, _ = self.listener.accept()
            except TimeoutError:
                continue
            with connection:
                connection.settimeout(1)
                should_stop = False
                try:
                    action = connection.recv(64).decode().strip()
                    with self.mutex:
                        if action not in ("status", "stop"):
                            result = {"error": "Unknown local voice command"}
                        else:
                            if (
                                action == "stop"
                                and self.snapshot["state"] != "stopping"
                            ):
                                self.snapshot["state"] = "stopping"
                                should_stop = True
                            result = dict(self.snapshot)
                    connection.sendall(json.dumps(result).encode())
                except (OSError, UnicodeError):
                    pass  # A disconnected local client must not kill the worker.
                finally:
                    if should_stop:
                        self.stop()


def command(directory, action):
    if not directory.exists():
        return None
    prepare_directory(directory)
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(2)
        try:
            client.connect(str(directory / "control.sock"))
        except (FileNotFoundError, ConnectionRefusedError):
            return None
        client.sendall(action.encode())
        chunks = []
        while data := client.recv(8192):
            chunks.append(data)
        return json.loads(b"".join(chunks))


def health():
    try:
        # Do not use corporate proxies for the local SDK health endpoint.
        with build_opener(ProxyHandler({})).open("http://127.0.0.1:8081/", timeout=1):
            return "healthy (not proof of a working model session)"
    except (OSError, URLError):
        return "unavailable; inspect the owning terminal"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["status", "stop"])
    args = parser.parse_args()
    directory = runtime_directory()
    try:
        result = command(directory, args.action)
        if result is None:
            print("No managed voice worker in this checkout.")
            print(
                "Older/unmanaged workers may still exist: lsof -nP -iTCP:8081 -sTCP:LISTEN"
            )
            return 0
        print(json.dumps(result, indent=2))
        if args.action == "status":
            print("SDK health:", health())
            print(
                "Jobs/workerId are the last SDK snapshot, not a live connection guarantee."
            )
            if result["checkedAt"] is None or time.time() - result["checkedAt"] > 15:
                print("SDK snapshot is missing/stale; check worker logs.")
            return 0
        print(
            "Stopping this local worker; waiting up to 45s for session cleanup. No force kill."
        )
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if command(directory, "status") is None:
                print("Voice worker stopped.")
                return 0
            time.sleep(0.25)
        print(
            "Worker still stopping. Check its terminal; no other process was signaled."
        )
        return 1
    except (OSError, RuntimeError, ValueError) as error:
        print(f"Local voice control unavailable: {error}")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
