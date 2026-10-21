"""The local setup check reads files only; network checks are not exercised here."""

import base64
import hashlib
import hmac
import json
import tempfile
import unittest
from pathlib import Path

from doctor import REQUIRED, admin_token, check_workload_secret, http_url, read_env, run


def decode(part):
    return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))


class DoctorTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="voice-doctor-")
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name)

    def write(self, name, text):
        path = self.path / name
        path.write_text(text)
        return path

    def test_reads_env_lines_ignoring_comments_and_quotes(self):
        path = self.write(".env", "# note\nA=1\nB='two'\n\nC=\"x=y\"\n")
        self.assertEqual(read_env(path), {"A": "1", "B": "two", "C": "x=y"})

    def test_reads_the_old_workload_secret_name(self):
        path = self.write(".env", "ZENITH_TEAM_HUDDLE_AI_WORKLOAD_SECRET=old\n")
        self.assertEqual(read_env(path)["HUDDLE_AI_WORKLOAD_SECRET"], "old")

    def test_maps_websocket_urls_to_http(self):
        self.assertEqual(http_url("ws://127.0.0.1:7880"), "http://127.0.0.1:7880")
        self.assertEqual(
            http_url("wss://example.livekit.cloud/"), "https://example.livekit.cloud"
        )

    def test_admin_token_is_signed_and_only_lists_rooms(self):
        token = admin_token("devkey", "secret-value", now=100)
        header, claims, signature = token.split(".")
        expected = hmac.new(
            b"secret-value", f"{header}.{claims}".encode(), hashlib.sha256
        ).digest()
        self.assertEqual(
            base64.urlsafe_b64decode(signature + "=" * (-len(signature) % 4)), expected
        )
        self.assertEqual(
            decode(claims),
            {"iss": "devkey", "nbf": 100, "exp": 160, "video": {"roomList": True}},
        )

    def test_missing_env_points_to_setup(self):
        results = run(self.path / ".env", self.path / "dev.env")
        self.assertEqual(results, [(False, ".env is missing. Run `make voice-setup`.")])

    def test_missing_values_are_named_without_printing_others(self):
        env = self.write(
            ".env", "LIVEKIT_URL=ws://127.0.0.1:7880\nOPENAI_API_KEY=sk-hidden\n"
        )
        [(ok, message)] = run(env, self.path / "dev.env")
        self.assertFalse(ok)
        self.assertIn("ZENITH_API_URL", message)
        self.assertNotIn("sk-hidden", message)

    def test_workload_secret_must_match_dev_env_for_a_local_api(self):
        dev = self.write("dev.env", "HUDDLE_AI_WORKLOAD_SECRET='expected'\n")
        env = {key: "x" for key in REQUIRED} | {
            "ZENITH_API_URL": "http://127.0.0.1:8788"
        }
        self.assertFalse(
            check_workload_secret(env | {"HUDDLE_AI_WORKLOAD_SECRET": "other"}, dev)[0]
        )
        self.assertTrue(
            check_workload_secret(env | {"HUDDLE_AI_WORKLOAD_SECRET": "expected"}, dev)[
                0
            ]
        )

    def test_remote_api_is_not_compared_with_dev_env(self):
        dev = self.write("dev.env", "HUDDLE_AI_WORKLOAD_SECRET='expected'\n")
        env = {key: "x" for key in REQUIRED} | {"ZENITH_API_URL": "https://api.example"}
        self.assertTrue(check_workload_secret(env, dev)[0])


if __name__ == "__main__":
    unittest.main()
