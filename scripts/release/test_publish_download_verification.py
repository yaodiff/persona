"""publish.sh verifies the bytes served for current and versioned release archives."""
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
PUBLISHER = ROOT / "scripts/release/publish.sh"


def publisher_functions():
    """check_served, node_archives and check_versioned as publish.sh defines them."""
    text = PUBLISHER.read_text()
    begin = text.index("# check_served")
    return text[begin:text.index("same_as_served()", begin)]


FAKE_CURL = r'''
curl() {
  local method=GET output="" url="" relative file
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -o|--output) output="$2"; shift 2 ;;
      -fsSI|-I|--head) method=HEAD; shift ;;
      http://*|https://*) url="$1"; shift ;;
      *) shift ;;
    esac
  done
  [ -n "$url" ] || return 2
  case "$url" in
    "$PERSONA_PUBLISH_URL"/*) relative="${url#"$PERSONA_PUBLISH_URL"/}" ;;
    *) return 2 ;;
  esac
  printf '%s %s\n' "$method" "$relative" >> "$CURL_LOG"
  file="$SERVED/$relative"
  [ -f "$file" ] || return 22
  if [ "$method" = HEAD ]; then
    wc -c < "$file" | awk '{print "Content-Length: " $1}'
    return 0
  fi
  [ "${FAIL_GET:-}" != "$relative" ] || return 22
  if [ -n "$output" ]; then
    cat "$file" > "$output"
  else
    cat "$file"
  fi
}
'''


class PublishDownloadVerificationTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="persona-publish-downloads-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.downloads = self.root / "downloads"
        self.served = self.root / "served"
        self.downloads.mkdir()
        (self.served / "dl").mkdir(parents=True)
        (self.root / "tmp").mkdir()
        (self.served / "dl/SLOT").write_text("a\n")
        self.version = "1.2.3"
        self.manifest = b'{"version":"1.2.3","files":[]}\n'
        (self.downloads / "latest.json").write_bytes(self.manifest)
        versioned = self.served / "dl" / self.version
        versioned.mkdir()
        (versioned / "manifest.json").write_bytes(self.manifest)

    def archive(self, name, expected, remote, versioned=False):
        local = self.downloads / name
        local.write_bytes(expected)
        destination = self.served / "dl" / self.version / name if versioned else self.served / "dl" / name
        destination.write_bytes(remote)
        return local

    def write_sums(self, *archives):
        lines = [f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in archives]
        (self.downloads / "SHA256SUMS").write_text("".join(lines))

    def check(self, function, fail_get=None):
        script = "\n".join((
            "set -euo pipefail",
            publisher_functions(),
            FAKE_CURL,
            'check_served "$CHECK_SUMS" a' if function == "served" else 'check_versioned "$CHECK_DIR" "$CHECK_VERSION"',
        ))
        env = {
            **os.environ,
            "PERSONA_PUBLISH_URL": "https://release.invalid",
            "SERVED": str(self.served),
            "CURL_LOG": str(self.root / "curl.log"),
            "CHECK_SUMS": str(self.downloads / "SHA256SUMS"),
            "CHECK_DIR": str(self.downloads),
            "CHECK_VERSION": self.version,
            "TMPDIR": str(self.root / "tmp"),
        }
        if fail_get:
            env["FAIL_GET"] = fail_get
        result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, timeout=10)
        self.assertEqual(list((self.root / "tmp").iterdir()), [], "the check left temporary download files")
        return result

    def requests(self):
        log = self.root / "curl.log"
        return log.read_text() if log.exists() else ""

    def test_served_rejects_stale_equal_length_bytes(self):
        archive = self.archive("personad-linux-amd64.gz", b"new-artifact-bytes", b"old-artifact-bytez")
        self.write_sums(archive)
        result = self.check("served")
        self.assertNotEqual(result.returncode, 0, "stale equal-length served bytes passed")
        self.assertIn("GET dl/personad-linux-amd64.gz", self.requests())

    def test_served_accepts_the_expected_bytes(self):
        archive = self.archive("personad-linux-amd64.gz", b"new-artifact-bytes", b"new-artifact-bytes")
        self.write_sums(archive)
        result = self.check("served")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("GET dl/personad-linux-amd64.gz", self.requests())

    def test_served_verifies_sums_when_the_local_archive_is_absent(self):
        archive = self.archive("personad-linux-amd64.gz", b"new-artifact-bytes", b"new-artifact-bytes")
        self.write_sums(archive)
        archive.unlink()  # SITE_ONLY fetches SHA256SUMS but not the archives themselves.
        result = self.check("served")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("GET dl/personad-linux-amd64.gz", self.requests())

    def test_served_rejects_a_get_failure(self):
        archive = self.archive("personad-linux-amd64.gz", b"new-artifact-bytes", b"new-artifact-bytes")
        self.write_sums(archive)
        result = self.check("served", fail_get="dl/personad-linux-amd64.gz")
        self.assertNotEqual(result.returncode, 0, "a failed archive GET passed")

    def test_versioned_rejects_stale_equal_length_bytes(self):
        archive = self.archive("persona-node-linux-amd64.gz", b"new-artifact-bytes", b"old-artifact-bytez", True)
        result = self.check("versioned")
        self.assertNotEqual(result.returncode, 0, "stale equal-length versioned bytes passed")
        self.assertIn("GET dl/1.2.3/persona-node-linux-amd64.gz", self.requests())

    def test_versioned_accepts_the_expected_bytes(self):
        self.archive("persona-node-linux-amd64.gz", b"new-artifact-bytes", b"new-artifact-bytes", True)
        result = self.check("versioned")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("GET dl/1.2.3/persona-node-linux-amd64.gz", self.requests())

    def test_versioned_rejects_a_get_failure(self):
        self.archive("persona-node-linux-amd64.gz", b"new-artifact-bytes", b"new-artifact-bytes", True)
        result = self.check("versioned", fail_get="dl/1.2.3/persona-node-linux-amd64.gz")
        self.assertNotEqual(result.returncode, 0, "a failed versioned archive GET passed")

    def test_versioned_still_requires_the_exact_manifest(self):
        self.archive("persona-node-linux-amd64.gz", b"new-artifact-bytes", b"new-artifact-bytes", True)
        (self.served / "dl" / self.version / "manifest.json").write_bytes(self.manifest + b"\n")
        result = self.check("versioned")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("served dl/1.2.3/manifest.json differs", result.stderr)
        self.assertNotIn("GET dl/1.2.3/persona-node-linux-amd64.gz", self.requests())


if __name__ == "__main__":
    unittest.main()
