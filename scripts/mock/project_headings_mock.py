"""Two same-named projects on one computer for the native By Project headings (#13558).

MOCK_PROJECT_HEADINGS=1 replaces the Agents with a compact fixture: two projects named "site" on one computer,
differentiated by their folders, another computer's project, and a standalone Agent. It clears pending cards so the
project headings stay in view while the UI test starts an Agent in one of them.
"""
import os

MODE = os.environ.get("MOCK_PROJECT_HEADINGS") == "1"
HELP = "MOCK_PROJECT_HEADINGS=1  two same-named projects on one computer and one on another (#13558)"


def install(hub):
    hub.SESSIONS[:] = [
        hub.sess("macbook", "45e3b8c7-0000-4000-8000-000000000001", "Check the desktop site", "/tmp/site",
                 "working", "working", "site", 85, "review", 1, 0, "Checking an example.",
                 workspace_cwd="/Users/me/Desktop/site"),
        hub.sess("macbook", "45e3b8c7-0000-4000-8000-000000000002", "Check the code site", "/Users/me/code/site",
                 "working", "working", "site", 86, "build", 1, 1, "Checking another example."),
        hub.sess("studio", "45e3b8c7-0000-4000-8000-000000000003", "Check the studio site", "/Users/me/work/site",
                 "working", "working", "site", 87, "inspect", 1, 2, "Checking the second computer."),
        hub.sess("macbook", "45e3b8c7-0000-4000-8000-000000000004", "Loose agent", "/Users/me/notes",
                 "working", "working", "No project", 88, "notes", 1, 3, "A standalone conversation.",
                 space_id=None, workspace_id=None),
    ]
    hub.INTERACTIONS.clear()
    hub.MOCK_INTERACTIONS.clear()


def register(d):
    d.help(HELP)
    if not MODE:
        return
    install(d.hub)
    d.on_reset(lambda: install(d.hub))
