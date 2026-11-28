# Meeting minutes (offline meetings)

An in-person meeting or hallway discussion becomes a recording, a transcript, and structured minutes. This is not a new recording product: it is the recordings already in the knowledge base (#3235, [recording.md](../apple/recording.md), [knowledge.md](./knowledge.md) "Recordings"), with a minutes document instead of a short summary when the recording is a meeting.

The iPhone and Apple Watch entry points, the Live Activity, and showing minutes inside the app are #2694. This document is the contract they follow. Mac recording is #2704 and uses the same fields.

## Why the recordings we already have don't produce minutes

The knowledge base currently holds no recordings at all, so nothing is being captured. In order:

1. **The entry is too deep to remember.** Recording starts at Knowledge › Recordings, the last kind capsule, then a language choice, then start. Nobody does that while walking into a room. A one-tap entry is the highest priority, and it belongs to #2694 (an in-app entry, an App Intent, a Control Center control, a Live Activity).
2. **The summary is not minutes.** `prompts/kb/recording.md` produces at most 600 Chinese characters under "what this is / key points / to-dos and open questions". It has no decisions, no "who does what by when", no place, and its points are not pinned to transcript timestamps. A transcript over 24k characters keeps only its head and tail, so the middle of a long meeting is dropped.
3. **The calendar is ignored.** A Lark meeting or event in the same time window already has a title (`meetings`, `events`), and a recording never reads it. (Those items carry only a title and a start time: no end, place or attendees.)
4. **A failed sync looks like a connection problem.** With Knowledge off or read-only, recording sync says it can't reach the hub and retries forever (#1915). The user thinks it was captured.

## End to end

1. The user starts a **meeting recording** (#2694: one tap, Siri, the Action button, or Control Center). A recording started the old way, from Knowledge › Recordings, stays an ordinary recording.
2. The phone (or the watch, relayed through the phone) records in segments and transcribes **on device**, exactly as today. Audio never leaves the device, except for a meeting the user records in hub mode (#3862, "Phase 5: hub transcription") and, in a meeting call, what is said while Talk is on (#3862, "Phase 4: realtime voice in the meeting").
3. The phone uploads the transcript so far with `PUT /api/v1/recordings/{id}`, now also saying whether this is a meeting and, if it knows, which calendar event it is. `complete: false` until recording and transcription are both finished.
4. Once `complete: true` and the recording is a meeting, the assistant generates **minutes** with the model it already uses for knowledge-base compression. An ordinary recording still gets the short summary it gets today.
5. Minutes are stored inside the recording's directory. Deleting the recording deletes them. When they are ready, the user gets one notification.
6. Action items are **suggested to-dos**. Nothing is created until the user confirms.
7. The web Knowledge › Recordings detail shows the minutes; the iOS (#2694) and Android (#2738) details do the same, and all three offer Add to to-dos, Add all and Regenerate (iOS since #4062). Asking the assistant "what were the minutes of the meeting just now" resolves through `kb_get` / `kb_search`.

## Review a transcript on the web

Open the recording in Knowledge, then open the review controls in the transcript section. Search for a phrase and move to the previous or next matching passage, or show just the bookmarked moments. The recording detail scrolls to the selected passage, including when several segments have the same timestamp.

Download the transcript as a timestamped text file to read it locally. The download includes the full retained transcript and its bookmarks, regardless of the active search. After speech has expired, only retained bookmarks are available. With no retained lines, there is nothing to download.

## Who does what

| | iOS / watch (#2694) | Hub (the assistant) |
| --- | --- | --- |
| Capture and transcription | On device, segmented, as today | Never sees audio (hub mode, #3862, is the opt-in exception) |
| Marking it a meeting | Sends `kind` and, when it has one, the calendar hint | Trusts the phone; also treats a recording as a meeting when it overlaps a calendar item in the knowledge base, or when the user asks for minutes |
| Calendar | Reads the on-device calendar for the current event's title, attendees and place | Reads `meetings` / `events` already in the knowledge base for the title (they have no attendees) |
| Minutes | Displays them; does not generate them | Generates, stores, regenerates on request |
| To-dos | Offers the suggested ones for confirmation | Creates a to-do only for the action items the user confirmed |

## Field contract

Added to [protocol.md](../protocol/protocol.md) ("Recording sync"). Everything else about an upload is unchanged, and a client that sends none of the new fields behaves exactly as before.

On `PUT /api/v1/recordings/{id}`:

```jsonc
{
  "kind": "meeting",            // optional: "meeting" or "note" (default "note")
  "calendar": {                 // optional, only when the device matched an event
    "title": "Design review",
    "attendees": ["Ada", "Bo"],
    "location": "Room 3"
  }
}
```

- `kind` is `meeting` when the user explicitly started a meeting recording, or when the device matched an event happening now. Anything else, including omitted, is `note`.
- `calendar` is whatever the device already knows. It is a hint, not the source of truth: the hub prefers a `meetings` or `events` item in the knowledge base whose time overlaps the recording. The device does not send a calendar event id, because the hub cannot see the device's calendar. Other keys in `calendar` are ignored; a title or location over 200 characters, or more than 100 attendees or one over 100 characters, is `400`.
- The phone keeps owning the transcript. These fields travel with the full-state upload, so a later upload replaces them like everything else.

Stored on the recording, and returned by `GET /api/v1/kb/recordings/{id}` inside `detail.meta` (alongside the fields already there):

| Field | Meaning |
| --- | --- |
| `kind` | `meeting` or `note` |
| `calendar` | The hint above, when one was sent |
| `meeting` | The matched knowledge-base item, when one was found: `{kind, id, name}` (`kind` is `meetings` or `events`) |
| `minutes_state` | `pending` (a meeting whose minutes for the current revision are not written yet: waiting for the transcript, the model, or regenerating), `ready`, `failed`, or absent when the recording is not a meeting (minutes it got while it was one stay in `detail.minutes`) |
| `minutes_asked` | `true` once the user asked for minutes (`POST …/minutes`), which makes the recording a meeting whatever its `kind` |
| `trimmed_at` | RFC 3339, set by the hub when the transcript expired with `recording_text_days` and only the minutes were kept ("Privacy" below); absent while the recording is whole |

`GET /api/v1/recordings` (reconciliation) carries the same `minutes_state` on each recording when it is set, so the phone knows when to fetch a detail, and `trimmed_at` on a trimmed one. Since #8873 each row and the upload's `200` also carry the recording's `name` and `name_source` ("Titles (#8873)" below).

The minutes themselves are `detail.minutes`, present once minutes have been written. While newer ones are `pending` or `failed` (a re-transcription, a regenerate), the previous minutes stay; their `revision` is then below `meta.revision`:

```jsonc
{
  "version": 2,                      // increases each time the minutes are rewritten; sent back when confirming to-dos
  "revision": 7,                     // the recording revision they were made from
  "generated_at": "2026-11-10T07:12:03Z",
  "title": "Design review",
  "when": "2026-11-10 14:00–15:10",  // in the user's time zone, one fixed format: for older clients and the Markdown
  "start": "2026-11-10T14:00:00+08:00", // the same span, RFC 3339 (#4713): what clients format
  "end": "2026-11-10T15:10:00+08:00",
  "where": "Room 3",                 // omitted when nobody said and no calendar has it
  "attendees": ["Ada", "Bo"],        // omitted when not known; never invented
  "points": [
    {"text": "The export path drops entries over 4 MB.", "at": 312.0, "who": "Ada"}
  ],
  "decisions": [
    {"text": "Ship the cap as-is for this release.", "at": 1504.5}
  ],
  "actions": [
    {"id": "a1", "who": "Bo", "what": "Add the regression test.", "by": "Friday", "due_date": "2026-11-13", "at": 2100.0, "todo_id": ""}
  ],
  "open": [
    {"text": "Whether the cap should be per entry or per recording.", "at": 2408.0}
  ],
  "overview": { … },                 // optional (#6563): the minutes as one summary card, "Summary card (#6563)" below
  "debates": [ … ],                  // optional (#16728): real disagreements and each side's stance, "Disagreements (#16728)" below
  "partial": false,                  // true when the transcript was too long and its end was not read; omitted when false
  "language": "en",                  // what they are written in, the Markdown's headings too: zh-Hans, en or ja (#5906)
  "markdown": "# Design review\n…"    // the same minutes as Markdown with clock times: what Copy / Share send, and minutes.md on disk
}
```

- `start` / `end` are the recording's span (its start, and its end or at least a minute after the start) in the user's time zone, to the second (#4713). Clients format them with their own localized date and time, as they show the recording's own dates, and show `when` only when they are absent (minutes from an older hub). `when` is never parsed. Minutes written before these fields existed get them from their recording when the detail is read.
- `at` is seconds from the start of the recording, the same clock as `segments[].start`, so a client can jump straight to that part of the transcript. Every point, decision, action and open question carries one.
- `who` is omitted unless the transcript or the calendar actually supports it: a name appears only when someone was addressed by name or is the only attendee who could have said it; otherwise the field is absent, not guessed. Speaker numbers and owner-chosen speaker names never enter `who` ("Speakers (#17674)").
- `by` is the deadline as spoken ("Friday", "before the 20th"), not a parsed date. Absent when none was said. `due_date` (`YYYY-MM-DD`) is added only when the spoken deadline resolves to one date unambiguously, counting from the meeting day.
- `id` names an action within one `version` (`a1`, `a2`, …). `todo_id` is set once the user confirmed it as a to-do.
- Sections with nothing in them are omitted, not filled with "none".
- `points`, `decisions`, `actions` and `open` keep the transcript's order.
- `language` is an i18n code (`zh-Hans`, `en`, `ja`): the language every text field and the Markdown are in ("How the hub generates them" below). Minutes written by an older hub have none and are Chinese.
- `overview` is written only when the hub could put every point and decision under a topic. Minutes from before it, and minutes from an older hub, have none. Its presence is the only thing that switches a client to the summary card; `version` is still only a count of rewrites ("Summary card (#6563)").
- `debates` is written only when the meeting had at least one real disagreement with two or more cited positions. It is omitted otherwise ("Disagreements (#16728)").

Reading it back:

- `GET /api/v1/kb/recordings/{id}` returns `detail.minutes` and the meta fields above. `detail.summary` stays the short summary, for a meeting as well, so existing clients keep working.
- `kb_get` on a recording prints the minutes (`minutes.md`: the same items, each at its clock time) before the transcript, and `kb_search` matches the minutes text as well as the transcript, so "the minutes of the meeting just now" resolves without a new tool.
- Past meetings (#2981, phase 0) resolve through the same tools. "What did last week's design review decide", "what's still open from the X meeting" and "who owns the export to-do" each need three things, and the tools give them:
  - **Finding the meeting.** `kb_search` takes an optional `kind`; `kind=recordings` reads only the recordings' files (`meta.json` with the user's title, `calendar.title` and the matched `meeting.name`, `minutes.md`, `summary.md`, `transcript.md`), so a broad query can no longer fill its limit with chats first. Within a kind, entries whose name matches come first, then as `kb_list` ranks them (a recording's id is random, so the walk alone would not put last week's first). `kb_list` shows the start time of `meetings` and `events` items as it does for recordings, in the user's time zone, so "last week's" can be told apart.
  - **From the calendar item to its minutes.** `kb_get` on a `meetings` or `events` item lists the recordings matched to it, each with its start, length and minutes state.
  - **Where each action item stands.** After `minutes.md`, `kb_get` prints every action item with `who`, `what` and the deadline as said, and its state now: a suggestion that is not a to-do yet, or its to-do open (with the to-do's due date), done, cancelled or deleted. The to-do is the one `todo_id` names, or the one a confirmation made under the action's deterministic id when writing `todo_id` back failed. The order stays minutes, action items, then the transcript, which the 12k cut may end early.

Regenerating: `POST /api/v1/recordings/{id}/minutes` asks for a fresh generation. It also works on a recording that is not a meeting (an older app, the watch, a forgotten mark), which becomes one (`minutes_asked`). `202 {"state":"pending"}` when it is queued, `409` when the transcript is not `complete`, `409 transcript_expired` when the recording was trimmed and has no transcript left, `404` when there is no such recording. It does not change the transcript or the revision.

Confirming suggested to-dos: `POST /api/v1/recordings/{id}/minutes/todos` with `{"version": 2, "actions": ["a1", "a3"]}` creates one Persona to-do per listed action and writes its `todo_id` back. The title is `what`, prefixed with `who: ` when someone was named; the due date is `due_date` when set; the notes name the meeting, its date and the item's clock time. It is an ordinary to-do (no task, no Agent).
`200 {"todos": [Todo], "minutes": Minutes}`. An action that already has a `todo_id` is not created again, and a retry after a lost response creates nothing new. `409 minutes_stale` when `version` is not the current one (refetch and ask again); `404` no such recording, no minutes, or an unknown action; `400` an empty `actions`; `403` from a device that cannot write to-dos.

## What the minutes must contain

The theme (the title), when and where, attendees and speakers only when known, the points discussed, the decisions, the action items (who, what, by when), and the open questions. Each of those carries the transcript timestamp it came from. Nothing the transcript and the calendar don't support is added: a misrecognized word is read in context, but an unspoken decision is not invented. Since #6563 the minutes also have a one-line headline, the core conclusions, and the points and decisions grouped by topic, each with its key figure or status marked ("Summary card (#6563)").

## How the hub generates them

The minutes' writer is the recording module's (`server/internal/modules/recording/writers`: `minutes.go`, the summary
card in `minutes_overview.go`, the slow-model rules in `minutes_timeout.go`). It reads and writes the knowledge base
only through the storage port (`recordingwire.Core`), and the knowledge base starts its round only through
`recordingwire.Provider`; reading minutes that exist (`ReadMinutes`, the detail, search) is the knowledge base's own
and works without the module (`docs/architecture/core-and-modules.md` "Recording"). The same package holds the
module's other writers of a recording: the live title and the owner's rename (`recording_title.go`), the photo intake
(`recording_photos.go`), a hub-mode meeting's audio (`recording_audio.go`), the manager's changes of the transcription
(`transcription.go`), the hub's own transcript (`hub_segments.go`), the upload and its cleaner with the echo rule
(`recordings.go`, `echo.go`), the live log (`live.go`), meeting.json (`meeting.go`) and the material of an answer
(`meeting_material.go`), all behind the same port. Only the short summary's round is the knowledge base's own code
(`server/internal/kb/recordings_summary.go`); it too runs only with the module.

- For a meeting: `kind: meeting`, a calendar item in the knowledge base overlapping a recording of at least 5 minutes, or `minutes_asked`. Only once `complete: true` and the current revision has no minutes yet. The knowledge base loop's minutes lane does it (its own lane since #4247, so a long meeting's minutes no longer hold back archiving, [knowledge.md](knowledge.md) "Pipeline"), at most two recordings a run, on the **Meeting minutes** model (`meeting.minutes`, which follows Information processing until it is set apart, [model-config.md](model-config.md) "Meeting minutes and the in-meeting conversation (#7980)"; `kb.compress` before #7980). No new service. An upload that completes a recording wakes that round, so minutes start within seconds.
- A long transcript is handled in segments: consecutive chunks in time order, each chunk reduced to candidate points, decisions, actions and questions with their timestamps, then one merge pass that deduplicates and orders them. The middle of a meeting is no longer dropped on the floor. Chunks are sized to fit the model's context with room for the instructions.
- When a `meetings` or `events` item overlaps the recording's time window (it starts at most 15 minutes before the recording and before its end; the closest start wins, events before meetings), it becomes `meeting` and its name names the recording unless the user typed a title or the phone sent `calendar.title`. Attendees and place come only from the phone's `calendar` hint or from the transcript. The overlapping `meetings` item alone, by the same rule, is its `imported_meeting` (#8872): the meeting a connected app imported, even when an event won `meeting`. The apps' Meetings list shows the recording and that meeting as one row (the recording's, `meeting_id` on its list item). An imported meeting comes after the call, so a matched recording without one is looked at again until a day after it ended; linking one leaves `meeting`, the name and the minutes as they were.
- Every item must cite a line the model actually read; items whose `at` matches no transcript line are dropped, and so is a `due_date` that is not a real date from the meeting day to a year after it.
- The same answer also groups the points and decisions by topic and adds a headline and the core conclusions. How it does that, and when the hub leaves it out, is in "Summary card (#6563)".
- **Language** (#5906). The minutes are written in the recording's `language` (`zh-CN`, `en-US`, `ja-JP`: the one the phone transcribed it in), else, for a recording that says none or one the hub has no catalog for (`ko-KR`), in the owner's language on the hub (the hello's `locale`), else in Chinese. Whatever languages the transcript mixes, everything the model writes (the title, every item, a deadline as said) is in that one language; names and terms stay as they were said, and so does the place (`where`), which the hub keeps only when it matches the transcript. The Markdown's headings and fixed words (`## Key points`, `- When:`, "(photo 3)") come from the hub's catalog `kb-minutes.json` in the same language, its section names the web's recording page's (`recording.json`, `TestMinutesHeadingsAreTheWebsSections`). Minutes nothing names (no title from the user, the calendar, the model or the recording's summary) are called when the meeting started in that language too (`Recording 2026-11-10 14:00`), not by the recording's Chinese name in lists. `minutes.json` keeps it as `language`, so the Markdown written again later (a photo deleted) says the same; minutes from before it was kept have none and stay Chinese. The prompts stay Chinese and name the language ([prompts.md](prompts.md) "Template rules"); `TestMinutesLanguageLive` runs a mixed-language meeting through a real model ([prompts.md](prompts.md) "Behaviour checks"). A recording's short summary (`prompts/kb/recording.md`, its section names included) and an all-day recording's recap ([day-recap.md](day-recap.md)) follow the same rule. Transcription is unchanged: one language per recording, on the phone.
- The minutes are written next to the recording (`minutes.json`, and `minutes.md` for search, in its directory) and removed by the same delete that removes the recording. The retention sweep keeps them at least as long as the transcript: past a finite `recording_text_days` it trims the recording to its minutes, and only past a finite `minutes_days` does it remove them with the rest (forever by default) ("Privacy").
- On success the user gets a single push naming the meeting, with the number of decisions and suggested to-dos; it has a stable id per recording and `version`, so a restart does not repeat it. A tap opens Persona's main chat, as reminders do; the Lark bot and channels with pushes on get the same text with the recording's web link. Opening the recording straight from a push needs a new push field and is later work.
- No model configured, or the model paused: the minutes stay `pending` and the next round tries again. An answer that cannot be used (not JSON, refused, nothing citable) counts as a failure; after three for one revision they are `failed` until a new revision or a regenerate. Failures do not notify.
- A slow model is not a failure (#3170). A minutes answer is several times longer than a summary, so a call gets 6 minutes where a summary call gets 3. A try that runs out of its time (one call, or the 45 minutes of all the calls of one recording) is counted apart from unusable answers: the recording waits 2, then 4, then 8 minutes before its next try, the other recordings going first meanwhile, and each try gets 6 more minutes per call than the one before (6, 12, 18, 24). After four such tries for one revision the minutes are `failed`, so a recording no model ever finishes cannot hold the queue for ever.
- Each action item is offered as a **suggested to-do** the user confirms (`POST …/minutes/todos` above). An unconfirmed suggestion stays only a suggestion. None are created automatically.

## Titles (#8873)

A meeting or note that nobody named is named from what was said, while it records and again once it is complete, so lists, the meeting page, the Live Activity and the web show what it was about instead of when it started.

- **Order.** A recording's `name` is, first to last: a name the owner gave on the web (`renamed`, below), the owner's own title (the upload's `title`), the calendar's (the phone's `calendar.title`, or the `meetings` / `events` item it overlaps), the model's title (`ai_title`), else when it started. `name_source` says which: `renamed`, `title`, `calendar`, `model`, or absent for the start time (and for an all-day recording named by its day). The phone fills `title` with the calendar event's title when it matched one, so a `title` equal to `calendar.title` counts as `calendar`.
- **While it records** (the live title, `server/internal/modules/recording/writers/recording_title.go`): a recording that is not complete, not an all-day one, with neither the owner's nor the calendar's name, is named by the model once its text (`chars`: the transcript of record, Persona's echo left out) reaches 400 characters, about two minutes of talk. The next is asked for only once the text has at least doubled since the last and 10 minutes have passed, at most 4 calls a recording, failed ones included; a recording with no upload for 12 hours (a lost phone) is left as it is. Each call reads the start of the transcript and its latest part (2000 and 4000 characters) with the title it has, and may keep that title (`keep`) when the talk has not moved on. It does not depend on the room conversation: a meeting Persona is not in (`notes`, `off`), a note and a watch recording are named the same way.
- **When it is complete** the summary's title (`prompts/kb/recording.md`) replaces the live one, and a meeting's minutes then settle it: the title the minutes' model wrote becomes `ai_title`, so the list, the meeting page and the minutes' header say the same. A later summary never replaces the minutes' title. `ai_title_from` (in `detail.meta`) is `live`, `summary` (or absent, from before #8873) or `minutes`.
- **Renaming.** A title the owner gives on the phone (the upload's `title`) comes first from then on, so nothing generated replaces it; clearing it brings the generated title back.
- **Renaming on the web** (#8877): `PUT /api/v1/recordings/{id}/title` `{"title": "…"}`, full credentials only (an extension's token never opens it). The name (trimmed, one line, at most 200 characters; else `400`) is kept as `renamed` with `renamed_at` in `meta.json`, which uploads do not replace, and comes before the upload's `title`. The newer name wins: an upload whose `title` is the one the hub already has (the phone did not rename it) keeps `renamed`; one whose `title` changed (the phone renamed it since, or took the web's name) clears it. `""` clears it too. `200` carries `name` and `name_source` as the upload's answer; `404 recording_not_found`, `410 recording_deleted`, `403 kb_read_only` as the other recording routes. A trimmed recording can be renamed (its `meta.json` stays). The iPhone takes a `renamed` name as its own title, so its next upload carries it; an app that does not know `renamed` keeps showing its own title, and since its uploads leave `title` unchanged the hub keeps the web's name. After a rollback an older manager rewrites `meta.json` without `renamed`, and the phone's title names it again.
- **Language** as the minutes' ("How the hub generates them": the recording's `language`, else the owner's, else Chinese), at most 60 characters on one line (a cut ends on "…").
- **Model.** `meeting.title`, a use of Information processing like chat titles ([model-config.md](model-config.md)); it is not set apart in Model settings. A call sends at most about 6000 characters of transcript and asks for a few words: 2 to 3 calls for a half-hour meeting, 4 at most however long, and none extra at the end (the summary and the minutes run anyway). The calls are background ones: a paused connection or no model set up puts them off without counting.
- **Where it shows.** The upload's `200` and each row of `GET /api/v1/recordings` carry `name` and `name_source` (both absent while it is named only by its start); `detail.meta` carries `name`, `name_source`, `ai_title` and `ai_title_from`. The upload that makes a live title due wakes the knowledge base's titles lane, which writes it within seconds; the phone reads it in the answer to its next upload, about 30 seconds later, and the final one at its next reconciliation or when it opens the recording. The web's lists and detail read `name` as before.
- **Compatibility.** An app that does not read the new fields shows what it showed before; a phone talking to an older manager gets no `name` and keeps its own. After a rollback an older manager rewrites `meta.json` without `ai_title_from` and the live-title counts: a recording still recording may then get one more live title.

## Summary card (#6563)

After the meeting, the minutes read as one summary card, the way good minutes tools lay them out. A headline says what the meeting was and the **core conclusions** say where it landed. Then come the topic cards: one per topic or workstream, grouped under short headings, each with a few short lines whose key figure or status is bold. After them come the action items, the open questions, and a note that AI wrote it. The owner asked for the redesign in #6563 ("you are free to redesign the page"). The summary card replaces the iPhone's four cards (#5026) and the web's flat lists, but only for minutes that carry it. Minutes without it look exactly as before.

### The `overview` field

`detail.minutes.overview` (also in `minutes.json`) is optional. It holds the headline, the conclusions, and the points and decisions grouped by topic. `points`, `decisions`, `actions` and `open` are still always written and mean what they did, so an older client shows the same minutes as before.

```jsonc
"overview": {
  "headline": "Export ships with the size cap; the release date waits on the cache fix", // optional, one line
  "conclusions": [                       // optional: at most 4, the most important first
    {"text": "Export ships with the 4 MB cap and a warning", "strong": ["4 MB"], "at": 1504.5},
    {"text": "The release date hangs on the cache fix by Wednesday", "strong": ["Wednesday"], "at": 2290.0},
    {"text": "Load target raised to 40K QPS", "strong": ["40K"], "at": 1830.0}
  ],
  "groups": [                            // at least one: a single group titled "", or 2–4 titled groups
    {"title": "Project progress", "topics": [
      {"title": "Export", "icon": "progress", "bullets": [
        {"text": "Export drops entries over 4 MB", "strong": ["4 MB"], "at": 312.0, "who": "Ada"},
        {"text": "The export size-limit message is done", "strong": ["done"], "at": 655.0},
        {"text": "Export keeps the cap, with a warning when an entry is skipped", "strong": ["keeps the cap"], "at": 1504.5, "decision": true}
      ]},
      {"title": "Release date", "icon": "schedule", "bullets": [
        {"text": "The release on the 20th is at risk", "strong": ["at risk"], "at": 1810.0},
        {"text": "The release moves a week unless the cache bug is fixed by Wednesday", "strong": ["a week"], "at": 2290.0, "decision": true}
      ]}
    ]},
    {"title": "Tooling and performance", "topics": [
      {"title": "Load testing", "icon": "performance", "bullets": [
        {"text": "Load-test QPS raised to 40K", "strong": ["40K"], "at": 1830.0, "who": "Bo"}
      ]},
      {"title": "Cache bug", "icon": "quality", "bullets": [
        {"text": "The cache bug reproduced once on a weak network", "strong": ["once"], "at": 1950.0, "photo": 2}
      ]}
    ]}
  ]
}
```

In the same minutes, `points` holds the five point bullets and `decisions` the two decision bullets, word for word and in time order.

What the hub guarantees, so clients check only what "Clients" below says:

- **Present means usable.** There is at least one group, every group has at least one topic, and every topic has at least one bullet.
- **The bullets are the points and the decisions.** Every point and every decision is exactly one bullet, and every bullet is exactly one of them, with the same `text`, `at` and `photo` (and `who` for a point). `decision: true` marks a decision and is left out for a point. `who` is on points only. Actions and open questions are never bullets.
- **Groups.** There is either one group whose `title` is `""` (clients show no heading for it), or 2 to 4 groups with distinct titles.
- **Order.** Bullets are in `at` order. Topics within a group are ordered by their first bullet, and groups by their first topic. Conclusions keep the model's order, most important first, not time order.
- **Citations.** Every conclusion and bullet has `at`, plus `photo` when it cites one, resolved exactly as items are ("How the hub generates them").
- **No Markdown.** No text contains `**`. Bold is given by `strong` (below).
- **Icons.** `icon` is one of `progress`, `schedule`, `release`, `performance`, `metrics`, `tooling`, `quality`, `design`, `people`, `customer`, `budget`, `risk`, `general`. A client treats a value it does not know as `general`, so the list can grow.

Limits, in characters (Unicode code points). A text cut to its limit ends in "…", which counts towards it:

| Field | Limit |
| --- | --- |
| `headline` | One line, at most 120. Left out when empty or the same as `title`. |
| `conclusions` | At most 4, each at most 200. |
| Group `title` | At most 24. |
| Topics | At most 12 in all. `title` at most 40. |
| Bullet `text` | At most 400, like the text of every item (a cut keeps 399 and "…"). |
| `strong` | At most 2 per text, each 1 to 20 characters, never the whole text. |

### Bold (`strong`)

`strong` lists the substrings of `text` to set in bold, in the order they appear. Every client, and the Markdown, find them the same way:

1. Start at the beginning of `text`.
2. For each entry, find its first exact occurrence at or after that point (comparing code units, with no case folding or normalization) and bold it.
3. Continue from the end of that occurrence. An entry that is not found is skipped; its words are still shown, just not in bold.

The hub keeps an entry only when this rule finds the exact place the model marked, so a figure that appears twice is never bolded in the wrong place. It also moves spaces and punctuation at the ends of an entry outside it, except the signs a figure or a name holds (`%`, `‰`, `‱`, `#`, `@`, `&`, `-`): `**92%.**` gives `92%`, `**(-5%)**` gives `-5%`, and `**#42**` stays `#42`.

`strong` uses substrings rather than offsets because Go, Swift, JavaScript and Kotlin count string positions differently. No client parses Markdown in these texts: words from a transcript or a photo could otherwise become live links.

### How the hub makes it

- **What the model writes.** The prompts (`prompts/kb/minutes.md`, `minutes_merge.md`) ask for the same minutes in a new shape. Besides `title`, `where`, `attendees`, `actions` and `open`, the answer has:
  - `headline`: about 40 Chinese characters.
  - `conclusions`: 2 to 4 entries, each `{text, line}` (or `photo`), saying where the meeting landed. A conclusion cites a photo only when a point or decision of a topic cites the same photo; otherwise it cites the line that supports it ("Conclusions" below).
  - `topics`: 2 to 8 of them (at most 10), in the order they were first discussed. Each is `{title, group, icon, points, decisions}`, and its points and decisions are written as before (`{text, line, who}`, `{text, line}`).

  Each point or decision is one short, self-contained line that names its subject and reads right without its topic or group heading (older apps and the meeting material show it on its own), and marks its key figure or status with `**…**`. Actions and open questions stay plain text, with no markers. It marks at most two places of a few words each, such as "module X **done**" or "QPS raised to **40K**". A topic has 2 to 4 of these lines, and one with more than 5 is split into two topics. `group` is a short name (about 8 Chinese characters), given only when there are 4 or more topics that fall into 2 to 4 groups. `icon` is one of the list above, always written in English.
- **Checking it.** `buildMinutes` checks every point and decision in every topic exactly as it checks items today: an allowed citation, a name that was spoken or is on the calendar, no duplicates. It takes the `**` markers out of the text. The plain text goes into `points` or `decisions`, and the marked places into `strong`. A stray `**` in the minutes' title, the place, a name or a deadline as said (actions and open questions too) is taken out as well, so a name still matches the calendar and the transcript. The legacy lists are sorted and capped as before, and action ids (`a1`…) are numbered as before.
- **Conclusions** need an allowed citation too, lose their markers the same way, and are capped at 4. They never count as the citation that makes an answer usable: minutes still need an item that cites the transcript. A conclusion may cite a photo only when a bullet of the overview cites the same photo; otherwise its photo citation does not count, and it is dropped unless it also cites a line. So the photos the items cite are all the photos the minutes cite, which an older hub's photo delete and trim also see.
- **Titles and icons.** The headline and the topic and group titles are put on one line, stripped of markers and clipped to their limits. An icon not in the list becomes `general`. Two topics with the same title (ignoring case) are merged into one.
- **Groups.** Topics keep their groups only when there are at least 4 topics, every topic names a group, and there are 2 to 4 distinct groups. Otherwise all topics go into one group without a title.
- **What the counts count.** The gates below, the 12-topic limit and the group rule count what is kept: items that passed the citation, name and duplicate checks (an unsupported name is cleared, as today, without dropping the point), topics with at least one kept item after their titles are cleaned, and same-titled topics merged (ignoring case: the first one in the answer gives the merged topic its title, `group` and `icon`; its place follows its first bullet's time). Group names are compared after cleaning, ignoring case.
- **When there is no overview.** The overview is written only when every point and decision belongs to a titled topic. The hub leaves it out, writes the minutes exactly as before, and logs why when:
  - the answer has no point or decision;
  - it has points or decisions outside `topics` (the old shape, or a mix of both);
  - a topic with items has no title;
  - there are more than 12 topics.

  A malformed `headline` or `conclusions` (or one entry of them), or a topic's malformed `title`, `group` or `icon`, never makes the answer unusable: that value is dropped (an icon becomes `general`, a topic left without a title leaves the overview out, as above), and the topic's points and decisions are kept. The answer is unusable only when `topics` is not a list (a `null` one is none, as for the answer's other lists), a topic is not an object (`null` included, even beside usable topics), or a topic's `points` or `decisions` are malformed the way a malformed `points` is today; that counts towards `failed` ("How the hub generates them"). The minutes' own `title` is decoded as today.
- **Long meetings.** Each part's notes keep every point and decision the part gives, inside `topics` or not. Those under a titled topic carry its title, so the merging call reads lines like `[12]〈Export〉 text`; the others carry no tag. Before the per-part cap (`max(2, 60/parts)` per section, as today), the top-level and topic items of each section are put in the order of the line or photo they cite (photo first, as citations resolve). The cap then keeps a part's earliest notes whatever order the topics came in; later notes, a whole later topic included, can still be left out, as later items are today. The merging call is asked to:
  - merge the parts' topics (the same topic may have a different name in each part);
  - put every point and decision, tagged or not, into one merged topic;
  - write the headline and conclusions for the whole meeting from the notes, with later parts overriding earlier ones.

  A part writes no headline and no conclusions. Notes carry no `**`; the merging call marks the texts again. Its answer is checked as above, and it can still cite only the lines and photos that appear in the notes.
- **Live topics and highlights.** The overview is made from the transcript and photos only. It does not read the room's topics and highlights ("Phase 2: live view", "Phase 2b: highlights"): they exist only for meetings Persona attended, they are made from the live lines rather than the final transcript, and they are deleted with the transcript.
- **Photos.** Before the recording is trimmed, deleting a photo also removes the conclusions and bullets that cite it, together with their items ("Phase 3: photos"). A topic left without bullets is removed, then a group left without topics, and the group rule above is applied again; the overview is removed when no topic is left. On a trimmed recording the minutes stay as they are, the overview included, and are not made again; a citation of the deleted photo shows "Photo removed", as Phase 3 says. A photo a conclusion cites is also cited by a bullet (above), so it is kept at the transcript cut.
- **Older minutes and regenerating.** Minutes written before #6563 have no overview and are never rewritten automatically to add one. Regenerate writes new minutes with it; to-dos carry over by `who` and `what`, as with every rewrite. If an older hub (after a rollback) rewrites minutes that have an overview, it drops the overview, since it does not know the field, and clients show the minutes as before. Confirming a to-do there does not render the Markdown again, so `markdown` and `minutes.md` keep the summary-card layout until the minutes are next rendered (a photo deleted, new minutes); every photo the minutes cite is still cited by an item, so its photo delete and trim work as today.

### Markdown

`markdown` and `minutes.md` follow the overview when there is one. Minutes this hub renders without one are written byte for byte as before (a write that only links to-dos keeps the stored Markdown, as today). Headings and fixed words come from `kb-minutes.json` in the minutes' language, which gains `conclusions`, `decided` and `ai_note`:

```
# Sprint review

- When: 2026-11-10 14:00–15:10
- Location: Room 3
- Attendees: Ada, Bo, Cy

Export ships with the size cap; the release date waits on the cache fix

## Core conclusions

- [14:25:04] Export ships with the **4 MB** cap and a warning
- [14:38:10] The release date hangs on the cache fix by **Wednesday**
- [14:30:30] Load target raised to **40K** QPS

## Project progress

### Export

- [14:05:12] Export drops entries over **4 MB** (Ada)
- [14:10:55] The export size-limit message is **done**
- [14:25:04] Decided: Export **keeps the cap**, with a warning when an entry is skipped

### Release date

- [14:30:10] The release on the 20th is **at risk**
- [14:38:10] Decided: The release moves **a week** unless the cache bug is fixed by Wednesday

## Tooling and performance

### Load testing

- [14:30:30] Load-test QPS raised to **40K** (Bo)

### Cache bug

- [14:32:30] The cache bug reproduced **once** on a weak network (photo 2)

## Action items

- [14:35:00] Bo: Add the warning text and the regression test (Friday)
- [14:39:10] Cy: Share the load-test report

## Open questions

- [14:40:08] Whether the cap is per entry or per recording

*AI-generated from the recording. Check anything important.*
```

- With one group, each topic is a `##` heading and there are no `###` headings. The headline line is left out when there is no headline.
- There is no "Key points" or "Decisions" section. Decisions sit in their topic after `decided`, so `kb_get` and `kb_search` still find them.
- Action items are written as in minutes without an overview, with no checkbox: the minutes do not know a to-do's state, and `minutes.md` is also what a follow-up's seed and `kb_search` read, so a `[ ]` would say the action is still open. `kb_get` prints the real state right after the minutes.
- A literal `*` in the text is written as `\*`. Bold places are wrapped according to the rule above.
- If the minutes are partial, the partial note comes before the AI note.

### Clients

A client decodes `overview` on its own, apart from the legacy fields, and shows the summary card only when a bullet is left after this cleaning; in every other case it shows the minutes as before, from the same payload. The mock hub, the iPhone and the web check the same malformed cases against one shared fixture.

- A missing, `null` or non-object `overview`, or a missing or non-array `groups`, means no overview.
- In `groups`, `topics` and `bullets`, members that are not objects are dropped. A group without an array `topics`, and a topic without an array `bullets` or without a non-blank string `title`, are dropped. A group `title` that is missing or not a string is `""`.
- Bullets and conclusions without a non-blank string `text` are dropped. A missing or wrong-typed `headline`, `conclusions`, `who` or `strong` counts as absent; in `conclusions` and `strong`, invalid entries are dropped. Only the boolean `true` marks a decision.
- An unknown `icon` becomes `general`.
- An `at` that is not a non-negative number shows no time.
- A `photo` that is not a positive whole number counts as absent.
- A `strong` entry that is not found bolds nothing.
- Then empty topics and empty groups are dropped.

The choice between the two layouts is made where the minutes are prepared for display, never while drawing. A broken overview never stops the rest of the minutes from decoding.

Both clients follow these rules:

- **Order.** First the summary card: the headline (or the minutes' title when there is no headline) and the core conclusions. Then the topic cards under their group headings, then Action Items, then Open Questions, then the AI note. Points and decisions are not shown again in cards of their own.
- **Bullets.** A bullet shows its words with the `strong` places in bold, its photo as items show theirs, and its time, which jumps to the transcript as an item's time does. A decision shows the decision mark instead of the dot, and the word "Decision" where a point shows its speaker, so nothing depends on the glyph or a color alone.
- **Folding.** A topic card shows its first 4 bullets, then "Show N more" (and "Show less"), which open the rest in place.
- **Conclusions.** A conclusion shows its words, its bold places, its photo and its time. It has no transcript disclosure, because it sums up more than one line.
- **Colors.** The cards of a group share one of four topic tints, chosen by the group's position (the first group gets the first tint). The model never chooses a tint, and a tint is never a status color: no red, orange, green or yellow, and not the AI color. The conclusions box uses the AI tint and the lightbulb, because these are the AI's own words. Each client draws its own glyph for `icon`.
- **Action items.** Add to to-dos, Added and Add all stay, and the versioned confirmation (`POST …/minutes/todos`) is unchanged. On the iPhone, an added action whose to-do the app holds shows that to-do's done circle, which ticks it as the To-dos list does. A suggested action never shows a box.
- **Copy and Share** send `markdown` (the iPhone too, in this layout; minutes without an overview keep the iPhone's own text).
- **AI note.** It is the client's own string, with the same words as the Markdown's.

**iPhone** (`RecordingMinutesSummary`, prepared in `RecordingMinutesCards`): one column on every iPhone. Each topic is its own card, a section of the page's list like the four cards today. In the sketch, `*x*` stands for bold.

```
+-----------------------------------------------+
| Sprint review                                 |  page header, switch, Copy and
| Tue, Nov 10, 14:00-15:10 . Room 3             |  Share: unchanged
+-----------------------------------------------+
| Export ships with the size cap; the release   |  headline
| date waits on the cache fix                   |
| +-------------------------------------------+ |
| | (bulb) Core Conclusions                   | |  AI tint well
| | Export ships with the *4 MB* cap and a    | |
| | warning                           [25:04] | |
| | The release date hangs on the cache fix   | |
| | by *Wednesday*                    [38:10] | |
| | Load target raised to *40K* QPS   [30:30] | |
| +-------------------------------------------+ |
| [ Regenerate ]                                |
+-----------------------------------------------+
  o Project progress                               group heading, dot in its tint
+-----------------------------------------------+
| [tile] Export                                 |  icon tile in the group's tint
| .  Export drops entries over *4 MB*           |
|    Ada                          [5:12]  (v)   |
| .  The export size-limit message is *done*    |
|                                [10:55]  (v)   |
| #  Export *keeps the cap*, with a warning     |  # = the decision mark
|    when an entry is skipped                   |
|    Decision                    [25:04]  (v)   |
+-----------------------------------------------+
+-----------------------------------------------+
| [tile] Release date                           |
| ...                                           |
+-----------------------------------------------+
  o Tooling and performance
  ...
+-----------------------------------------------+
| [todo] Action Items                       (2) |
| (o) Add the warning text and the regression   |  (o) = its to-do's circle, once added
|     test                                      |
|     Bo . Friday . Due Nov 13   [35:00]  (v)   |
|     ---------------------------------------   |
|     Share the load-test report [39:10]  (v)   |
|     Cy                                        |
|     [ Add to To-Dos ]                         |
+-----------------------------------------------+
| [?] Open Questions                        (1) |
| ...                                           |
+-----------------------------------------------+
  (sparkle) AI-generated from the recording.       footer
  Check anything important.
```

- The summary card shows when, where and the attendees only where the page header does not already.
- The conclusions box is an inset well inside the summary card, never a `.surface`; a surface's shadow is cut off in a list row.
- At the largest text sizes, times and disclosures go under the words (as items do today), the tile goes above the topic's title, and group headings wrap.
- VoiceOver headings, in order: the headline, Core Conclusions, each group, each topic, Action Items, Open Questions. The dot, the decision mark and the tiles are hidden from VoiceOver. "Show N more" names its topic.
- iPad and landscape layouts with several cards per row come later. Until then they use the one column.

**Web** (`RecordingMinutes.tsx`): one card, as today, inside the recording page's existing column. Each group's topics form a grid with as many columns as fit cards of the minimum width token, but never more than three. That is two columns in today's 720 px column, one on a narrow screen, and a lone card takes its whole row. The topic area is a single child of the card, so there are no dividers between topic cards.

```
 Minutes                                          [Copy] [Regenerate]
+--------------------------------------------------------------------+
| Export ships with the size cap; the release date waits on the      |
| cache fix                                                          |
| Tue, Nov 10, 2026 14:00-15:10 . Room 3 . Ada . Bo . Cy             |
| +----------------------------------------------------------------+ |
| | (light) Core conclusions                                       | |
| | Export ships with the *4 MB* cap and a warning           14:25 | |
| | The release date hangs on the cache fix by *Wednesday*   14:38 | |
| | Load target raised to *40K* QPS                          14:30 | |
| +----------------------------------------------------------------+ |
| Project progress                                                   |
| +-------------------------------+ +------------------------------+ |
| | [trend] Export                | | [calendar] Release date      | |
| | . Export drops entries over   | | . The release on the 20th is | |
| |   *4 MB*  Ada           14:05 | |   *at risk*            14:30 | |
| | . The export size-limit       | | # The release moves *a week* | |
| |   message is *done*     14:10 | |   unless the cache bug is    | |
| | # Export *keeps the cap*, ... | |   fixed by Wednesday         | |
| |   Decision              14:25 | |   Decision             14:38 | |
| +-------------------------------+ +------------------------------+ |
| Tooling and performance                                            |
| +-------------------------------+ +------------------------------+ |
| | [gauge] Load testing   ...    | | [shield] Cache bug     ...   | |
| +-------------------------------+ +------------------------------+ |
|--------------------------------------------------------------------|
| Action items                                            [Add all]  |
| Bo  Add the warning text and the regression test                   |
|     [Fri, Nov 13]  14:35                         [Add to to-dos]   |
|--------------------------------------------------------------------|
| Open questions                                                     |
| - Whether the cap is per entry or per recording              14:40 |
|--------------------------------------------------------------------|
| (sparkles) AI-generated from the recording. Check anything         |
| important.                                                         |
+--------------------------------------------------------------------+
```

- On a narrow screen everything is one column: the conclusions box, then each card under its group's heading.
- Headings: "Minutes" is the section's h2 and the headline (or title) h3. Core conclusions, Action items and Open questions are h4. With named groups, each group is a `section` labelled by its h4 heading and its topics have h5 headings; with the single untitled group there is no group heading, the grid is a plain `div`, and each topic has an h4 heading. Each topic is an `article` labelled by its title. The reading order runs row by row.
- Times and photos use the page's existing citations, as items do today: the cited photo, otherwise the transcript time. A time is disabled when there is no transcript line to jump to, as after the trim; a photo the trim kept still opens under Photos, and a deleted one shows "Photo removed".
- Topic tints are design tokens with light and dark values; the bold text and the times keep their contrast on every tint.

### Compatibility

| Hub | Client | Result |
| --- | --- | --- |
| Older | Any | No `overview`, so the minutes look as before. |
| This one | Older (any platform, the watch) | `overview` is ignored. The client gets the same `points`, `decisions`, `actions` and `open`, now short card-style lines. Copy on the web and on Android sends the new Markdown. |
| This one | This one | The summary card when `overview` is present. Minutes from before it, and those the hub wrote without one, look as before. |
| Rolled back to an older one | This one | The first rewrite by the older hub drops `overview`, and the page falls back. A to-do confirmed there leaves the summary-card Markdown in place until the minutes are next rendered. Photo deletes and the trim work as today, since every cited photo is also cited by an item. |

## Disagreements (#16728)

Minutes of a meeting with a real disagreement list it; all other minutes look exactly as before. A **Disagreements** section is written by the same minutes call, with no new setting. For each real disagreement it gives the question in dispute, two to four positions citing the transcript line where each side's holder says it, and whether the meeting settled it. Without a speaker timeline, a position names a person only when the transcript or the calendar supports it, exactly as `who` does; when a hub-transcribed meeting carries a speaker timeline, positions also take their speaker attribution from it ("Speakers (#17674)").

Unchanged: the "minutes ready" push text (still counts decisions and to-dos), the meeting summary card's counts, the material later meetings read (still copies only points, decisions, actions and open questions), and minutes written by extensions (`KBRecordingMinutes`, `schema/extension/kb.schema.json`).

### The `debates` field

`detail.minutes.debates` (also in `minutes.json`) is an optional top-level array beside `overview` (`omitempty`), never written when there is no disagreement.

```jsonc
"debates": [
  {
    "question": "Should the export cap count items or recordings?",
    "topic": "Export",                    // optional: summary card topic placement hint only
    "status": "settled",                  // "open" or "settled"
    "at": 312.0,                          // earliest position's time; debates are ordered by at
    "positions": [
      {"stance": "Per item: one large file should not fail the whole export", "at": 312.0, "who": "Ada"},
      {"stance": "Per recording: easier to explain", "at": 340.5}
    ],
    "outcome": {"text": "Per item for this release", "at": 1504.5, "decision": 0} // only when settled
  }
]
```

- `question`: the point in dispute, one sentence, on one line.
- `topic` (optional): the summary card topic it belongs to, a placement hint only.
- `status`: a named closed type in `server/internal/recordingwire` (not a bare `string`) with two values and no new count:
  - `open` (**still disagreed**): people put opposing claims and the meeting ended without deciding between them.
  - `settled` (**settled**): people put opposing claims and the meeting later decided between them.
  A client that reads a value it does not know shows the disagreement with no status word.
- `at`: the earliest position's time; disagreements are kept in the order of `at`.
- `positions`: 2 to 4 positions (a disagreement with fewer than 2 is never written). Each position has:
  - `stance`: a short sentence with no names in it, on one line;
  - `at`: the start of the cited transcript line, the same clock as `segments[].start` (clients jump to that line with it, as they do for decisions; photos cannot be cited);
  - `who` (optional): the person who holds that stance, only when the transcript or the calendar supports it, following the existing `who` rule.
- `outcome` (present only when `status` is `settled`): `text` (one line), `at` (the cited transcript line where it was decided), and `decision` (optional, non-negative integer): the 0-based index in `decisions` of the kept decision that cites the same line. The hub works this link out on every write; clients never match times themselves.
- **Extensibility**: clients ignore keys they do not know at every level of `debates` (each debate, position and `outcome`), so fields such as end time, estimated timing or further supporting citations can be added later when something reads them.

Limits, in characters (Unicode code points; a text cut to its limit ends in "…", which counts towards it):

| Field | Limit |
| --- | --- |
| `debates` | At most 6 per meeting. |
| `question` | One line, at most 200. |
| `topic` | One line, at most 40. |
| `positions` | 2 to 4 per disagreement. |
| `stance` | One line, at most 400. |
| `outcome.text` | One line, at most 400. |

### How the hub makes it

- **What the model writes.** In the same minutes call (`server/internal/prompts/kb/minutes.md`, `server/internal/prompts/kb/minutes_card.md`, `server/internal/prompts/kb/minutes_merge.md`, `server/internal/prompts/kb/minutes_merge_input.md`), the model's JSON answer gains `debates: [{question, topic, status, positions: [{stance, line, who}], outcome: {text, line}}]`. The prompts instruct the model:
  - record only real disagreements, where people put different claims; ordinary discussion is not one; when in doubt, leave it out;
  - each position cites the line where its holder says it, not a line where someone else restates it;
  - stances carry no names; a name the transcript or calendar supports goes in `who`;
  - when it was settled later, set `status` to `settled` and give `outcome` citing the same line as the matching decision; a disagreement still open is not repeated under open questions.
- **Checking it** (`server/internal/modules/recording/writers/minutes.go`):
  - `debates` is read leniently, as `conclusions` is (`modelList` / `modelText` in `server/internal/modules/recording/writers/minutes_overview.go`): a malformed `debates` value or entry is dropped and never makes the answer unusable (whereas a JSON type error in the core answer fields counts as unusable and towards `failed`).
  - A position is kept only when its `line` is a transcript line the model actually read; photos cannot be cited on positions or outcomes.
  - Any `**` markers are stripped, and `question`, `topic`, `stance` and `outcome.text` are put on one line within their limits.
  - A disagreement with an empty `question` or fewer than 2 valid positions is dropped. At most 6 disagreements and at most 4 positions per disagreement are kept.
  - A `settled` disagreement without a valid `outcome` (missing, empty `text`, or citing a line not read) becomes `open` with no `outcome`. An `open` disagreement carries no `outcome`.
  - `who` is checked with the existing `named()` rule (an unsupported name is cleared without dropping the position).
  - For each `settled` disagreement, the hub sets `outcome.decision` to the index in kept `decisions` of the decision that cites the same transcript line (ignoring photo-only citations), or omits it when none matches.
  - Each debate's `at` is set to the earliest `at` among its kept positions, and disagreements are sorted by `at`.
  - Disagreements do not count as the citation that makes an answer usable (as for `conclusions`): the minutes still need at least one kept point, decision, action or open question citing what the model read.
  - Calls stay within the existing 6000-token output cap (`JSONMax(…, 6000, …)`; a truncated answer is unusable), unless raised intentionally after checking the longest case.
- **Lenient read of `minutes.json`.** `Minutes.UnmarshalJSON` (`server/internal/recordingwire/minutes_overview.go`) decodes `debates` on its own, as it does `overview`: a `debates` block it cannot read is treated as absent so the rest of `minutes.json` still reads.
- **Long meetings.** Meetings written in parts and merged make four changes:
  1. Each part gives its own disagreements, and the part's notes for the merging call carry them with their line numbers (`notesForMerge`, `server/internal/prompts/kb/minutes_merge_input.md`; `KBMinutesPart` gains a dedicated field while `KBMinutesNote` keeps its shape because the day recap shares it).
  2. `notedLines` (the lines the merged answer may cite) also collects the lines cited by each part's positions and outcomes, so positions of a long meeting survive verification.
  3. The merge prompt's rule "cite the earliest line when the same thing appears more than once" (`server/internal/prompts/kb/minutes_merge.md`) has an explicit exception for disagreements: a position cites its holder's own line, and an outcome cites the line where it was finally decided.
  4. The merging call may form a disagreement from the parts' notes when its sides fall in different parts. Disagreements carry their own per-part quota in the notes, separate from the existing per-section cap (`max(2, 60/parts)`).
- **Photos.** Positions and outcomes cite transcript lines only, never photos. Deleting a photo (`photoUncite`) leaves `debates` and the Disagreements section intact when the minutes are re-rendered.
- **Older minutes and rewrites.** Older minutes are never rewritten just to add disagreements. Any rewrite writes them: Regenerate (`POST /api/v1/recordings/{id}/minutes`), a new revision, a photo added or deleted, or a hub-mode meeting's final transcript stored. A recording whose transcript is kept (by default since #16445 the hub keeps the text forever) can be regenerated and confirmed to-dos carry over as on any rewrite; a trimmed recording still answers `409 transcript_expired`.

### Markdown

The section follows the existing line format (`- [HH:MM:SS] text`, a speaker formatted with `who` as ` (Ada)` in English or `（Ada）` in Chinese and Japanese) using new keys in `kb-minutes.json` (`en`, `zh-Hans`, `ja`):

```
## Disagreements

### Should the export cap count items or recordings? (settled)
- [10:05:12] Per item: one large file should not fail the whole export (Ada)
- [10:05:40] Per recording: easier to explain
- [10:25:04] Outcome: Per item for this release
```

- **Summary card layout:** after Core conclusions (or after the headline and metadata when there are no core conclusions) and before the first group or topic. A literal `*` is escaped as `\*`, as in the rest of the summary-card Markdown.
- **Legacy layout:** before Open questions — that is, between Action items and Open questions (splitting the hub's `tail`).
- **Headings and status words:** the section heading and status words match the web's `web/src/i18n/locales/*/recording.json` (checked by `TestMinutesHeadingsAreTheWebsSections`), and the status words never reuse the wording of "Open questions" (one label, one definition):

| Language | Heading | `open` | `settled` | Outcome prefix |
| --- | --- | --- | --- | --- |
| `en` | Disagreements | still disagreed | settled | `Outcome: ` |
| `zh-Hans` | 分歧 | 仍有分歧 | 已谈拢 | `结果：` |
| `ja` | 意見の相違 | 未合意 | 合意済み | `結果：` |

  A disagreement with an unknown `status` renders its `###` heading with no status word.
- **No disagreement:** at rendering time, both layouts render byte for byte as before (regenerating is a new model call, not a re-render).
- Search, `kb_get` and the follow-up conversation read `minutes.md`, so they include the section with no other change.

### Clients

A client decodes `debates` on its own, apart from the rest of the minutes, and shows the Disagreements section only when at least one valid disagreement remains after cleaning. A malformed `debates` block never prevents the rest of the minutes from decoding or displaying. The hub, mock hub, web and iPhone verify the same cleaning cases against a shared fixture.

**Cleaning rules:**

- A missing, `null` or non-array `debates` means no disagreements.
- Members of `debates` that are not objects are dropped.
- A disagreement without a non-blank string `question` or without an array `positions` is dropped.
- In `positions`, members that are not objects or lack a non-blank string `stance` are dropped. A missing, non-string or blank `who` counts as absent. An `at` that is not a non-negative finite number shows no time.
- A disagreement left with fewer than 2 valid positions after cleaning is dropped.
- `status` is `"open"` or `"settled"`; any missing, wrong-typed or unknown value is kept as unknown and shown with no status word.
- A missing, non-string or blank `topic` counts as absent.
- A debate's `at` that is not a non-negative finite number counts as absent (it does not drop the disagreement).
- `outcome` is kept only when `status` is `"settled"` and `outcome` is an object with a non-blank string `text`. Its `at` follows the same rule (non-negative finite number, else no time). Its `decision` is kept only when it is a non-negative integer within the bounds of `decisions` (otherwise no decision link). A missing or invalid `outcome` on a `settled` disagreement omits the outcome row without dropping the disagreement.
- Unknown keys at every level (`debates[]`, `positions[]`, `outcome`) are ignored.
- When no disagreement remains after cleaning, no section is shown and the minutes look identical to minutes without `debates`.

**Web** (`web/src/modules/recording/minutes-overview.ts`, types in `web/src/lib/kb.ts`):

- **Summary card layout:** a Disagreements group after Core conclusions and before the topic cards, one small card per disagreement: the question as its title, the status as a text label (never color alone; omitted when unknown), one row per position (name when present, stance, and time jumping to the transcript line), and for a settled disagreement a last row with the outcome that leads to its linked decision when `outcome.decision` is valid.
- **Legacy layout:** the same section after Action items and before Open questions.
- **Heading levels and reading order:**
  - Summary card layout: "Minutes" (`h2`) → headline or title (`h3`) → Core conclusions (`h4`, when present) → **Disagreements** (`section` labelled by an `h4` heading, each disagreement an `article` with an `h5` heading for its question) → topic groups (`h4` group / `h5` topic, or untitled `h4` topic) → Action items (`h4`) → Open questions (`h4`).
  - Legacy layout: "Minutes" (`h2`) → title (`h3`) → Key points (`h4`) → Decisions (`h4`) → Action items (`h4`) → **Disagreements** (`section` labelled by an `h4` heading, each disagreement an `article` with an `h5` heading for its question) → Open questions (`h4`).
  - Reading order runs in visual order (top to bottom, row by row across cards).

**iPhone** (`apple/Shared/Recording/RecordingMinutes.swift`, compiled for the watch too):

- Every new field is optional and the block is decoded with `try?` so a malformed block reads as none and the rest of the detail still decodes.
- **Summary card layout** (`RecordingMinutesOverview.swift`): after the summary card (headline and Core Conclusions) and before the first topic group or card.
- **Legacy layout** (`RecordingMinutesSummary.swift`): before Open Questions (the iPhone's legacy card order is Key Points, Decisions, Open Questions, Action Items, so Disagreements sits after Decisions and before Open Questions).
- **Boxed mode:** the meeting page's after mode reuses these cards in boxed mode (`minutesBoxed`), so the new disagreement card supports boxed mode.
- **Copy and Share:** in the summary card layout Copy and Share already send `markdown`. In the legacy layout `text(title:)` gains the Disagreements section (after Action Items, before Open Questions) only when there are disagreements; without them, `text(title:)` is unchanged.
- **VoiceOver heading order:**
  - Summary card layout: headline → Core Conclusions (when present) → **Disagreements** (section heading, then each disagreement's question heading in order) → each group → each topic → Action Items → Open Questions.
  - Legacy layout: Key Points → Decisions → **Disagreements** (section heading, then each disagreement's question heading in order) → Open Questions → Action Items.
  - Decorative icons and marks are hidden from VoiceOver; status is read as text, never color alone.

**Other platforms:**

- **Android and HarmonyOS:** no screen change; Android's Copy and Share and HarmonyOS's rendering use `minutes.markdown`, which includes the section.
- **Not shown:** Apple Watch (it gets a short relay from the phone) and visionOS (it shows the legacy sections only).

### Compatibility

| Hub | Client | Result |
| --- | --- | --- |
| Older | Any | No `debates`; the minutes look as before. |
| This one | Older (any platform, the watch) | `debates` is ignored on screen. Copy on the web (always), on the iPhone in the summary card layout and on Android sends the hub's Markdown, which has the section the screen does not show; HarmonyOS renders that Markdown. |
| This one | This one | The section when there is a disagreement; otherwise the minutes look as before. |
| Rolled back to an older one | This one | The older hub's first rewrite drops `debates`. Confirming a to-do there leaves the section in `markdown` and `minutes.md` until the minutes are next rendered (a photo deleted, new minutes), so screens show no section while Copy, `kb_get`, search, Android and HarmonyOS still have it. |

## Speakers (#17674)

A meeting the hub transcribes can carry a **speaker timeline**: anonymous numbered speakers (`s1`, `s2`, …) with the times they spoke. A meeting without a speaker timeline looks exactly as before; with one, the transcript and the disagreements show their speakers with no action, and the owner only names them.

- Every transcript line whose time is reliable gets its speaker (or "?" when unsure).
- Each position of a disagreement in the minutes is attributed to a speaker, worked out by the hub from the line it cites, never by the model.
- The owner names speakers once ("Speaker 2" → a name); names, merges and unmerges re-render the minutes and their Markdown immediately, with no model call, no push and no new minutes `version`.

Nothing recognises a voice, across meetings or within one, and no voice features are stored: only which numbered speaker spoke when and the names the owner chose. Until a separation engine exists on the hub (a later phase), only the debug import stores a timeline.

### Where things are kept

| What | Where | Why |
| --- | --- | --- |
| The speaker list, state and quality | a `speakers` block in `meeting.json` | Kept when a transcript expires and is trimmed, so names survive; has per-part revisions and the `meeting_live` event; readable by the minutes gate and by Core with the recording module off |
| The timeline | `speaker_turns.json` | Part of the transcript: expires with it (not on the trim keep list) and excluded from search (`kb/find.go` `searchable()`) |
| Whether the hub transcript's times can be trusted | `hub_segments.json`: one `timing` field for the whole transcript | Core cannot ask an extension; absent (older transcripts) reads as `spread` |
| A position's attribution | on the position in `minutes.json` | So names, merges and unmerges still work after the transcript expires |

None of this data goes into `meta.json` (rewritten on every upload), the upload's undo journal or Postgres.

### Field contract

`meeting.json` gains a `speakers` block:

```jsonc
"speakers": {
  "state": "ready",            // no block = never separated; waiting | ready | failed | cleared
  "since": "2026-11-26T08:10:03Z",
  "tries": 1,
  "done": 1260.0,              // waiting only: recording seconds separated so far, when reported
  "reason": "",                // failed only: engine_unavailable | no_audio | too_long | timeout | gave_up | audio_gone | error
  "quality": "ok",             // ok | low ("Quality" below)
  "quality_why": "",
  "next": 6,                   // the next new speaker number
  "people": [
    {"id": "s1", "name": "Ada"},
    {"id": "s2", "name": "Grace", "me": true}, // the owner: "me" makes the hub fill in the owner's name
    {"id": "s3"},                              // unnamed: shown as "Speaker 3"
    {"id": "s4", "merged_into": "s1"},         // a merge, which can be undone
    {"id": "s5", "minor": true}                // spoke very little: folded, never used to attribute
  ],
  "stats": {"s1": [720.5, 34], "s2": [410.0, 21]},  // per original id: seconds and lines, last computed; summed per merged person on read
  "edits": {"name": 3, "merge": 1}                   // how many edits of each kind, counts only
}
```

- State, reason and quality are named closed types in `server/internal/recordingwire`, not bare strings, with no `*bool` fields.
- `Meeting.Clone()` copies `speakers` deeply so uploads never drop the speaker list.
- `speakers` uses `meeting.json`'s per-part revision (`speakers`); storing a timeline raises it under the same lock.
- Names come only from the owner (picked or typed), trimmed, on one line, at most 100 characters.
- State transitions:

| Now | Timeline stored | Separation fails or times out | `clear` |
| --- | --- | --- | --- |
| no block | `ready` (through the debug import when no job ran) | — | no change |
| `waiting` | `ready` | first failure: retry after a minute, `tries` + 1; second failure, or the wait limit: `failed` with its reason | `cleared`, the job cancelled |
| `ready` | `ready`, the new timeline, names kept by the pairing rule | — | `cleared` |
| `failed` | `ready` | — | `cleared` |
| `cleared` | refused (`409 speakers_cleared`) | — | no change |

`cleared` is a tombstone: after the owner deletes a meeting's speakers, a late timeline is refused. Writing `failed` first verifies the state is still the same attempt's `waiting`, so it never overwrites a `ready` stored a moment earlier.

`speaker_turns.json`:

```jsonc
{
  "run": "…",                                  // id of this timeline (one per attempt or import)
  "source": "hub",
  "engine": "example-engine 1.0 / model q8 / 10s",
  "covered": [[0, 3412.5]],                     // recording seconds the hub had audio for
  "turns": [[12.30, 18.92, "s1"], [18.40, 25.10, "s2"]],   // may overlap
  "signals": {"slots": 6, "hole_share": 0.02}  // numbers and enums only, used by the quality check
}
```

- **Every time is in recording seconds**: the same clock as `segments[].start`, the minutes' `at` and the audio the hub receives (writer sample count, pauses excluded).
- **The hub numbers speakers.** An incoming timeline carries its own labels (`c1`, `c2`, …); the hub maps them to stable ids (`s1`, `s2`, …). The first timeline numbers them in order of first speech. A later timeline for the same meeting is paired one to one with the previous `turns` by overlap: largest overlap first, each old id taken at most once, and a pair counted only when the overlap is at least half of the new cluster's or of the old id's speaking time. Unpaired clusters get new ids from `next`. Names follow their ids.
- **A merge never changes the timeline**: it is recorded as `merged_into` on `people`, so it can be undone:
  - only naming triggers an automatic merge: giving a speaker the name another speaker already has merges the one who spoke less into the one who spoke more;
  - `merged_into` points only at an unmerged speaker (no chains);
  - an unmerged speaker comes back unnamed so two people with one name do not automatically merge again on the next edit; undoing an automatic merge uses the server's `undo` list to unmerge and restore the old name.
- **Minor speakers.** A speaker with under 20 seconds of speech (absolute, counted per merged person; a starting value) is marked `minor`: folded on the naming screen, their lines still carry their number, and they are never used to attribute a position. Merged into a normal speaker, they stop being `minor`.
- When a recording is shortened, turns past its new length are cut.

`hub_segments.json` carries `timing` for the whole hub transcript: `exact` (the adapter declares the transcription service gives sentence times), `turn` (the built-in adapter's per-reply timing), or `spread` (everything else, and any transcript without the field).

### Aligning lines to speakers

One pure rule is implemented once on the hub and checked against shared fixtures under `spec/fixtures/`:

1. **Layer one (original ids):** for a line `[a, b]`, compute the overlap in seconds with each original id in `turns`. Merges are ignored here.
2. **Layer two (merged people):** sum the overlap of ids merged into the same person and pick the largest. No overlap at all gives no label.
3. **Certainty:** a speaker is sure when the largest person has over 70% of all overlap, at least 1 second of overlap, and is not `minor`. An unsure line still shows its number with a `?` but never attributes a position.
4. **Unreliable times and holes:** a line from the hub (`source: hub`) when the transcript's `timing` is `spread` gets no label. Lines inside a hole of `covered` get no label and are counted apart.
5. **Echo:** lines marked as Persona's echo (`echo: true`) get no label.
6. **Low quality:** when the meeting's `quality` is `low`, every labelled line counts as unsure: numbers show with `?` and none is used to attribute a position.

Layer one is what is stored on minutes positions so a merge or unmerge only redoes layer two. 70% and 1 second are starting values (named constants); the target is at least 90% accuracy on attributed positions while attributing as many as possible within that bound.

**Display names** (one shared rule for hub detail, Markdown and clients), for a position:

| Order | When | Shows |
| --- | --- | --- |
| 1 | a sure `speaker` the owner named | the owner-chosen name |
| 2 | a sure `speaker` not named | "Speaker N" (`Speaker 2` / `发言人 2` / `話者 2`) |
| 3 | the model wrote `who` (named in the transcript, passed `named()`) | `who` |
| 4 | otherwise | no name, no hint |

### Quality

Because nobody manually corrects every line, the hub judges each meeting and records `quality` (`ok` or `low`) with `quality_why`. When `quality` is `low`: the minutes do not attribute positions to speakers (a transcript `who` still shows), the minutes model does not see speaker numbers, and every speaker number in the transcript carries `?`.

Quality is computed under the lock when a timeline is stored (before waking the minutes round) and recomputed whenever the transcript of record is replaced (final transcript stored or a full re-upload). Starting conditions (named constants) that mark a meeting `low`:

- over a fifth (> 20%) of substantive lines (at least 2 seconds, or at least 8 CJK unified ideographs; English lines are judged by duration only) have no label, excluding lines in `covered` holes and lines left unlabelled by alignment rule 4;
- one speaker has over 95% of speaking time while the calendar lists three or more attendees (skipped when there are no calendar attendees);
- under 60 seconds of speech in total, or the timeline covers under half of the recording;
- the calendar lists more than seven attendees.

### Routes

Feature flag `meeting_speakers` appears in WS `hello.features` and trusted `/health` only while the default manager is connected and names `meeting_speakers` beside `meeting_transcription` in its `manager.hello` capabilities (an extension's report cannot add it). With it the hub serves `detail.speakers`, `segments[].speaker` / `speaker_unsure` and the edit route. Engine selection and enablement settings arrive in the later engine phase under their own keys in `GET /api/v1/assistant`; clients show that settings row only when those keys are present.

| Route | Owner | Use |
| --- | --- | --- |
| `POST /api/v1/recordings/{id}/speakers` | recording module | Edit: `{rev, ops: […]}` → `200 {speakers, undo: […]}`; `undo` is the inverse the server computed, sent back as is to undo. `rev` mismatch: `409 speakers_stale` |
| `GET /api/v1/kb/recordings/{id}` (existing) | Core | Adds `detail.speakers` (below); each `detail.segments[]` gains optional `speaker` and `speaker_unsure`. Served with the recording module off too |
| `meeting_live` event (existing) | | `changed` and `revs` gain `speakers` |
| `PUT /api/v1/recordings/{id}/speakers/turns` | recording module | **Debug only; no client uses it.** Answers `404` unless the hub machine's `manager.json` enables `kb.speaker_turns_import` (off by default, read at start; no app or `PUT /api/v1/assistant` can change it). Full credentials only. Body `{run, engine, covered, turns, source, signals?}`; the same `run` with the same body gives the same answer, with a different body `409 op_id_reused`; at most 20000 turns and 2 MiB. Imports a timeline into a recorded meeting whose transcript is kept |

`detail.speakers`:

```jsonc
{
  "rev": 7, "state": "ready", "reason": "", "quality": "ok", "source": "hub",
  "people": [{"id": "s1", "name": "Ada", "seconds": 720.5, "lines": 34, "samples": [312.0, 1504.5]},
             {"id": "s2", "name": "Grace", "me": true, "seconds": 410.0, "lines": 21},
             {"id": "s4", "merged_into": "s1"},
             {"id": "s5", "minor": true, "seconds": 11.2, "lines": 3}],
  "candidates": ["Ada", "Grace"],
  "spans": [[12.30, 18.92, "s1"], [18.40, 25.10, "s2"]],   // original ids; merges are in people
  "covered": [[0, 3412.5]],
  "trimmed": false   // true after the transcript expired: spans empty; name, merge, unmerge and clear still work
}
```

- While `state` is `waiting`, `detail.speakers` adds `done` and `end` (seconds separated, total seconds) for progress.
- `samples` holds the start times of two or three of the person's longest sure lines.
- `candidates` (computed on read, names only, never matched by voice) combines the owner ("me" is a separate choice), calendar attendees, names heard in the minutes (`who`), and names used in earlier meetings with overlapping calendar attendees or the same title.

Edit operations (only these four):

```jsonc
{"op": "name",    "id": "s2", "name": "Ada"}   // empty name clears it; "me": true marks the owner
{"op": "merge",   "from": "s4", "into": "s1"}   // the hub also does this automatically when two get one name
{"op": "unmerge", "id": "s4"}
{"op": "clear"}                                 // deletes timeline and list, state cleared, running job cancelled; cannot be undone
```

Errors reuse the existing recording codes (`400` for an invalid body, unknown op, unknown speaker, or a name that is multiline or over 100 characters; `404`; `410` deleted; `403 forbidden` without full credentials; `403 kb_read_only`; `409 not_meeting` for a non-meeting, track recording or device-mode meeting; `413 too_large` over the body or 20000-turn limit) plus `409 speakers_stale`, `409 speakers_cleared` and `409 op_id_reused`. Write routes require full credentials only and stay out of `extwire.RouteScopes`.

**Exposure.** `detail.segments` is part of the extension KB contract (`schema/extension/kb.schema.json`, `sdk/go`, `sdk/ts`): extensions with `kb.read` see speaker numbers and names alongside the transcript and minutes they already read.

### The minutes

Positions in `debates` gain attribution fields beside `stance`, `at` and `who`:

```jsonc
{"stance": "Per recording: easier to explain",
 "at": 340.5, "span": [340.5, 351.0], "over": {"s2": 3.1, "s1": 2.8}, "said_by": "s2",
 "speaker": "s2", "speaker_unsure": true,
 "also": [{"at": 402.0, "span": [402.0, 409.1], "over": {"s3": 6.2}, "said_by": "s3", "speaker": "s3"}],
 "who": "Grace"}
```

- `span`: the cited line's start and end seconds for alignment; `at` stays the jump target and never changes. `est: true` marks a line whose time is unreliable (alignment rule 4); it is not attributed. `over`: layer-one overlap seconds per original id. `speaker` / `speaker_unsure`: layer two plus cross-checks, ready for clients. `said_by`: the model's `holder`. `also`: up to two further lines where others back the same position. `who` keeps its meaning and string type.
- **What the model sees.** With a timeline and `quality` `ok`, a transcript line `[12] 10:03:15 text` becomes `[12] 10:03:15 〔S2〕 text`, or `〔S1?〕` when unsure. Only anonymous numbers are shown — never owner-chosen names and never which speaker is the owner; `named()` is unchanged. The prompt chooses between two wordings depending on whether a timeline is present, and chunking weights include the marker. With numbers, the prompt gives three rules: numbers are machine-separated and may be wrong (`?` more so); never write a speaker number (`S2`, `Speaker 2`) into any text field; for each position also give `holder`, the number of who holds it (usually the cited line's number, differing when someone else restates it). The model output gains `holder` and `also: [{line, holder}]` on positions: `holder` is read as `S` plus digits, lowercased (anything else counts as missing), and `also` is checked and merged on long meetings like the main citation (its lines join `notedLines`).
- **Attribution at store time.** Computed under the lock when minutes are stored (not outside the lock in `buildMinutes`), for every cited line (main and `also`): store `at`, `span` and `est` from the lines the generation read; compute `over` from the current timeline; compute `speaker` and `speaker_unsure`. Cross-checks — any failing marks the attribution unsure:
  - the model's `holder` (`said_by`) differs from the computed speaker;
  - the same person appears on two positions of the same disagreement (`also` included);
  - the model wrote `who`, the computed speaker has an owner-chosen name, and the two differ.
  When the model saw no numbers (a late timeline), `said_by` is absent and the first check is skipped. Ordinary points, summary bullets and action items get no speaker; their `who` keeps its existing rule. Minutes written without `span` get no attribution until regenerated.
- **After a name, merge, unmerge or new timeline.** Recompute attribution (transcript kept: from the stored `span` and the current timeline; transcript trimmed: from the stored `over` and current merges), re-render the Markdown, write `minutes.json` and `minutes.md`, and emit `meeting_live` with `speakers` plus the recording's `summary` notification (as confirming to-dos does) so clients that do not read the new keys reload the minutes. No model call, no push, and `minutes.version` stays unchanged so pending to-do confirmations are not rejected. A trimmed recording uses an explicit re-render path that reads only the stored position data and speaker list, without reading a transcript ("Privacy" below).
- **A timeline later than the minutes** (including one arriving while minutes are generating): applies the same attribution update immediately, with no model call, no push, unchanged `minutes.version` and no silent full regeneration. A generation already in progress computes attribution under the same lock when it stores.
- **The gate.** `SpeakersHolds` in `recordingwire/rules.go`, beside `TranscriptionHolds`: while `speakers.state` is `waiting`, hub transcription has ended (`final` or `failed`) and the wait limit has not been reached, minutes generation waits. Both "may the minutes start" checks use it. The wait limit is the later of 10 minutes after the final transcript was stored and "audio length ÷ 5 + 2 minutes" after `since`; a restart does not extend it. At the limit, after checking the state is still the same attempt's `waiting`, the hub marks `speakers.state` `failed` with reason `timeout` and minutes proceed without numbers. Storing a timeline wakes the minutes round. Audio deletion is unchanged because it already waits until minutes are written or will not be written, downstream of this gate.
- **Markdown.** Disagreements lines put the display name in parentheses (`(Ada)`, `(Speaker 3)`, listing each attributed speaker when `also` adds more) by the display-name table above. Without a timeline, both layouts render byte for byte as before.
- **Unchanged surfaces.** `transcript.md`, the live log, in-meeting batches, highlights and the material read by later meetings never carry a name derived from the speaker timeline.

### Compatibility

| Combination | Result |
| --- | --- |
| New client, older hub | No `meeting_speakers`, no `detail.speakers`; behaves as before |
| Older client, this hub | Speakers and names appear in the minutes' Markdown (Copy, Android, HarmonyOS); older apps show no labels in the transcript |
| Rolled back to an older hub | The older hub's first minutes rewrite drops disagreements and attribution; its first `meeting.json` write for a meeting drops that meeting's speaker list; its search indexes `speaker_turns.json` (numbers and ids only). Back on this hub: regenerating brings back disagreements where the transcript is kept; a lost timeline cannot be rebuilt once hub audio is gone; lost names must be given again |
| Recording module off | `detail.speakers` and names still show (served by Core); speaker write routes answer `404` |

## Privacy

Transcription stays on device, only text is uploaded, audio never leaves the phone, and the phone deletes its recordings after `recording_audio_days` (default 14), while the hub keeps their text for `recording_text_days` (forever by default, #16412). A meeting recorded in hub mode (#3862, chosen per meeting) is the exception: "Privacy per mode" below. In a meeting call, what is said while the user holds Talk goes to the realtime voice service in either mode (#3862, "Decided"). In a participant meeting, the text of an answer said aloud goes from the hub's voice extension to the realtime voice service the owner set up, to be said in its voice (#5004); without that service it is said by the phone's on-device voice.

Minutes are kept at least as long as their transcript (#3780). A recording's text (transcript, speaker timeline `speaker_turns.json`, summary, live log) stays for `recording_text_days`: **forever by default** (#16412), otherwise 1–3650 days. Past a finite one the hub **trims** a recording that has minutes instead of deleting it: its transcript (`segments.json`, `transcript.md`), its speaker timeline (`speaker_turns.json`), the summary made from it, the live log and any undo journal are deleted, and only `meta.json` (now with `trimmed_at`), `meeting.json` (keeping the speaker list and names), `minutes.json` and `minutes.md` stay, according to the assistant setting `minutes_days`: **forever by default** (unset or `0`), otherwise 1–3650 days after the meeting (#3862). A recording without minutes, or one past a finite `minutes_days`, is deleted entirely as before. With forever, retention never deletes trimmed minutes; the user’s delete still removes everything. `GET /api/v1/assistant` returns `0` for forever, otherwise `max(minutes_days, recording_text_days)`, and `0` while the text is kept forever. `PUT` accepts `0` or an integer from 1–3650 (`400` otherwise). `assistant.minutes_days` in `persona_settings` accepts `0` or `"forever"`, shows the value in words, and confirms changes before applying them. A positive value below `recording_text_days` (or any value while the text is kept forever) is saved as asked for, by both, and counts once `recording_text_days` is shorter. Before #16412 transcripts followed `recording_days` (14 by default), which is now the phone's audio window (`recording_audio_days`); a saved value of it is read as that window, so the upgrade deletes no text. The trim writes `meta.json` first; files a trim cut short leaves (a crash, a failed removal) go on the next sweep, also once a longer `recording_text_days` covers the recording again.

A trimmed recording:

- Is still returned by `GET /api/v1/kb/recordings/{id}` with `detail.meta` (`trimmed_at` included, `minutes_state` `ready`), `detail.minutes`, no `segments` and no `summary`; `kb_get` prints the minutes and says the transcript expired; `kb_search` still finds `minutes.md`; earlier-meeting retrieval remains available in code but is disabled for in-meeting answers until a private-meeting mark exists (#3862).
- Still turns confirmed action items into to-dos (`POST …/minutes/todos`), but cannot regenerate them: `POST …/minutes` is `409 transcript_expired`.
- Still lets the owner name, merge, unmerge and clear speakers (`POST /api/v1/recordings/{id}/speakers`): `detail.speakers` returns `trimmed: true` with empty `spans`, and the minutes re-render from the attribution data stored on each position without reading a transcript ("Speakers (#17674)").
- Takes no upload: `PUT /api/v1/recordings/{id}` answers `expired` (the hub never takes a transcript back, even when `recording_text_days` grows again or becomes forever). Its attendance is turned off with the live log gone (its lines, asks, topics and highlights with it: `GET …/live` reads them empty, their revisions 0, "The `meeting_live` event"), and no live log is written to it again, not even by a write that read the log before the trim.
- Stays in reconciliation (`GET /api/v1/recordings`) at its revision with `trimmed_at`, while `days` stays the audio window (`recording_audio_days`). A phone deletes its own copy at `days` and never uploads one that old; one that still holds it sees the hub has that revision and sends nothing, so the phone contract is unchanged.
- Is deleted entirely by the user's delete, which leaves a tombstone as before. Tombstones keep the `recording_audio_days` window (forever with it): they only refuse late uploads, and no phone uploads a recording older than that window.

Attending a meeting (below, off by default) also puts the meeting's lines into the chosen session's conversation. Those copies stay in that conversation and do not expire with `recording_days`.

### Decided: cloud transcription and speaker separation

Decided across #3862 and #17674:

- **Hub mode** ("Meeting conversation (#3862)", phase 5) transcribes on the hub with a GPT-Live-compatible live model, and separates speakers locally on the hub from the meeting audio the hub already holds ("Speakers (#17674)"). Only the timeline of who spoke when and the names the owner chooses are kept; no voice features are stored and cloud speaker diarization is not used.
- **Privacy mode** (`device`) transcribes on the device, never uploads meeting audio, and has no speaker separation: minutes name a speaker only when the words themselves or the calendar support it (`who`).

## Attending a meeting (#2981)

Persona can attend a recorded meeting while it happens, not only write minutes after it. This is the first version, kept for phones from before #3862: newer phones use "Meeting conversation (#3862)" below on a hub that lists `meeting_conversation`, and a recording whose upload does not ask for a room conversation still follows this section. The owner decided four things in #2981:

1. **The role is chosen each time a recording starts**: `off`, `listener` or `participant`. A global default (`meeting_attendance_default`, below) pre-selects it.
2. **One session attends, chosen per recording**: the main chat, another Persona chat or an Agent. It is not fixed to the main chat.
3. **In front of others Persona says only this meeting’s material** until the private-meeting mark exists (#3862): its title, calendar hint, live lines and earlier asks. Earlier meetings, their projects and their action-item to-do states are excluded together. Never private chats, mail, health or other personal content. The hub enforces this by what it gives the answering model ("What the hub gives the answering model" below), not by the prompt.
4. **Tap to ask.** No wake phrase.

| Role | During the meeting | Answers |
| --- | --- | --- |
| `off` (default) | Nothing new: the recording is uploaded as before and gets minutes afterwards | None |
| `listener` | The phone streams finished sentences to the hub, which feeds them to the attending session; the session keeps running notes | On the phone's screen only |
| `participant` | The same as listener | On screen, and the phone says them aloud |

### Attending, step by step

1. The user starts a meeting recording. If the hub lists the `meeting_attendance` feature, the phone offers the role (pre-selected from `meeting_attendance_default`) and the attending session.
2. With a role other than `off`, the phone uploads the recording at once, before it has any text (`PUT /api/v1/recordings/{id}` with `attendance`, `attending_session`, `segments: []`, `complete: false`), so the hub knows the role. Afterwards it uploads exactly as before; every full-state upload carries the same two fields.
3. As sentences are recognized, the phone appends them with `POST /api/v1/recordings/{id}/live`. They are append-only and resume after a reconnect (`next`).
4. The hub feeds the new lines to the attending session in batches. The session keeps running notes in its own conversation, where the user can open them.
5. The user taps to ask (`POST /api/v1/recordings/{id}/ask`), with a typed question or, from the Live Activity, with none: then the question is what was just said in the room. The hub answers from meeting material only. The phone shows the answer and, for a participant, says it aloud.
6. The recording ends and completes as before, and gets minutes as before. A meeting that completes with no text at all gets no minutes (`minutes_state` absent).

### Attendance fields

All of it is optional and additive. A phone that sends none of it, and a hub without the `meeting_attendance` feature, behave exactly as before. Characters are counted as Unicode code points (Go runes, Swift `unicodeScalars.count`).

**The feature flag.** The hub lists `meeting_attendance` in WS `hello.features` and trusted `/health` only while the default manager (the one recording routes go to) is connected and names the capability `meeting_attendance` in its `manager.hello`, as with `agent_session_archives` (protocol.md "Health check and feature flags"); an extension's report cannot add it. It means that manager takes everything in this section: an older hub or manager accepts the upload fields silently and does nothing with them. The flag only decides whether the phone offers a role when a recording starts, from the last `hello` it saw. A recording that attends keeps its role whatever later `hello`s say; the phone stops attending it only on a `404` without `recording_not_found` from `…/live` or `…/ask`, or an upload `200` with no `attendance` (an older manager took it). A `503` is retried with backoff.

**On `PUT /api/v1/recordings/{id}`:**

```jsonc
{
  "kind": "meeting",
  "attendance": "participant",       // optional: "off" (default), "listener" or "participant"
  "attending_session": {"machine_id": "<machine id>", "session_id": "<session id>"}  // optional: absent = the main chat
}
```

- `attending_session` names one session the way the rest of the API does. Absent, `null`, or both ids empty means the main chat, as it is at each batch. Each id is at most 200 characters; other keys are ignored.
- **A problem with these two fields never fails the upload**, since the same request carries the transcript. The transcript is stored as usual and the attendance is stored as `off` when `attendance` is not one of the three values (`attendance_problem: "unknown_role"`), when it is not `off` on a recording whose `kind` is not `meeting` (`"not_meeting"`), or when `attending_session` has exactly one id or one over 200 characters (`"bad_session"`). The `200` response then adds `"attendance"` (the role in effect) and `"attendance_problem"`. On a hub with the feature, every `200` carries `attendance`.
- Like `kind` and `calendar`, both fields travel with the full-state upload: a later upload that omits `attendance` turns attending off, and one that names another session moves the meeting to it.
- `GET /api/v1/kb/recordings/{id}` returns them in `detail.meta`: `attendance` (`listener` or `participant`; absent when off) and `attending_session` (as stored; absent for the main chat).
- Watch recordings, also those the phone tags as meetings, and notes never carry a role.
- A meeting that completes with no text gets no minutes: `minutes_state` is absent rather than `pending`.

**The default role** is an assistant setting next to `recording_days`: `GET /api/v1/assistant` returns `meeting_attendance_default` (`off` until changed, `listener` or `participant`). `PUT /api/v1/assistant {"meeting_attendance_default": "listener"}` changes it; another value is `400`. `assistant.meeting_attendance_default` in `persona_settings` reads and changes it too. There is no default attending session, and the main chat is never one by default (owner decision in #3862, 2026-11-12: every line a meeting feeds a chat fills that chat's context, so it reaches the main chat only by the owner's choice): the phone pre-selects the last-picked session that is not the main chat, else Persona stays off until the owner picks; the main chat attends only when picked for that meeting. A phone never sends an attending role without its target: with no session chosen it sends `off`, since a role without `attending_session` means the main chat.

**Live sentences:** `POST /api/v1/recordings/{id}/live`

```jsonc
{
  "from": 12,                                                    // required: position of segments[0] in the phone's live log
  "segments": [{"start": 61.2, "end": 64.0, "text": "Let's keep the cap for this release."}]  // finished sentences, in order; may be empty
}
```

`200`:

```jsonc
{
  "next": 13,                    // how many positions the hub holds: send from here next time
  "attendance": "participant",
  "attending_session": {"machine_id": "<machine id>", "session_id": "<session id>", "main": true},  // the session being fed; absent when there is none
  "fed": 9,                      // how many positions the session has been given at least once (fed ≤ next)
  "problem": "attendee_unavailable"  // optional, see below
}
```

- **Positions belong to the phone's own append-only live log**, not to the transcript's sentences: the phone appends each finished sentence it sends, keeps the log with the recording and never renumbers it. A re-transcription, or Persona's echo kept out of the log ("Persona's echo" below), does not change positions already sent. Only a sentence that ends while the role is `listener` or `participant` enters the log: what is said while attending is `off`, before it was turned on included, is never sent. Positions below `next` are already stored and skipped and the rest appended, so a replay or a retry after a lost response stores nothing twice. An empty `segments` only asks for `next`, for example after a reconnect.
- `start` / `end` are seconds from the start of the recording, the same clock as the upload's `segments`. Only finished sentences: one the recognizer is still revising is sent once final. **A bad sentence never fails the request**, since positions must stay contiguous: a negative `start` becomes 0, an `end` before `start` becomes `start`, text over 4000 bytes is cut at a character boundary, and an empty text holds its position but is never fed.
- Errors, checked in this order: `503` (`kb_disabled`, `kb_unavailable`, `assistant_offline`) as for the other recording routes, retried with backoff. `413 too_large`: a body over 1 MiB. `410 recording_deleted`: stop attending; recording sync handles the rest. `404` with `code: "recording_not_found"`: no upload of this recording yet; upload it (with no text if need be), then send from `next`. A `404` without that code means the route is missing (an older manager): stop attending this recording and leave recording sync alone. `403 kb_read_only` while the manager's knowledge base is read-only. `400`: `from` missing, negative or not an integer, more than 200 segments, or a time that is not a number. `409 not_attending`: the stored upload has `attendance` `off`. `409 live_gap`: `from` is above `next`; the body is `{"error", "code": "live_gap", "next": 13}`; send again from `next`. `413 live_full`: more than 20000 positions or 4 MB of live text for one recording; stop sending live lines for it (uploads and asks are unaffected, and `problem` reads `live_full`).
- The phone sends no `…/live` or `…/ask` for a recording until an upload carrying its current `attendance` and `attending_session` has returned `200`; after a change of role or session it sends that upload first. Until it lands the hub keeps the previous role.
- Live sentences are not the transcript. The upload still owns it, and the minutes are made from the uploaded transcript as before. Live sentences only feed the attending session and the answers.
- `problem` is an open set; a client shows a value it does not know as "Persona can't follow this meeting right now". Today: `attendee_unavailable` (the session cannot take messages now: gone, closed, its computer offline, or no main chat; lines keep arriving and are fed once it can), `attendee_not_allowed` (the hub will not give this session other people's words, "Feeding the attending session" below; asks still work), `live_full`. `fed` below `next` for a while with no `problem` means the session is busy; the phone may show "catching up".

`GET /api/v1/recordings/{id}/live` returns the same object plus `asks` (every question of this meeting, oldest first), on a read-only knowledge base too. With `attendance` `off` it is `{"attendance": "off", "next", "fed", "asks"}`. `410 recording_deleted`, `404 recording_not_found`. The meeting conversation adds parts to it, which a #2981 phone ignores: `revs` and `parts` (phase 1), `topics` and, with `?from=`, `lines` (phase 2; "Meeting conversation (#3862)" below).

**Asking:** `POST /api/v1/recordings/{id}/ask` (the path's `{id}` is the recording; the body's `id` is the question's)

```jsonc
{
  "id": "<UUID>",            // required: the phone's id for this question; a retry sends the same one
  "question": "What did we decide about the export cap last time?",  // optional: typed; absent = what was just said
  "at": 1834.2,              // seconds from the start of the recording when the user tapped; required without question
  "live_next": 57            // optional: the phone's live-log count once the lines up to the tap are sent
}
```

- **Without `question`** (a tap on the Live Activity, or the user tapping while someone in the room asks), the question is what was said in the 45 seconds before `at`: every live sentence that overlaps them, one still being said at the tap included. The phone first cuts its open audio segment at the tap, transcribes it and sends its lines with `…/live`, then asks with `live_next` set to its live-log count after them; the hub waits (at most 15 seconds) until it holds that many positions. Without `live_next` it does not wait. The returned `question` is the question the hub understood, and `question_source` says `heard`.
- The hub holds the request at most 20 seconds: `200 {"ask": Ask}` once answered, else `202 {"ask": Ask}` with `state: "pending"` while the answer keeps running. The phone uses a request timeout of at least 30 seconds. On a `202`, a timeout, a `5xx` or a dropped connection it sends the same request again (same `id`) a few seconds later, or reads `asks` from `GET …/live`.
- The same `id` never makes a second answer: a retry returns the stored ask, or waits for it while it runs. It is matched on the request as sent: the same `id` with another `question` (two requests without one match each other) or another `at` is `409 op_id_reused`; a heard ask's understood question is never compared. Ids are case-insensitive, stored and returned lowercase; compare them case-insensitively.
- **A request whose `id` is already stored is answered from that ask**, right after the `404` / `410` checks: `200`, or `202` after waiting up to 20 seconds while it runs. It never gets `not_attending`, `asks_full`, `ask_busy` or `kb_read_only`. Only a `failed` ask asked again goes through those checks, and is then tried again.

`Ask`:

```jsonc
{
  "id": "<uuid, lowercase>",
  "question": "What did we decide about the export cap last time?",
  "question_source": "typed",             // typed | heard
  "at": 1834.2,                           // omitted when not sent
  "state": "answered",                    // pending | answered | failed
  "answer": "At the design review on November 3 you kept the 4 MB cap for this release. Bo owns the regression test; it is still open.",
  "lang": "en",                           // BCP 47: the language the answer is written in
  "speak": true,                          // participant: say it aloud; listener: screen only
  "sources": [{"kind": "recordings", "id": "<recording id>", "name": "Design review", "when": "2026-11-03 14:00–15:10",
               "start": "2026-11-03T14:00:00+08:00", "end": "2026-11-03T15:10:00+08:00"}],
  "error": "model_unavailable",           // only when failed
  "asked_at": "2026-11-10T07:12:03Z",
  "answered_at": "2026-11-10T07:12:09Z"   // omitted until answered
}
```

- `answer` is plain text, at most 400 characters, a few short sentences that read well aloud: no Markdown, no links. When the material does not hold the answer it says so instead of guessing. `answer` is `""` and `sources` `[]` unless `state` is `answered`.
- `lang` is the recording's `language`, else the request's language. The phone says the answer in the realtime voice when the hub offers it (the voice extension's `speak`, #5004, [voice.md](./voice.md)), with `lang` as its hint, else with an on-device voice for `lang`.
- `speak` is fixed when the ask is made, from the role then. The phone says an answer aloud at most once, only for an ask it made in this run, only while the recording runs and only when the answer arrives within 60 seconds of the tap; otherwise it only shows it.
- `sources` are the earlier meetings (`kind: "recordings"`) and projects (`kind: "projects"`) the answer drew on; each opens with `GET /api/v1/kb/{kind}/{id}`. `when`, `start` and `end` are the earlier meeting's minutes' (clients format `start` / `end` and show `when` only without them, as for the minutes); absent for projects, and `start` / `end` absent in answers from before #4713.
- `asked_at` / `answered_at` are RFC 3339 in UTC.
- `error` (open set; unknown values are shown as a generic failure): `model_unavailable` (no model configured, or paused), `model_error`, `model_timeout` (no answer within 3 minutes), `interrupted` (the hub restarted while answering; ask again with the same `id`), `nothing_heard` (no question and no live sentence in the 45 seconds before `at`).
- Errors, checked in this order: `503` as above, `413 too_large` (a body over 64 KiB), `410 recording_deleted`, `404 recording_not_found`, `400` (`id` missing or not a UUID, a `question` over 1000 characters, or neither `question` nor `at`), then a stored `id` as above (`409 op_id_reused` when it does not match), `403 kb_read_only` (no model call is made then), `409 not_attending`, `409 asks_full` (100 questions in this meeting already), `429 ask_busy` (two of its questions are being answered now; no `Retry-After`: ask again once one is answered).

**Who may call:** `…/live` (both methods), `…/ask` and changes to `meeting_attendance_default` take full credentials only. The watch's answer-only token and extension tokens never reach them (they stay out of `extwire.RouteScopes`), and the manager answers `403 forbidden` to any other role.

### What the hub gives the answering model

Everything in the answer's input may end up said aloud, and the question and the live sentences come from people in the room, who can ask for anything in it. So the boundary is what the hub puts there, not the ranking or the prompt. An answer is one model call, purpose `meeting.answer` (a use of Information processing, `kb`, [model-config.md](./model-config.md)), with **no tools**. It stays there when the In-meeting conversation is set apart (#7980): it answers when the room cannot, so it must not depend on the room's launcher or computer. Until a meeting can be marked private (#3862 phase 1), its input contains **only this meeting**: the upload’s title and calendar hint, live lines and earlier asks. `kb.Engine.EarlierMeetingsInAnswers` is false in production and gates earlier meetings, their projects and their action-item to-do states together. No earlier-meeting lookup or to-do-state lookup runs for an answer. The retrieval code and its opt-in tests stay ready for phase 1 to enable eligible material per meeting with the mark.

The current source (1) and the future, gated sources (2–4) are:

1. **This meeting**: the upload's `title` and `calendar` hint (never the knowledge base's matched calendar item), its live sentences so far (the latest 12000 characters), and the questions already asked and answered in it.
2. **Earlier meetings (disabled for now)** still in the knowledge base (minutes outlive a finite `recording_text_days`, forever by default): recordings whose stored `kind` is `meeting` or whose minutes the user asked for (`minutes_asked`), never one that got minutes only because a calendar item overlapped, and never ordinary notes. From each, its minutes only: title, when, attendees, points, decisions, action items (`who`, `what`, `by`, `due_date`) and open questions. Never its transcript, summary, live sentences or asks. At most 6, ranked by the question, this meeting's title and attendees, and recency.
3. **Projects** those earlier meetings are linked to: the name and the status (`active`, `paused`, `done`) only. What was said about a project comes from those meetings' minutes (2). Never a project's goal, summary, people, chats, Moments or timeline, which are distilled from chats.
4. **To-dos** made from those meetings' action items: only whether each is open or done. What is said about it is the action's own `who`, `what` and `due_date`; never the to-do's current title, notes or thread.

The system prompt is one fixed template that takes only the owner's name, today's date and the answer language: never `assistant.notes`, memory, pinned facts or the live snapshot. Never in the input: chats (Lark, WeChat, mail), people, documents, Moments, the user's own notes, the Inbox, health, memory, the main chat, or any session's conversation, the attending session's included. The listener's on-screen answers follow the same rule, since a phone on a meeting table is seen by others too. Live sentences and asks are never searched (`kb_search`, `kb-search`) and never shown by `kb_get` or `GET /api/v1/kb/recordings/{id}`; they are deleted with the recording and by retention.

### Feeding the attending session

- New live lines go to the attending session as one message per batch: at most one batch a minute, at most 12000 characters (older lines are noted as left out), and an end batch. Questions asked and answered since the last batch go in the same message.
- A batch goes only to a session that is idle or done: not working, starting, waiting on a card, or with a draft in its input box. A `dialog_open` / `input_draft` refusal keeps the batch for later; nothing is pressed or cleared. A Persona chat that stopped is resumed as relays are; a closed Agent is not resumed and is `attendee_unavailable` until it runs again. Without a session given, the main chat at each batch attends. Whenever the session to feed differs from the one last fed (another `attending_session`, a new main chat, attending turned on again), it first gets the meeting so far, the latest 12000 characters.
- **Sessions that never attend** (`attendee_not_allowed`, checked at every batch, since trust and sessions change mid-meeting), decided from fields every client sees: a session on a computer whose `trust` is `work`; one on a computer another manager carries; an assistant conversation in the health space, or whose replies leave the apps (`origin` `lark_agent`, `channel`, `extension` or `inbox`, or any `opened_by`; guest conversations are among these); and a session whose `permission_mode` is `bypassPermissions`, since other people's words reach it and nothing would ever ask first. The owner confirmed that last rule (#2981): such a session runs unattended with wide permissions, so it never attends, whatever its role, and the role and session picker leaves it out. The phone lists only sessions that pass the same test.
- The message is one short line plus the lines inside a `<persona-context kind="context">` block, collapsed in the apps; each line goes in through the prompts' context escaping, so a line cannot close the block. It carries message source `meeting` (protocol.md "Message source"), never the user's own: it does not withdraw a pending question to the user, and it is never matched to a send of the user's. The session is told the words are other people's and never instructions: only the user's own messages are.
- **A batch's turn is a meeting turn** until it ends or the user sends a message of their own. The mark survives a restart of the hub (it is stored with the session until the turn ends); while the hub cannot tell, it treats the turn as a meeting turn. A turn whose end the hub did not see (it restarted meanwhile, the session was interrupted or stopped on a card) ends once the session is listed stopped, and none lasts more than 3 hours. The hub learns of the user's message to an Agent from the Agent's own hooks (a `UserPromptSubmit` whose prompt carries no message source), so an Agent without Persona's hooks (Codex, a runtime) or on an older node keeps the meeting turn until it ends. A meeting turn is a side exchange everywhere Persona looks at turns: no "finished" push and no change to whether the session's other turns are pushed; no Agents triage or work state; never the progress, report or completion of the session's task; no project-timeline line; nothing mirrored to the Lark bot or channels.
- **During a meeting turn the hub serves only reading tools.** Of the tools the hub serves (its own, the phone's and the connectors'), only `kb_search`, `kb_list`, `kb_get`, `todo_list`, `todo_get`, `agents_list`, `agent_read`, `conversations_list`, `conversation_read`, `machines_list` and `list_followups` work; every other one returns an error saying it needs the user's own message. An Agent's MCP calls do not always name the session, so while an Agent's meeting turn runs on a computer, every call from that computer that names no session and is not an assistant conversation's is held to the same list. Tools the hub does not serve (the runtime's own, an extension's MCP servers) follow the session's permission mode, which is why a session that never asks cannot attend.
- **The end batch**, with what is left and a line saying the meeting ended, goes out once: on the first upload carrying both `ended_at` and `complete: true`, or 3 minutes after the first upload carrying `ended_at`, whichever comes first. Live lines that arrive before then are part of it; later changes of `attendance` or `attending_session` are stored but feed nothing. Turning attending `off` mid-meeting keeps the live lines and asks; nothing is fed while it is off.
- `fed` counts positions given at least once; after a hub restart a batch may be given again.

### What the phone does (#3375)

- Offers the role and the attending session when a meeting recording starts, only when the hub lists `meeting_attendance`, with the role pre-selected from `meeting_attendance_default`. Starts without a picker (Control Center, Siri, the Action button, the drawer's one-tap row) use the default role with the last-picked session that is not the main chat, else start with Persona off: never the main chat by default (owner decision in #3862, 2026-11-12); the main chat attends only when picked for that meeting, and turning Persona on mid-meeting with nobody chosen asks who attends first. The Live Activity shows the role at once with a way to turn it off. Settings changes the default with `PUT /api/v1/assistant`.
- Lists as attending sessions: the main chat (sent by omitting `attending_session`, never by its id); the other conversations of `GET /api/v1/assistant` that are not archived; and the Agent sessions of `GET /api/v1/sessions` that take messages; in each case only those that pass "Sessions that never attend" below.
- Uploads an attending recording at once, ahead of other pending uploads and outside the save debounce, then streams finished sentences with `…/live`, resuming from `next` after a reconnect or a `409 live_gap`.
- Shows that an AI is attending, on the recording screen and in the Live Activity, for as long as the role is not `off`.
- Asks with a tap: typed on the recording screen, or with no text from the Live Activity. Shows the answer (on the recording screen, and the latest one in the Live Activity; the hub posts no notification for answers), and in a participant meeting says it aloud through the voice route (headset, else the loudspeaker): in the voice of an extension's `speech` contribution when one runs and reads (#8712, [extensions.md](../extensions/extensions.md) "Speech": the built-in Volcengine speech's); else in the realtime voice of voice chats, with the model and voice the owner uses, when the hub's voice extension lists `speak` (#5004); in the phone's on-device voice when neither does, it fails or hasn't started within 8 seconds.
- Keeps Persona's echo out of the transcript and the live log (required): a sentence that overlaps a span when the phone was speaking and says the words Persona said there ("Persona's echo" below). It stays on the recording, marked, and goes up marked to a hub that lists `meeting_echo`. Echo cancellation is optional, set when a recording starts as `participant`. Turning it on when the speaker turns on mid-recording is an iPhone experiment behind Settings › Meetings › Experimental (#8731 B2, off by default): the microphone stops for a moment, and it stays on to the end. Persona's answers stay on the hub as `asks`, not as transcript lines.

### Persona's echo (#5905)

When Persona says an answer out loud in a meeting (`participant`), the recording's microphone hears it too, and the transcript cannot tell voices apart. Echo cancellation removes most of it; what gets through is Persona's echo. Until #5905 every sentence at least half inside a span when Persona spoke was dropped, so whatever anyone said meanwhile was lost too: someone asking it to stop, people talking over it, a reply that starts at its last words.

- **The rule.** A transcript line is Persona's echo only when, for one span when Persona spoke, both hold:
  - **time**: it overlaps the span widened by 0.5 s on each side (`line.end >= span.start - 0.5` and `line.start <= span.end + 0.5`; a line whose end is not after its start, when its start is inside the widened span);
  - **words**: they are what Persona said in that span. Both texts are normalized (Unicode NFKC, lower case, letters and decimal digits only: punctuation, spaces, symbols and marks go), and at least 60% of the line's distinct character bigrams are among the answer's. A line of one character after normalizing is echo only when the answer holds that character; a line with nothing left is never echo.

  Anything else said during an answer stays the meeting's, and so does a line where the echo and someone's words were transcribed together, unless Persona's words make 60% of it. Character bigrams work alike for Chinese, Japanese, English and mixed lines. The margin (0.5 s) and the threshold (0.6) are starting values, to be calibrated against labelled recordings. The phone (`apple/Shared/Recording/MeetingEcho.swift`) and the hub (`modules/recording/writers/echo.go`) run the same cases, `spec/fixtures/meeting-echo/`.
- **A span saved before #5905** has no text: it keeps the old time rule (at least half of the line inside the span, no margin; a line of no length, when it starts inside), so older recordings read as they did.
- **Kept and marked, never deleted.** The phone keeps every line with the recording. Echo lines are hidden wherever the transcript is shown (the transcript, the live box, the timeline, Talk about this meeting, the minutes' jumps), and never enter the live log (checked against the span being said too, which carries the answer), so the attending session, the room and the Live Activity never get them; `…/live` is unchanged.
- **The upload** to a hub that lists `meeting_echo` carries every non-blank line, echo lines with `"echo": true`, and the meeting's spans as `"spoken": [{"start", "end", "text"?}]` (the answer said in each; the one being said included, checked against the answer as a whole). The hub marks a segment echo when the phone did or when the same rule says so against `spoken`. It keeps echo lines in `segments.json` and `detail.segments` (`echo: true`) and marks them in `transcript.md` (`[Persona 回声]`), and leaves them out of the minutes (their line numbers still count every segment, so citations stay right), the recording's summary, the follow-up's seed and `chars`: a meeting whose only words are Persona's gets no minutes. At most 1000 spans; a text over 4000 bytes is cut at a character boundary; a `spoken` that is not a list or is over the limit, and a malformed span, are ignored and never fail the upload. An all-day recording has no echo (Persona never speaks in one): its PUT and its appends (`POST …/segments`) store every line unmarked. The web's and Android's knowledge views hide echo lines too, so a time in the minutes never opens one; an extension reads the mark as `KBRecordingSegment.echo` (`kb.get`).
- **A hub without `meeting_echo`** (an older manager, which would put echo lines into the minutes): the phone leaves echo lines out of the upload, as before, by the same rule, and sends no `spoken`.
- `meeting_echo` is listed in `hello.features` only while the default manager names it beside `meeting_attendance` in its `manager.hello`, like the other meeting flags; an extension's report cannot add it. The phone reads it from the hub's own `hello` at every upload. Before that hello (an upload at launch, another hub just picked) it leaves echo lines out, as for an older hub: a flag kept from the last hub heard may be another hub's, and only Persona's own words are lost on the hub's copy.

### Not yet

- Minutes that use the attending session's running notes or the asks.
- Marking a meeting as private, so it is never material for answers.
- A wake phrase, and attending from the watch, the Mac, the web or Android. (Spoken answers in the realtime voice came with #5004: the voice extension's `speak`.)

## Meeting conversation (#3862)

Persona attends a meeting through **one meeting conversation**, instead of a recorder that feeds lines to a chosen session beside a separate tool-less answerer. This section is the contract of that redesign: the hub, the web and the iPhone build against it, one phase at a time. "Attending a meeting (#2981)" above stays the contract for phones from before it (#3746), and a hub keeps answering it for every recording whose upload does not ask for a room conversation.

### Owner decisions

Decided by the owner in #3862; every phase follows them.

1. **Retention.** Minutes are kept forever by default (`minutes_days`, `0` = forever, #3936), transcripts for `recording_text_days` (forever by default, #16412). A photo counts as transcript and goes with it, unless the minutes cite it: then it stays with the minutes.
2. **Earlier meetings are said aloud only once a meeting can be marked private.** The mark is part of phase 1, and with it the minutes of earlier meetings marked not private come back as material for later meetings ("The private mark"; the knowledge base's `EarlierMeetingsInAnswers`, off until then; `modules/recording/writers/meeting_material.go`).
3. **Two channels.** The **room conversation** may be heard by others, so the hub limits it to meeting material. **Just me** is private, on screen only, and goes to the main chat.
4. **After the meeting, a new conversation**: a normal Persona conversation seeded with the meeting (minutes, transcript, the room thread), not the room conversation lifted in place. The apps show the room and the follow-up as one thread. It finds other meetings with `kb_search` (`kind=recordings`) and `kb_get`.
5. **Transcription.** Privacy mode transcribes on the iPhone, and the meeting's audio never leaves it (only Talk's spans go to the realtime voice service, decision 7 and "Decided"). Hub mode transcribes with a GPT-Live-compatible live model, through that interface only (no second vendor), and is never locked to the recording's language: Chinese, English and sentences that mix them are written as spoken, never translated ("Language is a hint, never a lock"). Audio may be kept on both sides as quality needs: the phone keeps its audio until the hub confirms, and gaps can be uploaded and transcribed after the meeting. The architecture is the measured one in "Phase 5".
6. **Speakers are told apart, never identified.** For a meeting the hub transcribes, the hub separates the voices in the audio it already holds into numbered speakers and keeps only who spoke when. People name them; nothing recognises a voice across meetings and no voice features are stored. Names used in earlier meetings with the same attendees are offered again as candidates, never matched by voice. A name the owner chose shows where the hub derives it from the timeline: in the transcript and on the positions of a disagreement. It never enters `who`, the highlights or the material of later meetings, and nothing says it aloud during a meeting; the minutes carry it, and so do the follow-up conversation and the Agents that read them. Nothing is labelled during the meeting. Meetings the iPhone records and transcribes itself (privacy mode) have no speakers. Apple Watch meetings have none yet; a later phase may send their audio to the hub, for speakers only, once the owner turns that on.
7. **Realtime voice first**, connected for the whole meeting (its cost is not a concern). Addressing stays a tap or push-to-talk (#2981 decision 4: no wake phrase): the voice session hears only what the owner says to it, while transcription hears everything.
8. **Echo.** The system's echo cancellation (voice processing, on every iOS Persona supports), and a transcript line that overlaps a span when Persona spoke and says its words is Persona's echo: kept and marked, left out of the minutes, the summaries and the live log ("Persona's echo", #5905; before it, every line in such a span was dropped).
9. **A new `transcription` extension point**, not a mode of the dictation proposal (#2083).
10. **A new meeting card**: an in-app meeting page plus the Live Activity and Dynamic Island, with the live transcript and topics, Ask / Talk, photos, the role and the Just me switch, instead of today's pause / bookmark / stop recorder.
11. **v1 stays.** #3746 shipped the iPhone side of #2981; the redesign comes in new PRs, and phones from before it keep the #2981 contract (`attendance` / `attending_session`, `…/live`, `…/ask`).

### Phase plan

Each phase ships on its own, behind its own flag ("Flags and compatibility").

| Phase | Ships | Hub | Web | iPhone | Flag |
| --- | --- | --- | --- | --- | --- |
| 0 Retention | Minutes outlive their transcript | Done (#3936) | Done | Done | — |
| 1 Meeting conversation | The room conversation and asking it, Just me, the private mark (and earlier meetings marked not private back as material), the follow-up conversation, one thread | The room (guest kind `meeting` without the owner's Claude Code setup, meeting tools, batches), `meeting.json`, `…/ask` answered by it, `…/just-me` with its turn mark, `…/private`, `…/followup`, two origins, the `meeting_live` event, deleting the room at the transcript cut | One thread on the recording detail, the private switch, the follow-up composer | The meeting page (thread, Ask, Just me, role, private), the Live Activity's answer and Turn off | `meeting_conversation` |
| 2 Live view | Live transcript and topics | Done (#3862): `lines` and `topics` on `GET …/live`, the topics tool, `lines` and `topics` in `meeting_live` | Done (#3862): Topics and live lines on the detail while it records | Done (#3862): 15-second segments; Topics and Live on the page; topic and latest line in the Live Activity | `meeting_live_view` |
| 2b Highlights | 3–4 highlights of the meeting so far, each back to its moment in the transcript (#5891) | Done (#5891): `highlights` and `highlights_at` on `GET …/live` (with `revs` and `?parts=highlights`), `meeting_highlights_set` resolving each `said_at` to its minute's lines, the highlights in the batches and the follow-up's seed, `[m:ss · HH:MM]` lines from `meeting_lines`, `highlights` in `meeting_live` | — (the recording detail keeps Topics and Live) | Done (#5891): the flag kept from the start; the Highlights card at the top of the page (the gist only, each time opening the transcript at its minute, lit; a waiting line before the first while Persona attends), Full Transcript one tap away, the newest highlight alone while typing | `meeting_highlights` |
| 3 Photos | Photos as meeting material | Done (#3862): `…/photos`, storage with the recording, forwarding to the room, notes, citations in the minutes, retention | Photos on the detail, photo citations in the minutes | Camera and library on the page, on-device text recognition | `meeting_photos` |
| 4 Realtime voice | Talk to Persona by voice during the meeting | Done (#3862): meeting calls in the built-in voice extension (`meeting` in `features`; `start.mode` `meeting` with `recording_id`, checked with `…/live`, else `not_meeting_room`; a room refuses other calls, `meeting_room`, its origin said on its session row by the manager; `delegate_task` to the room only, with no prefixes, the meeting instructions, the room's own questions and answers, no notes; `talk` frames, silence fed between spans, a span closed past 90 s; no pausing, no observed turns; the user-speech gate always on, with no Send Anyway) | — | Done (#3862): the meeting call on the recorder's capture (`mode: "meeting"`, `recording_id`, the room as `target`, `echo_cancellation`; never claims the microphone, no idle end, reconnects then every 30 s), Talk held on the page and a talk window from the Live Activity (`talking`, `TalkInMeetingIntent` with the action its button showed, its speech the owner's over the room's level), audio only inside spans (placed where it was captured, the press never early), Talk without a call through `…/ask` (`spoken: true`, also for a span the call lost and for any span opened before the Wording was on screen or seen), typed questions as `text` frames, echo cancellation from a participant start, the voice's and spoken answers' spans kept as they played with the words said in them (the call's: its captions), so Persona's echo is marked by #5905's rule, the privacy Wording, no voice in a room opened as a chat | `meeting` in the voice extension's `features` |
| 5 Hub transcription | Hub mode | The `transcription` extension point and its built-in adapter, the audio socket, the live log's fallback to the phone's text, gap uploads, the transcript of record, the 4× final pass | Mode and gaps on the detail | Done (#3862): the uplink from the recorder's tap (the 60-second ring, the resend from `ready.from`, gaps for what fell out), the timeline, no `POST …/live` in hub mode, gap uploads from the phone's segments, Hub Transcription on Wi-Fi Only, the mode on the start (On the Hub only while ready) and the default and glossary in Settings › Meetings, the hub's lines and caption on the page and its line on the Lock Screen, the fallback notice, the mode, state and gaps on the recording's page, privacy texts per mode | `meeting_transcription` |
| 6 Talk about this meeting | The follow-up from any recording, by text and voice | Done (#3862): the minutes push's `recording_id`; `kind` on `GET /api/v1/kb-search` and on the voice `search_knowledge`; `…/followup` for any recording | Done (#3862): **Talk about this meeting** on the detail | Partly (#3862): the same on the detail and after Stop; the minutes push opens the meeting; voice once the follow-up exists, dictating the first request not done | none (phase 1's route) |
| 7 Long meetings | Lean batches every two minutes and a scheduled in-place compaction (#8253) | Lean batches, lists at boundaries, the room's CLAUDE.md, the compaction | — | — | none (no wire change) |

### The parts

| Part | What it is | Who sees or hears it |
| --- | --- | --- |
| Room conversation | One Persona conversation per attended meeting, opened by the hub and limited to meeting material ("The room conversation") | The room: on screen, in the Live Activity, and aloud for a participant |
| Just me | The owner's private questions during the meeting, sent to the main chat with the meeting as context | The owner only, on the meeting page; never spoken, pushed or in the Live Activity |
| Follow-up | A normal Persona conversation about the meeting, made when the owner first talks about it afterwards | The owner, like any conversation |
| Private mark | Per meeting: a meeting marked private is never material for a later meeting ("The private mark") | — |
| Transcription mode | `device` (privacy mode) or `hub`, chosen when the recording starts | — |
| Talk | Push-to-talk to Persona | The voice session hears only the Talk spans |
| Live view | The transcript so far, the topics and (phase 2b) the highlights | The owner's apps |

### Flags and compatibility

Each phase has a feature flag that the hub lists in WS `hello.features` and trusted `/health` exactly as it lists `meeting_attendance`: only while the default manager is connected and names the capability in its `manager.hello`; an extension's report cannot add one. The phone reads them from the last `hello` when a recording starts, and a meeting keeps what it started with.

- `meeting_conversation` (phase 1): the upload's `room` and `private`, the room conversation, `…/ask` answered by it, `…/just-me`, `…/private`, `…/followup`, the origins `meeting` and `meeting_followup`, the `meeting_live` event, and the fields phase 1 adds to `GET …/live` and `detail.meta`. A manager that names it names `meeting_attendance` too.
- `meeting_live_view` (phase 2), `meeting_photos` (phase 3) and `meeting_transcription` (phase 5): what those phases add. Each comes only with `meeting_conversation`, and `meeting_transcription` only with `meeting_live_view` too (it uses phase 2's `lines` and `?from=`).
- `meeting_speakers` (#17674): what "Speakers (#17674)" adds. It comes only with `meeting_transcription`.
- `meeting_highlights` (phase 2b): what "Phase 2b: highlights" adds. It comes only with `meeting_live_view`. Without it the meeting page stays as phase 2 has it.
- `meeting_notes` (#8851): the room's `notes` role ("The notes role (#8851)"). It comes only with `meeting_live_view`. Without it a meeting Persona is not in starts `off`, with no live view.
- `meeting_echo` (#5905) comes with `meeting_attendance`, with or without the conversation: echo lines marked on the upload and `spoken` ("Persona's echo"). The phone reads it at every upload, not once per meeting.
- Phase 4 belongs to the built-in voice extension: `meeting` in the `features` of its `GET /info` and `ready`.
- Phase 6 adds no flag: it uses phase 1's route, and the new push field and `kind` parameter are optional.
- Every route in this section takes full credentials only, as `…/live` and `…/ask` do: the watch's answer-only token and extension tokens never reach them (they stay out of `extwire.RouteScopes`; `403 forbidden` otherwise). The built-in voice extension calls them with the hub's own token.
- **Retries** reuse the operation receipts ([protocol.md](../protocol/protocol.md) "Operation receipts (client_ops)") rather than a scheme per route: Just me and the follow-up derive theirs from the body's `id`. Two keep ids of their own: asks keep #2981's, which #3746 phones rely on, and a photo's `id` is the photo's own, stored with it in `photos.json` for as long as the photo.

| Phone | Hub and manager | What happens |
| --- | --- | --- |
| From before #3746 | Any | Recordings and minutes as before |
| #3746 | With `meeting_conversation` | The #2981 contract, unchanged: the attending session is fed and `…/ask` answers from meeting material. From phase 1 earlier meetings come back into those answers, but only meetings marked not private, which a #3746 phone never marks ("The private mark") |
| New | With `meeting_attendance` only | The phone behaves as #3746 |
| New | With `meeting_conversation` | This section |
| New | An older manager after a rollback | An upload `200` without `room` means an older manager. It never reads the role inside `room`, so it stored the meeting with attending off and feeds nothing, neither on that upload nor when it restarts. The phone keeps uploading as usual, sends no `…/live` or `…/ask` for the recording and shows that Persona can't take part, until a `200` carries `room` again |

- The web and Android from before list the room and follow-up conversations as ordinary auto chats (their origins are unknown, so **Other**). The watch is unchanged: watch recordings never attend (#2981).
- A node that does not know guest conversations, or `assistant.guest_delete`, gives a room `unavailable` with `node_too_old` (asks fall back to meeting material), or a room that cannot be deleted yet ("The room conversation").

### Phase 1: the meeting conversation

#### Upload fields

On `PUT /api/v1/recordings/{id}`, with `kind: "meeting"`:

```jsonc
{
  "room": {"attendance": "participant"},   // optional: the meeting's own room conversation attends, in this role
  "private": false                         // optional: the meeting's private mark as it starts; taken only while the recording has none
}
```

- `room` replaces the top-level `attendance` and `attending_session`: the hub opens one room conversation for the meeting and feeds it. `room.attendance` takes #2981's roles and rules, and `notes` on a hub that lists `meeting_notes` ("The notes role (#8851)"): any other value is stored as `off` with `attendance_problem: "unknown_role"`, a role on a recording whose `kind` is not `meeting` as `off` with `"not_meeting"`. A `room` that is not an object counts as absent. Absent on every upload: the #2981 behavior.
- With `meeting_conversation` the phone sends `room` on every upload of every meeting recording, whatever the role (`{"attendance": "off"}` included), and never the top-level `attendance` or `attending_session`; the hub ignores those two beside `room`. The role travels inside `room` so that an older manager after a rollback, which reads only the top-level `attendance`, sees a meeting that is off and feeds nothing ("Flags and compatibility").
- A recording that once had `room` stays a room meeting: a later upload without it turns the role `off` and changes nothing else (its top-level `attendance` and `attending_session` stay ignored), so a meeting never moves from its room to another session.
- The `200` carries the top-level `attendance` (the role in effect, as every #2981 `200` does) and `room` (the object in "Fields on `GET …/live` and `detail.meta`") for every room meeting, at least `{"state": "none"}`. Only a `200` without `room` means an older manager. A problem with `room` or `private` never fails the upload, which carries the transcript.
- `private` is the start sheet's choice. The hub takes it only while the recording has no mark, so a later upload never undoes a change made with `…/private`; a value that is not a boolean is ignored. The phone sends it for every meeting recording, attended or not, since every meeting's minutes could be material later.
- As in #2981, the phone sends no `…/live` or `…/ask` for a recording until an upload carrying its current `room` has returned a `200` with `room`.
- **Where the manager keeps it.** A room meeting's role, the room's state and ids, `private`, the follow-up's ids, the pending creations ("Made once" below) and (phase 5) the transcription mode and the timeline are kept in `recordings/<id>/meeting.json` beside `meta.json`, never in `meta.json`: an older manager rewrites `meta.json` without the fields it does not know, and when it starts resumes feeding every recording whose `meta.json` has an `attendance`. `detail.meta` and `…/live` still report `attendance`, read from `meeting.json`. Search skips `meeting.json`, and the trim keeps it (an older manager's trim removes it, which leaves the meeting with no mark, so private). A manager test pins that a room meeting's `meta.json` never carries `attendance` or `attending_session`, after an upload, a role change or the trim.

#### The notes role (#8851)

A meeting Persona is not in still gets its live view. Since #8731 a meeting starts with only the user, and Persona joins when invited; until then the phone starts the meeting as `notes`.

- **Where it is taken.**
  - `room.attendance` takes `notes`, and so do the timeline's `roles` (phase 5) and the `attendance` that `GET …/live` and the upload's `200` report.
  - #2981's top-level `attendance` does not: `notes` there is `unknown_role`.
- **What the room does.** It opens and is fed as for `listener`:
  - sentences that end while the role is `notes` join the live log;
  - the room keeps the topics and highlights;
  - photos reach it.

  Its first message and its batches name the role, the note-taker, and no question reaches it.
- **Persona answers nothing.**
  - `POST …/ask` answers `409 not_attending`, as for `off`. An ask accepted before the switch still gets its answer.
  - The phone runs no meeting call and no Talk.
- **Inviting and removing.** Inviting Persona switches the same room to `listener` or `participant`; removing it switches the room back to `notes`. The room is never replaced.
- **Default.** `meeting_attendance_default` keeps its three values. When it is `off` and the hub lists `meeting_notes`, the phone starts a meeting in `notes`. The iPhone (#8731) shows it as one switch, Invite Persona Automatically (on: `listener`), and its quick start invites Persona per meeting ([recording.md](../apple/recording.md) "Persona in meetings").
- **Older managers.** An older manager after a rollback reads `notes` as an unknown role, so the meeting is stored `off`: no live view, and minutes as usual. A hub-mode timeline that names `notes` is ignored whole there, like any malformed timeline.

#### The room conversation

- **When.** The hub opens it once a room meeting has the role `notes`, `listener` or `participant`, after the upload has been answered (never inside the upload request: the start needs the node). Until then `room.state` is `none`; a meeting that starts `off` and turns attending on later gets its room then, and one that stays `off` never gets one. One per recording, never a second, and none after the meeting's end ("The meeting's end" below).
- **Made once, also across a restart.** Before it asks the node, the manager writes the creation to `meeting.json` as pending, with the session id the conversation will have: a UUID derived from the recording id and the part (`room`, or `followup` for "After the meeting: the follow-up", whose record also keeps the `id` of the call making it). The node starts it under that id (a new `session_id` in the node's start, Claude Code's `--session-id`, never the warm spare), and a start under an id the node already has returns that conversation instead of a second one; its first message's `client_msg_id` (`meeting-<recording id>` for the room's header, `followup-<id>` for the follow-up's) is sent once. A pending creation found after a restart is settled first: when the node has a conversation under that id, that is the one; when it has none, the room is started again under the same id, and the follow-up's record is dropped so the phone's repeat of the call (same `id`) makes it. A node that cannot start under a given id gives the room `unavailable` with `node_too_old`.
- **What it is.** A guest conversation ([assistant.md](./assistant.md) "Guest conversations") of a new kind, `meeting`, on the assistant's computer:
  - Claude Code only, on the CLI, and every process it runs in has `--tools ""` and `--strict-mcp-config`. It runs on the **In-meeting conversation** model (`meeting.room`, #7980: the main chat's launcher, model and effort until it is set apart in Model settings, [model-config.md](./model-config.md) "Meeting minutes and the in-meeting conversation (#7980)"), resolved when the room starts and kept until it is deleted. No live snapshot, none of the assistant's CLAUDE.md, memory or skills, never the warm spare process.
  - One directory per meeting, `.persona/guest-meeting/<recording id>` in the node's state directory, prepared with `assistant.guest_prepare` like the extensions' guests but with a meeting CLAUDE.md (the owner's name, the language and the rules below), not the directory extension guests share.
  - **Nothing of the owner's Claude Code setup.** `--tools ""` and `--strict-mcp-config` remove only built-in tools and other MCP servers; Claude Code would still load the user's own instructions, settings and hooks. The room's processes load none of them, as no guest conversation's do ([assistant.md](./assistant.md) "Guest conversations"): not the user's CLAUDE.md (`~/.claude/CLAUDE.md`, or the one under `CLAUDE_CONFIG_DIR`) or rules (`~/.claude/rules`), not a CLAUDE.md, AGENTS.md or `.claude/rules` in a parent of its directory (such as `~/CLAUDE.md`), not auto memory, not the hooks, output style or plugins of the user's settings. Only the meeting CLAUDE.md and what the node passes itself (Persona's MCP server and hooks, the launcher's environment in the room's own `--settings`) reach the model.
  - **It signs in as the assistant does.** The room starts in the profile the assistant's conversations had before each new conversation got a private profile (`config_dir` `legacy`, [assistant.md](./assistant.md)): a private profile is a config directory with no login in it. The manager asks for it (on a node too old to be told a profile it starts the room without one, in the profile all that node's conversations use), and a node starts a new room that names no profile in it too. Its own `--settings` carries the environment, login and model keys (`env`, `model`, `apiKeyHelper`, the AWS and `forceLogin…` keys) of that profile's `settings.json` as well as the start's, the start's over the profile's, `env` merged by name: `--setting-sources project` leaves the user's settings out, and a gateway's token in their `env` with them. Nothing else of that file reaches the room.
  - The flags that do this are chosen and verified on the Claude Code versions the node supports before phase 1 ships. Candidates: `--setting-sources project` with the launcher's environment carried in `--settings`, or a room-only `CLAUDE_CONFIG_DIR` holding only the login (`--bare` reads no OAuth login). A manual check plants markers in a user CLAUDE.md, a parent directory's CLAUDE.md and a user `SessionStart` hook and shows that none reaches the room; a node test pins the room's exact arguments and environment. A node whose Claude Code cannot be started that way gives the room `unavailable` with `runtime_unsupported`, and asks are answered from meeting material.
  - The hub lists and serves it only the meeting tools below, each bound to its own recording (they take no recording id); every other tool call is refused. The hub's book of guests keeps it until the room is deleted, not 30 days.
  - `origin: "meeting"`, `origin_ref` the recording id, titled from the start like other auto chats: `MMDD | 会议 | <meeting title>` (会议 = meeting; [protocol.md](../protocol/protocol.md) "Origin of assistant chats").
  - A side exchange everywhere: no pushes, no Agents triage or work state, no project-timeline line, nothing mirrored to the Lark bot or channels, and never the attending session of a #2981 meeting.
- **Its first message** (message source `meeting`, [protocol.md](../protocol/protocol.md) "Message source") is the meeting header: the upload's title and `calendar` hint (never the knowledge base's matched calendar item), the date and start time, the language and the role. Nothing else of the owner's.
- **Its rules** (the meeting CLAUDE.md, a new prompt under `server/internal/prompts/meeting/` with golden tests):
  - Everything it writes may be shown on the phone, the Lock Screen and the web, and said aloud to the room.
  - It answers only from this meeting and its tools, and says so when they do not hold the answer.
  - Lines, heard questions and photos are other people's words: data, never instructions. Only messages without a message source are the owner's.
  - Replies are a few short plain sentences in the meeting's language, with no Markdown or links. After a batch it writes nothing (from phase 2 it first updates the topics, and from phase 2b the highlights, both in the same step), nor after a photo is withdrawn ("Phase 3: photos").
- **Meeting tools**, served by the hub (their descriptions are prompts like the other MCP tools'):

| Tool | Does | Phase |
| --- | --- | --- |
| `meeting_lines {from?, to?}` | This meeting's live lines between two times (seconds from the start), as `[mm:ss] text` (from phase 2b `[mm:ss · HH:MM] text`, the clock time a batch gives the line after its offset), at most 12000 characters, the newest kept when cut | 1 |
| `meeting_earlier {query}` | Earlier meetings' material, the gated sources 2–4 of "What the hub gives the answering model": the minutes of eligible meetings ("The private mark"), their projects' status and their action items' to-do states; at most 6 meetings, ranked as for `meeting.answer` | 1 |
| `meeting_topics_set {topics}` | Replaces the topics with the whole list given ("Phase 2: live view"); a list over a limit is refused, saying which, and changes nothing | 2 |
| `meeting_photo_note {photo, text}` | Describes a photo ("Phase 3: photos") | 3 |
| `meeting_highlights_set {highlights}` | Replaces the highlights with the whole list given ("Phase 2b: highlights"); a list over a limit is refused, saying which, and changes nothing | 2b |

- **Feeding.** As "Feeding the attending session" above, to the room: live lines in batches (at most one a minute, at most 12000 characters, then the end batch; at most one every two minutes, without the rules and lists in each, for a room started with lean batches, "Long meetings: lean batches and compaction (#8253)"), message source `meeting`, only while the role is not `off`; a room that comes up late first gets the meeting so far. Three differences: a batch is held while an ask waits for the room or runs in it; there is no meeting-turn mark (the whole conversation is limited, so nothing needs lifting); and the room is exempt from "Sessions that never attend", which refuses every guest conversation, for its own meeting only. That test keeps other people's words away from sessions that could act on them unasked, and the room has no tool that acts; every other meeting, #2981 ones included, still refuses it.
- **States** (`room.state`), an open set: a client reads a value it does not know as `starting`.
  - `none`: no room, since the role has been `off` from the start. It stays `none` while the role does, also after the meeting's end. No ids.
  - `starting`, `ready`, and `unavailable` (with `problem`).
  - `ended`: the meeting is over for the room ("The meeting's end").
  - `deleted`: removed at the transcript cut or with the recording ("Deletion"). No ids.
  - `problem`, an open set (an unknown value reads "Persona can't take part in this meeting right now"): `runtime_unsupported` (the In-meeting conversation's launcher, the main chat's unless it is set apart, is not Claude Code, the assistant's computer has none, or none that can run a room without the owner's setup), `node_too_old`, `machine_offline`, `start_failed`. The last two are retried every minute until the meeting's end; the first two are not.
  - While the room is `unavailable`, asks are answered from meeting material by `meeting.answer`, as in #2981 (`via: "material"`); while it is `starting` they wait for it ("Asking the room"). A start that has not made the room `ready` within 2 minutes turns it `unavailable` with `start_failed`, so no ask waits longer.
- **The meeting's end** is the moment #2981's end batch is due: the first upload carrying both `ended_at` and `complete: true`, or 3 minutes after the first upload carrying `ended_at`, whichever comes first. A meeting whose phone never sends `ended_at` (lost or reset mid-meeting) ends 12 hours after its last upload, when the feeder stops looking at it (`feedStale`), and a trimmed one has ended. Then:
  - a `ready` room whose role is not `off` gets the end batch and turns `ended` once that turn ends, 3 minutes later at the latest (an end batch not delivered by then is dropped);
  - any other `ready`, `starting` or `unavailable` room stops retrying and turns `ended` at once, keeping its ids if it had any;
  - a `none` room stays `none`.

  So every room is `none`, `ended` or `deleted` at most 6 minutes after the first upload carrying `ended_at` (12 hours and 3 minutes after the last upload when none ever does), whatever its role or its computer did.
- **Messages from elsewhere.** A full device's `POST /api/v1/sessions/{mid}/{sid}/messages` into the room is the owner's question while the meeting records (an app that does not know rooms opened it); it is never spoken. Once the room is `ended` the manager refuses it, `409 meeting_ended`, and the apps write in the follow-up instead.
- **Deletion.** At the transcript cut (the trim) and on the recording's delete, the hub deletes the room: the node's new `assistant.guest_delete {dir, session_id}` stops the room's process and waits for it to exit (Claude Code writes its last lines into the transcript as it exits, #4665), then removes its transcript, its `session-env` folder and any files saved for it, and the hub drops it from the session lists. A node that is offline, or too old for the method, is asked again at every retention pass; meanwhile `room.state` stays `ended`, and the detail says a copy is still on the assistant's computer. The retention pass also finds rooms by their origin records (origin `meeting`, `origin_ref` the recording): one whose recording is gone, trimmed, or has a `meeting.json` that does not name it (an older manager's trim or delete after a rollback removed the file) is deleted too.

#### Asking the room

`POST /api/v1/recordings/{id}/ask` keeps the request, response, idempotency, hold and errors of "Attendance fields". In a room meeting:

- A **typed** question goes to the room as the owner's own message, the question's text only. So does a **spoken** one: `spoken: true` in the request (with `question`) marks a question the phone transcribed from a Talk span ("Phase 4: realtime voice in the meeting"); its `question_source` is `spoken`.
- A **heard** question (no `question`) goes as a message with source `meeting`: the lines of the 45 seconds before `at` in a context block (the same lines and `live_next` wait as #2981) and a line asking for the question just asked in the room. Its `question` stays `""`, and `heard` lists those lines.
- **Attempts.** The message an ask sends has `client_msg_id` `ask-<id>-<k>`, `k` its attempt (1 for the first). The ask keeps each attempt, its message id and the room turn it started, with the asks in `live.json`. A repeated request while an attempt is pending observes that attempt and its turn. Asking a `failed` ask again ("Attendance fields") starts attempt `k+1` under the next id, so the send-once check of `client_msg_id` cannot swallow it. After a restart the manager first looks for attempt `k`'s message and turn in the room before it decides anything: a turn still running is awaited, a turn that ended settles the ask, and a message never delivered is sent again under the same id. The ask's own `id` never changes.
- The answer is the room's reply to that message: the turn's answer with Markdown removed, cut at a sentence end to at most 400 characters (the thread keeps the whole reply). A turn that ends with no text is `failed` with `no_answer`. A turn that ended in a model error (its row's `turn_error`: Claude Code's own words for a failed request, such as "Not logged in · Please run /login", are no answer) is `failed` with `model_error`; when the error is one the owner must fix (the sign-in, a key, the model, a usage limit) the room also turns `unavailable` with `start_failed`, so the asks after it are answered from meeting material until the room is retried. `sources` are the earlier meetings and projects `meeting_earlier` returned during that turn, at most 6.
- Order: an ask waits for a batch turn that is running (and in a room started with lean batches for a compaction, as long as one may run: "Long meetings: lean batches and compaction (#8253)"), then goes ahead of any held batch. One room answers one ask at a time; `429 ask_busy` still allows two in flight. `model_timeout` counts 3 minutes from the message's send.
- **Which way an ask goes** (`via`) is fixed when its first attempt goes out, and never changes. While the room is `starting` the ask waits for it (within its own hold, then as a pending ask) and goes to the room once it is `ready`; if the room turns `unavailable` or `ended` first, it goes to `meeting.answer`. While the room is `unavailable`, `meeting.answer` answers at once. Until its first attempt goes out an ask has no `via`. Asking a `failed` ask again goes the same way: a `room` ask asked again while the room is `unavailable` fails at once with `room_unavailable`, and the phone then asks a new question (a new `id`), which goes to `meeting.answer`.
- `409 meeting_ended` once the room is `ended`, right after the `409 not_attending` check: ask in the follow-up.
- `Ask` gains `via` (`room` or `material`; absent on #2981 meetings, which are `material`, and while the ask waits for a `starting` room), `heard` (`[{start, end, text}]`, heard asks in a room meeting), `question_source` `spoken`, and `error` `no_answer` and `room_unavailable`.

#### Just me

`POST /api/v1/recordings/{id}/just-me` sends the owner's private question to the main chat, with the meeting as context.

```jsonc
{"id": "<UUID>", "text": "Should I push back on the vendor?", "images": [{"mime": "image/jpeg", "data": "<base64>"}]}  // images optional
```

`200`:

```jsonc
{"conversation": {"machine_id": "<machine id>", "session_id": "<main chat's session id>"}, "client_msg_id": "just-me-<id>"}
```

- The hub sends one message to the main chat (starting one when there is none) as the owner's own: a collapsed context block naming the meeting (its title, the recording id and the time in the meeting), then the text and images. The title and any calendar text go into the block through the prompts' context escaping and are labeled as the meeting's data, not the owner's words: a calendar invite's title is written by whoever organized the meeting. With the recording id the main chat can read the meeting with `kb_get`.
- **Retries.** Each request leaves a durable receipt in `client_ops`, derived from the body's `id` as Lark sends derive theirs ([protocol.md](../protocol/protocol.md) "Operation receipts (client_ops)"). Its digest covers the recording and the whole body, and its result is the response, with the main chat the hub resolved. A repeat with the same `id` returns that result without sending again, naming the same conversation even when the main chat has changed since; another body or recording under it is `409 op_id_reused`. A definite failure is replayed too, and the phone asks again with a new `id`. The receipt holds the digest and the response, never the text, and follows the `client_ops` retention (30 days).
- **The Just me turn** (the main chat's turn this message starts) carries a mark, kept with the main chat until the turn ends and for at most 3 hours, as a meeting turn's is ("Feeding the attending session"). While it stands, nothing of the turn reaches a screen other than the meeting page, since a phone lying on the table shows its notifications to the room:
  - no completion push, and nothing mirrored to the Lark bot or channels;
  - `notify_user` sends no push and no Lark or channel copy; its text stays in the conversation, which the meeting page shows;
  - a card the turn raises (`ask_user`, a permission or a confirmation) is pushed only with content-free text, as a health-space card is, and never to Lark or channels; the Fleet Live Activity shows it without its text;
  - `conversation_send` and `conversation_new` refuse any `quote` during the marked turn, including an answer to a private card. The refusal tells the assistant to keep the words out of its relay text too. After the mark ends, the question's `just-me-` client message id still identifies it as private while the node retains its send record (up to 24 hours from sending). Once that id is absent, the transcript item has no proof of owner authorship, so it remains ineligible and gets the ordinary refusal. Sending a message without a quote follows the usual rules;
  - otherwise it is an ordinary turn of the main chat, with every tool.
- Nothing of it is stored with the recording: it is never meeting material, never in `asks`, the room or the follow-up's seed.
- The phone shows the main chat's items after that message, and the turn's cards (answered there), until its turn ends, only on the meeting page under **Just me**; never in the Live Activity, never spoken.
- `text` at most 8000 characters, at most 4 `images` of at most 3 MiB each, and at least one of the two.
- Errors, checked in this order: `403 forbidden` (not full credentials), `503` as for the other recording routes, `413 too_large` (a body over 32 MiB), `410 recording_deleted`, `404 recording_not_found`, `400`, a stored receipt for `id` (its result, or `409 op_id_reused`), `409 not_meeting` (the recording's `kind` is not `meeting`), `409 meeting_ended` (the recording has `ended_at`), then the main chat's own send errors as `POST /api/v1/sessions/{mid}/{sid}/messages` gives them. The receipt comes before `meeting_ended`, so a response lost before Stop is still recovered after it.

#### The private mark

- The hub keeps `private` on the recording: `true` or `false`, absent until chosen. Only the upload's first `private` and `PUT /api/v1/recordings/{id}/private` `{"private": true}` set it (→ `200 {"private": true}`), from the iPhone or the web, during the meeting or any time after, on a trimmed recording too. The assistant's tools cannot change it.
- Errors, checked in this order: `403 forbidden`, `503`, `413 too_large` (a body over 1 KiB), `410 recording_deleted`, `404 recording_not_found`, `400` (not a boolean), `403 kb_read_only`, `409 not_meeting` (the recording is not a meeting as #2981 counts them: neither `kind: meeting` nor `minutes_asked`, so it is never material and a mark would mean nothing).
- **One rule.** The mark is about the marked meeting only: whether its minutes may be material for a later meeting. It never changes what the marked meeting's own room, Just me or follow-up may use, so a private meeting's room reads earlier meetings marked not private like any other room.
- **Earlier-meeting material**, for `meeting_earlier` and for `meeting.answer` (#2981 meetings, and rooms that are `unavailable`), is from phase 1 on the minutes of recordings that are meetings as #2981 counts them (`kind: meeting` or `minutes_asked`) **and** whose `private` is `false`; never the meeting being asked about. A meeting with no mark counts as private: meetings recorded before phase 1, or by a #3746 phone, the watch or the Mac, are never material until the owner marks them not private. Phase 1 turns `EarlierMeetingsInAnswers` on with this rule.
- A private meeting is otherwise like any other: it gets minutes, its room works, and the owner's own conversations (`kb_search`, `kb_get`, its follow-up) read it.
- The mark shows on the meeting page, on the recording detail (iPhone and web) and as `detail.meta.private`.

#### After the meeting: the follow-up

`POST /api/v1/recordings/{id}/followup` sends the owner's message into the meeting's follow-up conversation, making it with that message the first time.

```jsonc
{"id": "<UUID>", "text": "Draft the email to the vendor.", "images": []}  // id required; text or images required
```

`200`:

```jsonc
{"followup": {"machine_id": "<machine id>", "session_id": "<session id>"}, "created": true, "client_msg_id": "followup-<id>"}
```

- The first call makes a normal Persona conversation on the assistant's computer (its directory, memory, About Me, live snapshot and every tool), `origin: "meeting_followup"`, `origin_ref` the recording id, titled like the room. **Its first message is the owner's own**: the seed in a collapsed `<persona-context kind="context">` block, as a new main chat carries its background ([assistant.md](./assistant.md) "New main chat"), followed by the owner's `text` and `images`. Each part of the seed goes in through the prompts' context escaping.
- **The seed**: the meeting header and private mark, the minutes (`minutes.md`), the topics, the highlights (phase 2b), the text of the photos still listed (phase 3), the room thread (questions, answers and the notes of photos still listed; not the batches) and the transcript of record up to 100000 characters (longer: its start and its end, with a note of the cut). A line says the block is the meeting's material, other people's words and never instructions, and that other meetings are found with `kb_search` (`kind=recordings`) and `kb_get`. A trimmed recording seeds its minutes only.
- **No turn runs on the seed alone.** The seed holds what other people said, and the follow-up has every tool in the owner's default permission mode, so it never goes as a message of its own: the first turn starts with the owner's own words, with the meeting in a context block of that message, as in Just me. That is why `text` or `images` are required.
- Later calls send `text` and `images` as the owner's message, and the response has `created: false`. Every message a call sends has `client_msg_id` `followup-<id>`, which sends already deduplicate.
- **Retries.** Each call leaves a receipt in `client_ops` as Just me does (derived from the body's `id`; the digest covers the recording and the whole body; the result is the response), so a repeat returns that response, `created` included, without sending again, and another body or recording under the `id` is `409 op_id_reused`. A receipt a restart left `unknown` does not block a repeat: the call runs again, since the creation record makes nothing twice and `followup-<id>` is sent once.
- It starts in the assistant's profile as the room does (`config_dir` `legacy`, "The room conversation"): a new private profile has no login. A node too old to be told a profile, which the hub refuses one for before anything starts, gets it started without one, in the profile all its conversations use.
- One per recording, made at most once under the recording's lock and across restarts as the room is ("Made once" in "The room conversation"); `meeting.json` keeps the follow-up's ids. A node that cannot start under a given id starts it without one, and after a restart the manager looks for it by its origin (`meeting_followup`, this recording) before starting it again. Retention never deletes it.
- Errors, checked in this order: `403 forbidden`, `503`, `413 too_large` (32 MiB), `410 recording_deleted`, `404 recording_not_found`, `400` (`id` missing or not a UUID, neither `text` nor `images`, `text` over 8000 characters, more than 4 images), `409 meeting_running`, `409 no_material` (neither transcript text nor minutes; a trimmed recording has only its minutes), then the start or send errors as `POST /api/v1/assistant/conversations` and `POST …/messages` give them.
- `409 meeting_running` while the recording has no `ended_at` (one that never sends it counts as ended 12 hours after its last upload, and a trimmed one as ended), or, in a room meeting, while its room is still `starting`, `ready` or `unavailable`, which lasts at most 6 minutes after the first upload with `ended_at` ("The meeting's end"). A recording with no room, or whose room is `none`, `ended` or `deleted`, never waits for a room.

#### Handing a meeting to an Agent (#8852)

`POST /api/v1/recordings/{id}/handoff` sends the meeting to one of the owner's Agents after it, as the owner's own message to that Agent: the meeting's name, calendar title, start and minutes in a context block (`prompts/meeting/handoff.md`; its transcript too when asked, its start and its end within 40,000 characters), then the owner's line, or, without one, a plain ask for what in it concerns the Agent's work. An Agent has no `kb_*` tools, so everything it gets is in the message.

```jsonc
{"machine_id": "<machine id>", "session_id": "<session id>", "title": "Site redesign", "text": "Update the home page.", "transcript": false}
```

`200`: `{"handoff": {"id", "at", "machine_id", "session_id", "title"?, "transcript"?, "state": "sent" | "failed", "problem"?}}`.

- **Who:** full device credentials only (an extension token gets `403`).
- **The Agent:** held to attending's rules ("Sessions that never attend"). One of this manager's computers, not a work computer, reachable, listed there, taking messages, not `bypassPermissions`, not closed. A Persona conversation is not an Agent (the follow-up is its way). A busy Agent takes it as it takes any of the owner's messages. A refusal is kept as `failed` with `problem`: `not_agent`, `not_allowed`, `unavailable`, or `send_failed` when the computer didn't take it.
- **When:** `409 meeting_running` while the meeting goes on (as the follow-up); `409 no_material` with neither minutes nor (asked for) a transcript.
- **Kept:** each try in `meeting.json`'s `handoffs`, oldest first, the last 20, and as `detail.meta.handoffs`, for the meeting's page to show the last one ("Handed to …", or why not, with Retry). No part of the live protocol: the apps read them with the detail and from the call's answer.
- **`title`** is the name the app showed for the Agent (at most 120 characters), kept only for that row.

In the apps (web, iPhone and Android):

- **Where:** the recording's page on the web (**Hand to Agent…** beside Ask Persona and Rename); on the iPhone the meeting's page after it, in its **⋯**; on Android the recording page's **Hand to Agent** toolbar action. Only once the meeting is over and has something to hand (its minutes, or its text on the hub), and only for someone with an Agent that can take it: someone who only uses the assistant never sees it.
- **The Agents:** the Inbox's Hand to AI list (not Persona's own, taking messages, live, on a computer online now), less those the hub refuses outright: a subagent, one cut off by a restart, one in Allow All; the iPhone also leaves out what "Sessions that never attend" keeps out by what it can see (a work computer, another manager's). Android uses the computer's current online state and only the recording page's hub group. The latest is picked.
- **The sheet:** the Agent, an optional line, **Include the transcript** (off; offered while the transcript is kept), and what it gets ("the summary, decisions, to-dos and open questions"). A try kept as `failed` says why in the sheet, where another Agent can be picked. Android reuses the Inbox's Hand to AI targets and design-system sheet.
- **The row:** at the top of the page, the latest hand-off: **Handed to "…"** with **Open** (its conversation), or **Couldn't hand to "…"** with the reason and **Retry**, which opens the sheet again with that Agent and transcript choice picked (the line is not kept). A try made on the page shows at once, before the detail is read again. On Android the open sheet and Retry context survive Activity recreation in the page's ViewModel.
- **Mock hub:** `POST …/handoff` as the hub has it, kept in `detail.meta.handoffs`; `MOCK_HANDOFF_FAIL=<problem>` or `POST /mock/meeting_handoff {"fail": "<problem>"}` keeps every try as failed (`scripts/mock/test_meeting_handoff_mock.py`).

#### One thread

- The apps show a meeting's conversations as one thread: the room's items, oldest first, then a divider (**After the meeting**), then the follow-up's items, with the seed block of its first message collapsed as the meeting's material. Batches show collapsed ("N lines from the meeting"), heard questions as "What was just said (mm:ss)", photos as thumbnails (phase 3), and a photo that `GET …/photos` no longer lists as "Photo removed", with no thumbnail. The room's header shows nothing on the web (#9411). The iPhone shows "Persona joined the meeting" for it, unless the meeting began with the note-taker. It also shows Persona invited in or taken out during the meeting where it happened; only the iPhone knows those changes.
- The composer writes to the room while the meeting records (through `…/ask`, or `…/just-me` with **Just me** on), and to the follow-up afterwards (`…/followup` until it exists, then `POST /api/v1/sessions/{mid}/{sid}/messages`).
- In the sidebar, apps that know the two origins show one row per meeting in Recent: the follow-up when there is one, else the room; it opens the thread. Apps that don't list both among their auto chats.
- Once the room is deleted, the thread starts at the follow-up's seed, which holds the room thread's text. A meeting whose room never came up shows its `asks` instead, as #3746 does.

#### Fields on `GET …/live` and `detail.meta`

`GET …/live` (and the `200` of `POST …/live`) adds:

```jsonc
{
  "private": false,                    // absent until chosen
  "room": {"state": "ready", "machine_id": "<machine id>", "session_id": "<session id>"},  // room meetings only; see "States"
  "followup": {"machine_id": "<machine id>", "session_id": "<session id>"}                  // once made
}
```

- `room` is present for every room meeting, `{"state": "none"}` before it has a room; the upload's `200` carries the same object. `state` is an open set ("States"); `problem` comes with `unavailable`; the ids come once the room has started, and never with `none` or `deleted`. The upload's `room` (`{"attendance"}`) and this one share a name, not a shape.
- `detail.meta` of `GET /api/v1/kb/recordings/{id}` carries the same three. In a room meeting the top-level `attendance` is the room's role, `attending_session` is absent, and the top-level `problem` keeps only `live_full`; the room's own problems are in `room.problem`.
- `GET …/live` also returns `revs` and takes `parts` ("The `meeting_live` event"); `detail.meta` has neither.
- Later phases add more. Both `GET …/live` and `detail.meta` get `transcription` (phase 5). Only `GET …/live` gets `lines`, `more`, `topics`, `topics_at` (phase 2), `highlights`, `highlights_at` (phase 2b) and `interim` (phase 5): `detail.meta` never carries live lines, topics, highlights or captions, since extension tokens with `kb.read` reach `GET /api/v1/kb/recordings/{id}` and never `…/live`. Photos are listed by `GET …/photos` (phase 3), in neither.

#### The `meeting_live` event

A manager's WebSocket event, content-free: `{"type": "meeting_live", "recording_id": "<id>", "changed": ["room", "asks"], "revs": {"room": 3, "asks": 12}}`. It goes out when the room's state, the asks, the private mark or the follow-up change, and from later phases on new lines, topics, highlights, photos, transcription state, captions and speakers; at most one a second per recording. `changed` is an open set: `room`, `asks`, `private`, `followup`, `lines`, `topics`, `highlights`, `photos`, `transcription`, `interim` (phase 5: "Gaps and the final pass"), `speakers` ("Speakers (#17674)").

- `revs` holds each changed part's revision: a number per recording and part that only goes up (for `lines`, the live log's length). Numbers only, so the event still carries nothing of the meeting. The one exception is the trim: it removes the live log, so `lines`, `asks`, `topics` and `highlights` read empty and their revisions start again from 0, with no event for them. A client compares those revisions only within a recording that is not trimmed (the live view is shown only while the recording is partial and not trimmed anyway).
- `GET …/live` returns `revs` for every part it has, and takes `parts` (`?parts=asks,room`: only those parts, with their `revs`). `photos` has a revision there but no field: `?parts=photos` returns only its revision, and the photos come from `GET …/photos`. A client keeps the revisions it has read and on an event reads only the parts that are newer: `GET …/live?parts=…`, the new lines with `?from=` its own next position (phase 2), `GET …/photos` only for `photos` (phase 3). Without `parts` the read returns everything, as #2981 reads do.

Its row in protocol.md's WebSocket table and the hub's list of manager message types come with the code.

#### What the phone and the web do

- **iPhone.** With `meeting_conversation`, the start sheet offers the role and **Private meeting** (no attending-session picker); one-tap starts use the default role and not private. It uploads every meeting at once, whatever the role, with `room` (the role inside it) and `private`, and after a `200` without `room` behaves as "Flags and compatibility" says; streams live lines with `…/live`; opens the meeting page ("The meeting card") and, once `room.state` is `ready`, subscribes to the room (WS `subscribe`, items paging); asks through `…/ask` and **Just me** through `…/just-me`; says answers with `speak` aloud as #3746 does; after the end writes into the follow-up.
- **Web.** The recording detail gets a **Conversation** section (the one thread: read-only while the meeting records, the follow-up composer afterwards) and the private switch; the asks of #2981 meetings stay where they are.

### Phase 2: live view

- `GET /api/v1/recordings/{id}/live?from=<n>` adds `lines`: the live log from position `n`, `[{"n", "start", "end", "text"}]`, at most 500, and `more: true` when there are more (read again from the last `n` plus one). Every position is listed, so `n` counts on without a hole; a placeholder position (an empty sentence) has `text` `""`, which clients skip. Without `from` there are no `lines`, so #2981 reads stay small; a `from` that is not a non-negative integer is `400` (after `410` and `404`). A `from` at or past the end gives `[]`.
- `topics`, always present on `GET …/live` from this phase (`[]` before the first), and `topics_at` (RFC 3339, the last change; absent before the first): `[{"title", "state", "at"?, "note"?}]`, `state` `done`, `current` or `open` (at most one `current`), `at` the seconds the topic began, `title` at most 80 characters, `note` at most 200, at most 20 topics. The `200` of `POST …/live` carries neither, as it carries no `revs`. The room sets them with `meeting_topics_set` after a batch: each batch shows it the topics so far (a room started with lean batches sees them only at the boundaries in "Long meetings: lean batches and compaction (#8253)" and otherwise works from the list it set), and it changes them only when the meeting moved on. A call that breaks a limit is refused with an error saying which, and changes nothing; titles and notes are put on one line first. The same list again changes nothing (no new revision). A room started before the hub had the tool keeps the tools it started with: its batches show no topics, and it sets none.
- Topics come from the room, so they are meeting material: the follow-up's seed lists them after the minutes. They live in `live.json` and go with the transcript. The minutes do not read them ("Summary card (#6563)").
- `?parts=lines` returns the lines (with `more`) only beside `from`, and `revs.lines` always; `?parts=topics` returns `topics` and `topics_at` with `revs.topics`. `revs.topics` goes up with every change of the topics.
- Live lines are shown only through `…/live`, to full devices; `kb_get` and `GET /api/v1/kb/recordings/{id}` still never show them (#2981).
- `meeting_live` adds `lines` (after every append that stored a position, `revs.lines` the live log's length) and `topics` (after every change).
- **The phone's own lines, sooner.** A meeting Persona attends writes 15-second segments in the foreground and the background (the writer's minimum is 10), so a line reaches the page and the room within about 15–30 seconds. The page shows the phone's own lines; other devices read `lines`. Hub mode (phase 5) brings captions to under a second (`interim`) and lines to within one window, about 15 seconds ("The built-in adapter").
- **Web.** The recording detail shows Topics (once the room set any) and the live lines while Persona attends the meeting (its room `starting`, `ready` or `unavailable`) and the recording is partial, read again on `meeting_live` (polling every 5 seconds without the WebSocket, as today).

### Phase 2b: highlights

While a meeting records, the top of the meeting page shows **3–4 highlights of the meeting so far**: the points that matter most (decisions, conclusions, commitments, open questions), each one short sentence with the moment in the transcript it comes from (#5891). The topics say where the meeting is; the highlights say what it has come to. The owner sees the gist at a glance and opens the transcript only when they want the details. Like the topics, the highlights come from the room, in the turn that follows a batch, so they cost no model call of their own.

- **On `GET …/live`.** `highlights` (always present from this phase, `[]` before the first) and `highlights_at` (RFC 3339, the last change; absent before the first): `[{"text", "at", "end"}]`, at most 4, the earliest `at` first. `text` is at most 80 characters on one line. `at` and `end` are seconds from the recording's start: the start of the first line, and the end of the last, of the minute of the transcript the point was made in (the minute the room named, below). The `200` of `POST …/live` carries neither. `?parts=highlights` returns `highlights` and `highlights_at` with `revs.highlights`, which goes up with every change. `meeting_live` adds `highlights` after every change.

  ```jsonc
  "highlights": [
    {"text": "Keep the export cap for this release.", "at": 312, "end": 358.4},
    {"text": "The vendor stays through Q3; the cost is checked next week.", "at": 1120.5, "end": 1171},
    {"text": "Open: who owns the hiring plan?", "at": 1385, "end": 1399.2}
  ],
  "highlights_at": "2026-11-13T06:23:10Z"
  ```
- **The tool.** The room sets them with `meeting_highlights_set {highlights}`, the whole list, each `{text, said_at}`. `said_at` is the clock time of a batch line the point was made in, `H:MM` or `HH:MM` as the batch writes it in its `[HH:MM]` (brackets and spaces around it are dropped), never an offset from the meeting's start such as the `m:ss` of the topics. From this phase `meeting_lines` writes each line as `[m:ss · HH:MM] text`, its offset and then the clock time a batch gives it, so a point whose batch is no longer in the room's context can still be named; the tool's description says to copy an `HH:MM` that a batch, the highlights so far or `meeting_lines` shows, never to work one out from an offset.
  - The hub resolves the minute against the live log so far, in the time zone its batches are written in at the time of the call. The minute's lines are the positions with text (placeholders never count) whose start, written as a batch writes it, is that minute; `at` is the earliest start among them and `end` the latest end. A minute the clock shows twice (when it goes back an hour) is its first occurrence. After the time zone changes, a time copied from an earlier batch may be refused; the next batch shows the highlights in the new zone.
  - It sorts the list by `at` (highlights of the same minute in the order given) and puts each text on one line, as topics are.
  - A call is refused, with an error saying which limit it broke, and changes nothing when `highlights` is absent, `null` or not a list; when it has more than 4; when a highlight is not an object with a string `text` and a string `said_at`, has no text or more than 80 characters of it; or when a `said_at` is not `H:MM` or `HH:MM`, or no line of the live log so far is in that minute.
  - The same list again, once resolved (each `text`, `at` and `end`), changes nothing (no new revision). A kept highlight whose minute has gained lines since gets the later `end`, and that is a change. Only `[]` clears the highlights.
  - Why a minute and not the line: batch lines carry only their `[HH:MM]` (both apps read that form when they show a batch's lines in the thread, and a wider stamp would change those rows), and the room copies it from the line without counting seconds; an exact line would take a `meeting_lines` call in every batch's turn. The phone lights the minute's lines, so the point is among them.
- **After each batch.** A batch shows the room the highlights so far after the topics in its context block ("None yet" before the first; for a room started with lean batches only at the boundaries in "Long meetings: lean batches and compaction (#8253)"), each on a line of its own as `- [HH:MM] text`, never as a line that starts with `[`: both apps read every line of a batch's block that starts with a clock stamp as a line of the meeting. Beside the topics (both calls may go in the same step), the room updates the highlights when the batch changed what matters most in the meeting so far, and otherwise leaves them. The instruction asks for 3 to 4 once the meeting has that many points, fewer before; each one short sentence in the meeting's language (about 30 characters in Chinese or Japanese, about 12 words otherwise), stating the point itself rather than that it was discussed, with the `[HH:MM]` of the line where it came to a head.
  - Highlights it keeps are sent again word for word with their `[HH:MM]`, so a kept point never changes its wording; it changes only what the batch changed. A point can give way to a weightier one, and a decision outweighs the discussion that led to it. An open question the meeting has answered gives way to its answer, at the answer's line. It never clears them.
  - A person is named only when the words name them (owner decision 6: nothing is labelled during the meeting, and speaker names never enter highlights), since the highlights are shown and may be said aloud.
- **Older rooms.** A room started before the hub had the tool keeps the tools it started with: its batches show no highlights and it sets none, so its meeting's `highlights` stay `[]`.
- **Meeting material.** The highlights come from the room, so they are material like the topics: the follow-up's seed lists them after the topics. They live in `live.json` and go with the transcript: the trim empties them, and `revs.highlights` starts again from 0 ("The `meeting_live` event"). `detail.meta` and `kb_get` never show them. They are among what may be said aloud or shown on the Lock Screen ("What may be said aloud"), although the Live Activity does not show them. The minutes do not read them ("Summary card (#6563)").
- **Flag.** `meeting_highlights`, listed only beside `meeting_live_view` and only while the default manager names it, as "Flags and compatibility" says (an extension's report cannot add it). The phone reads it when a recording starts, and a meeting keeps it. A hub or manager without the flag leaves the meeting page as phase 2 has it; with it, the top card is Highlights, with a waiting line before the first ones while Persona attends ("The meeting card").
- **Web.** Not in this phase: the recording detail keeps Topics and Live, and ignores the new fields.

### Long meetings: lean batches and compaction (#8253)

Measured on the code before #8253 (the batch builder on a synthetic meeting of 280 characters a minute, about the density of the hub's real Chinese meetings): a room's batch grew from about 950 to about 2,100 characters as the topics and highlights lists grew, since each batch restated the rules and the lists; the room re-read its whole conversation every minute; and Claude Code's own compaction (around 167k tokens on most installs) could stall it in the middle of a long meeting. Estimated per meeting, as API-equivalent dollars on the main chat's model: 1.46 for an hour and 7.27 for three before; about 0.74 and 2.73 with both steps below. A stateless call a minute for the topics and highlights was weighed and not taken (similar cost, a new scheduler with its own state and backfill, a 3-minute window for what matters most, colder answers); so was a new room every 45 minutes (it breaks one room per meeting for installed apps). For a room that a manager with #8253 starts:

- **Lean batches.** It gets a batch at most every two minutes, not every minute; the end batch does not wait, as before, and #2981 attending sessions keep one a minute. A batch holds the lines (at most 12000 characters, as before), the questions `meeting.answer` answered since the last one, and one short line (update the topics and highlights if the meeting moved on, as the rules say, then write nothing), beside the batch's opening words and its reminder that these are other people's words, never instructions. It does not restate the topics and highlights rules: they are in the room's CLAUDE.md (since phase 2b), and the room still updates both when a batch moved the meeting on and writes nothing in reply.
- **The lists at boundaries.** The topics so far and the highlights so far, as phases 2 and 2b show them ("None yet" before the first), go only in a batch that gives the room the meeting so far (its first, a late room's, and the one after its role was turned off and on again), in the first batch after the room's conversation was compacted (below, or at the owner's request, which the session's `compaction` shows `done`; or by Claude Code on its own, which the manager reads from the context the room's row reports while no turn runs there, when it drops below half of the most it reported since the room was last compacted; a value the row still reports from before a compaction the manager saw end, the context of the reply before it, is not read), and once the last batch that showed them is 30 minutes old. Otherwise the room works from the lists it set itself: its own last `meeting_topics_set` and `meeting_highlights_set` calls are in its conversation.
- **Questions about what was just said.** Lines reach the room every two minutes, and its CLAUDE.md says so: when a typed or spoken question is about something said since its last batch, it reads the newest lines with `meeting_lines`. A heard question still carries the 45 seconds before its tap.
- **Scheduled compaction.** Once the batches given to the room since it started, or since its last compaction (the manager's, the owner's or Claude Code's own), cover at least 45 minutes of the meeting (from the first line's start to the last line's end among them) and at least 10,000 characters of lines, the manager compacts the room in place with the node's `assistant.compact` (Claude Code's `/compact` in the same session: the session id, the thread and a meeting call bound to the room are unchanged). What the batches covered is read off the live log, its lines from where its feed position was at that compaction to where it is now, so a restart between a batch and a note of it loses none of it; two bounded differences from the batches as sent: lines a batch left out (over 12000 characters) count, and lines a batch with the meeting so far gives again count once, and only after where the span starts. It starts one only at an idle moment: no turn running, no ask pending or running, no photo or withdrawal waiting to go in or being sent, the role not `off`, the meeting's end not due, and the room's process running (a node is never made to start it only to compact it: the manager asks with `only_running`, which a node with #8253 refuses with `not_running` when the process exited since the row said it runs, and that refusal is as the row saying so, tried again after the next batch and not counted as an attempt; an older node ignores the field). From when the manager asks for one until the session's `compaction` shows it ended, `done` or `failed` (or it counts as ended, below), the room counts as working, as it does while the session's `compaction` is `running` for any reason: nothing goes into it, asks, batches and photos wait for it as for a turn, an ask for as long as a compaction may run (10 minutes, the node's own limit) rather than the 3 minutes it waits for a turn. Once it is `done` the span starts again from where the feed position was when it was asked for, and the next batch shows the lists. A compaction refused (busy, already compacting) or failed is tried again at a look at least 5 minutes later, at most 3 times for the same span; then the room waits until its batches cover another 45 minutes. One asked for whose answer did not come (the time ran out, the manager stopped, a link on the way dropped with it out) counts as asked for: the node may have taken it. One the session's `compaction` does not show 10 minutes after it was asked for (never, or no longer: its node lost it) has ended and counts as failed; the next batch shows the lists, since it may have compacted the room. Until then no second one starts. A node without `assistant.compact` gets none, nor one that refuses because it could only compact the room by starting its process without the room's restrictions ("The room conversation"): a node with #8253 never starts a guest's process on a launcher that runs another agent by now, whoever asks.
- **Kept with the room.** The room's record in `meeting.json` marks a room started with lean batches (an older room keeps a batch a minute with the rules and lists in each until it is deleted, the same across a restart or a rollback; an older manager after a rollback drops the mark when it rewrites `meeting.json`, and the room is fed as an older one from then on) and keeps when the lists last went, when it was last compacted, how often, where in the live log its batches since begin, the latest compaction of its session it took into account, and the attempts at the current span, so a restart neither compacts twice nor loses a running one (the session's `compaction` says whether it is still running).
- **What the apps see.** Nothing new: no field, state, event or flag. `room.state` stays `ready` during a compaction; the room's thread keeps everything said before it (the transcript keeps the items before a compaction boundary, which shows as a notice); batches come half as often and each shows a little more of the meeting.

### Phase 3: photos

`POST /api/v1/recordings/{id}/photos`:

```jsonc
{
  "id": "<UUID>",             // the phone's id for this photo; a retry sends the same one
  "at": 1834.2,               // seconds from the start of the recording, when it was taken
  "mime": "image/jpeg",       // image/jpeg or image/png
  "data": "<base64>",
  "text": "Q3 budget: …",     // optional: the phone's on-device text recognition, at most 8000 characters
  "caption": "Whiteboard"     // optional: the owner's words, at most 200 characters
}
```

`200 {"photo": Photo}`, where `Photo` is `{"n", "id", "at", "mime", "width", "height", "text"?, "caption"?, "note"?, "cited", "forwarded", "added_at"}`: `n` counts from 1 per recording and is never reused; `note` is the room's description; `cited` says whether the current minutes cite it; `forwarded` whether the hub sent it into the room ("Into the room"), so the meeting page knows that deleting it withdraws it there.

- An image is at most 1 MiB decoded and 4096 pixels on its long side; the phone sends the 1600-pixel JPEG its chat photos use. (The room's items keep inline images only up to 1.5 MiB of base64.) `at` is required; outside the recording (before its start, or after its `ended_at`, or after now while it records) it is clamped into it. `data` must be a whole image (a header alone is `400`). A `text` over 8000 or a `caption` over 200 characters as sent (blanks around it included) is `400`, as is either one that is not a string; `null` or blank ones count as absent.
- The same `id` returns the stored photo; another body with it is `409 op_id_reused`. The `id` of a photo the owner deleted is `410 photo_deleted`: a retry never brings a deleted photo back. Both are answered on a read-only knowledge base too, before `403 kb_read_only`. The body's digest (its `at`, `mime`, `data`, `text` and `caption`) is kept with the photo in `photos.json`.
- `GET /api/v1/recordings/{id}/photos` → `{"photos": [Photo]}`, oldest first; `GET …/photos/{n}` → the image with its `Content-Type`; `DELETE …/photos/{n}` → `{"deleted": true}`, or `{"deleted": false}` for a photo the owner already deleted (a repeat whose answer was lost); a repeat first finishes what the delete left undone (its minutes, its file) and answers an error while the file can't be removed, so the client tries again. Both reads work on a read-only knowledge base; a trimmed recording lists and serves only its cited photos.
- Errors of `POST`, checked in this order: `403 forbidden`, `503`, `413 too_large` (a body over 2 MiB), `410 recording_deleted`, `404 recording_not_found`, `400` (a bad `id`, `at` or `mime`, data that is not that image, over the sizes), `409 op_id_reused`, `410 photo_deleted`, `403 kb_read_only`, `409 not_meeting` (the recording's `kind` is not `meeting`), `409 transcript_expired` (trimmed), `413 photos_full` (50 photos). `GET` and `DELETE`: `403 forbidden`, `503`, `410 recording_deleted`, `404 recording_not_found`, `404 photo_not_found` (no such `n`, or one a trim removed; for `GET` also one the owner deleted), and for `DELETE` then `403 kb_read_only`. `GET …/photos/{n}` answers with `X-Content-Type-Options: nosniff` and `Cache-Control: private, no-store`.
- **Deleting a photo** first takes the items citing it out of the current minutes, in `minutes.json` and `minutes.md` at once (a new `version`, the action items numbered again, each keeping its to-do; no push), then removes its entry and its file, and has the minutes made again as below. So `kb_get`, search, `meeting.answer` and a follow-up's seed made once the delete answered leave it out, before the new minutes are written. What already saw it keeps what it saw: while the room is `ready` the hub sends it a line with source `meeting` saying photo N was withdrawn and must not be described or used again, and the room's own copy (the image inline in its conversation, its note) goes when the room is deleted. A follow-up made before keeps its copy of the note. The meeting page says so when a photo it deletes was sent to the room. On a trimmed recording a cited photo can be deleted too: the minutes stay as they are and are not made again, and their item shows "Photo removed".
- **Into the room.** The hub forwards each new photo as a message with source `meeting`: the image inline, its time, caption and recognized text in a context block, and a request for `meeting_photo_note` (at most 1000 characters: what in it matters for the meeting). The message's `client_msg_id` is `photo-<id>` (the photo's `id`), on the image's item and the text's, so the apps tell which photo a room item shows and say "Photo removed" once `GET …/photos` no longer lists it. An ask goes ahead of it; batches wait for it. The photo is `forwarded` from just before its send, so one whose send's answer is lost (or a restart cuts it off) may be noted by the room and is withdrawn when deleted; it stays queued until a send succeeds, and is sent again under the same `client_msg_id` (the hub's once-only guard answers delivered, or in doubt). A photo added while the role is `off` or after the room ended is stored but not forwarded.
  - A photo added while the room attends but is not `ready` yet (`none`, `starting`, `unavailable`) is queued and goes in once it is; queued photos go oldest first, one message at a time, each only while the room runs no turn, and never after the meeting's end. A photo queued while the role is `off` again waits for it to be on.
  - The withdrawal line (source `meeting`) has `client_msg_id` `photo-withdrawn-<id>`, is sent once, also while the role is `off`, and only while the room is `ready`; the apps show neither it nor the photo message's text as a batch.
  - One photo message or withdrawal goes a look, whatever comes of it (a send whose answer is lost may have started a turn). One the room's node fails to take holds the batches for three looks (a minute apart); then it goes after the other messages, and the batches go on without it. A failure never counts as sent: it stays queued (or pending) and is tried again at later looks, under its `client_msg_id`.
  - `meeting_photo_note` takes `photo` (its number) and `text` (1 to 1000 characters) and is refused, with what is wrong, for a photo of another meeting, one that is not listed, never went into the room or was withdrawn. A note moves the `photos` revision and goes into `photos.md`, but does not make the minutes again. A room started before the hub had the tool keeps the tools it started with, so a meeting in progress across that update gets its photos but no notes.
- **Storage.** `recordings/<id>/attachments/p<n>.jpg` (or `.png`) and `photos.json` beside `meta.json`. `photos.json` is bookkeeping (the bodies' digests, the photos the owner deleted, those still to go into or be withdrawn from the room) and never searched; `photos.md` beside it has a line per listed photo (`[P3] 14:32:05 photo: caption · text · note`, the clock in the user's zone; as in the minutes input, a `</persona-context>` in the words is left out so it can close no context block), which is searched with the recording and which `kb_get` prints after the action items, before the transcript. Search skips `attachments/`. Photos are never in an upload's undo journal.
- **Minutes.** The minutes input lists the photos that have words as numbered lines of their own among the transcript's, in time order (`[P3] 14:32:05 photo: caption · text · note`, clipped as transcript lines are). An item may cite a photo (`"photo": 3`) instead of a transcript line, and its `at` is then the photo's; the citation rule ("How the hub generates them") becomes "every item cites a line the model read or a known photo". `markdown` ends such an item with "(photo 3)" in the minutes' language, like its headings (`（照片 3）` in Chinese; "How the hub generates them"). Adding or deleting a photo after the minutes were written makes them again, without a second push (not on a trimmed recording, which has no transcript to make them from): that generation is marked quiet in `meta.json` (`minutes_quiet`) until it settles, so a restart does not push it either, nor does a retry of the accepted upload (the same `revision`), and a newer revision's minutes are pushed as before. While minutes are still being made (the first ones, or ones the owner asked for), a photo change makes the call running now not settle them, and they are pushed as they would have been. Minutes never keep a citation of a photo deleted while they were made, minutes made while the recording was trimmed are dropped (the trim kept the minutes it cut against, and their photos), and a change in which photos the minutes cite moves the `photos` revision (`cited`) before the minutes are written (a failure there leaves them pending). The web and the iPhone show a cited photo's thumbnail with its item.
- **Retention.** At the transcript cut the photos the current minutes cite stay (file and entry); the others go with the transcript, as does the room with its inline copies. The trim keeps only those files of `attachments/`, `photos.json` (with the owner's deletions, so a retry still gets `410 photo_deleted`) and `photos.md`, and moves the `photos` revision when photos went. Every pass makes `photos.md` what `photos.json` lists (none without a photo), so a trim cut short between the two is finished by the next. Cited photos go with the minutes (`minutes_days`, or the delete).
- The hub's own models never see an image: the minutes and `meeting.answer` read the photos' text (`meeting.answer` the latest 8000 characters of their captions, texts and notes, with this meeting's material), and only the room's model sees the image itself. The follow-up's seed holds at most 20000 characters of the photos' recognized text, the earliest photos' first; `kb_get` has the rest.

### Phase 4: realtime voice in the meeting

- **Who.** A `participant` meeting whose room is `ready`, while the built-in voice extension runs and its `features` include `meeting`. Otherwise Talk is transcribed on the phone (below).
- **The call.** Protocol 2 ([voice.md](./voice.md) "App → extension") with a meeting mode, bound to the room:

```jsonc
{"type": "start", "protocol": 2, "mode": "meeting", "recording_id": "<id>", "target": {"machine_id": "<machine id>", "session_id": "<room session id>"}, "echo_cancellation": true}
```

  The extension checks with `GET /api/v1/recordings/{id}/live` that `target` is that recording's room and that it is `ready`; otherwise the fatal error `not_meeting_room` (a hub that cannot say, with a `5xx` or no answer, is fatal without a code, as a target that cannot be looked up is). `ready.features` include `meeting`.

  A room takes only meeting calls: a protocol 2 `start` without `mode: "meeting"` whose target is a room (origin `meeting`, which the manager says on the room's session row) is the fatal error `meeting_room`. An ordinary call bound to a room (from an older app, the web, or an iPad that opened the room as a chat) would bring `search_knowledge` and `list_agents` in front of the room. Apps that know the origin show no voice button in a room.

  What a meeting call hands the room goes as the owner's words, so the user-speech gate ([voice.md](./voice.md) "Meeting calls") always holds: a `start.speech_gate` (#5904) neither turns it off nor brings Send Anyway, and the phone sends none.
- **The fast layer in a meeting.** Its only tool is `delegate_task`, which hands work to the room; no `search_knowledge`, `list_agents` or other tool. Its opening snapshot is the room's own recent turns. Its instructions (a new prompt under `server/internal/prompts/voice/`): others hear it; it answers meeting questions only through the room and says nothing of its own about the meeting; answers are short. They take only the owner's name and the language: never the owner's voice notes (`Notes`, which every other call's instructions carry), and a resumed call's history only from its own captions. Runs it did not delegate (batches, asks, photos) are never spoken, nor quoted in its snapshot. Every delegation is a question for the room: a meeting call reads none of a bound call's prefixes (a status would speak the room's last turn, a stop would cut its work short), and its `delegate_task` is described without them. It never suspends (decision 7).
- **Hearing only Talk.** New app frames `{"type": "talk", "on": true}` and `{"type": "talk", "on": false}`. In a meeting call the app sends audio only between the two; between spans the extension feeds the upstream silence itself, so the session stays open and the model hears nothing. It does not trust the app for this: an audio frame that arrives outside a span is replaced by silence and logged, and a span left open past 90 seconds (longer than any Talk the phone opens) is closed by the extension, so a phone bug cannot stream the meeting to the voice service. A `text` frame (a typed question while the call is live) works as today. Transcription, on the phone or the hub, hears everything from the same capture, Talk spans included.
- **Talk in privacy mode** sends the Talk spans' audio to the call too (owner decision, "Decided" below): what the owner says while Talk is on, and whatever else the microphone picks up during those seconds, goes to the realtime voice service in both modes. The rest of a privacy-mode meeting's audio still never leaves the phone, and the extension's silence outside spans (above) is what keeps it so. The Talk button and the start sheet say so in every language before phase 4 ships.
- **Talk on the phone.** On the meeting page **Talk** works like every mic (#9658): a tap turns it on and the next tap ends it and sends; pressed and held it is on until let go. The span opens as the finger lands either way. A tapped Talk on the page ends after 20 seconds without speech or after 60 seconds, never on a pause (the page says why it stopped). On the Lock Screen and in the Island a tap opens a talk window (Live Activity buttons can't be held), which ends on another tap, on 2 seconds of silence after speech, after 20 seconds without speech, or after 60 seconds.
- **One capture.** The recorder holds the microphone for the whole meeting, and the call never claims it ([recording.md](../apple/recording.md), `apple/Shared/Recording/RecordingMicrophone.swift`). The recorder's single input tap, already converted to 16 kHz, feeds the segment writer, the hub uplink (phase 5) and, during Talk, the call (PCM16 at the call's `input_rate`). The call's audio plays through a player on the recorder's engine, so the system's echo cancellation can remove it.
- **A meeting call is not a phone call.** It does not end in the background, after 10 idle minutes or on an interruption (it pauses and resumes with the recording), and it does not keep the screen on. It runs while the role is `participant`, from the start or from a later change, until Stop or another role. It reconnects as protocol 2 does (`resume`); after three failed attempts the phone tries again every 30 seconds while the meeting records, and Talk is transcribed on the phone meanwhile.
- **Echo.** A meeting that starts as `participant` asks for the system's echo-cancelled input when recording starts and never switches it mid-recording; `start.echo_cancellation` says whether it is on. Either way **the echo rule** applies, stated here once for the phone and the hub ("Persona's echo" has its details and the shared cases): a transcript sentence is Persona's echo when it overlaps a span when Persona spoke, the span widened by half a second on each side, and its words are what Persona said in that span (at least 60% of its character bigrams, after normalizing, are among the answer's); a span saved without its text keeps the time rule from before #5905. Persona's echo is kept and marked, never deleted: hidden where the apps show the transcript, and left out of the live log, the minutes and the summaries. The phone marks it in its transcript and keeps it out of its live log, as #2981 requires; the hub in hub mode marks it in its transcript and keeps it out of the live log, from the spans and the answers the phone reports ("The timeline" in phase 5). The hub has the rule in one place (`modules/recording/writers/echo.go`, which also marks the upload's segments), so a refinement of it reaches every use at once. The phone keeps the meeting call's playback spans as it keeps a spoken answer's: as they played (the rule widens them, the phone never does), each with the words the call's voice said in it, its own captions ([recording.md](../apple/recording.md) "Realtime voice (phase 4)").
- **Talk without a call** (a listener, no voice extension, or the call down): the phone cuts its segment at the span's start and end, transcribes the span on device and asks with `…/ask` (`question`, `spoken: true`); a participant hears the answer as #3746 does: in the realtime voice through the voice extension's `speak` when it can, else in the phone's on-device voice (#5004).
- **Typed questions** go as `text` frames while the call is live (the fast layer hands them to the room and says the answer), otherwise through `…/ask`.

### Phase 5: hub transcription

- **Language is a hint, never a lock.** The recording's `language` (the upload's tag) is a hint only. The hub, and every contribution that transcribes for it, writes each word in the language it was said in: Chinese as Chinese, English as English, a sentence that mixes them mixed. It never translates, never normalizes to one language and never drops words for being in another language. The built-in adapter sends no language setting to the upstream ("The built-in adapter"), and the phone's text, made in one language, only fills where the hub has none ("The transcript of record").

#### Choosing the mode

- **Upload**, with `kind: "meeting"`: `"transcription": "device"` (the default) or `"hub"`, from the start sheet. A `transcription` that is not `"device"` or `"hub"` (another string, another type, `null`) names no mode and never fails the upload. The hub takes the mode of the first upload that names one and ignores later values: the mode never changes mid-recording. It is kept as it was decided, also when it was `device` because the hub was `unavailable`, and also when the hub becomes ready later.
- **The `200`.** From a manager with `meeting_transcription`, the `200` of every `kind: meeting` upload carries `transcription`, the mode in effect (`device` when none was named), and `transcription_problem` when it is not the mode asked for: `unavailable` (the chosen contribution is not ready, "The `transcription` extension point") or `not_meeting`. A `200` without `transcription` is an older manager: the phone records that meeting in `device` mode and opens no audio socket. After such a `200` mid-meeting (a rollback) the phone stops the uplink and keeps `device` mode for the rest of the recording; audio a newer manager stored is deleted by its next retention pass once it is back, and by the trim.
- **Other kinds.** On a `day` or `note` upload `transcription` and `timeline` are ignored; a named `hub` is answered `transcription: "device"` with `transcription_problem: "not_meeting"`, and nothing is stored.
- **Settings** in `GET` / `PUT /api/v1/assistant`. Changing any of the first three takes full credentials (`403 forbidden`), as `meeting_attendance_default` does.
  - `meeting_transcription_default`: `device` until changed, or `hub`; another value is `400 bad_transcription_mode`.
  - `meeting_glossary`: names and words the transcription should spell right, a list of strings (`[]` by default and to clear it). Each is trimmed and put on one line, and blanks and repeats (case-insensitive) are dropped; more than 200 terms or a term over 40 characters (Unicode characters) is `400 bad_glossary`.
  - `meeting_transcription_extension`: which contribution transcribes. `""`, the default, is the built-in voice extension's; otherwise `"<extension id>/<contribution id>"` naming an installed extension's `transcription` contribution, and anything else is `400 bad_transcription_extension`. One that is off is taken (`meeting_transcription_ready` is then `false`).
  - `meeting_transcription_ready`, read-only (ignored on `PUT`): the chosen contribution can transcribe ("Which one transcribes" below).
- **`persona_settings`**: `assistant.meeting_transcription_default`. `get` says the default and whether hub transcription is ready; `set` takes `device` or `hub` and confirms with a card that says hub mode sends the meeting's audio to the hub's transcription service and that the hub deletes it once the minutes are written (and, when not ready, that the start sheet offers it only once it is). A hub without a database refuses it. The glossary and the extension change only through the API (the iPhone's and the web's settings), never through the assistant's tools.
- The start sheet offers **On the hub** only with `meeting_transcription` and `meeting_transcription_ready`, and its privacy line names the extension when it is not the built-in one. One-tap starts use the default, or `device` while the hub is not ready.

#### Audio from the phone

`GET /api/v1/recordings/{id}/audio`, upgraded to a WebSocket. The phone authenticates as on every route, `Authorization: Bearer <device token>` (`?token=`, as on `/api/v1/ws`, is also taken and never reaches the manager). The hub relays the upgrade over a relay stream to the default manager, as it relays extension sockets, with the caller's role in its headers; revoking the device ends the socket.

- **Refusals.** Before the upgrade the socket is refused only for the caller and the manager: `403 forbidden` (answer-only and extension tokens), then `503` (`assistant_offline`, or the knowledge base off or unavailable). The phone's WebSocket reads only a refused handshake's status, so the recording's own refusals come as the first frame after the upgrade, a fatal `error`, checked in this order: `recording_deleted`, `recording_not_found` (upload first), `kb_read_only`, `not_hub` (the recording's mode is not `hub`), `transcript_expired` (trimmed), `transcription_final` (the final pass has started).
- **One socket per recording.** A new one replaces the one before, which gets the fatal `error` `replaced` and is closed.

Phone → hub:

| Frame | Meaning |
| --- | --- |
| `{"type": "start", "at": 0.0, "timeline": {…}}` | First frame, within 10 seconds of the upgrade: `at` is the oldest second the phone still holds, in seconds of the recording's own clock (the writer's frame position, which leaves paused time out, the same clock as `segments`); `timeline` is the whole timeline so far ("The timeline" below) |
| Binary | PCM16 little-endian, mono, 16 kHz, contiguous, whole samples; about 100 ms a frame, at most 64 KiB; none before `ready` |
| `{"type": "seek", "at": 312.4}` | The next binary frame starts at `at` |
| `{"type": "role", "at": 0.0, "attendance": "participant"}` | The role from `at` on, at the start and on every change: only sentences that end while it is `listener` or `participant` join the live log |
| `{"type": "speaking", "on": true, "at": 1834.2, "text": "…"}` | Persona starts speaking at `at`, saying `text` (the answer); sent as playback starts, so ahead of the audio of that moment, and right after `start` for a span still open when the socket starts |
| `{"type": "speaking", "on": false, "at": 1840.9}` | Persona stopped speaking at `at`: the echo rule ("Echo" in phase 4) applies to the span, against its `text` |
| `{"type": "end", "at": 3733.5}` | The recording stopped at `at` |

Hub → phone:

| Frame | Meaning |
| --- | --- |
| `{"type": "ready", "from": 312.4, "gaps": [[0, 4.2]], "next": 57}` | Send from `from`, the end of the latest audio the hub holds (`0` with `gaps: []` for a fresh recording); `gaps` are earlier spans it lacks; `next` is the live log's length |
| `{"type": "ack", "at": 330.0}` | Stored up to `at`, the end of the contiguous audio the hub holds around the latest frames; about every 5 seconds. It says nothing about transcription |
| `{"type": "interim", "start": 328.1, "text": "…"}` | The caption being said now, replaced by the next one |
| `{"type": "line", "n": 57, "start": 320.2, "end": 326.0, "text": "…"}` | One of the hub's own final sentences; `n` is its position in the live log |
| `{"type": "problem", "code": "upstream_forbidden"}` | Transcription is degraded (an open set: `upstream_forbidden`, `upstream_busy`, `upstream_unavailable`, `upstream_failed`, `no_interim`; "Problems" below); audio is still stored |
| `{"type": "problem", "code": null}` | Transcription is back: the hub's `line` frames go on from the next position |
| `{"type": "error", "code": "replaced", "fatal": true}` | The socket closes after a fatal one ("Errors" below) |

- **Handshake and positions.** The phone sends no binary frame until `ready`; after it, the audio starts at `ready.from` unless a `seek` comes first. A `seek` past what the hub holds leaves the span between as a gap; audio the hub already holds is ignored, so a `seek` back is harmless. Audio behind the live edge (a hole filled late) is stored for the final pass and not transcribed live. Times are recording seconds, rounded to a sample (1/16000 s).
- **Limits.** A binary frame is whole samples, at most 64 KiB; a text frame at most 8.5 MiB (a whole timeline fits: 1000 speaking spans with 4000 bytes of answer each and 100 role changes, also when JSON escaping doubles the answers' size). The hub keeps at most 12 hours of audio per recording (`audio_full` past it).
- **Errors.** `error.code` is an open set: `bad_frame` (a text frame that is not JSON or over the limit, a binary frame before `ready` or of an odd length; an unknown `type` is ignored, not an error), `replaced`, `audio_full`, the refusals above, and `recording_deleted`, `kb_read_only`, `transcript_expired` and `transcription_final` when they happen while the socket is open. Each is sent `fatal: true`, and after a fatal one the hub closes the socket (1008, or 1011 for its own failure); after `end` it closes with 1000. The phone acts on the `error` code, never on the close code; an unknown code reads as "transcription on the hub stopped for this meeting", and the phone keeps its own transcription.
- **Problems.** Right after `ready` the hub sends the `problem` that stands, if any. The upstream's kind, from the job's fatal code (#13446, `spec/fixtures/meeting-transcription-problems/`): `upstream_forbidden`, the service refused (`upstream_forbidden`: a key it won't take, a model the account hasn't turned on, a quota it says is gone); `upstream_busy`, the service is busy or rate-limited (`upstream_busy`: too many sessions, the quota used up); `upstream_unavailable`, it can't be reached or no job can be opened (the chosen contribution is not ready, no `ready` in time, or it answered `no_upstream` or `upstream_unavailable`); `upstream_failed`, anything else: the job ended with `upstream_failed` or another code, or its socket dropped and could not be opened again at once. `no_interim`: lines go on, without captions. `{"code": null}` comes once the hub's sentences come again (for `no_interim`: once captions do). Only the kind goes to the phone, never the extension's `message` (diagnostics, which may carry the service's log id); a restart reads the standing problem back from `meeting.json`. Before #13446 the hub sent `upstream_unavailable` for a refused or busy service too; a phone reads any code it doesn't know as a degraded hub with no reason of its own.
- **Lines.** `line` frames carry only the hub's own sentences and come once per window, about 15 seconds ("The built-in adapter"); captions come within a second. A `line` frame is never sent twice, also across a reconnect: the phone reads the positions it missed while away with `GET …/live?from=` (`ready.next` is the live log's length).
- **`end`.** On `end` the hub sends `ack` at `end.at`, then, within 15 seconds, the `line` frames of the last sentences, and closes with 1000.
- **Backpressure.** The phone keeps the last 60 seconds in memory whatever was acknowledged, and resends from `ready.from` after a reconnect; audio not acknowledged before it falls out of that buffer becomes a gap, uploaded after the meeting ("Gaps and the final pass"). The hub stores what it reads at once and never makes the phone wait for transcription: a job more than 30 seconds behind the live edge is moved forward with `seek`, and the final pass covers what it skipped.
- **The uplink belongs to the recording**: it runs in the background, pauses with it and ends with it. Either side may ping; the hub closes a socket that has sent nothing, not even a ping, for 60 seconds. A pause may close the socket (the app may be suspended); the phone opens it again on resume. The socket's absence while paused fills nothing, since the phone uploads no segments for that time.
- **Whatever the role.** The uplink runs whatever the role, `off` included: the role decides only what joins the live log. A sentence that ends while the role is `off` goes into the hub's transcript but sends no `line` frame and takes no position. `interim` frames go out whatever the role (they reach only this phone); `GET …/live` has no `interim` while the role is `off`.
- **No echo goes out first.** The hub holds back a sentence that ends after an open speaking span began (less 0.5 s) until the span closes, then marks it or not by the echo rule, against the span's `text`, so an echoed sentence never reaches a `line` frame, the live log or the room only to be taken back (a marked one stays in the hub's transcript). Sentences join the live log in order: one held back holds those after it. A span the timeline has closed (a socket's `start.timeline` or an upload's `timeline` repairing a `speaking` off a dropped socket lost) closes it at once; while the socket that sent its `speaking` on is up, an upload's `timeline` does not close it, since that socket's own `speaking` off will come. A span still open after 3 minutes, longer than any answer the phone says (at most 150 seconds), is assumed open from then on: the sentences it held go, and they and every sentence after them are judged against it, open to each sentence's end, with the text it carried, until a `speaking` off closes it (written to the timeline as the phone sent it, also when it comes late) or the timeline does. A `speaking` on at a later time while a span is open means that span ended unseen: it counts as closed at the new span's start for the sentences it held. A span still open at `end` counts as closed at `end.at`: the sentences it held are judged then, and their `line` frames go out before the socket closes. The hub never writes an end it assumed into the timeline, which keeps only what the phone sent. While a speaking span is open, assumed or not, the hub sends no `interim` frames.
- **The timeline.** Frames are lost while the socket is down, so the phone keeps the recording's role changes and speaking spans with its audio and sends them whole, `{"roles": [{"at": 0.0, "attendance": "participant"}], "speaking": [{"start": 1834.2, "end": 1840.9, "text": "…"}]}` (each span as the upload's `spoken` has it, with the answer said in it): in every socket's `start` frame (a reconnect repairs what the socket missed) and as `timeline` on every hub-mode upload (`PUT /api/v1/recordings/{id}`), the `complete: true` one included. `timeline.speaking`, in `start` and in uploads alike, holds only spans that have ended, unlike `spoken`, which may include the one being said, ended at the upload's time: the hub reads a span in a timeline as one whose end is known. A span still open when a socket starts is not in `start.timeline`: the phone sends `{"type": "speaking", "on": true, "at": …, "text": "…"}` right after `start`. The final pass waits for the `complete: true` upload, so gap uploads need no timeline of their own.
  - The hub keeps the union in `meeting.json`: one role change per `at` (the latest received wins), and overlapping or touching spans merged, their texts joined in time order (a span received again counts once). The role at a moment is the timeline's last role change at or before it; before the first one, the meeting's role (`room.attendance`).
  - It applies the timeline to every sentence it makes, live, from gaps and in the final pass: the role decides only what joins the live log, and the echo rule ("Echo" in phase 4) marks Persona's echo in the hub's transcript too, which leaves it out of the minutes and the summaries.
  - Every `at` is a number of 0 or more, every span has `start <= end` and a string `text` if any (cut at 4000 bytes, as in `spoken`), and roles are the three of #2981; a timeline with any other entry, or over 100 role changes or 1000 spans, is ignored whole and never fails the upload. The limits apply to the union too: past them the hub keeps the first 100 role changes and 1000 spans.
- **In hub mode the live log is the hub's.** `POST …/live` is `409 hub_transcribes` (checked right after `409 not_attending`), and `…/ask` ignores `live_next`: the hub sends the job a `flush` and waits, at most 15 seconds, until its sentences up to `at` are final. `lines` carry `source`: `hub`, or `device` for the fallback below; it is absent in device mode.
- **When the hub has no sentences, the phone's text fills the live log.** The room, the topics, other devices and heard asks depend on the live log, and the phone keeps transcribing on device in hub mode, so a failing upstream must not leave them with nothing. The fallback starts when the audio socket has been down for 30 seconds (it counts as down from the first upload in hub mode until it first sends `start`, so a socket never opened counts too), when the extension ends the job with a fatal `error`, or when a `problem` other than `no_interim` has stood for 30 seconds:
  - the manager appends the upload's segments to the live log: each one that ends after the hub's last sentence and that the hub's sentences, taken together, cover no more than half of, only if it ends while the role is `listener` or `participant` and it is not Persona's echo (marked by the phone, or by the echo rule against the timeline; positions continue the hub's; `source: "device"` on `lines`);
  - the phone in hub mode therefore keeps writing 15-second segments and uploading them, as phase 2 does;
  - a heard ask waits, at most 15 seconds, for an upload whose segments reach `at`;
  - after a fatal `error` the manager opens a new job every minute while the meeting records.

  Once the hub's sentences come again it sends `{"type": "problem", "code": null}`, its `line` frames go on from the next position, and a hub sentence more than half inside the spans already filled from the phone, taken together, is left out of the live log (it still goes into the hub's transcript).

#### Gaps and the final pass

- `PUT /api/v1/recordings/{id}/audio?at=<seconds>` uploads audio the hub lacks: an `application/octet-stream` body of PCM16 16 kHz mono, at most 10 MiB (about 5 minutes) a request, which the phone decodes from its own segments; `at` is a decimal number of seconds, rounded to a sample. → `200 {"gaps": [[from, to]]}`, what is still missing; parts the hub already holds are ignored, and audio past the recording's end is taken up to the 12-hour limit. Errors, checked in this order: `403 forbidden`, `503`, `413 too_large`, `410 recording_deleted`, `404 recording_not_found`, `400` (`at` missing or negative, a body that is not whole samples), `403 kb_read_only`, `409 not_hub`, `409 transcript_expired` (trimmed), `409 transcription_final`, `413 audio_full` (past 12 hours of audio).
- **The recording's end and its gaps.** The recording's end is the socket's `end.at`, else the `duration` of the `complete: true` upload or the end of the audio the hub holds, whichever is later; an ended hub-mode recording's stored `duration` and `ended_at` are likewise the longer of the upload's and the audio held (#9587: a phone killed mid-segment loses the segment it was writing and ends the recording before it, while the hub got that audio over the socket). `gaps` are the spans of `[0, end]` (during the meeting `[0, ready.from]`) the hub holds no audio for, merged when less than 0.1 s apart. Once the final pass has run, `gaps` are those it had no audio for, and they stay after the audio is deleted.
- **When the final pass runs.** It starts once the recording is complete, no audio socket is open (or it sent `end` and got its last `line` frames) and no gap is left, or 30 minutes after the `complete: true` upload, over the audio the hub holds ("The built-in adapter"); gaps left keep the phone's text. A hub-mode recording that never gets a `complete: true` upload counts as complete for the final pass 12 hours after its last upload (`feedStale`, as in "The meeting's end"), and loses its audio once that pass stored its text. From the pass's start the audio socket answers `transcription_final` and `PUT …/audio` `409 transcription_final`.
- **How it ends.** A pass that fails is tried again twice (after 1 and 5 minutes) from its first window not done; then `state` is `final` with the live sentences, or `failed` when the hub has no sentence of its own. A try that never reached the service (`upstream_unavailable` or `upstream_busy` with nothing billed: the hub machine's route to it was down, or the service turned the job away) costs nothing, so it is counted apart and tried again after 1, 5, 15, 30 and 60 minutes, about 1 h 50 min in all; a failed try after such ones still has its two retries (#9535). A replay job that sends nothing for 5 minutes (it keeps its socket open, its upstream stalled) is a failed try. The recording's delete or trim ends a pass at once, its replay job closed. A recording with no audio at all is `failed` at once. A contribution without `replay` makes the live sentences `final`.
- **`transcription`** on `GET …/live` and in `detail.meta`, for every recording whose mode was decided: `{"mode": "device"}`, or `{"mode": "hub", "state": "live", "gaps": [[600.0, 642.5]]}` with `problem` (the socket's standing problem) and `fallback: true` (the live log is being filled from the phone) while they apply. `state` is `live`, `finishing` (complete: the final pass is due or running), `final` or `failed` (the hub made nothing usable; the phone's text is the transcript). `gaps` is always present, `[]` when none. A trimmed recording keeps `mode` and `state`, with `gaps: []`. While the final pass runs (`finishing`, once it has started, also while it waits for its next try) and the recording's end is known, `final_done` and `end` say how far it is (#13461): the recording seconds it has finished (the end of its last window, `0` as it starts and again after a restart, which starts over) and the recording's end, `final_done` at most `end`; neither otherwise. The manager writes `final_done` to `meeting.json` as each window ends, so it moves the transcription's revision about once a minute.
- **Its revision, and `interim`.** `transcription` moves (in `revs` and `meeting_live`) when its `mode`, `state`, `gaps`, `problem` or `fallback` change, not when only the timeline grows; `?parts=transcription` returns it with its revision. `interim`, on `GET …/live` only, is `{"start", "text"}`: the latest caption while the meeting records, the role is not `off` and the caption is at most 10 seconds old; absent otherwise. `?parts=interim` returns it; `meeting_live` adds `interim`, at most once a second, with `revs.interim` the time the caption came, in milliseconds.
- **Where the hub keeps it.** The audio goes in `recordings/<id>/attachments/audio/` (PCM chunks and the index of the spans held), where knowledge-base backups ([knowledge.md](./knowledge.md) "Backups"), whole-install backups ([server.md](../server/server.md) "Whole-install backup"), Web development update snapshots ([hub-update.md](../operations/hub-update.md) "Web development updates"), and search never look, so no backup taken during a meeting outlives the deletion below; tests check that backups taken while hub audio exists hold none of it. The hub's own transcript is `hub_segments.json` beside `meta.json`, which search skips as it skips `segments.json` (`transcript.md`, written from the transcript of record, is the searched text).
- **Deleting the audio.** The hub keeps no meeting audio (owner decision, "Decided" 5). It deletes a recording's audio once its `state` is `final` or `failed` and either the minutes made from that transcript are written (a quiet remake counts), its minutes end `failed`, or it gets no minutes (not a meeting, or no text). Every retention pass deletes audio that should be gone (a crash between the steps). The trim and the recording's delete remove it too, whichever comes first, so it never outlives `recording_days`. A regenerate later needs only the transcript. The phone keeps its own audio as today, until `days` or the owner's delete, and the hub's `state` never extends that: a hub that never reaches `final` cannot make the phone keep audio longer. Within `days` the phone keeps a hub-mode recording's audio at least until `state` is `final` or `failed`, for gap uploads; gaps still open at `days` keep the phone's text.

#### The transcript of record

- Device mode: the upload's `segments`, as today.
- Hub mode: the hub's sentences (the final pass's, else the live ones), with the phone's uploaded segments where the hub has none: a phone segment is used when the hub's sentences cover no more than half of it, counting the union of their spans (so several short sentences that each cover a little count together, and nothing is said twice). The phone's text is made on the device in the recording's language only, so in a mixed meeting it is the weaker text: it fills only where the hub has none.
- `detail.segments`, `transcript.md`, search, `kb_get`, the minutes, the short summary (made again after the final pass) and the follow-up's seed use it, and each segment gains `source` (`hub` or `device`; absent means `device`). A `source` in the upload's `segments` is ignored: only the hub sets it. Persona's echo stays in it marked (`echo: true`: the hub's sentences by the echo rule, the phone's as uploaded) and is left out of the minutes, the summary and the seed as in device mode ("Persona's echo"). The upload still owns `segments`, `complete` and `revision`; the hub keeps its text apart, in `hub_segments.json`, with its own revision.
- In hub mode every check of whether a recording has text (the minutes, `POST …/minutes`, `409 no_material`, `chars` in `detail.meta` and the lists) counts the transcript of record without Persona's echo, so a meeting whose phone wrote nothing still gets its minutes.
- In hub mode the minutes wait for `state` `final` or `failed`, at most 45 minutes after `complete`; a later final pass makes them again without a second push. Once hub transcription has ended (`final` or `failed`), the minutes also wait while `speakers.state` is `waiting`, up to the later of 10 minutes after the final transcript was stored and "audio length ÷ 5 + 2 minutes" after `since` (not extended by a restart; at the limit `speakers.state` becomes `failed` with reason `timeout` and the minutes proceed without numbers; "Speakers (#17674)").
- **What the apps say while they wait** (#13461, `spec/fixtures/minutes-wait`). While `minutes_state` is `pending` and no minutes are shown, the iPhone, Android and web say the hub is checking the whole recording as long as `transcription` is `hub` and `finishing`, with the minutes done of the total once `final_done` and `end` come ("Checking the whole recording (40 of 105 min)…": `end / 60` rounded, at least 1; `final_done / 60` rounded down, at most that), and "Writing the minutes…" otherwise. An older hub sends no numbers, and the line says only that it is checking. HarmonyOS keeps "Writing the minutes…".

#### The `transcription` extension point

A new point, version 1, modeled on `voice`: `contributes.transcription: [{"id", "name"?}]`, needing `runtime.http`. The built-in voice extension contributes one that shares its upstream and key ("The built-in adapter"). The manager writes the live log and the hub's transcript; the extension stores nothing. Its row in [extensions.md](../extensions/extensions.md) "Extension points" and in the manifest schema comes with the code.

- **Which one transcribes.** Hub mode uses the built-in voice extension's contribution, and another extension's only when the owner names it in `meeting_transcription_extension` ("Choosing the mode"): installing an extension never sends it meeting audio. `meeting_transcription_ready` is true while the chosen contribution's extension is enabled and running and its `/info` answers protocol 1 with `live` and no `setup` (read at most every 30 seconds; an answer stands while it is read again, so only another choice makes it unknown); while it is not ready hub mode is unavailable, with no fallback to another contribution. The choice is pinned to the installation the owner named: an extension uninstalled and installed again, or another one installed with its id, is not ready and gets no job until the owner names it again. The pin holds while the extension stays installed: across upgrades, rollbacks, a rename by its market (its old id still names it) and the manager's restarts. A changed choice ends the live jobs running: the rest of a meeting's audio goes to the contribution chosen now, or to none while it is not ready (`upstream_unavailable`, and the phone's text fills). The same choice also transcribes the audio of extensions granted `transcription` (their own audio, in `replay` mode, `docs/extensions/extensions.md` "Transcription for extensions"): one job per extension and two across extensions at a time, meetings' jobs not counted.
- **Endpoints**, on the extension's own socket under the contribution's id, so they never meet the voice point's `GET /info` and `GET /ws` (which stay as they are, with the voice protocol and its `features`):
  - `GET /transcription/{cid}/info` → `{"protocol": 1, "features": ["live", "replay", "interim"], "setup"?: …}`: `interim` only when the upstream offers interim captions; `setup` when it cannot transcribe: as the voice extension's when no upstream is usable, and `live_upstream` when none is GPT-Live ("The built-in adapter").
  - `GET /transcription/{cid}/ws`: one job per connection.
  - Both answer `404` for a `cid` the extension does not contribute. Only the manager dials them. The apps' forward to an extension (`/api/v1/extensions/<id>/…`) refuses paths under `/transcription/`.

Manager → extension:

| Frame | Meaning |
| --- | --- |
| `{"type": "start", "mode": "live", "language": "zh-CN", "glossary": ["…"], "at": 0.0}` | `mode` `live` (real time) or `replay` (faster, with `"speed": 4`). `language` is the recording's language tag, a hint only ("Language is a hint, never a lock"); `glossary` below |
| Binary | PCM16 little-endian, mono, 16 kHz, contiguous from `at` |
| `{"type": "seek", "at": …}` | The next binary frame starts at `at` |
| `{"type": "flush"}` | Finish the sentences up to now as soon as possible (an ask needs them); answered by `flushed` |
| `{"type": "reference", "from": …, "to": …, "text": "…"}` | Replay only: one segment of the transcript of record before the pass, to check the sessions against ("The built-in adapter") |
| `{"type": "end"}` | No more audio: finish, send `done`, close |

Extension → manager:

| Frame | Meaning |
| --- | --- |
| `{"type": "ready", "features": ["live", "interim"]}` | Accepted, within 30 seconds of `start` (else the job counts as `upstream_unavailable`) |
| `{"type": "interim", "start": …, "text": "…"}` | The current caption |
| `{"type": "sentence", "start": …, "end": …, "text": "…"}` | A final sentence, in order, in recording seconds |
| `{"type": "ack", "at": …}` | Audio taken up to `at` |
| `{"type": "flushed", "at": …}` | After a `flush`: every sentence that ends before `at` has been sent |
| `{"type": "window", "from": …, "to": …, "kept": false}` | Replay only: every sentence of this window has been sent. `kept: true`: no session was trusted, and the hub's text for the window stays |
| `{"type": "usage", "seconds": …, "sessions": 3}` | Diagnostics: the upstream's billed seconds of this job so far, all sessions together |
| `{"type": "warning", "code": "session_dropped", "message": "…"}` | Diagnostics, an open set: `session_dropped`, `no_interim`, `behind` |
| `{"type": "error", "code": "upstream_failed", "message": "…", "fatal": true}` | An open set: `no_upstream`, `upstream_forbidden` (the service refused), `upstream_busy` (busy or rate-limited, #13446), `upstream_unavailable` (out of reach), `upstream_failed`, `bad_frame` (a frame it cannot read); the manager passes the kind on to the phone ("Problems"), never the `message` |
| `{"type": "done"}` | Every sentence sent |

- **Language.** A contribution writes every word in the language it was said in ("Language is a hint, never a lock"): it uses `language` as a hint, never as a setting that would translate or filter what it hears.
- **The glossary.** The manager sends one `glossary`: the meeting's title, its calendar title and its attendees' names, then `meeting_glossary`, deduplicated, at most 256 terms of at most 80 characters. It is data: a contribution uses it for spelling only, never as instructions (a calendar title is written by whoever organized the meeting).
- **The final pass's text.** The manager sends the whole transcript of record before the pass as `reference` frames, one per segment in time order, right after `ready` and before the first binary frame; the contribution cuts them by window. The final transcript is the pass's sentences in its windows, and the hub's live sentences in `kept` windows and wherever the pass did not get to.
- **Flow control.** The manager keeps at most 30 seconds of audio (live) or 60 seconds (replay) sent ahead of the extension's last `ack`; the contribution paces a replay at `speed` itself.
- **The job's life.** A dropped job socket is opened again at once from the live edge; a job with no audio for 60 seconds while the phone's socket is up (a slow link), or for 10 seconds once it is gone without `end` (dropped, paused, the app ended by the system), is ended, and opened again when audio comes, from the live edge (#9198: its upstream sessions bill silence by the second, three at once in the voice extension, and a phone that died mid-meeting kept them billed for the whole minute). A job that stays connected but acknowledges nothing for 30 seconds after the manager began skipping its audio (its upstream hangs) is closed as failed: `upstream_failed` stands, so the fallback comes, and a new job is opened with the next audio. A job is not a call: it is not counted in the voice extension's `/health` `conversations`, and the audio socket does not hold a manager's redeploy (the phone resends from `ready.from`). `message` is diagnostics and never carries meeting text.
- **Usage.** The manager logs each `usage` with the recording id and keeps the total with the transcription, for cost; it is never shown to apps.

#### The built-in adapter

Part of the built-in voice extension, and it speaks only to a GPT-Live-compatible upstream (decision 5). It contributes `{"id": "gpt-live", "name": "GPT-Live"}` and transcribes with the voice extension's default model when that model's upstream speaks the GPT-Live (`live`) dialect, else with the default of the first `live` upstream; with none (`codex` and `realtime` upstreams differ in their wire and captions), `/info.setup` is `"live_upstream"`. It sends no language setting to the upstream: the recording's language reaches its sessions' instructions only as the language usually spoken, and picks the Chinese script (Simplified for `zh-CN` and `zh-Hans`, Traditional for `zh-TW`, `zh-HK` and `zh-Hant`). The instructions are written in the recording's language when it is Chinese, English or Japanese, since that is what the model hears, else in the hub's. The numbers come from a measurement on a synthetic six-minute meeting (three voices, clean audio, 195 sessions); they are starting values, to be tuned on real meetings, which will be harder.

- **During the meeting:**
  - One **caption session**, told never to reply, with the upstream's interim-caption session extension where the upstream offers it (negotiated when the session starts). Its captions are `interim` (about 0.3–0.45 s after speech, 6.5–10% character error rate) and the reference the other sessions are checked against. The standard input-caption events are never used: they do not arrive while the model does not reply, and when it replies they are cut to the last few words.
  - **How a caption is placed.** The real service writes a caption session's whole meeting as one caption item: its `start_ms` is the first speech's, counted from the session's start rather than its first audio, and never changes; no caption is ever final; within the item the text grows through a sentence and the next sentence's text replaces it. So the adapter never reads `start_ms` and tells captions apart by their text: a frame that revises the caption being written is of it, and only a frame that clearly begins another sentence ends it, its last text staying with the window it began in, and begins a new caption where the session's audio was when that frame came (half a second earlier, moved to the start of the speech then). A frame within a second of the caption's first one always revises it (the service rewrote a caption's first word at once, two frames at the same moment); a later one revises it when its text grew it (its last words maybe revised) or rewrote its first words and kept the rest; one with little in common with it that comes later is the next sentence. A superseded hypothesis is never a caption of its own. A service that gives each sentence an item of its own, or marks the last caption final, is handled the same way; a final caption of the item replaces the caption's interim text whatever it says ("twenty five", then a final "25."). `interim` carries the caption's start.
  - Two or three **verbatim sessions**, three when there are no captions: text-only output, told to write out each sentence word for word once it ends, in the language it was said in, and nothing else, with the glossary. They measured 0.7–1.2% character error rate in most runs, about 1.5 s after a sentence ends; the glossary does not help the captions. They ask for the interim-caption extension too and ignore its captions: with it the service ends a turn at each sentence, and without it, with instructions it did not follow sentence by sentence, it wrote several sentences in one late reply (one 23-sentence reply at the end of a two-minute meeting). A service that refuses the extension gets them without it. Their instructions follow the wording measured to give one reply per sentence, 0.05–0.3 s after it ends: a meeting's verbatim transcriber that nobody is talking to and that, each time someone finishes a sentence, outputs only its words.
  - **Windows.** Speech is cut into windows of about 15 seconds, closed at the first silence after that (at once on `flush`, then `flushed`). A window is decided once every session had its audio and 3 seconds more (on `flush`, `end`, a hole or a stall: 1.5 seconds of silence the adapter adds), and every verbatim session has begun its reply to the window's last speech, waiting at most 5 seconds for that after the session got the window's end: replies come 0.05–0.3 seconds after a sentence ends with the measured wording, and up to 3 seconds otherwise. Each reply is of one turn, the first speech it had not covered that was over when the reply began, however soon before (speech parted by less than half a second is one turn): a reply that claimed the sentence before its own would leave its session a sentence behind and, across a window's end, put its text in the window before, where it duplicates a sentence, leaves the next window to the captions and gets a healthy session replaced. In each window, every verbatim session's text is compared with the captions (normalized character edit distance), and a session above 0.5 is dropped for that window: healthy sessions measured 0.25–0.3, and one that began to play a meeting participant and invent replies measured 0.89. Of the rest the **medoid** wins, the session whose text is closest to the others'. Its sentences are sent, with times spread over the window's speech (the adapter's own energy-based speech detection; about a second is precise enough for the minutes and jumping to a line). Without captions it is the medoid of three, dropping one that is above 0.5 from both others. With no verbatim session left, the captions' text is sent. A window the speech detection found no speech in (a quiet room, a phone on the table) is decided too: its sessions are checked against each other, not the captions, nobody is counted dropped in it, and its sentences are spread over the whole window.
  - A session dropped in two windows in a row is closed and replaced by a fresh one, since an invented role persists in its context. Sessions are rolled over before the upstream's session limit (25 minutes, or 2 minutes before the end `session.started`'s `expires_at` gives, whichever is first), overlapping by 10 seconds, and a lost connection (or a session that does not start, at the job's start too) is retried three times and replayed from the adapter's 60-second buffer. Only verbatim sessions are replayed, at up to 4× real time; a caption session opened again starts at the live edge and never gets audio faster than real time (captions break faster than that), so the windows before it keep the captions of the session it rolled over, or have none after a reconnect. A caption session more than three seconds behind the live edge (audio that came in a burst: the manager's backlog after `ready`, a resend) skips to the edge rather than staying behind, which would hold back every caption from then on; a window whose audio it skipped has no captions.
  - Merging sentences across sessions did not beat one healthy session, so nothing is merged: the extra sessions buy robustness, not accuracy.
- **The final pass**, after the meeting: the stored audio at 4× speed through verbatim sessions, with no captions (they break faster than real time), in windows of at most 6 minutes cut at silences, each window in fresh sessions given the previous window's last sentences, and ended by a `window` frame. It measured 0.8% and 2.3%, in about 115 seconds for six minutes.
  - **The reference** is the window's text in the transcript of record before the pass (the hub's live sentences, with the phone's segments where they have none), from the manager's `reference` frames. Windows are also cut where the reference starts or stops covering speech (more than 10 seconds of speech with no reference text), so a window's reference covers all of its speech or none of it; a partial reference never drops a good session.
  - **With a reference**: two sessions, three when it is set aside. A reference much shorter than the text of the sessions that wrote any (under half their lower median length, or under 2 characters per second of speech while they wrote more; a session that wrote nothing does not make it long enough) counts as none, before any session is checked against it, and the window is chosen as without one among the sessions that wrote text (of two that agree, the one that wrote more): a session near a few words of the window is no better than one that heard all of it. One session is never taken uncorroborated: with fewer than two of them agreeing, the window gets a third session, as a window without a reference has, and is chosen again so (still fewer than two: `kept: true`). Otherwise a session is dropped when its text is more than 0.5 from the reference, or when it stops more than 10 seconds of speech before the window's end (a 2× run fell behind and lost its last sentences); the medoid of the rest wins (of two, the one closer to the reference). With none left, sessions within 0.3 of each other are trusted over the reference; else it stays (`kept: true`). A live caption that caught a few words of a window must not keep out sessions that heard all of it: measured, a 22-character caption over 14 seconds of speech lost the meeting's first 18 seconds, against two sessions of 164 characters in English and, in Chinese, one session of 204 characters and one that wrote nothing.
  - **Without a reference** (audio that only gap uploads brought, where the phone's transcription has nothing either): three sessions, chosen as live without captions: one that stops early, or is more than 0.5 from both others, is dropped and the medoid of the rest wins; with fewer than two left the window gets no hub text (`kept: true`).
- Cost: about four sessions for the length of the meeting, then two or three for a quarter of it, reported in `usage` (the upstream's usage events, summed over the job's sessions).

#### On the phone

- The uplink is a second consumer of the recorder's one input tap ("One capture" in phase 4), stamped with the writer's frame position.
- On-device transcription keeps running in hub mode: it fills gaps and is the upload's `segments`.
- Cellular data is allowed for the uplink. With the setting **Hub transcription on Wi-Fi only** (off by default) and no Wi-Fi, the phone sends no audio, reports that time as a gap, and uploads it with `PUT …/audio?at=` once on Wi-Fi ("Decided" 4). The socket meanwhile counts as down, so the live log is filled from the phone's segments ("When the hub has no sentences, the phone's text fills the live log"). The phone asks the system to keep the socket and the gap uploads off cellular and expensive networks then, so a path that changes after its own check never carries the audio, and it treats a network it doesn't know yet as not Wi-Fi. Through a tunnel to the hub (SSH, a local proxy, an in-app tunnel), whose own connection that request doesn't reach, it sends audio only while its path is Wi-Fi and has been since the tunnel opened, and stops a socket or a gap upload as the path leaves Wi-Fi.
- Only audio recorded while the recording was a hub-mode meeting leaves the phone. A recording that was a note before it became a meeting, or between two meeting stretches, keeps that stretch's audio on the phone: the uplink isn't running then, and the gap uploads skip it, so the hub's gaps there keep the phone's text (and the final pass starts 30 minutes after the `complete: true` upload). After a rollback (an older manager's `200`) the phone keeps the recording in `device` mode for good, also across a relaunch: no socket and no gap uploads. After hub transcription stopped for the meeting (a fatal `error` the phone stops on, an unknown code included, or a refused handshake; not `replaced`, which only means another socket has the meeting), its hub-mode time ends there, also across a relaunch: no socket, and no audio recorded from then on goes up as a gap.
- The meeting call's voice says its answer as its words stream in, so its `speaking` on carries no `text` (the hub applies the time rule to the span); the span goes into the timeline with the whole answer once it ended. An answer the phone says (`MeetingSpeaker`) carries its text.
- The page and the Live Activity show the hub's `interim` and `line` frames while the socket is up and no `problem` other than `no_interim` stands, the phone's own lines otherwise, with a notice while transcription has fallen back: why, from the standing `problem`'s kind (the speech service, by name, refused, is busy, can't be reached, or had an error) with what to do (check its console; it tries again by itself; check the hub machine's network), and on a second line that this iPhone's transcript shows, or why it can't (#13446). While the role is `off` they show the hub's `interim` and the phone's own lines. A pause, the end after Stop and a reconnect of a few seconds are no fallback. On the meeting page they are in the Live box; under the Highlights card (phase 2b), which shows no lines, they are on the Full Transcript page, and the notice is also on the card.
- The privacy texts change per mode ("Privacy per mode"): the microphone usage descriptions in `apple/project.yml`, the meeting intent's description in `apple/LiveActivity/RecordingIntents.swift`, the Recordings footer in `apple/Persona/Recording/RecordingsSection.swift`, the site's privacy page (`site/public/privacy/index.html` and its English sibling `site/public/en/privacy/index.html`) and [recording.md](../apple/recording.md) "Why send only text", in every language.
- The iPhone's UI tests run against the mock hub's hub mode ([testing.md](../operations/testing.md) "Meeting transcription").

### Phase 6: talk about this meeting

- **Text.** **Talk about this meeting** on any recording's detail (iPhone and web) and on the meeting page after Stop opens the one thread and writes into the follow-up (`POST …/followup`), for any recording with text or minutes.
- **Voice.** From the same places, a voice call bound to the follow-up: ordinary protocol 2 (`target` the follow-up, no meeting mode, since the owner is no longer in front of the room). With no follow-up yet, the owner's first request is dictated on the iPhone and makes it through `…/followup` (as `text`, so no turn runs on the seed alone); the call then binds to it and says the answer to that request as it does for a turn already running when a call opens (its `followup-` message counts as the owner's own).
- **Any recording.** `…/followup` has no kind check: a note, or a meeting that never had a room, gets its follow-up once it has ended (its `ended_at`, or 12 hours after its last upload) and has text or minutes, without waiting for a room ("After the meeting: the follow-up"). A trimmed recording's material is its minutes only (`chars` stays in its meta, the transcript is gone), so an all-day one past `recording_days`, which keeps its recap and has no minutes, is `409 no_material`.
- **Where the apps offer it.** For meetings and notes, not for all-day recordings (kind `day`: the day recap is their page); the iPhone also leaves out its rolling recording, whose page is for pulling and saving ranges of it (what it syncs reaches the hub as a `note`). That is the apps' rule: the hub refuses no kind, and a follow-up or room a recording already has still shows its thread. The web offers it only once `GET …/live` has answered for the recording, since `hello.features` describes the default manager: a recording in another manager's knowledge base whose manager has no `…/live` (a `404`) gets no composer.
- **Other meetings by voice.** The voice `search_knowledge` tool gets an optional `kind` (`recordings` for meetings; the same kinds as `kb_search`, its description a prompt under `server/internal/prompts/mcp/voice/`), and `GET /api/v1/kb-search` takes `kind` to search one kind as `kb_search` does, the lines and `results=1` alike. Another `kind` is a `400`, which the voice tool reports as a failed search; a hub from before ignores `kind` and searches every kind.
- **The minutes push** carries the recording id, a new optional field: the manager's notification has `recording_id`, and the hub puts it at the top level of the push payload and on the WebSocket `notify` (beside `machine_id` and `session_id`, which still name the main chat), only when it is a UUID. Apps that know it open the meeting's thread, others the main chat as today; an older hub drops it, so its pushes open the main chat.

### The meeting card

The meeting page is pushed from the drawer's **Recording… Open** row while a meeting records (the row is **Recordings** otherwise, #5517), the Live Activity, a push or Recordings (the Persona and Agents tabs stay the only primary destinations). It is built from the design system only (tokens, `Chip`, `.surface`, `.themedButton`, and the shell composer: no second composer), with one-line button labels, strings in en, zh-Hans and ja, and dark and light screenshots through DEBUG launch arguments. It replaces the recording section of Recordings for a meeting; an ordinary recording keeps today's controls.

While the meeting records:

```
+--------------------------------------------------------+
| <  Design review                     (*) 23:41   [...] |
|    Persona takes part . On this iPhone . Private       |
+--------------------------------------------------------+
| TOPICS                                                 |
|   [done] Export cap   [now] Pricing   [ ] Hiring       |
+--------------------------------------------------------+
| HIGHLIGHTS                                             |
|   [05:12]  Keep the export cap for this release.       |
|   [18:40]  The vendor stays through Q3; the cost is    |
|            checked next week.                          |
|   [23:05]  Open: who owns the hiring plan?             |
|                                     Full Transcript >  |
+--------------------------------------------------------+
| PERSONA                       may be heard by the room |
|   You      What did we decide on the vendor?           |
|   Persona  On Nov 3 you kept them through Q3.          |
|            Design review, Nov 3                        |
|   [photo 23:12]  Whiteboard: three owners named        |
+--------------------------------------------------------+
| Just me [ ]                                            |
| [+]  [ Ask Persona...                      ]  [ Talk ] |
|        [ Pause ]      [ Bookmark ]      [ Stop ]       |
+--------------------------------------------------------+
```

The top card is **Highlights** for a meeting started with `meeting_highlights` (phase 2b): the gist of the meeting so far in the room's words, and **Full Transcript** one tap away. It shows no transcript lines: during the meeting the owner reads the points, not the words as said, which are on the Full Transcript page (the owner's call, #5891). Before the room sets the first highlights, while Persona attends and its room still gets the meeting (the role is not `off` and the hub took it, the phone still sends the meeting's lines, and the room is neither `unavailable` nor missing on an older manager), the card holds one waiting line instead:

```
| HIGHLIGHTS                                             |
|   Persona is listening. Highlights come after a few    |
|   minutes.                                             |
|                                     Full Transcript >  |
```

Where there can be no highlights (a hub without `meeting_highlights`, or a meeting Persona does not attend and that has none), the top card is the **Live** box of phase 2:

```
| LIVE                                about 20 s behind  |
|   22:58  ...so we keep the cap for this release.       |
|   23:05  Can we keep the vendor through Q3?            |
|   Full Transcript                                  >   |
```

- **A highlight** shows its words and its time from the recording's start (`at`), earliest first, all of them (at most 4). The words take at most two lines at the regular text sizes, cut at the end, and VoiceOver reads them whole; at the accessibility sizes, where the page scrolls as one, they take what they need and the time goes under them.
- **Its time** is the minutes cards' time ("Minutes cards (#5026)" in [DESIGN_SYSTEM.md](../design/DESIGN_SYSTEM.md)): `RecordingFormat.clock` in a compact accent `Chip` inside a 44 pt hit area, read as "Show at m:ss in the transcript". Its words are not a button. A tap pushes the full transcript scrolled to the first of its lines whose start is from `at` to `end` (the phone's, or in hub mode the hub's while the page shows them, phase 5 "On the phone"), with every such line lit for two seconds (`Theme.accentTint`), as the recording's page lights a line. When none of its lines starts there (the phone's own lines of a meeting the hub transcribed), it scrolls to and lights the line `RecordingMinutes.sentenceIndex` picks for `at`.
- **The card** reads its title as a header, then the highlights or the waiting line, and ends on **Full Transcript** at the trailing side. A new or changed highlight is never announced, and the switch from the waiting line to the first highlights cross-fades on `Theme.Motion.state`, at once with Reduce Motion. In the regular layout, where only the thread scrolls, a card with four two-line highlights is about two rows taller than the Live box, and the thread gives up that height.
- **While the keyboard is up** (regular sizes; at the accessibility sizes nothing folds), the card keeps only its newest highlight (the latest `at`), or the waiting line, without Full Transcript. It changes with the keyboard on `Theme.Motion.state`, as the subtitle and Topics do.
- The Full Transcript page shows the meeting's lines as in phase 2 (in hub mode the hub's lines and caption, or the phone's own with the fallback notice, as "On the phone" in phase 5 says); the highlights are the room's. They stay on the page after the role turns `off` (the room stops getting batches, so they stop changing); a meeting with none by then shows the Live box. After Stop the page goes on as the thread, as sketched below, and shows no highlights; the follow-up's seed carries them. The Live Activity and the Dynamic Island do not show highlights.

With **Just me** on, the Persona part and the composer change; Talk is hidden:

```
| JUST ME                 only on this screen, never said aloud |
|   You      Should I push back on the vendor?                  |
|   Persona  Their last two invoices were late, so...           |
| Just me [x]                                                   |
| [+]  [ Ask privately...                             ]         |
```

After Stop, the same page continues as the thread:

```
| Design review . 14:00-15:10 . Minutes ready  [Open minutes] |
| ------------------- After the meeting -------------------- |
|   You      Draft the email to the vendor.                  |
|   Persona  Here is a draft: ...                            |
| [+]  [ Ask about this meeting...               ]  [ Talk ] |
```

| Control | Where | Does | Phase |
| --- | --- | --- | --- |
| Ask (composer) | Page | A typed question to the room: `…/ask`, or a `text` frame while the call is live | 1 |
| What was just said | Page (the + menu), Lock Screen until phase 4 | A heard ask (`…/ask` with `at`) | 1 |
| Just me | Page (switch) | The composer goes to `…/just-me` | 1 |
| Role | Page (menu, and the subtitle) | Listen, Take part or Off: an upload with the new `room.attendance` | 1 |
| Private meeting | Page (menu), recording detail | `PUT …/private` | 1 |
| Photo | Page (the + menu: camera, library) | `…/photos` | 3 |
| Talk | Page (a tap until the next, or held), Lock Screen and Island (a tap opens a talk window) | The meeting call, or a spoken ask without one | 4 |
| A highlight's time | Page (the Highlights card) | Opens the full transcript at the highlight's minute, its lines lit | 2b |
| Bookmark, Pause, Stop | Page; Bookmark and Stop on the Lock Screen and in the Island too | As today | — |
| Turn Persona off | Page (menu); Lock Screen and Island while Persona attends, without unlocking (as #3375) | The role `off` | 1 |

The Lock Screen keeps within its height of about 160 pt ([live-activity.md](../apple/live-activity.md) "Pitfalls"): three short text lines and one row of at most four buttons, only the first with words.

```
+--------------------------------------------------------+
| (*) Design review                               23:41  |
|     Persona taking part . Now: Pricing                 |
|     Persona: On Nov 3 you kept them through Q3.        |
|  [ Talk ]      [bookmark]     [turn off]      [stop]   |
+--------------------------------------------------------+
```

- Under the name, Persona's role and then the current topic: the role first, so it is never cut (whoever sees the phone learns that Persona is in the meeting), and the topic after it, which may run onto a second line and is what gets cut. Beside a topic the third line takes one line, since two lines each would pass the height; without one it may take two. Cutting the role and topic to one line instead left a character or two of a Chinese topic (up to 60 characters in the state) beside the role.
- The third line is the latest answer for 60 seconds after it came, otherwise the latest live line, and "Listening to you..." while a talk window is open. Just me never appears there.
- Before phase 4 the first button is **Ask** (what was just said); with the role `off`, only Bookmark and Stop. **Turn off** stays on the Lock Screen while Persona attends, as #3375 promised for one-tap starts: the owner silences Persona without unlocking the phone. Photo is on the page only, since opening the camera needs the phone unlocked anyway.
- Dynamic Island: compact shows the recording mark and the time (a talk mark instead while a talk window is open; no repeating animation); minimal the mark; expanded the title and time, the Lock Screen's line under the name on one line (the role, then `Now: <topic>`), the third line above, and the Lock Screen's buttons.

```
compact:   (*) ......................... 23:41
expanded:  Design review                        23:41
           Persona taking part . Now: Pricing
           Persona: On Nov 3 you kept them through Q3.
           [ Talk ]     [bookmark]   [turn off]    [stop]
```

- `ContentState` gains `topic` (the current topic's title, at most 60 characters), `line` (the latest live line, at most 120) and `talking` (a talk window is open), beside `persona` and `answer`; the whole stays far under the 4 KB limit. The app updates the activity at once for an answer, a talk window or a change of phase, and at most every 5 seconds for a new line or topic. It stays local (`pushType: nil`). In the background the app has no WebSocket, so no `meeting_live`: while it records, after a `POST …/live` the hub took it reads `GET …/live?parts=topics`, at most every 20 seconds, so the topic follows the room with the phone locked.
- Talk is a `LiveActivityIntent` that runs in the app, like Ask today; Turn off stays `TurnOffMeetingAttendanceIntent`. Both are `.alwaysAllowed`, as Stop is. `persona://record/<id>` opens the meeting page. The watch's Smart Stack stays as it is.

### Privacy per mode

What leaves the iPhone:

| | Privacy mode (`device`) | Hub mode (`hub`) |
| --- | --- | --- |
| The meeting's audio | Never | To the hub, which sends it to the transcription service set up in the voice extension (GPT-Live-compatible), or to the extension the owner chose (`meeting_transcription_extension`) |
| Talk (phase 4) | The Talk spans' audio to the realtime voice service while the meeting call runs; without a call, its words (transcribed on the iPhone) through `…/ask` | The Talk spans' audio to the realtime voice service while the meeting call runs; without a call, its words through `…/ask` |
| The transcript | Made on the iPhone; text to the hub | Made by the service; the iPhone's text fills gaps |
| Questions and Just me | Text (and Just me's images) to the hub, and to the room's or the main chat's model | The same |
| Photos | To the hub and the room's model | The same |
| Spoken answers | The realtime voice: the meeting call's, else the voice extension's `speak` (#5004, the answer's text goes to the realtime voice service); the iPhone's on-device voice when neither can | The same |
| Who spoke when | None: no speaker separation | Separated on the hub from the audio it already holds; only the timeline and owner-chosen names are kept, never voice features |

Where things are kept, and for how long:

| What | Where | Kept |
| --- | --- | --- |
| Transcript: the upload, the hub's text, the speaker timeline (`speaker_turns.json`), the live log, asks, topics, highlights | The hub, in the recording's directory | `recording_text_days` (forever by default), then trimmed |
| Minutes and the speaker list (`meeting.json` `speakers`) | The hub | `minutes_days` (forever by default) |
| Photos the minutes cite | The hub | With the minutes |
| Other photos | The hub | With the transcript |
| The room conversation (lines, inline photos, questions, answers) | The assistant's computer | Deleted at the transcript cut or with the recording |
| The follow-up conversation | The assistant's computer | Like any conversation (archiving keeps it): it holds a copy of the transcript and the room thread |
| Just me | The main chat; a receipt (digest and response, no text) in `client_ops` | Like the main chat; the receipt 30 days |
| Audio on the iPhone | The iPhone | Until `days` or the owner's delete, as today, whatever the hub's state |
| Audio on the hub | `recordings/<id>/attachments/audio/`, never in knowledge-base, `personad backup`, or development-update backups | Until the minutes from the final transcript are written, or the final pass ends when its minutes fail or it gets none ("Gaps and the final pass"); at most `recording_days` |
| What the services received | The transcription and voice services | Under their own terms |

**What may be said aloud** or shown on the Lock Screen is only what the room may know: this meeting's header, its lines said while Persona attended, its photos and their notes, its topics and highlights, its earlier questions and answers, and earlier meetings' minutes with their projects' status and their action items' to-do states, only of meetings marked not private ("The private mark"). Never Just me, the follow-up, the main chat, memory, About Me, the owner's own Claude Code instructions, or anything the room's tools do not return.

**Wording.** Privacy mode says: "Audio stays on this iPhone, except while Talk is on: what is said then goes to the voice service." (Until #9658, when Talk could only be held, it said "while you hold Talk"; the meaning is the same, and the owner agreed to the change.) Hub mode says that this meeting's audio, Talk included, goes through your hub to the speech service it is set up with (named when the owner chose another extension), and that the hub deletes it once the meeting's minutes are written; the transcript and the minutes stay as long as the retention settings say. Both texts change in every language in the PR that ships phase 4, before Talk sends audio.

### Decided

The owner answered the questions this contract first left open (#3862):

1. **Meetings recorded before the mark** count as private: nothing recorded before phase 1 (or by a #3746 phone, the watch or the Mac) is said aloud until the owner marks it not private.
2. **Talk in privacy mode** sends the Talk spans' audio to the realtime voice service, as in hub mode ("Phase 4"); the privacy texts change first.
3. **Agents attending.** New phones offer no attending session: the room attends, and an Agent gets the meeting through the follow-up. #3746 phones keep choosing one.
4. **Hub mode on cellular data** is allowed (about 115 MB an hour up: PCM16 at 16 kHz, no compressed codec yet). The iPhone's setting **Hub transcription on Wi-Fi only** (off by default) changes that: on cellular the phone then keeps transcribing on device and sends no audio, reports the time as a gap, and uploads it with `PUT …/audio?at=` once on Wi-Fi, before the final pass ("Gaps and the final pass").
5. **The hub's audio** is deleted once the minutes are generated from the final transcript; the hub keeps no meeting audio. The transcript and the minutes keep their retention (`recording_days`, `minutes_days`). Details in "Gaps and the final pass" and "Privacy per mode".

## Phases

1. **This document and the field contract**, merged first so #2694 and #2704 can build against it.
2. **Hub**: generate minutes for meetings, store and delete them with the recording, notify once, suggest to-dos and create the confirmed ones, use an overlapping calendar item, and answer `kb_get` / `kb_search` (`kb_get` prints the minutes before the transcript; `kb_list recordings` shows each recording's start and whether it has minutes). Unit tests with a fake model.
3. **Web**: the Knowledge › Recordings detail renders the minutes, with a light and a dark screenshot. Android renders them too (#2738).
4. **iOS** (#2694) and **Mac** (#2704) send `kind` and `calendar` and render the same `detail.minutes`.
5. **Attending** (#2981): the role, the attending session, live sentences and tap-to-ask above; the iPhone side is #3375.
6. **Later**: a push that opens the recording; a "meeting starting, record it?" nudge from the knowledge base's calendar; attendees, end and place on connector calendar items.
7. **Meeting conversation** (#3862): its own phase plan, in "Meeting conversation (#3862)" above.

Out of scope: recognising voices across or within meetings, storing voice features or profiles, labelling speakers during a meeting, and creating to-dos without confirmation. Uploading audio and transcription off the phone come only with hub mode (#3862, phase 5), and speaker separation applies only to hub-transcribed meetings ("Speakers (#17674)").
