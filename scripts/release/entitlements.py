#!/usr/bin/env python3
"""Each bundle's entitlements in a release build: sign an unsigned archive with them, check a built one has them.

The app shares keychain items with PersonaNotify (the push key) and files with PersonaWidgets (the Live Activity's
persona, in an App Group; docs/operations/release.md "entitlements"), which only works when every bundle is signed with its
target's keychain groups and App Groups. An archive built unsigned (CODE_SIGNING_ALLOWED=NO, for a cloud-signed export on a Mac without an
Apple Distribution certificate) carries none, and the export signs each bundle with what it finds in it: build 92
shipped the widget extension without any (the island drew the default persona whatever was chosen) and the app with
groups whose bundle id was empty (2026-11-02).

  scripts/release/entitlements.py sign <unsigned .xcarchive>      # ad hoc, inside out, each bundle with its target's file
  scripts/release/entitlements.py check <.ipa | .pkg | .xcarchive | .app>  # every target's groups are in its signature

In an exported .ipa / .pkg the push entitlement (`aps-environment`, on a Mac
`com.apple.developer.aps-environment`) must also say production: the archive says development, and only the App Store
export changes it.

Both read the targets from the Xcode project (`xcodebuild -showBuildSettings`, after xcodegen) and expand the build
settings in each entitlements file; `$(AppIdentifierPrefix)` is the team's (the signature's own, when checking). A
setting that is not there stops it rather than turning into nothing.
"""
import json
import os
import plistlib
import re
import subprocess
import sys
import tempfile
import zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
PROJECT = os.path.join(ROOT, "apple", "Persona.xcodeproj")
VAR = re.compile(r"\$[({](\w+)[)}]")
APP_GROUPS = "com.apple.security.application-groups"
APS = ("aps-environment", "com.apple.developer.aps-environment")  # iOS, macOS
# Signed in only when the App ID has the capability: without a profile that allows it the export either fails or (a
# cloud-signed export of an unsigned archive) silently drops it, and every push the hub marks time-sensitive then shows
# as active (#1572).
TIME_SENSITIVE = "com.apple.developer.usernotifications.time-sensitive"
# The same for HealthKit background delivery (config.json app.health_background_delivery): without it a workout report
# waits for Persona to open, while Settings says it comes right after the workout (#2526).
HEALTH_BACKGROUND = "com.apple.developer.healthkit.background-delivery"


def targets():
    """(bundle id, platform) → that target's build settings, Release."""
    out = subprocess.run(["xcodebuild", "-project", PROJECT, "-alltargets", "-configuration", "Release",
                          "-showBuildSettings", "-json"], capture_output=True, text=True, check=True).stdout
    found = {}
    for t in json.loads(out):
        s = t["buildSettings"]
        if s.get("PRODUCT_BUNDLE_IDENTIFIER"):
            found.setdefault((s["PRODUCT_BUNDLE_IDENTIFIER"], s.get("PLATFORM_NAME", "")), s)
    return found


def expand(text, settings, prefix):
    def one(m):
        name = m.group(1)
        value = prefix if name in ("AppIdentifierPrefix", "TeamIdentifierPrefix") else settings.get(name, "")
        if not value:
            sys.exit("entitlements: $(%s) has no value for %s" % (name, settings.get("TARGET_NAME", "?")))
        return value
    return VAR.sub(one, text)


def wanted(settings, prefix):
    """The target's entitlements, expanded; None when it has none."""
    path = settings.get("CODE_SIGN_ENTITLEMENTS")
    if not path:
        return None
    with open(os.path.join(settings["PROJECT_DIR"], path)) as f:
        return plistlib.loads(expand(f.read(), settings, prefix).encode())


def contents(bundle):
    """Where a bundle keeps its Info.plist and what it contains: Contents/ in a Mac bundle, the bundle itself on iOS."""
    mac = os.path.join(bundle, "Contents")
    return mac if os.path.isdir(mac) else bundle


def bundles(app):
    """The app and every app / extension inside it, innermost first (a bundle is signed after what it contains)."""
    found = []
    for sub in ("PlugIns", "Watch", "Extensions"):
        d = os.path.join(contents(app), sub)
        if os.path.isdir(d):
            for name in sorted(os.listdir(d)):
                if name.endswith((".app", ".appex")):
                    found += bundles(os.path.join(d, name))
    return found + [app]


def identity(bundle):
    with open(os.path.join(contents(bundle), "Info.plist"), "rb") as f:
        info = plistlib.load(f)
    return info, (info.get("CFBundleIdentifier", ""), info.get("DTPlatformName", ""))


def team_prefix(settings):
    team = settings.get("DEVELOPMENT_TEAM", "")
    if not team:
        sys.exit("entitlements: no DEVELOPMENT_TEAM (python3 scripts/persona_config.py xcconfig, then xcodegen)")
    return team + "."


