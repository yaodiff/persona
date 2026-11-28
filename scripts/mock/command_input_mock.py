"""An Antigravity Agent's background command waits for a line of input (#14527).

MOCK_COMMAND_INPUT=1: the sign-in task waits at its prompt. Send answers it with
values.input; Not now answers no. GET /mock/resolved has the answers.
"""
import os

HELP = "MOCK_COMMAND_INPUT=1  An Antigravity command waits for its sign-in code; Send answers it (#14527)"


def register(d):
    d.help(HELP)
    if os.environ.get("MOCK_COMMAND_INPUT") != "1":
        return
    hub = d.hub
    hub.COMMAND_INPUT = "c0de0000-0000-4000-8000-000000014527"
    hub.COMMAND_INPUT_PROMPT = "Enter the authorization code:"
    hub.COMMAND_INPUT_CARD = {
        "id": "ci_0c14527a5e1f2b3c", "machine_id": "macbook", "session_id": hub.COMMAND_INPUT,
        "session_title": "Sign in to the cloud CLI",
        "cwd": "/Users/me/code/notebooks", "kind": "permission", "created_at": hub.ago(minutes=1),
        "expires_at": hub.later(hours=24),
        "tool_name": "command_input", "tool_title": "Command input", "agent": "agy", "agent_label": "Antigravity",
        "tool_input": {"command": "example-cli auth login --no-browser", "task": "task-7",
                       "prompt": hub.COMMAND_INPUT_PROMPT, "masked": True,
                       "output": "To sign in, open this page in any browser:\n  https://auth.example.com/authorize?client=demo&scope=usage\n"
                                 "After approving, the page shows a code."},
        "summary": "A command is waiting for your input: " + hub.COMMAND_INPUT_PROMPT,
        "fields": [{"key": "input", "label": hub.COMMAND_INPUT_PROMPT,
                    "placeholder": "Type what the command asks for", "secret": True}]}
    hub.SESSIONS.append(hub.sess(
        "macbook", hub.COMMAND_INPUT, "Sign in to the cloud CLI", "/Users/me/code/notebooks", "working", "needs_you",
        "notebooks", 9, "agy", 1, 1, "The sign-in is waiting for the code from its page.", agent="agy",
        agent_label="Antigravity", launcher="agy", pending=1))
    hub.ITEMS[hub.COMMAND_INPUT] = [
        hub.it("user", "text", 4, text="Sign in to the cloud CLI and tell me this month's usage."),
        hub.it("assistant", "text", 2,
               text="The sign-in is running. Open the address it printed, approve, and send me the code the page shows."),
    ]
    hub.INTERACTIONS.append(hub.COMMAND_INPUT_CARD)
