"""Integration test: real Git worktrees, fake GitHub/model, no live PRs."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
WATCH = ROOT / "scripts" / "operations" / "pr_watch.py"

FAKE_GH = r'''
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
state_path, log_path = Path(os.environ["FAKE_GH_STATE"]), Path(os.environ["FAKE_GH_LOG"])
state = json.loads(state_path.read_text())
entry = {"argv": args}
out = ""
code = 0
if args[:1] == ["api"]:
    entry["input"] = sys.stdin.read() if "--input" in args else ""
    entry["review"] = json.loads(entry["input"]) if entry["input"] else None
if args[:1] == ["api"] and args[1].endswith("/reviews"):
    payload = entry["review"]
    code = 422 if os.environ.get("FAKE_REJECT_INLINE") == "1" else 0
    entry["returncode"] = code
    with open(log_path, "a") as log:
        log.write(json.dumps(entry) + "\n")
    print(json.dumps({"id": 1}))
    sys.exit(code)
if args[:2] == ["repo", "view"]:
    out = state["repo"] if "--jq" in args else json.dumps({"nameWithOwner": state["repo"]})
elif args[:2] == ["pr", "list"]:
    out = json.dumps([] if state["pr"]["state"] != "OPEN" else [{k: state["pr"][k] for k in ("number", "headRefOid", "isDraft", "baseRefName")}])
elif args[:2] == ["pr", "view"]:
    out = json.dumps(state["pr"])
elif args[:2] == ["api"] and "/commits/" in args[1]:
    out = state["pr"]["baseRefOid"]
elif args[:2] == ["api"] and args[1].endswith("/reviews"):
    payload = json.loads(sys.stdin.read())
    entry["review"] = payload
    if os.environ.get("FAKE_REJECT_INLINE") == "1":
        code = 422
    else:
        out = json.dumps({"id": 1})
    entry["returncode"] = code
    with open(log_path, "a") as log:
        log.write(json.dumps(entry) + "\n")
    print(out)
    sys.exit(code)
elif args[:2] in (["pr", "review"], ["pr", "comment"]):
    entry["body"] = args[args.index("--body") + 1]
elif args[:2] == ["pr", "merge"]:
    entry["head"] = args[args.index("--match-head-commit") + 1]
    entry["subject"] = args[args.index("--subject") + 1]
    entry["body"] = open(args[args.index("--body-file") + 1]).read()
    if entry["head"] != state["pr"]["headRefOid"]:
        code = 1
    else:
        state["pr"]["state"] = "MERGED"
        state["merged"] = entry
        state_path.write_text(json.dumps(state))
entry["returncode"] = code
with open(log_path, "a") as log:
    log.write(json.dumps(entry) + "\n")
print(out)
sys.exit(code)
'''

FAKE_REVIEWER = r'''
import json, os, sys
findings = json.loads(os.environ.get("FAKE_REVIEW_FINDINGS", "[]"))
prompt = sys.stdin.read()
assert "independent code reviewer" in prompt
assert "fix.py" in prompt
assert "diff --git" in prompt
with open(os.environ["FAKE_GH_LOG"], "a") as log:
    log.write(json.dumps({"reviewer": sys.argv[1:], "cwd": os.getcwd(), "prompt": prompt, "findings": findings}) + "\n")
result = {"subtype": "success", "is_error": False, "permission_denials": [],
          "structured_output": {"findings": findings}}
print(json.dumps(result))
'''


class WatcherIntegrationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="pr-watch-integration-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.repo = self.root / "repo"
        self.origin = self.root / "origin.git"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.repo.mkdir()
        for name, code in (("gh", FAKE_GH), ("reviewer", FAKE_REVIEWER)):
            executable = self.bin / name
            executable.write_text("#!" + sys.executable + "\n" + code)
            executable.chmod(0o755)
        self.state_path = self.root / "github.json"
        self.log_path = self.root / "calls.jsonl"
        self.env = {key: value for key, value in os.environ.items() if not key.startswith(("GIT_", "GH_", "PYTHON"))}
        self.env.update({"PATH": str(self.bin) + os.pathsep + os.environ["PATH"], "HOME": str(self.root / "home"),
                         "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                         "GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
                         "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
                         "FAKE_GH_STATE": str(self.state_path), "FAKE_GH_LOG": str(self.log_path)})
        (self.root / "home").mkdir()
        self.git("init", "-q", "--bare", str(self.origin), cwd=self.root)
        self.git("init", "-q", "-b", "main")
        self.git("remote", "add", "origin", str(self.origin))
        (self.repo / "README.md").write_text("Example fixture.\n")
        self.git("add", ".")
        self.git("commit", "-qm", "Base")
        self.git("push", "-q", "origin", "main")
        self.base = self.git("rev-parse", "HEAD")
        self.git("checkout", "-qb", "fix")
        (self.repo / "fix.py").write_text("def increment(n):\n    return n + 1\n")
        self.git("add", "fix.py")
        self.git("commit", "-qm", "Fix")
        self.git("push", "-q", "origin", "+HEAD:refs/pull/7/head")
        self.head = self.git("rev-parse", "HEAD")
        self.pr = {"number": 7, "title": "fix: increment", "body": "Fixes #6", "state": "OPEN", "isDraft": False,
                   "headRefOid": self.head, "headRefName": "fix", "baseRefName": "main", "baseRefOid": self.base,
                   "isCrossRepository": False, "mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN",
                   "reviewDecision": "", "url": "https://example.com/pr/7"}
        self.state_path.write_text(json.dumps({"repo": "example/one", "pr": self.pr}))
        self.config_path = self.root / "config.json"
        self.config_path.write_text(json.dumps({
            "repositories": [{"name": "example/one", "checkout": str(self.repo), "base": "main", "merge": True,
                              "verify": {"profile": "commands", "commands": [["python3", "-c", "print('checked')"]]}}],
            "reviewer": {"command": "reviewer", "timeout_seconds": 60},
            "state": str(self.root / "watch-state.json"), "poll_seconds": 60, "workers": 1, "webhook": {}}))

    def git(self, *args, cwd=None):
        result = subprocess.run(["git"] + list(args), cwd=cwd or self.repo, env=self.env, capture_output=True, text=True)
        if result.returncode:
            self.fail(result.stdout + result.stderr)
        return result.stdout.strip()

    def run_watch(self):
        result = subprocess.run([sys.executable, "-I", str(WATCH), "--config", str(self.config_path), "--once"],
                                cwd=self.root, env=self.env, capture_output=True, text=True, timeout=90)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = [json.loads(line) for line in self.log_path.read_text().splitlines()]
        return calls

    def test_clean_pr_merges_exact_head_and_restart_does_not_merge_again(self):
        calls = self.run_watch()
        self.assertEqual(self.pr["headRefOid"], json.loads(self.state_path.read_text())["merged"]["head"])
        self.assertEqual(self.git("rev-parse", "HEAD"), self.head)  # source working tree untouched
        before = len(calls)
        self.assertFalse(any(call.get("argv", [])[:2] == ["pr", "merge"] for call in self.run_watch()[before:]))
        self.assertTrue(any("reviewer" in call for call in calls))

    def test_findings_use_changed_line_and_do_not_merge(self):
        self.env["FAKE_REVIEW_FINDINGS"] = json.dumps([{"path": "fix.py", "line": 2, "severity": "P1",
                                                        "summary": "Example defect", "suggestion": "Correct it"}])
        calls = self.run_watch()
        review = next(call["review"] for call in calls if "review" in call)
        self.assertEqual(review["commit_id"], self.head)
        self.assertEqual(review["comments"][0]["path"], "fix.py")
        self.assertEqual(review["comments"][0]["line"], 2)
        self.assertFalse(any(call.get("argv", [])[:2] == ["pr", "merge"] for call in calls))
        self.assertNotIn("merged", json.loads(self.state_path.read_text()))

    def test_invalid_inline_anchor_falls_back_to_a_grouped_review(self):
        self.env["FAKE_REJECT_INLINE"] = "1"
        self.env["FAKE_REVIEW_FINDINGS"] = json.dumps([{"path": "fix.py", "line": 2, "severity": "P1",
                                                        "summary": "Example defect", "suggestion": "Correct it"}])
        calls = self.run_watch()
        review = next(call for call in calls if call.get("argv", [])[:2] == ["pr", "review"])
        self.assertIn("Example defect", review["body"])
        self.assertFalse(any(call.get("argv", [])[:2] == ["pr", "merge"] for call in calls))

    def test_invalid_finding_fails_closed_without_comment_or_merge(self):
        self.env["FAKE_REVIEW_FINDINGS"] = json.dumps([{"path": "fix.py", "line": 99, "severity": "P1",
                                                        "summary": "Wrong anchor", "suggestion": "None"}])
        calls = self.run_watch()
        self.assertFalse(any("review" in call for call in calls))
        self.assertFalse(any(call.get("argv", [])[:2] == ["pr", "merge"] for call in calls))


if __name__ == "__main__":
    unittest.main()
