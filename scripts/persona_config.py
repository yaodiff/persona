#!/usr/bin/env python3
"""Reads the personal build / deploy config (docs/operations/config.md) so no script hard-codes one person's setup.

The config lives outside the repository so clean worktrees (releases) see it too:
  $PERSONA_CONFIG, else ~/.config/persona/config.json   (template: config.example.json)

  python3 scripts/persona_config.py env              # shell exports: eval "$(python3 scripts/persona_config.py env)"
  python3 scripts/persona_config.py xcconfig         # writes apple/Config.xcconfig (bundle id, team) for XcodeGen / Xcode
  python3 scripts/persona_config.py export-options <out.plist> [upload|export]
  python3 scripts/persona_config.py get <dotted.key>  # one value, e.g. hub.url
"""
import json
import os
import plistlib
import re
import shlex
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "scripts"))
from persona_identity import config_path, environment_aliases, LEGACY_PREFIX


def path() -> str:
    return config_path()


def load() -> dict:
    p = path()
    if not os.path.exists(p):
        sys.exit(f"missing {p}\nCopy config.example.json there and fill it in (docs/operations/config.md).")
    with open(p) as f:
        cfg = json.load(f)
    return cfg


def get(cfg: dict, key: str, default=None):
    cur = cfg
    for part in key.split("."):
        if not isinstance(cur, dict) or part not in cur:
            return default
        cur = cur[part]
    return cur


def machine(cfg: dict, mid: str) -> dict:
    for m in cfg.get("machines", []):
        if m.get("id") == mid:
            return m
    sys.exit(f"machine {mid!r} is not in {path()}")


def env(cfg: dict) -> dict:
    store = cfg.get("app_store", {})
    hub = cfg.get("hub", {})
    hub_ssh = machine(cfg, hub["machine"]).get("ssh", hub["machine"]) if hub.get("machine") else ""
    out = {
        "PERSONA_BUNDLE_ID": get(cfg, "app.bundle_id", ""),
        "PERSONA_WATCH_WIDGET_BUNDLE_ID": watch_widget_bundle_id(cfg),
        "PERSONA_WATCH_APP_GROUP": watch_app_group(cfg),
        "PERSONA_TEAM_ID": get(cfg, "app.team_id", ""),
        "ASC_KEY_ID": store.get("key_id", ""),
        "ASC_ISSUER_ID": store.get("issuer_id", ""),
        "ASC_KEY_PATH": os.path.expanduser(store.get("key_path", "")),
        "ASC_APP_ID": str(store.get("app_id", "")),
        "PERSONA_HUB_MACHINE": hub.get("machine", ""),
        "PERSONA_HUB_SSH": hub_ssh,
        "PERSONA_HUB_URL": hub.get("url", ""),
        "PERSONA_MACHINES": " ".join(m["id"] for m in cfg.get("machines", [])),
        "PERSONA_SIM": cfg.get("simulator", ""),
        # Where scripts/release/publish.sh puts personad and install.sh (the site/ worker's domain), and the wrangler to run.
        "PERSONA_PUBLISH_URL": get(cfg, "publish.site_url", "").rstrip("/"),
        # Where the personad built here looks for its updates (-X main.updateURL): publish.update_url, else <site_url>/dl.
        "PERSONA_UPDATE_URL": update_url(cfg),
        "PERSONA_WRANGLER": os.path.expanduser(get(cfg, "publish.wrangler", "")),
        # Meta glasses (docs/apple/meta-glasses.md): the project.yml include that adds the Meta Wearables SDK is on only for YES.
        "PERSONA_META_WEARABLES": "YES" if get(cfg, "app.meta_wearables.enabled", False) is True else "NO",
    }
    # Which Xcode builds (and releases) the app; a DEVELOPER_DIR already in the environment wins. iPhone Duo needs
    # Xcode 27 or later: built with an older one, the app does not reach under the status bar and the camera.
    if cfg.get("xcode") and not os.environ.get("DEVELOPER_DIR"):
        out["DEVELOPER_DIR"] = os.path.join(os.path.expanduser(cfg["xcode"]), "Contents", "Developer")
    for key, default in list(out.items()):
        if key.startswith("PERSONA_"):
            out[LEGACY_PREFIX + key[8:]] = default
    return out


def update_url(cfg: dict) -> str:
    """The update source built into personad (-X main.updateURL, docs/operations/hub-update.md "Update source"):
    publish.update_url as given (the directory latest.json is in, e.g. a GitHub repository's releases/latest/download),
    else publish.site_url's dl/, else none. A hub's hub.json update.url still replaces it at run time."""
    value = get(cfg, "publish.update_url", "")
    if value in ("", None):
        site = get(cfg, "publish.site_url", "")
        return site.rstrip("/") + "/dl" if isinstance(site, str) and site.strip() else ""
    if (not isinstance(value, str) or not re.fullmatch(r"(https://[^/\s?#@]+|file://)(/[^\s?#]*)?", value.strip())
            or value.strip().rstrip("/").endswith("/latest.json")):
        sys.exit("publish.update_url must be an https:// (or file:///) directory without a user, query or fragment: "
                 "the one latest.json is in (docs/operations/config.md)")
    return value.strip().rstrip("/")


