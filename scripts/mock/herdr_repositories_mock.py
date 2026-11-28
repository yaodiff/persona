"""Herdr repository grouping fixtures for #13505.

MOCK_HERDR_REPOSITORIES=1 makes one repository with a main checkout, an active linked tree, a removed linked tree and
a pinned unavailable folder workspace. It is a domain of scripts/mock/mock_domains.py, so mock_hub.py takes no new
MOCK_* switch.
"""
import os

ROOT = "/Users/me/code/repo-fixture"
TREES = "/Users/me/.herdr/worktrees/repo-fixture"

HELP = ("MOCK_HERDR_REPOSITORIES=1  one herdr repository with a main checkout, an active and a removed linked tree, "
        "and a pinned unavailable workspace (#13505)")

WORKSPACES = [
    {"id": "repo-main", "machine_id": "macbook", "source": "herdr", "source_id": "w81",
     "title": "Main checkout", "cwd": ROOT, "online": True, "available": True},
    {"id": "repo-linked", "machine_id": "macbook", "source": "herdr", "source_id": "w82",
     "title": "Active tree", "cwd": TREES + "/active",
     "branch": "feature/active", "online": True, "available": True},
    {"id": "repo-notes", "machine_id": "macbook", "source": "herdr", "source_id": "w84",
     "title": "notes-fixture", "cwd": "/Users/me/code/notes-fixture",
     "online": True, "available": False, "pinned_at": "2026-11-16T10:00:00Z"},
]


def install(hub):
    if not os.environ.get("MOCK_HERDR_REPOSITORIES"):
        return
    hub.SESSIONS[:] = [
        hub.sess("macbook", "a1f3c2d4-0000-4000-8000-000000013501", "Main checkout agent", ROOT,
                 "working", "working", "Main checkout", 81, "main", 1, 1, "Reviewing the main checkout.",
                 repo_root=ROOT, worktree=ROOT),
        hub.sess("macbook", "a1f3c2d4-0000-4000-8000-000000013502", "Active tree agent", TREES + "/active",
                 "working", "working", "Active tree", 82, "active", 1, 2, "Reviewing an active worktree.",
                 repo_root=ROOT, worktree=TREES + "/active", branch="feature/active"),
        hub.sess("macbook", "a1f3c2d4-0000-4000-8000-000000013503", "Removed tree agent", TREES + "/removed",
                 "done", "unread", "Removed tree", 83, "removed", 1, 3, "This worktree's changes were merged."),
        hub.sess("macbook", "a1f3c2d4-0000-4000-8000-000000013504", "Notes agent", "/Users/me/code/notes-fixture",
                 "working", "working", "notes-fixture", 84, "notes", 1, 4, "Organizing example notes."),
    ]


def workspaces(h, req):
    if not os.environ.get("MOCK_HERDR_REPOSITORIES"):
        return False
    return h.send_json({"workspaces": WORKSPACES})


def register(d):
    d.help(HELP)
    d.route("GET", "/api/v1/workspaces", workspaces)
    d.on_reset(lambda: install(d.hub))
    install(d.hub)
