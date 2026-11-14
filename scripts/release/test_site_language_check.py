"""The deploy-time language check catches an entry cache that reuses either language's response."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import subprocess
import threading
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/release/site_language_check.sh"


class Site:
    def __init__(self, mode):
        self.mode = mode
        self.root_requests = []
        site = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                path = self.path.split("?", 1)[0]
                if path == "/":
                    language = self.headers.get("Accept-Language", "")
                    # Only the checker's seven negotiated root calls belong to this sequence. Ephemeral local HTTP
                    # fixtures can also receive probes without Accept-Language, which are not language negotiation.
                    if language:
                        site.root_requests.append(language)
                    if site.mode == "redirect":
                        self.answer(302, "", location="/en/")
                    elif site.mode == "chinese":
                        self.answer(200, '<html lang="zh-CN"><title>中文</title></html>')
                    elif "persona_lang=en" in self.headers.get("Cookie", ""):
                        self.answer(302, "", location="/en/")
                    elif language == "zh-CN":
                        self.answer(200, '<html lang="zh-CN"><title>中文</title></html>')
                    else:
                        self.answer(302, "", location="/en/")
                elif path == "/install.sh" and site.mode == "installer":
                    self.answer(200, "#!/bin/sh\nprintf 'install\\n'\n", content_type="text/x-shellscript")
                elif path == "/en/":
                    self.answer(200, '<html lang="en"><title>English</title></html>')
                else:
                    self.answer(404, "not found")

            def answer(self, status, text, location=None, content_type="text/html; charset=utf-8"):
                data = text.encode()
                self.send_response(status)
                if location:
                    self.send_header("Location", location)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(data)))
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


class SiteLanguageCheckTests(unittest.TestCase):
    def check(self, mode):
        site = Site(mode)
        self.addCleanup(site.close)
        result = subprocess.run(["bash", str(SCRIPT), site.url], capture_output=True, text=True, timeout=60)
        return result, site

    def test_a_correct_entry_passes_three_alternating_rounds_and_the_cookie_override(self):
        result, site = self.check("correct")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("site language check: PASS", result.stdout)
        self.assertEqual(site.root_requests[:7], ["zh-CN", "en-US", "en-US", "zh-CN", "zh-CN", "en-US", "zh-CN"])

    def test_a_publish_time_installer_is_fetched_and_accepted_as_non_html(self):
        result, _ = self.check("installer")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("installer present", result.stdout)

    def test_an_always_chinese_entry_fails_at_the_first_english_step(self):
        result, _ = self.check("chinese")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("site language check: FAIL (round 1 English)", result.stderr)
        self.assertIn("expected 302", result.stderr)

    def test_an_always_redirecting_entry_fails_at_the_first_chinese_step(self):
        result, _ = self.check("redirect")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("site language check: FAIL (round 1 Chinese)", result.stderr)
        self.assertIn("expected 200", result.stderr)


if __name__ == "__main__":
    unittest.main()
