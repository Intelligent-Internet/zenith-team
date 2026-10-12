"""Local process control never needs a LiveKit or model connection."""

import tempfile
import socket
import subprocess
import sys
import unittest
from pathlib import Path
from unittest.mock import Mock

from local_worker import LocalWorker, WorkerBusy, command, require_free_port


class LocalWorkerTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="voice-test-")
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name)

    def test_status_reports_owner_without_secrets(self):
        with LocalWorker(self.path, revision="abc123") as worker:
            status = command(self.path, "status")
            self.assertEqual(status["pid"], worker.pid)
            self.assertEqual(status["revision"], "abc123")
            self.assertEqual(status["state"], "starting")
            self.assertEqual(status["jobs"], [])

    def test_second_worker_cannot_replace_owner(self):
        with LocalWorker(self.path):
            with self.assertRaises(WorkerBusy):
                with LocalWorker(self.path):
                    self.fail("duplicate worker admitted")
            self.assertEqual(command(self.path, "status")["state"], "starting")

    def test_stop_is_idempotent_and_targets_owner_callback_only(self):
        stop = Mock()
        with LocalWorker(self.path, stop=stop):
            self.assertEqual(command(self.path, "stop")["state"], "stopping")
            self.assertEqual(command(self.path, "stop")["state"], "stopping")
            stop.assert_called_once_with()

    def test_restarts_after_clean_exit_and_stale_socket(self):
        with LocalWorker(self.path):
            pass
        self.assertIsNone(command(self.path, "status"))
        with socket.socket(socket.AF_UNIX) as stale:
            stale.bind(str(self.path / "control.sock"))
        with LocalWorker(self.path):
            self.assertIsNotNone(command(self.path, "status"))

    def test_existing_listener_is_never_stopped(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen()
            port = listener.getsockname()[1]
            with self.assertRaisesRegex(WorkerBusy, "already in use"):
                require_free_port(port)
            with socket.create_connection(("127.0.0.1", port), timeout=1):
                pass

    def test_stop_reaches_exact_process_and_releases_lock(self):
        script = """
import signal, sys, threading
from pathlib import Path
from local_worker import LocalWorker
done = threading.Event()
signal.signal(signal.SIGTERM, lambda *_: done.set())
with LocalWorker(Path(sys.argv[1])):
    print('ready', flush=True)
    done.wait(10)
print('cleaned', flush=True)
"""
        process = subprocess.Popen(
            [sys.executable, "-c", script, str(self.path)],
            cwd=Path(__file__).parent,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        try:
            self.assertEqual(process.stdout.readline().strip(), "ready")
            self.assertEqual(command(self.path, "status")["pid"], process.pid)
            command(self.path, "stop")
            output, error = process.communicate(timeout=5)
            self.assertEqual(process.returncode, 0, error)
            self.assertIn("cleaned", output)
            self.assertIsNone(command(self.path, "status"))
            with LocalWorker(self.path):
                pass
        finally:
            if process.poll() is None:
                process.kill()
            process.communicate(timeout=5)

    def test_unknown_command_cannot_stop_worker(self):
        stop = Mock()
        with LocalWorker(self.path, stop=stop):
            self.assertIn("error", command(self.path, "kill"))
            stop.assert_not_called()

    def test_snapshot_contains_only_public_job_identity(self):
        from types import SimpleNamespace

        job = SimpleNamespace(
            job=SimpleNamespace(
                id="job-1", metadata="attempt-1", room=SimpleNamespace(name="huddle:1")
            )
        )
        server = SimpleNamespace(id="worker-1", active_jobs=[job], draining=False)
        with LocalWorker(self.path) as worker:
            self.assertEqual(worker.load(server), 0.5)
            status = command(self.path, "status")
            self.assertEqual(status["workerId"], "worker-1")
            self.assertEqual(status["jobs"][0]["jobId"], "job-1")
            self.assertEqual(status["state"], "running")


if __name__ == "__main__":
    unittest.main()
