# Operating the iPhone

The assistant operates a paired iPhone the way it operates a Mac ([computer-use.md](computer-use.md)): it reads the
screen, acts on it in steps and reads it again. There is no app on the phone for this and no VPN: the node of a computer
the iPhone is paired with reaches it over RemotePairing through the device bridge, and the logic runs on that node, so a
tool call is one hub ↔ node round trip.

| Piece | Where |
| --- | --- |
| Device bridge (`device_info`, `screenshot`, `ax_tree`, `tap`, `long_press`, `drag`, `swipe`, `keys`, `button`, `launch_app`, `terminate_app`, `list_apps`) | `persona-iphone-bridge` ([iphone-bridge.md](iphone-bridge.md)) |
| Pairing, the node's session with the phone, the per-machine switch, the node's built-in `iphone` program | [iphone-pairing.md](iphone-pairing.md) |
| **Core**: element numbers, results, limits, approval rules (#3449) | `server/internal/iphonectl` |
| The assistant's tools, waiting for the owner's approval and its wording | the manager |

Status: the core is logic with unit tests against a fake bridge and the bridge's documented wire shapes. The node's
`iphone` program runs its looks and dos through it (`server/internal/node/iphone/iphone_core.go`), checked end to end against
a simulator ([iphone-pairing.md](iphone-pairing.md), "The node program `iphone` (#3450)"). **Not verified on a real
iPhone.**

## The core's interface

`iphonectl.Controller` (one per node) answers `Handle(ctx, bridge, Request) Response`:

- **Request**: `device` (opaque to the core), `session` (the conversation), `op`, `args`, `approved` (resume a do the
  owner approved), `confirm` (the owner's setting to be asked before any input) and `self_apps` / `self_names`
  (Persona's own apps, added to `Rules.SelfApps` / `Rules.SelfNames` for as long as the controller runs: a call
  without them never lifts the rule).
- **Response**: `output` (the result as JSON), `text` (the result for the model), `image` (a screenshot), and for a
  step waiting for the owner `needs_approval`, `approval_summary` (data, below) and `approval_id`. The approval fields
  are for the manager only: neither is in `text` or `output`. `refused` says the call was turned down as a whole
  before anything was sent, as a code: `bad_args`, `not_pending` (an approval it no longer holds) or `busy` (a resume
  while the phone is busy: the approval stays pending); `text` says why.
- `bridge` is the phone: `iphonectl.Wire` turns the bridge's JSON wire into the `Bridge` interface, given a function
  that sends one operation to the phone and returns its result or its error (`*iphonectl.Error` with the bridge's code;
  the caller sets `NotSent` when its session refused before writing anything, `MaybeSent` when it handed the operation
  over and lost the answer, whatever the code).
  With a `TempDir`, screenshots are written to a file (`out_file`) and `image.file` names it instead of carrying bytes.

| Op | `args` | Does |
| --- | --- | --- |
| `look` | `screenshot` (default true) | read the device, the elements and a screenshot |
| `do` | `steps`, `numbering`, `screenshot` (default false) | run steps in order, stopping at the first that does not complete, then read the screen again and list what changed |
| `status` | — | the phone (`device_info`) and this conversation's session: latest numbering, when it was read, a step waiting for approval, the last call's outcome |

Steps (`args.steps`, one JSON array, unknown fields rejected):

| `action` | Fields | Bridge |
| --- | --- | --- |
| `tap` | `target`, `count` (1–3; 2: double tap) | `tap` at the target's center |
| `long_press` | `target`, `ms` (default 800) | `long_press` |
| `drag` | `target`, `to`, `ms` (default 500), `hold_ms` | `drag` |
| `swipe` | `target` (default the screen's center), `to` or `direction` + `distance` (points, default half the screen), `ms` (default 300) | `swipe` from / to |
| `type` | `text`, `target` (tapped first, then 400 ms for the keyboard) | `keys {text}` |
| `key` | `key` (return, backspace, delete, tab, escape, space, up, down, left, right), `repeat` | `keys {key}` |
| `button` | `button` (home, lock, volume_up, volume_down), `ms` | `button` |
| `launch`, `terminate` | `app` (bundle id) | `launch_app`, `terminate_app` |
| `wait` | `ms` (up to 10 s) | — |

A target is one of `{"n": 4}` (an element number), `{"label": "Send", "role": "button"}` (exact label ignoring case and
spacing, else containing it; ambiguous labels are refused with the matching numbers) or `{"x": 120, "y": 640}` (points;
checked against the innermost element under it, so the approval rules see what it hits). Input goes out with fixed
timings: the same steps always send the same input.

## Without an element tree

The bridge's v1 has no `ax_tree` (`capabilities.ax_tree` false, [iphone-bridge.md](iphone-bridge.md)), so the
assistant works from screenshots:

- look says the elements can't be read, gives the screen's size in points, attaches the screenshot and says how its
  pixels map to points (`point_width / width`).
- Steps take points (`{"x": 120, "y": 640}`); a step that names an element by number or label is `not_sent`.
- **Fail-closed**: the rules can't see what anything hits, so **every** input (tap, long press, drag, swipe, type, key,
  button, launch, terminate; not a wait) waits for the owner with kind `unchecked`, each on its own: one answer never
  covers the steps after it, whose screen nobody has seen yet. The summary carries the point (`at`), the text, key,
  button or app, and the response carries the screenshot the owner is asked against.
- On resume the screen is photographed again; if it no longer matches the one the owner saw (see "Effect of an input"),
  the step is `not_sent`.
- After the steps a screenshot is always attached, since there are no elements to list.

The same happens when `ax_tree` answers `unsupported` although the capability said otherwise.

## Effect of an input

The bridge answers an input once it wrote it to the phone; the phone does not confirm it. So `sent` means "written to
the phone", never "confirmed", and every input that was sent (or may have been) is followed by a check of the screen:

- `Settle` (400 ms) after the input, a screenshot, compared with the one from just before the input;
- the comparison is on a 24 × 48 grid of average brightness, leaving out the top rows (the status bar: clock,
  battery), with a change of more than 10 levels in at least 3 cells counting as changed, so JPEG noise doesn't;
- the step's `effect` is `screen_changed`, `no_visible_change`, or `not_checked` (no screenshot capability, no
  time left, or a screenshot that couldn't be read) with the reason in its detail.

The do's line for the step reads `sent, screen changed` or `sent, no visible change`; a tap with no visible change
usually missed. The screenshot after the last input is the one attached to the result.

## Typing

The bridge types text as key presses on a US layout: only printable ASCII, tab and newline. A type step with any other
character (Chinese, accented letters, emoji) is `not_sent` before anything, its focusing tap included, and says at
which character (its position, never the character: what a type step types goes into no result, log or error) and
that sending the same text again fails the same way.

## Element numbers

The model names elements by the numbers a read gives (`tap [4]`). The numbers have to mean the same thing from one
read to the next, or a step aimed at one button lands on another.

- **Elements**: the `ax_tree` nodes, depth first; containers (`application`, `window`, `other`, `group`, scroll views,
  tables) are left out unless they have a label, value or identifier. A node's `id` means nothing beyond one read and
  is not kept.
- **Stable**: an element still on screen at the next read keeps its number. A new element gets the next unused
  number; a number is never given to another element, so an element that leaves and comes back is new.
- **Identity**: the role plus the app's `identifier` when there is one, otherwise the role plus the label. A changed
  value (text typed into a field) keeps the identity.
- **Matching**: an identity found once in each read is paired outright. Those pairs give the screen's shift (their
  median movement, a scroll). Identities several elements share (a "Reply" in every row) and elements with neither
  identifier nor label are paired only where that shift puts them, within half their size; otherwise they are new.
  So after a scroll each row's "Reply" keeps its own number instead of taking the number of the row that was there.
- **Replaced screen**: another app in front, or less than 30% of the larger read's elements in common: numbers start
  again from 1 and the **numbering** goes up by one. Scrolling part of a list keeps the numbers; a new page doesn't.
- **Never acting on a stale number**: every read says its numbering, and `do` can be given the numbering its numbers
  came from. A step that names a number is not sent when there has been no read in this conversation yet (or the node
  restarted), when it names an older numbering than the latest read's, when the read just before it started a new
  numbering (the screen was replaced since the look, or by an earlier step), or when the number is no longer on screen.
- **Per conversation**: each session keeps its own numbers; two conversations on one phone don't share them.
- **Reading order**: a fresh numbering goes top to bottom, then left to right within an 8 pt row.

## Results

Only facts, in English, the same as the other tools' results:

- **look**: the app in front, the numbering, the element count, the screen size in points, whether the phone is locked,
  one line per element (`[3] text field "Message" = "hi" focused (16,700 343×36)`; a secure field's value is never
  shown), marks `+` / `~` for elements new or moved since the read before, and whether a screenshot is attached. What
  could not be read is a note (`Elements not read: service_unavailable: …`); the rest still answers, so a phone that can
  give a screenshot but no elements still gets a look.
- **do**: one line per step, what it was (`tap [4] → button "Send"`), where a touch landed, its **delivery** and how
  long it took, then the screen after it:
  - `sent`: the bridge wrote the input to the phone (which doesn't confirm it; see the step's effect);
  - `not_sent`: it certainly did not reach the phone: not attempted, refused, waiting for approval, or the bridge
    failed before sending (`bad_request`, `pairing_missing`, `pairing_rejected`, `unreachable`, `wrong_device`,
    `tunnel_failed`, `developer_mode_off`, `ddi_not_mounted`, `service_unavailable`, `locked`, `app_not_found`,
    `unsupported`);
  - `unknown`: it may have reached the phone (`timeout`, `disconnected`, `internal`, any other failure, or no answer
    within the limit). It is **never sent again**; the result says to look first;
  - `done`: a wait finished.
  After the steps only what changed is listed: new elements (`+`), moved ones (`~`, with where they were) and the
  numbers gone; a replaced screen is listed whole. A typed text is only counted, never repeated.
- A type step whose focusing tap was sent but whose text was not is `unknown`: sending it again would tap again.
- An element without a frame (`null` or empty) is listed but never touched: naming it is refused; a point works.
- A secure field's value is replaced by `(hidden)` as soon as the tree is read, so it is in neither `text` nor
  `output`.
- Reads (`device_info`, `screenshot`, `ax_tree`, `list_apps`) are retried once after `disconnected`; inputs never are.

**Before any input** the do reads `device_info` (again after a button press):

- locked: only `button` steps are sent, the rest are `not_sent` ("the phone is locked");
- `capabilities.input` false: no input is sent, with the bridge's reason (`developer_mode_off`…);
- an input of an earlier call still out past its limit: nothing more is sent until it answers or `Stuck` has passed
  (status says so); reading still works.

## Limits: never hanging

Every call answers. A bridge that never returns, a phone that went to sleep or a link that dropped only make a step
`unknown`; they never hold the call.

| Limit | Default | Covers |
| --- | --- | --- |
| `Primitive` | 15 s | one bridge call, plus the time its gesture takes (a long press, a swipe, a drag) |
| `Action` | 30 s | one step, all its bridge calls together (read the screen, find the target, tap, type), plus its gesture time |
| `Call` | 90 s | a whole look or do |
| `Busy` | 10 s | waiting for the call before on the same phone (calls on a phone run one at a time, whichever conversation) |
| `Pending` | 15 min | how long a step waits for the owner's approval |
| `Settle` | 400 ms | after an input, before the screenshot that checks its effect |
| `Stuck` | 2 min | how long an input still out past its limit holds back the next ones (after it the bridge call counts as dead) |

- At a limit the core stops waiting; the bridge call keeps its context (which has ended) and its late answer is
  dropped. The step is `unknown`.
- A step that does not complete stops the do: the steps after it are `not_sent` ("not attempted: step 2 did not
  complete"), so nothing runs on top of an input whose fate is unknown.
- A share of the call (`min(Primitive, Call/4)`) is kept for the read after the steps, so a do that runs out still
  says what the screen shows. When even that read fails, the result says so.
- The context the node passes (the hub's `timeout_ms`) ends everything too.

## Approval

`iphonectl.Rules` marks four kinds of step as needing the owner's approval; this is always on:

| Kind | When |
| --- | --- |
| `payment` | touching an element named to pay, buy, subscribe, order or check out (English, Chinese, Japanese), or a button whose label is a price (`$4.99`, `12.00 €`); any touch in a payment app (Wallet) |
| `delete` | touching an element named delete, remove, erase, trash, clear all, empty trash… (the keyboard's delete key is not); a swipe or drag of 30 points or more to the left that starts on a row (`cell`) |
| `send_message` | touching an element named send or post; a line break typed, or return pressed, in a messaging app |
| `password` | typing into a secure field, or one whose label, identifier or placeholder says password, passcode, PIN or verification code; typing with a secure field on screen and no known field taking it (nothing reported with the keyboard, or the step taps a point with no element under it) |

- **What a touch touches**: the element the step names and every element containing the point where the touch
  starts or ends, so a tap on a button's text label is a tap on the button too.
- **Names**: an element's label and its app identifier written as words (`deleteButton` → "delete button"). A name
  matches when it is the phrase or starts with it (followed by a space or punctuation for Latin script): "Pay" and
  "Pay $4.99" match, "Payment Methods" and "Payee" don't; "Delete" matches, "Recently Deleted" doesn't.
- Elements that only show something (static text, headings, images) never match by themselves.
- Configuration can add words per kind and more messaging or payment apps.

With `confirm` (the owner's setting, off by default), the first input of a do also waits, with kind `confirm` and the
list of the do's steps from there; one answer covers the do, though a step the rules mark still waits on its own.

**The flow**: the core never waits for the owner.

1. A do runs its steps up to the first that needs approval, which it does not send. The response has
   `needs_approval: true`, the step's `approval_summary` and an `approval_id`; the result says which step waits and
   that nothing from it on was sent.
2. The manager asks the owner (on another screen) and, when they approve, calls `do` again with `approved` set to that
   id.
3. The core reads the screen and sends the step only if it is still what the owner approved: the same app, the same
   target and end (number, identity, label and value: a relabeled amount is not what was approved), the same field
   with the keyboard and the same kinds. Then it runs the rest
   of the do, which may stop at another step.

`approval_summary` is data; the manager words it in the owner's language:

| Field | Meaning |
| --- | --- |
| `step` | 1-based, within the do the model asked for |
| `action` | the step's action (`tap`, `type`, `key`…) |
| `needs` | `payment`, `delete`, `send_message`, `password`, `confirm`, `unchecked` |
| `app` | `bundle_id` and `name` of the app in front |
| `target` | `n`, `role`, `label` of the element acted on |
| `text`, `text_len` | what a type step will type and its length; for a password only `text_len` |
| `key`, `button`, `launch_app` | for those steps |
| `evidence` | the facts the rules found (`button "Pay"`, `app Wallet (com.apple.Passbook)`) |
| `at` | where the touch lands, when it is a point |
| `steps` | with `confirm`: the do's steps from there (`action`, `target` or `at`, `text_len`, `key`, `button`, `app`); a target named by number, or by a label only one element has, is given as on the screen the owner is asked on (`n`, `role`, `label`), any other as the step wrote it |

**The automation never approves itself**:

- Approval arrives only as `approved` in a call the manager makes after the owner answered. Steps have no field that
  approves anything and parsing rejects unknown fields; the id is random, never in what the model reads, and the
  manager never hands it to the model.
- An id resumes one waiting step of one conversation, once. A newer do in that conversation, an expired wait
  (`Pending`) or a node restart discards it, and the call answers that nothing was sent.
- No step touches or types into Persona's own app on the phone, where approval cards and their buttons appear, and
  launching it is refused; leaving it (home, another app) is fine. The app in front is Persona's when its bundle id is
  in `Rules.SelfApps` **or** its whole name is in `Rules.SelfNames` (both compared ignoring case and surrounding
  spaces), so the rule holds while no bundle id is known; another app with the same name is refused too (fail closed).
  The node starts with `SelfNames: ["Persona"]`, the public product name, and adds bundle ids at run time: those of
  the apps the device's own `list_apps` names like that (read per device at most hourly, a minute after a failure;
  an unreadable list leaves the name rule), and any the call sends (`self_apps` / `self_names`). Bundle ids are never
  written in code, fixtures, logs or docs, and the rules only ever grow.

## Not covered yet

- The manager sending `self_apps` / `self_names` (what the iPhone app reports of itself when it consents); until it
  does, the node's name rule and the device's app list are what keep the automation out of Persona's app.
- Notification banners from Persona shown over another app are not recognized as Persona's own UI; only the app in
  front is.
- Real-device checks of the bridge's roles and labels against the identity rules, the 30% threshold and the 400 ms
  wait for the keyboard.
