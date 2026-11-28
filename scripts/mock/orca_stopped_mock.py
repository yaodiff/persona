"""Orca's engine rows as the node finds Orca (#17037), a domain of scripts/mock/mock_domains.py.

MOCK_ORCA_STOPPED=1: each machine's Orca row carries the node's `state` (docs/integrations/engines.md "When Orca is not
running"): installed and not running on the Mac Studio, with the version its app says (its Agents list shows "Orca isn't
running" there when Orca is in its engine set, as with MOCK_ENGINE_SETS=1), an incompatible version on the server (its
version and the catalog's note), missing elsewhere. With the switch off the rows are as before.
"""
import os

ON = os.environ.get("MOCK_ORCA_STOPPED") == "1"
HELP = "MOCK_ORCA_STOPPED=1  Orca installed and not running on the Mac Studio, incompatible on the server (#17037)"
INCOMPATIBLE = "This version of Orca doesn’t work with Persona. Update both to the latest version"


def install(hub):
    for machine in hub.MACHINES:
        for engine in machine.get("engines", []):
            if engine.get("id") != "orca":
                continue
            if machine["id"] == "studio":
                engine.update({"available": False, "version": "1.4.277", "state": "stopped"})
                engine.pop("note", None)
            elif machine["id"] == "server":
                engine.update({"available": False, "version": "1.4.100", "state": "incompatible", "note": INCOMPATIBLE})
            else:
                engine["state"] = "missing"


def register(d):
    d.help(HELP)
    if not ON:
        return  # off: the rows are as the built-in fixtures made them
    install(d.hub)