def app_bundle(directory):
    """Find the renamed product or a legacy archive without guessing when both are present."""
    found = [os.path.join(directory, name) for name in ("Persona.app",)
             if os.path.isdir(os.path.join(directory, name))]
    if len(found) != 1:
        sys.exit("entitlements: expected one Persona.app or Persona.app in %s" % directory)
    return found[0]


def sign(archive):
    app = app_bundle(os.path.join(archive, "Products", "Applications"))
    known = targets()
    for b in bundles(app):
        _, key = identity(b)
        settings = known.get(key)
        if settings is None:
            sys.exit("entitlements: no target builds %s (%s)" % key)
        ents = wanted(settings, team_prefix(settings))
        cmd = ["codesign", "-f", "-s", "-"]
        with tempfile.NamedTemporaryFile(suffix=".entitlements") as tmp:
            if ents is not None:
                tmp.write(plistlib.dumps(ents))
                tmp.flush()
                cmd += ["--entitlements", tmp.name]
            subprocess.run(cmd + [b], check=True, capture_output=True)
        print("signed %s%s" % (os.path.relpath(b, os.path.dirname(app)), "" if ents is None else " with its entitlements"))


def signed(bundle):
    out = subprocess.run(["codesign", "-d", "--entitlements", "-", "--xml", bundle], capture_output=True).stdout
    return plistlib.loads(out) if out.strip() else {}


def check(path):
    exported = path.endswith((".ipa", ".pkg"))
    with tempfile.TemporaryDirectory() as tmp:
        if path.endswith(".ipa"):
            with zipfile.ZipFile(path) as z:
                z.extractall(tmp)
            subprocess.run(["chmod", "-R", "u+rwX", tmp], check=True)  # zipfile drops the modes codesign reads
            app = app_bundle(os.path.join(tmp, "Payload"))
        elif path.endswith(".pkg"):
            subprocess.run(["pkgutil", "--expand-full", path, os.path.join(tmp, "x")], check=True, capture_output=True)
            found = [os.path.join(r, d) for r, ds, _ in os.walk(os.path.join(tmp, "x"))
                     for d in ds if d in ("Persona.app",)]
            if len(found) != 1:
                sys.exit("entitlements: expected one Persona.app or Persona.app in %s" % path)
            app = found[0]
        elif path.endswith(".xcarchive"):
            app = app_bundle(os.path.join(path, "Products", "Applications"))
        else:
            app = path
        known = targets()
        problems = []
        for b in bundles(app):
            info, key = identity(b)
            name = os.path.basename(b)
            settings = known.get(key)
            if settings is None:
                problems.append("%s: no target builds %s" % (name, key[0]))
                continue
            have = signed(b)
            appid = have.get("application-identifier", "")
            prefix = appid[:appid.index(".") + 1] if "." in appid else team_prefix(settings)
            ents = wanted(settings, prefix) or {}
            groups = have.get("keychain-access-groups", [])
            for g in ents.get("keychain-access-groups", []):
                if g not in groups:
                    problems.append("%s: keychain group %s missing (signed with %s)" % (name, g, groups or "none"))
            app_groups = have.get(APP_GROUPS, [])
            for g in ents.get(APP_GROUPS, []):
                if g not in app_groups:
                    problems.append("%s: App Group %s missing (signed with %s)" % (name, g, app_groups or "none"))
            if ents.get(TIME_SENSITIVE) and not have.get(TIME_SENSITIVE):
                problems.append("%s: %s missing (time-sensitive pushes show as active; rerun asc_signing.py)"
                                % (name, TIME_SENSITIVE))
            if ents.get(HEALTH_BACKGROUND) and not have.get(HEALTH_BACKGROUND):
                problems.append("%s: %s missing (workout reports wait for Persona to open; allow it on the App ID's profile)"
                                % (name, HEALTH_BACKGROUND))
            for k in (k for k in APS if k in ents):
                if k not in have:
                    problems.append("%s: %s missing (no push)" % (name, k))
                elif exported and have[k] != "production":
                    problems.append("%s: %s is %s, not production" % (name, k, have[k]))
            # The groups the code is told to use (Info.plist: PersonaPushKeychainGroup, PersonaAppGroup) must be ones it has.
            for k, v in sorted(info.items()):
                if k.endswith("KeychainGroup") and v not in groups:
                    problems.append("%s: Info.plist %s = %s is not one of its keychain groups" % (name, k, v))
                if k.endswith("AppGroup") and v not in app_groups:
                    problems.append("%s: Info.plist %s = %s is not one of its App Groups" % (name, k, v))
        for p in problems:
            print(p)
        if problems:
            sys.exit("entitlements: %d problem(s) in %s" % (len(problems), path))
        print("entitlements ok: %d bundles in %s" % (len(bundles(app)), os.path.basename(path)))


def main():
    if len(sys.argv) != 3 or sys.argv[1] not in ("sign", "check"):
        sys.exit(__doc__)
    (sign if sys.argv[1] == "sign" else check)(sys.argv[2])


if __name__ == "__main__":
    main()
