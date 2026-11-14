# Persona desktop and Persona Link

The desktop app and Persona Link are separate products and install independently.

- **Persona desktop** is for everyone on macOS and Windows: it loads the hub's existing web app, preserves web sign-in, and adds OS notifications, the existing Needs-you badge, desktop presence, deep links, a single instance, launch at login and updates. It needs no local node or Link.
- **Persona Link** is for a computer whose Agents the hub should reach. Its CLI/service remains independently installable and runs without the desktop app. The desktop's local setup window can show and manage an installed Link; it never silently installs a background service.

The privileged setup page is packaged locally and has a separate preload from the hub-loaded page. Link commands use a fixed CLI/argv vocabulary, with pairing input on stdin. No page can read `link.json`, execute an arbitrary process or send an arbitrary WebSocket message.

## Development

```sh
npm ci
npm run typecheck
npm test
PERSONA_HUB_URL=http://127.0.0.1:18660 npm start
```

The production build needs macOS for the universal DMG/ZIP and Windows for the signed NSIS acceptance run. Unsigned development artifacts are not proof of signed/notarized acceptance. Release credentials belong in CI secrets, never in the repository or the packaged app. Standard platform TLS validation is used; overriding certificate errors with a saved pin is not part of this scaffold.

The lockfile omits an upstream package's deprecated-contact text so the repository's privacy scan stays clean. Package versions, resolved URLs and integrity hashes are unchanged, and `npm ci` verifies the lockfile against the registry.

## Updates

The app checks at launch and every four hours, downloads in the background and installs on quit or from the tray's "Restart to update". `node scripts/update-test.mjs` runs a real update of a packaged test build against a local feed (Windows, macOS, and Linux under Xvfb). Release publishing and the signing secrets are described in `docs/desktop/desktop.md` ("Updates").

This is an implementation scaffold, not yet platform acceptance evidence. The full product migration and maintained documentation are tracked in #6770.
