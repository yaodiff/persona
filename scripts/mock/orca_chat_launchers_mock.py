"""The Orca bridge's chat launchers in New Agent (#17047, node orcabridge_start.go).

MOCK_ORCA_CHAT_LAUNCHERS=1 adds to the server's launchers (GET /api/v1/machines/server/launchers) the bridge's entries for
the two providers its Orca has installed, as the node lists them: "Orca chat · Claude Code" (orca-claude, Claude Code's
choices) and "Orca chat · Codex" (orca-codex, the modes Orca takes for Codex), each with the models Orca's chats of
that provider list (GET …/launchers/{name}/models). web/e2e/orca-chat-launchers.mjs takes its screenshots with it.
Unset: nothing changes.
"""
import os

MODE = os.environ.get("MOCK_ORCA_CHAT_LAUNCHERS", "")

LAUNCHERS = [
    {"name": "orca-claude", "display_name": "Orca chat · Claude Code", "model": "opus", "source": "orca"},
    {"name": "orca-codex", "display_name": "Orca chat · Codex", "model": "gpt-5.5", "source": "orca", "agent": "codex",
     "agent_type": "codex", "permission_modes": ["default", "auto", "bypassPermissions"]},
]
MODELS = {
    "orca-claude": {"agent": "claude", "models": [{"id": "opus", "name": "Opus", "default": True, "efforts": ["low", "medium", "high", "max"]},
                                                  {"id": "sonnet", "name": "Sonnet", "efforts": ["low", "medium", "high"]}]},
    "orca-codex": {"agent": "codex", "models": [{"id": "gpt-5.5", "name": "GPT-5.5", "default": True, "efforts": ["low", "medium", "high"]}]},
}


def register(d):
    d.help("MOCK_ORCA_CHAT_LAUNCHERS=1  the server's Orca chat launchers in New Agent (#17047)")
    if not MODE:
        return
    hub = d.hub
    hub.LAUNCHER_MODELS.update(MODELS)
    listed = hub.machine_launchers

    def machine_launchers(mid=""):
        out = listed(mid)
        return out + [dict(l) for l in LAUNCHERS] if mid == "server" else out

    hub.machine_launchers = machine_launchers
