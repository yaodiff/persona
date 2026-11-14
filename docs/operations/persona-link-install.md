# Install Persona Link independently on Mac

Persona Link connects this Mac to your Persona hub so the hub can use Agents running here. It is a background connector and resident-node service, not a menu bar app and not a prerequisite of the Electron desktop app.

The independent helper package also contains **Persona Screen**, which owns the Screen Recording permission for live-screen capture, and **Persona Input**, which owns the Accessibility permission for scrolling the live screen by the point ([computer-use.md](../extensions/computer-use.md) "Pixel scrolling (#7775)"). It is not bundled into the Electron desktop's login items or process lifetime. See [link.md](link.md) for connection modes and protocol, and [desktop.md](../desktop/desktop.md) for the separate desktop boundary.

There is no signed distribution package yet. Build from source for local use; this guide does not claim Developer ID signing or notarization.

## Prerequisites

| Requirement | Details |
| --- | --- |
| macOS 15 or later | Persona Screen's minimum OS version |
| Xcode (the full app, not just command-line tools) | Builds Persona Screen with `xcodebuild` |
| Go | Builds persona-link and persona-node, using the version in `server/go.mod` |
| A running hub | And a browser already connected to it, to add this Mac |

No Apple developer account is needed for local ad-hoc signing. An ad-hoc build is not a notarized installer for other Macs.

## Build and install

```bash
git clone <repository-url> persona && cd persona
./scripts/app/mac-link.sh install
```

The command builds persona-link, persona-node, Persona Screen and Persona Input, places them under `/Applications/Persona Link.app/Contents/Helpers/`, then registers two independent launchd agents:

- `com.persona.link` runs the connector copied to `~/.persona/bin/persona-link`. This fixed path is also what the Electron setup window invokes.
- `com.persona.node` runs the resident node. An existing node service is preserved, including its program and data directory.

Both start after installation and at login, and restart on exit. There is no UI to open and no Electron process to keep running. On first LAN use, macOS may ask for local-network permission; allow it if this Mac connects to the hub over the LAN.

Build without installing: `./scripts/app/mac-link.sh build`; output is `apple/PersonaLink/build/Persona Link.app`. Use `UNIVERSAL=1` for Apple silicon and Intel. `mac.link_bundle_id` or `PERSONA_LINK_BUNDLE_ID` controls Persona Screen's bundle-id prefix, defaulting to `app.bundle_id` + `.link`; without either the build stops and says what to set. Keep it the same from build to build: macOS keeps Persona Screen's Screen Recording grant per bundle id, so a Mac that ran a build with another id (such as an earlier `com.example.persona.link` placeholder build) asks for Screen Recording again once (#7569).

### Moving from the retired menu bar app

Keep the existing `~/.persona/link.json` and node service. First stop the old menu bar app and any foreground `persona-link run`; otherwise two connectors will contend for the same run lock. The installer does not kill or replace an unmanaged running connector. Do not use `persona-link uninstall --purge`, which would delete the pairing you want to keep.

The historical migration record is in [link-menu-tray.md](../archive/operations/link-menu-tray.md), not a current setup procedure.

## Pairing

1. Open **Machines › Add computer** in the signed-in web app, choose Mac, name the computer and copy its pairing link.
2. If the Electron desktop is installed, open its local setup window and use the advanced **Persona Link** section. This is optional; it does not install Link for you.
3. Otherwise pair in a terminal:

   ```bash
   ~/.persona/bin/persona-link pair - <<< '<pairing-link>'
   ```

4. Return to the web screen and confirm **This is my computer**. A link is valid for 10 minutes and can be used once; never post it publicly.
5. Verify with `~/.persona/bin/persona-link status`. The command reports the connector's mode, connection, resident-node state as seen by the hub, and the launchd agent. `status --json` is available for the desktop's allowlisted bridge and scripts.

For passive mode, use `persona-link pair --listen` and paste the printed link into hub admin **Computers › Add computer › Hub connects**. SSH tunnel setup and other advanced options remain in the CLI; see [link.md](link.md), "SSH tunnel".

## Everyday commands

| Command | Purpose |
| --- | --- |
| `persona-link status [--json]` | Connector and service state; no credential contents |
| `persona-link reconnect` | Ask the running connector to reconnect immediately |
| `persona-link node status [--json]` | Resident-node service state without connecting to its socket |
| `persona-link logs -f` | Follow `~/.persona/link.log` |
| `persona-link pair --listen` | Generate a passive-mode pairing link |
| `persona-link name <new>` | Rename this computer on the hub while connected |

## Upgrade and uninstall

Upgrade by building and running `./scripts/app/mac-link.sh install` again. The connector executable is replaced at its stable path and its launchd agent is restarted. Existing pairing and node-service files stay. Reinstalling Link does **not** replace an already installed resident-node binary; see [node-update.md](node-update.md).

To remove the connector while keeping its pairing and resident node:

```bash
~/.persona/bin/persona-link uninstall
```

To remove the resident node too, inspect its actual service and binary first, then run `persona-link node uninstall`. `--purge` is a separate, explicit destructive operation that removes connector credentials, status, logs and its installed executable; revoke this computer on the hub as well. Persona Screen can then be removed with `/Applications/Persona Link.app` if no longer needed. Do not run those removal commands merely to troubleshoot a connection.

## Troubleshooting

- **Connector not running**: `launchctl print gui/$(id -u)/com.persona.link` and `persona-link logs -f`.
- **Hub cannot reach the local node**: `persona-link node status`; logs are in `~/.persona/node-attach.log`.
- **LAN unreachable**: check macOS local-network permission for the connector. Other routes and TLS errors are explained by `persona-link status`.
- **Persona Input permission**: `Contents/Helpers/Persona Input.app`; its Accessibility grant is its own. `mac-link.sh` rebuilds it only when its sources change (it keeps the built helper in `~/.persona/cache/persona-input`), so an update leaves the grant alone; `./scripts/app/mac-link.sh verify-input` checks that two clean builds are the same.
- **Persona Screen permission**: the helper remains at `/Applications/Persona Link.app/Contents/Helpers/Persona Screen.app`, the path the node already searches. Its Screen Recording grant is separate from the Electron app and the connector.
