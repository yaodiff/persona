# Plan: Orca support

Status: built, apart from what is below (2026-11-28, #17058). Orca is an engine of its own beside herdr, Herdr App, Tern, Rex
and the runtime engines, in any host set (`docs/integrations/engines.md`, "Host sets"): its terminals (list, state, send,
keys, close, starts and resumes, split, close tab, rename, worktrees) and its own chats (list, send, cards, modes, model,
rename, rewind, read, starts) all work, tracked to the end in #17060. The finished design moved to the maintained docs:
the host layer to [engines.md](../integrations/engines.md) ("The host layer"), Orca to [orca.md](../integrations/orca.md),
herdr-core to [herdr-core.md](../integrations/herdr-core.md). What stays here: the live terminal, which waits on a
streaming transport Orca does not offer over its local socket, the deferred fixes and the open decisions.

## Streaming transport (#17048)

Investigated on Orca 1.4.277 (its runtime bundle, `orcad-server.js`, and an isolated headless runtime with its own HOME
and profile): can Persona use the transport that `terminal.subscribe` and `agentSession.subscribeStatus` need, without
weakening security and without pairing the user has to repeat? **No-go for now**; the node keeps polling, and the plain
view reads faster instead (`docs/integrations/terminal.md`, "31. Orca terminals: scrolling up, and a quicker plain view (#17048)").

- **The local socket refuses streaming methods**: the dispatcher answers `method_not_supported` "Method terminal.subscribe
  requires a streaming transport" (measured), whatever the token. So does `agentSession.subscribeAvailability` and
  `managedRuntime.subscribe` (measured for #17684): whether Orca can open a provider's chat on the computer reaches a socket
  client only as `agent.launch`'s refusal (`docs/integrations/orca.md`, "Structured sessions").
- **The WebSocket transport** (`ws://127.0.0.1:<port>`, listed in `orca-runtime.json` next to the socket) starts with
  `e2ee_hello`: v1 sends the client's X25519 key and both sides seal with a NaCl box (the server's key is
  `publicKeyB64` in `orca-e2ee-keypair.json`, 0600), v2 adds a transcript hash; within 10 s the client sends a sealed
  `e2ee_auth` with a **paired device's** token (or, v2, a device-key proof). `handleAuth` resolves the token in Orca's
  device registry (`orca-devices.json`) only. Measured: the handshake completes with the key Persona can read, and
  `e2ee_auth` with the local socket's own token answers `e2ee_error {code: "unauthorized"}` and closes 4001.
- **Getting a device token** without a person: `pairing.createOffer` is callable on the local socket (permission
  `pairing-admin`) and returns a pairing URL carrying a device token, but for both scopes it first calls
  `ensureNetworkExposure()`, rebinding the WebSocket listener to every interface. Measured on the isolated runtime
  (`--refuse-public`): `{available: false, reason: "network_exposure_failed"}`. On a desktop Orca it would expose a
  full-control endpoint to the network, and a device seen with `pairingReach: "network"` makes Orca bind all interfaces
  again at every start unless `--bind` pins it (`resolveInitialWebSocketBindHost`). It also lists a paired device in
  Orca. That weakens security: not used. A loopback-only offer (`reach: "this-computer"`) exists inside Orca but no RPC
  parameter reaches it.
- **With a person**: `orca serve` prints a pairing offer at start (none with `--no-pairing`: `disabled_by_operator`), and
  the desktop app pairs phones by QR code. Persona would have to ask for that pairing on every computer, keep a device
  credential per Orca, and pair again whenever the user revokes or resets it: the churn this was to avoid.
- **Writing a device into `orca-devices.json`** (Orca adopts external edits) would grant access behind Orca's back: not
  considered.

What Orca would need to add, any one of which Persona would adopt behind a capability probe (`status.get`
capabilities, or falling back on `method_not_supported`), with a `host.Watcher` and a `host.Streamer`:

1. **A streaming subscription on the local socket** (preferred): `terminal.subscribe` and `agentSession.subscribeStatus`
   (or one `events.subscribe`) accepted on the authenticated local socket, as notifications on the same connection, as
   herdr-core's `events.subscribe` does. The socket already grants full control to its token's holder, so this adds no
   new trust.
2. **A colored screen read**: `terminal.read {screen: true, format: "ansi"}` with SGR rows, the cursor and the grid size,
   plus a long-poll for change (`terminal.wait {for: "output", afterSequence}`), so the plain view gets colors, a cursor
   and push refresh without a stream.
3. **Loopback pairing for a local client**: `pairing.createOffer {scope: "runtime", reach: "this-computer"}` over the
   local socket without network exposure, so a same-account client can pair itself once, silently and revocably.

For the chat launchers alone (#17684), a unary read of each structured agent's availability (`agentSession.agents` carrying
the `chatProgram` state `agentSession.subscribeAvailability` streams) would let the bridge offer only the chats Orca can open
before anyone tries one, instead of after Orca's first refusal.

## Fixes deferred from the host refactor

Issues found while adding the tests that pinned the refactor's behavior (2026-11-01), left for later so the baseline stayed
the same; each needs its test updated with it:

- Switching to `bypassPermissions`, `tuiSetMode` gives up after 6 shift+tab presses, leaving Claude in a different mode (not the original). Restore the original on failure, or verify the target exists in the cycle first (`node/modes.go`).
- Four existing `catchup_test.go` tests fail with `CLAUDE_CONFIG_DIR` set: they assume Claude config under HOME. Clear this variable in tests (new `herdrNode` already does).
- `TestTerminalControlEndsWhenTakenOrForgotten` in `terminal_test.go` changes global `termLease` while a still-running `controlLoop` reads it: occasional `-race` failures (seen once by the engine-selection worker with `-count=10`; not reproduced in 30 runs each on branch and master). Wait for `controlLoop` before test completion, or make the lease an instance field.
- Other suspicious behavior (not necessarily needing fixes): fallback from failed herdr snapshot to `agent.list` retains old space / slot names; `editQueue` clears lines based on transcript queue, not what the TUI actually restores.

## Open decisions

- Report capabilities per session or per host on each machine (leaning toward the latter, with sessions carrying only host kind).
  Today it is both: the terminal browser's actions per host (`panes.list`'s `abilities`, #17053) and a runtime row's per
  session (`capabilities`).
- Live terminal and colored screen reads on Orca: wait for one of the methods "Streaming transport" lists above.
- ~~Displaying one session in two hosts simultaneously~~: settled in #2576. It is listed once, owned by one host, and its row carries `also_open_in` (`docs/integrations/engines.md` "Host sets").
- Sessions outside any host but with a live process (ordinary terminal): mark "Running elsewhere" and disable sending? `sessions/<pid>.json` alone can detect this, independent of host abstraction, so it can be done first.
