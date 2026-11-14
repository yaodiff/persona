"""publish.sh's site deploy keeps a working push relay working (#1263).

The site's Worker also runs the push relay (/relay/*). Its settings are Worker secrets, kept under the Worker's name, so
a deploy under a new name answers 503 "push relay not configured" and every phone's push stops. deploy_site reads the
relay's health check before and after the deploy and fails when a relay that was healthy is not anymore. These tests
run publish.sh's own functions against a local site and a fake wrangler.
"""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest


ROOT = Path(__file__).resolve().parents[2]
PUBLISHER = ROOT / "scripts/release/publish.sh"

HEALTHY = (200, {"ok": True, "apns": True})
NOT_CONFIGURED = (503, {"error": "push relay not configured"})
NO_APNS = (200, {"ok": True, "apns": False})


def publisher_functions():
    """wr, settled, relay_ok and deploy_site as publish.sh defines them."""
    text = PUBLISHER.read_text()

    def between(start, end):
        begin = text.index(start)
        return text[begin:text.index(end, begin)]

    return "\n".join((between("# wr <dir> <args…>", "# check_served"),
                      between("# settled <what>", "served_is()"),
                      between("# relay_ok:", "\n\n# SITE_ONLY")))


class Site:
    """The relay's health check: one answer until the fake wrangler deploys (it creates the file deployed), then the
    answers after it, one per request, the last one repeating."""

    def __init__(self, before, after, deployed):
        self.before, self.after, self.deployed, self.requests = before, list(after), deployed, 0
        self.request_paths = []
        site = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path == "/relay/v1/health":
                    site.requests += 1
                    site.request_paths.append((self.path, site.deployed.exists()))
                if self.path != "/relay/v1/health":
                    status, body = 404, {"error": "not found"}
                elif not site.deployed.exists():
                    status, body = site.before
                else:
                    status, body = site.after[0] if len(site.after) == 1 else site.after.pop(0)
                data = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = "http://127.0.0.1:%d" % self.server.server_address[1]

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(5)


class DeploySiteRelayTests(unittest.TestCase):
    def deploy(self, before, *after, deploy_fails=False, language_check=True, finish_language=False):
        temp = tempfile.TemporaryDirectory(prefix="persona-publish-relay-")
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        (root / "site").mkdir()
        flag = root / "deployed"
        site = Site(before, after or [before], flag)
        self.addCleanup(site.close)
        wrangler = root / "wrangler"
        wrangler.write_text('#!/bin/sh\n[ "$1" = deploy ] || exit 2\n%s\n' % (
            "echo 'deploy refused' >&2; exit 1" if deploy_fails else 'echo deployed; : > "$TEST_DEPLOYED"'))
        wrangler.chmod(0o755)
        tools = root / "tools"
        tools.mkdir()
        (tools / "cf").write_text("#!/bin/sh\nexit 0\n")  # deploy_site refreshes cf's token when cf is there
        (tools / "cf").chmod(0o755)
        language = root / "language-check"
        language.write_text("#!/bin/sh\n" + ("exit 0\n" if language_check else "echo wrong language >&2; exit 1\n"))
        language.chmod(0o755)
        script = "\n".join((
            "set -euo pipefail",
            'WRANGLER="$TEST_WRANGLER"',
            'SITE_LANGUAGE_CHECK="$TEST_LANGUAGE_CHECK"',
            "sleep() { :; }",  # settled waits ten seconds between tries
            publisher_functions(),
            'deploy_site "$TEST_SITE"',
            "echo deploy_site returned",
            *(('site_language_result',) if finish_language else ()),
        ))
        env = {**os.environ, "PATH": str(tools) + os.pathsep + os.environ.get("PATH", os.defpath),
               "PERSONA_PUBLISH_URL": site.url, "TEST_WRANGLER": str(wrangler), "TEST_DEPLOYED": str(flag),
               "TEST_SITE": str(root / "site"), "TEST_LANGUAGE_CHECK": str(language)}
        result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, timeout=60)
        return result, site

    def test_a_healthy_relay_that_stays_healthy_passes(self):
        result, _ = self.deploy(HEALTHY, HEALTHY)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("deploy_site returned", result.stdout)
        self.assertNotIn("note:", result.stderr)

    def test_a_deploy_that_loses_the_relay_secrets_fails(self):
        result, _ = self.deploy(HEALTHY, NOT_CONFIGURED)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertNotIn("deploy_site returned", result.stdout)
        self.assertIn("the push relay worked before this deploy and does not now", result.stderr)
        self.assertIn("docs/operations/site.md", result.stderr)

    def test_a_relay_without_its_apns_key_after_the_deploy_fails(self):
        result, _ = self.deploy(HEALTHY, NO_APNS)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("the push relay worked before this deploy and does not now", result.stderr)

    def test_a_relay_that_comes_back_within_the_wait_passes(self):
        # Right after a deploy the edge can still answer from before for a while; settled retries.
        result, site = self.deploy(HEALTHY, NOT_CONFIGURED, NOT_CONFIGURED, HEALTHY)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("deploy_site returned", result.stdout)
        self.assertGreaterEqual(site.requests, 4)

    def test_a_site_without_a_working_relay_only_gets_a_note(self):
        result, _ = self.deploy(NOT_CONFIGURED, NOT_CONFIGURED)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("deploy_site returned", result.stdout)
        self.assertIn("note:", result.stderr)
        self.assertIn("/relay/v1/health was not healthy before this deploy either", result.stderr)

    def test_a_failed_deploy_still_stops_before_any_relay_check(self):
        result, site = self.deploy(HEALTHY, deploy_fails=True)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("deploy failed", result.stderr)
        self.assertEqual(site.requests, 1, site.request_paths)  # the check before the deploy only

    def test_a_language_failure_is_loud_but_does_not_skip_the_next_release_step(self):
        result, _ = self.deploy(HEALTHY, HEALTHY, language_check=False, finish_language=True)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("deploy_site returned", result.stdout)  # the step after deploy_site still ran
        self.assertIn("SITE LANGUAGE CHECK FAILED", result.stderr)
        self.assertIn("docs/operations/site.md", result.stderr)
        self.assertIn("after completing the other release checks", result.stderr)

    def test_the_language_check_also_runs_when_the_relay_was_not_healthy(self):
        result, _ = self.deploy(NOT_CONFIGURED, NOT_CONFIGURED, language_check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("deploy_site returned", result.stdout)
        self.assertIn("/relay/v1/health was not healthy", result.stderr)
        self.assertIn("SITE LANGUAGE CHECK FAILED", result.stderr)


if __name__ == "__main__":
    unittest.main()