def app_group(cfg: dict) -> str:
    return get(cfg, "app.app_group", "") or "group." + get(cfg, "app.bundle_id", "")


def watch_widget_bundle_id(cfg: dict) -> str:
    """A new identity only; existing registered Bundle IDs never change."""
    bid = get(cfg, "app.bundle_id", "")
    identifier = get(cfg, "app.watch_widget_bundle_id", "") or bid + ".watchkitapp.widgets"
    if not isinstance(identifier, str) or not identifier.startswith(bid + ".watchkitapp."):
        sys.exit("app.watch_widget_bundle_id must be a new child of the existing watch app Bundle ID")
    return identifier


def watch_app_group(cfg: dict) -> str:
    """The wrist and its complication share a container, separate from the phone's credentials."""
    group = get(cfg, "app.watch_app_group", "") or app_group(cfg) + ".watch"
    if not isinstance(group, str) or not group.startswith("group.") or group == app_group(cfg):
        sys.exit("app.watch_app_group must be a separate App Group, never the phone's App Group")
    return group


def provisioning_profiles(cfg: dict) -> dict[str, str]:
    """Configured signing names are account resources, not product display names."""
    bid = get(cfg, "app.bundle_id", "")
    profiles = {
        bid: "Persona App Store",
        bid + ".watchkitapp": "Persona Watch App Store",
        watch_widget_bundle_id(cfg): "Persona Watch Widgets App Store",
        bid + ".liveactivity": "Persona Live Activity App Store",
        bid + ".notify": "Persona Notify App Store",
        bid + ".share": "Persona Share App Store",
    }
    overrides = get(cfg, "app_store.provisioning_profiles", {})
    if not isinstance(overrides, dict):
        sys.exit("app_store.provisioning_profiles must be a Bundle ID to profile name object")
    for identifier, name in overrides.items():
        if identifier not in profiles or not isinstance(name, str) or not name.strip():
            sys.exit("app_store.provisioning_profiles requires known app Bundle IDs and nonempty profile names")
        profiles[identifier] = name
    return profiles


# The app's entitlements. The opt-in ones need the capability on the App ID first (CarPlay voice: Apple's approval,
# docs/apple/carplay.md; HealthKit background delivery, docs/assistant/health.md); the generated copy sits in apple/,
# outside the Persona/ source folder, so XcodeGen does not pick it up as a resource.
APP_ENTITLEMENTS = os.path.join("Persona", "Resources", "Persona.entitlements")
OPT_IN_ENTITLEMENTS = "PersonaOptIn.entitlements"
# config.json switch → the entitlement it adds.
OPT_IN = {
    "app.carplay_voice": "com.apple.developer.carplay-voice-based-conversation",
    "app.health_background_delivery": "com.apple.developer.healthkit.background-delivery",
}


def opted_in(cfg: dict, switch: str) -> bool:
    # Only JSON true turns one on: a string like "false" is truthy in Python.
    return get(cfg, switch, False) is True


def app_entitlements(cfg: dict) -> str:
    # Opt-in: an App ID without the capability cannot sign its entitlement, so signing without them must keep working.
    # Returns the path relative to apple/.
    added = [key for switch, key in OPT_IN.items() if opted_in(cfg, switch)]
    if not added:
        return APP_ENTITLEMENTS
    with open(os.path.join(ROOT, "apple", APP_ENTITLEMENTS), "rb") as f:
        entitlements = plistlib.load(f)
    for key in added:
        entitlements[key] = True
    with open(os.path.join(ROOT, "apple", OPT_IN_ENTITLEMENTS), "wb") as f:
        plistlib.dump(entitlements, f)
    return OPT_IN_ENTITLEMENTS


def xcconfig(cfg: dict):
    # Generated, git-ignored: the committed project only says $(PERSONA_BUNDLE_ID).
    out = os.path.join(ROOT, "apple", "Config.xcconfig")
    with open(out, "w") as f:
        f.write("// Generated from %s by scripts/persona_config.py; do not edit or commit.\n" % path())
        f.write("PERSONA_BUNDLE_ID = %s\n" % get(cfg, "app.bundle_id", ""))
        # The App Group the app shares files with the widget extension (docs/operations/release.md "App Group").
        f.write("PERSONA_APP_GROUP = %s\n" % app_group(cfg))
        f.write("PERSONA_WATCH_APP_GROUP = %s\n" % watch_app_group(cfg))
        f.write("PERSONA_WATCH_WIDGET_BUNDLE_ID = %s\n" % watch_widget_bundle_id(cfg))
        f.write("DEVELOPMENT_TEAM = %s\n" % get(cfg, "app.team_id", ""))
        # The push relay the app registers with (docs/operations/push-relay.md); "//" starts a comment in xcconfig, hence $().
        f.write("PERSONA_PUSH_RELAY = %s\n" % get(cfg, "app.push_relay", "").rstrip("/").replace("//", "/$()/"))
        # Which entitlements the app signs with (app_entitlements; docs/apple/carplay.md).
        f.write("PERSONA_APP_ENTITLEMENTS = %s\n" % app_entitlements(cfg))
        # Workout reports right after a workout (Info.plist PersonaHealthBackgroundDelivery, with its entitlement above).
        f.write("PERSONA_HEALTH_BACKGROUND_DELIVERY = %s\n" % ("YES" if opted_in(cfg, "app.health_background_delivery") else "NO"))
        # "No hub yet?" on the first screen opens this page (docs/operations/getting-started.md published; empty: the row is hidden).
        f.write("PERSONA_GETTING_STARTED_URL = %s\n" % get(cfg, "publish.getting_started_url", "").replace("//", "/$()/"))
        # Meta Wearables (docs/apple/meta-glasses.md): 0 means Developer Mode (no attestation), as Meta's docs say.
        f.write("PERSONA_META_APP_ID = %s\n" % (str(get(cfg, "app.meta_wearables.app_id", "") or "") or "0"))
        f.write("PERSONA_META_CLIENT_TOKEN = %s\n" % (get(cfg, "app.meta_wearables.client_token", "") or ""))
        # The in-app tunnel core (docs/operations/network.md"Connect to the hub through an in-app tunnel"): linked into device builds only when set.
        include, ldflags = tunnel_core(cfg)
        f.write("PERSONA_TUNNEL_INCLUDE = %s\n" % include)
        f.write("PERSONA_TUNNEL_LDFLAGS = %s\n" % ldflags)
        # Existing Xcode projects can consume the generated carrier names without changing account identities.
        for suffix in ("BUNDLE_ID", "APP_GROUP", "PUSH_RELAY", "APP_ENTITLEMENTS", "GETTING_STARTED_URL",
                       "META_APP_ID", "META_CLIENT_TOKEN", "TUNNEL_INCLUDE", "TUNNEL_LDFLAGS"):
            f.write(LEGACY_PREFIX + suffix + " = $(PERSONA_" + suffix + ")\n")


def tunnel_core(cfg: dict) -> tuple[str, str]:
    """`app.tunnel_core`: a directory with libpersonatunnel.a and the module.modulemap of PersonaTunnelCore."""
    raw = get(cfg, "app.tunnel_core", "") or ""
    if not raw:
        return "", ""
    d = os.path.abspath(os.path.expanduser(raw))
    missing = [n for n in ("libpersonatunnel.a", "module.modulemap") if not os.path.isfile(os.path.join(d, n))]
    if missing:
        sys.exit(f"app.tunnel_core {d}: missing {', '.join(missing)} (docs/operations/network.md「Connecting to hub through an in-app tunnel」)")
    q = '"%s"' % d
    return q, f"-L{q} -lpersonatunnel -lresolv -framework Security -framework CoreFoundation"


def export_options(cfg: dict, out: str, destination: str = "upload"):
    options = {
        "method": "app-store-connect",
        "destination": destination,
        "signingStyle": "manual",
        "signingCertificate": "Apple Distribution",
        "provisioningProfiles": provisioning_profiles(cfg),
        "teamID": get(cfg, "app.team_id", ""),
        "uploadSymbols": True,
        "manageAppVersionAndBuildNumber": False,
    }
    with open(out, "wb") as f:
        plistlib.dump(options, f)


def main():
    args = sys.argv[1:]
    cfg = load()
    if args[:1] == ["env"]:
        generated = env(cfg)
        for k, v in generated.items():
            print(f"export {k}={shlex.quote(v)}")
        print(environment_aliases(exclude=generated))
    elif args[:1] == ["xcconfig"]:
        xcconfig(cfg)
    elif args[:1] == ["export-options"] and len(args) >= 2:
        export_options(cfg, args[1], args[2] if len(args) > 2 else "upload")
    elif args[:1] == ["get"] and len(args) == 2:
        v = get(cfg, args[1])
        if v is None:
            sys.exit(f"{args[1]} is not set in {path()}")
        print(v if isinstance(v, str) else json.dumps(v))
    elif args[:1] == ["machine"] and len(args) == 2:
        print(json.dumps(machine(cfg, args[1])))
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()
