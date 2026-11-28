# Plan: a recording port in the knowledge base (#13407)

Status: PRs 1 to 5 have landed; the series is complete (final totals in section 7). First written 2026-11-25 on `origin/main` at 4f5ff696c; revised the same day
on `origin/main` at 06f0bc7db (every line number and count below is from that commit unless it says otherwise).
Audience: maintainers of the server core and of the `recording` module.

**PR 2 has landed** (the reader / kept-write / writer split, the write-side `Provider` with `recordingwire.NoProvider`,
the gated lanes, the Core delete route `DELETE /api/v1/kb/recordings/{id}`, the derived `paused` field and the
module-off checks; `docs/architecture/core-and-modules.md` "Recording" describes the result). Where the code differed
from this plan, the code was followed:

- `CleanRecordingUpload` (section 1, `recording_space.go`, R) stays with the writers: its only caller is the upload
  of a recording saved into a track, reached through the module's route, and its call closure is the whole upload
  cleaner (`cleanUpload`, the calendar and day checks, the echo marks).
- Two more files split: `echo.go` (`MeetingLines` is read by the transcript of record) and `minutes_timeout.go` (the
  planners read `minutesTimeoutTries`). `recordLocked` (`hub_segments.go`) is a kept write: the commit renders the
  transcript of record with it. `clipRunes` (`minutes.go`) is read by the topics code and stays with the readers.
- Declarations follow their users: the `recordingwire` aliases, constants and file names that reader or kept code
  names moved with it, found by building the package without the writer files. What that build still misses is the
  three calls the provider now carries (`AudioDropped`, `AudioClosed`, `AfterPrune`) and the store's `audioRec` field
  type, which PR 4 moves with the audio writers.
- The provider's rounds hang on the engine and its audio touch points on the store (`kb.Engine.SetRecordingProvider`
  sets both): the trim is a `Store` method, and a manager makes a new engine over the same store when its settings change.
- Retention with the module off (section 7, question 1) is one constant, `kb.RetentionWithoutRecordingModule`
  (`server/internal/kb/recording_port.go`), `true`, which the owner confirmed on 2026-11-25 (decided, below).
- PR 2 is merged (#15246). The follow-up for question 3 (stored photo images, no stored-audio route) is `GET
  /api/v1/recordings/{id}/photos/{n}` moved to Core (section 7, question 3). Measured by `scripts/diag/core_size.py --no-build`: PR 2 added 441 non-test lines to Core
  (829,602 to 830,043); the photo route adds 59 more (831,695 to 831,754: the route 56, the route list entries 3) and
  takes 17 out of the module (9,386 to 9,369). The estimates in sections 1 and 7 for PRs 3 to 5 (what leaves `kb` and
  the module) are unchanged; Core's read-side part is that much larger than the section 1 reader totals say.

**PR 3 has landed** (the minutes and day-recap writers, behind the storage port). The owner decided on 2026-11-25
that, as an exception, the port may be about 30 methods wide so that these writers move whole; the measurement that
led there and the result:

- **Measured coupling** (go/types over `internal/kb`, not a text search). The five writer files named, outside
  themselves, 31 methods and 13 fields of `Store` and `Engine`, 34 package-level functions of `internal/kb`, 34
  constants and 4 error values. In the other direction, writers that stay in `internal/kb` until PRs 4 and 5
  called 8 of the moved functions (the upload: `repairDayRecap`, `rebaseRecap`, `pruneRecapNotes`; the photos:
  `putMinutes`, `renderMinutes`, `remakeMinutes`, `quietMinutes`, `dropOverviewPhotos`).
- **The port, `recordingwire.Core`, is 32 methods** (`server/internal/recordingwire/core.go`; adapter
  `server/internal/kb/recording_core.go`). The read group, `Reader`, 15: `ReadOnly`, `Zone`, `Recording`, `Recordings`,
  `RecordingName`, `RecordingSnapshot`, `RecordSnapshot`, `TranscriptOfRecord`, `ReadMinutes`, `ReadRecap`,
  `PhotoTexts`, `PhotoWords`, `PhotoBook`, `CalendarItems`, `Path`. The write-and-lock group, `Writer`, 17: `Locks`,
  `RecoverRecording`, `WriteFile`, `PutMeta`, `PutSummary`, `BumpPhotosRev`, `Notify`, `Index`, `MeetingChanged`,
  `DropAudio`, `Now`, `Logf`, `Model`, `HasModel`, `OutputLang`, `WritingOwner`, `DayRecapClock`.
- **The port is strongly typed** (the owner's rule of 2026-11-25 for interfaces the slimming adds): ids are
  `recordingwire.ID`, files `recordingwire.File`, and `Change`, `MeetingPart`, `CalendarKind` and `Purpose` are closed
  sets; no method takes or returns a `map[string]any`; the output language, the owner's name for the model and a
  file's path are computed by Core. What stays untyped, with its reason, is listed in the pull request ("Typing of
  the port"). PRs 4 and 5 add to the port under the same rule.
- **Where the code differs from section 3.5**: the writers take the store's locks themselves through `Locks`
  (recordings, files, meta, in the store's order) instead of `UnderRecording`, `CommitRecording` and `UpdateMeeting`,
  because their bodies moved unchanged and they take `recFilesMu` and `metaMu` around their own file writes
  (`putMinutes`, `recapFilesLocked`, `putRecapNotes`). `Tombstoned` and `UpdateMeeting` are not needed by these
  writers. The pure rules both sides apply (the planners, the day span, the recording zone and id, the hub
  transcription checks: 23 functions) moved to `recordingwire` (`rules.go`) rather than behind the port, and the
  provider gained a transitional part, `MinutesFromCore` (8 methods), for the 8 reverse calls above.
- **Lines** (`scripts/diag/core_size.py`): the pull request states them. `internal/kb` lost about 3,570 non-test
  lines and `recordingwire` gained about 500 (the port, the rules, the transitional provider part), both Core.
- **PR 4 reuses the port.** It is expected to add: a read lock on the recording files for the audio appends (section
  4), the photo book's write primitives the photo intake calls (`putPhotosLocked` and what reads it), `UpdateMeeting`
  (meeting.json's read-modify-write under `meetMu`), the audio path, and `Tombstoned`; and to remove `DropAudio`
  (the audio writers become the module's) and, as the photo and title writers arrive, five of the eight
  `MinutesFromCore` methods. `RepairDayRecap`, `RebaseRecap` and `PruneRecapNotes` go with the upload in PR 5.

**PR 4 has landed** (the photo intake, the audio, title and hub-transcript writers, behind the same port). Where
the code differed from this plan and from what PR 3 expected of PR 4, the code was followed:

- **What moved**: `recording_photos.go` (the intake, the room's bookkeeping, the photo lists), `recording_audio.go`
  (whole: the chunk files, the socket's writer, the gap upload, the final pass's reads, the drop, the sweep after a
  prune), `recording_title.go` (the live title round, the rename), `transcription.go` (`TranscriptionUpdate`,
  `HubRecordings`) and `hub_segments.go` (`HubTranscriptPut`, the repair, `RecordOf`), into
  `server/internal/modules/recording/writers`, bodies unchanged (the pull request shows the comparison).
  `internal/kb` lost 2,189 non-test lines, `recordingwire` gained 321, both Core: a net 1,867 out of Core, against the
  estimate of about 2,580 in section 6.
- **`echo.go` did not move.** The echo rule is applied by the upload's cleaner (`cleanUpload`), which Core's track
  recordings call as `kb.CleanRecordingUpload` with no engine (`server/internal/assistant/space_recordings.go`), so it
  cannot go through the provider, and it imports `golang.org/x/text`, so it cannot be a rule of the contract (contracts
  import the standard library and other contracts only). It goes with the upload in PR 5, or stays Core as a rule of
  both sides; PR 5 decides.
- **The upload's part of `transcription.go` and `recording_title.go` stayed** (`putTranscriptionFields`,
  `transcriptionOf`, `putNamed`, the summary round's `titleLine`: `recording_upload_rest.go` in `internal/kb`, 124
  lines, until PR 5). Their callers are the upload and the summary round, PR 5's.
- **The audio writers' state is the module's.** `audioMapMu`, the map and each recording's `audioRec.mu` left
  `kb.Store` (`audioState`, `server/internal/modules/recording/writers/shim.go`). It is one per store, not per engine:
  the manager makes a new engine over the same store when its settings change, the module hooks it with new writers,
  and those take the state of the writers the store had (`Writer.Provider`), so that Core's retention hook for audio
  (`AudioDropped`, `AudioClosed`, section 3.3) reaches the recording an open socket writes. With the module off there
  is no state and the hook is the no-op, as before. The order is unchanged: `recMu` → `meetMu` → `liveMu` → audio
  rec mutex → `recFilesMu` → `metaMu`.
- **The port is 40 methods** (it was 32): `Reader` 17, `Writer` 23. Added: `Tombstoned`, `HubTranscriptLocked`,
  `RenderTranscript` (read); `WriteDurable`, `PutPhotoBook`, `PutPhotoText`, `UpdateMeetingLocked`, `PurgeSearch`,
  `Deferred`, `Provider` (write). Removed: `DropAudio` (the audio writers are the module's) and `PhotoTexts` (the
  photo book is the contract's type now, so the module reads the words off it). `Locks` gained `Meeting` and
  `FilesRead`, the read side of the files lock an audio append holds (section 4: the port hands out the lock, there
  is no `AudioAppend` wrapper, so an append costs what it did). The audio path needed no method: `File` gained
  `DirAttachments`, and the writers name their files in it. PR 3 expected about five additions; the five it did not
  name are the hub transcript's (`HubTranscriptLocked`, `RenderTranscript`, `WriteDurable`), the photo delete's
  (`PurgeSearch`), the title round's (`Deferred`: the model client's "paused" and "not set up" errors are not a
  contract's) and the audio state's (`Provider`).
- **The photo book's stored types are the contract's** (`recordingwire.PhotoBook`, `StoredPhoto`, same JSON; a
  photo's id is the named type `recordingwire.PhotoID` on them and on `Photo`, `PhotoRef` and `PhotoUpload`), and
  the span arithmetic (`UnionSpans`, `GapsIn`, the sample conversions; `recordingwire/spans.go`), `SettleTranscription`,
  `CleanLive`, `MeetingLines` and `RecordChars` are rules of `recordingwire`, used by both sides.
- **The provider**: its rounds and audio touch points are the module's code now but `SummarizeRecordings`
  (`WritersInCore`). The transitional part is `UploadFromCore`, six methods: `RepairDayRecap`, `RebaseRecap`,
  `PruneRecapNotes` (from PR 3) and `AudioGaps`, `AudioHeldEnd`, `TitleDue` (new: the upload reads the audio held and
  whether a live title is due). `PutMinutes`, `RenderMinutes`, `RemakeMinutes`, `QuietMinutes` and
  `DropOverviewPhotos` are gone with the photo writers. All six go with the upload in PR 5.
- **PR 5** moves what is left: the upload and the appends (`recordings.go`, `recording_day.go`), `live.go`, the
  meeting writers and material (`meeting.go`, `meeting_material.go`), the summary round, `recording_upload_rest.go`,
  and decides `echo.go`. It removes `UploadFromCore` and `WritersInCore`, and can replace the module's composed
  `MeetingUpdate` and its copies of `photosRecording`, `photosHereLocked` and `PhotoTexts` (which Core's readers
  still have) by one home each.

**PR 5 has landed** (the upload and a day's appends, the live log, the meeting writers and material, the echo rule,
behind the same port; the last step). Where the code differed from this plan and from what PR 4 expected of PR 5,
the code was followed:

- **What moved**: `recordings.go` (the upload with its cleaner, the reconcile list), `recording_day.go` (a day's
  appends), `live.go`, `meeting.go`, `meeting_material.go`, `recording_upload_rest.go` and `echo.go`, into
  `server/internal/modules/recording/writers`, bodies unchanged (the pull request shows the comparison: 2,103 lines
  identical, 17 call lines changed). `recording_space.go` is gone. `internal/kb` lost 2,379 non-test lines,
  `recordingwire` gained 143 and `recordinghost` 2, all Core: Core as `scripts/diag/core_size.py` counts it went
  down 2,230 lines, against the estimate of about 2,400 in section 6.
- **`echo.go` moved, by a parameter.** PR 4 left it because Core's track recordings ran the upload's cleaner as
  `kb.CleanRecordingUpload`. That call is reached only from the module's upload route, so the route now hands the
  cleaner over (`recordinghost.Host.PutSpaceRecordingRoute(w, r, u, clean recordingwire.UploadCleaner)`) and Core
  runs it at the same place in the same order. With the module off the route is not there and nothing needs the
  cleaner.
- **The summary round stayed in Core** (`server/internal/kb/recordings_summary.go`, 232 lines;
  `recordingwire.WritersInCore`, one method). It is the knowledge base's own summarise, tag and link-to-projects of
  an entry applied to a recording: it names the retry book, `applyTags`, `TopTags`, `knownProjects`, `FindProject`,
  a project's log and a project's `putEntity`, eight Core members of which six are not about recordings. Moving it
  would have put the tag and project machinery behind the recording port. It still runs only with the module: the
  module's provider carries it, and `NoProvider` answers 0.
- **`UploadFromCore` is gone** (six methods): the upload calls `repairDayRecap`, `rebaseRecap`, `pruneRecapNotes`,
  `audioGaps`, `audioHeldEnd` and `liveTitleDue` directly again, as it did before PRs 3 and 4. The provider is seven
  methods (it was thirteen): three rounds, `AfterPrune`, `AudioDropped`, `AudioClosed` (both take a
  `recordingwire.ID` now) and the summary round.
- **The port is 46 methods** (it was 40): `Reader` 22, `Writer` 24. Added, each for the upload or the material:
  `CommitRecording` (the commit under the undo journal, section 3.5), `RecordingFile` (one committed file through a
  pending journal: a day's append reads the stored segments, not the whole snapshot), `Deleted` (the tombstones the
  phone is told of), `RecordingDays`, `TranscriptionReady`, `EarlierMeetings` (the three settings the upload and the
  material read, computed by Core) and `Project` (a project's name and status for an earlier meeting's material).
  Removed: `TranscriptOfRecord`, a pure function that is a rule of `recordingwire` now. Port and provider together
  are 53 methods, as before this PR (40 and 13). `File` gained `FileMeeting` and `FileLive`. Four more rules both
  sides apply moved to `recordingwire` (`MeetingParts`, `MeetingSame`, `BumpRevs`, `TranscriptOfRecord`), and the
  append's two wire shapes (`RecordingAppend`, `RecordingAppended`).
- **The locks, finally** (section 4 said `liveMu` would be the module's and the rest Core's; the code agrees):

  | Lock, in order | Owner | Why |
  | --- | --- | --- |
  | `recMu` | Core (`kb.Store`), handed out by `Locks().Recordings` | Core takes it too: `DeleteRecording`, `pruneRecordings`, `HoldsRecording`, a hand edit (`update.go`) and project membership (`dedupe.go`) |
  | `meetMu` | Core, `Locks().Meeting` | the trim's part of the transcription takes it (`transcription_keep.go`) |
  | `liveMu` | **the module** (`storeState` in `writers/shim.go`) | only the live log's writers took it; retention removes `live.json` under `recFilesMu` alone |
  | a recording's audio mutex | the module (since PR 4) | audio writers only; Core reaches them through `AudioDropped` and `AudioClosed` |
  | `recFilesMu` | Core, `Locks().Files` and `FilesRead` | readers, the commit, the recovery, delete and the trim |
  | `metaMu` | Core, `Locks().Meta` | every entry's meta.json and the project log |

  The module's two are one per store, shared by the writers of every engine over it (`Writer.Provider`). The order
  did not change. Neither side takes the other's lock of the pair `meetMu` / `liveMu`
  (`TestLiveAndMeetingLocksNeverNest`), and a live append costs what it did: about 2.9 ms and 303 allocations
  before, 2.9 ms and 307 after, on a log of 200 sentences (`BenchmarkLiveAppend`; the four more are the writers'
  view of the engine made per call, `TestLiveAppendAllocatesLittleMoreThroughThePort`).
- **The debt line stays, shorter.** The module's non-test code named 169 objects of `internal/kb` in 823 places
  (go/types, not a text search) and names 23 in 120; `writers` names none. 20 non-test files import `internal/kb`
  (25 before) and 23 test files (43 before). What is left is the handle Core hands the module and Core's readers
  on it, which this plan keeps in Core: the type `kb.Engine` (69 uses: `Hook`, `Routes`, the sweeps and every
  request take one) with its fields `Store`, `MeetingChanged` and `DayRecordingName`; `kb.RecordingCore`,
  `kb.RecordingWriters`, `Engine.RecordingProvider` and `SetRecordingProvider` (the hook); the readers
  `Engine.DayRecordings`, `HubTranscript`, `PhotoImage`, `PhotoTexts`, `ReadDayRecap`, `RecordingDays`,
  `Store.ReadMinutes`, `ReadOnly`, `Recording`, `RecordingSegments`, `RecordingZone`; Core's delete
  `Engine.DeleteRecording`; `kb.Model` with `JSONMax` (the answer model the host hands out) and the constant
  `kb.KindProject`. Reaching zero would mean a second handle type in `recordinghost.Layer` and a dozen read methods
  copied onto the port; section 6 said to prefer the short line, and
  `server/internal/archguard/testdata/module_services.txt` keeps `recording internal/kb` with that reason.
- **Tests**: 46 tests moved from `internal/kb` to the module's `writers` (the live log's, the meeting writers',
  the day appends', the material's, the echo rule's and the trim's, run on a real knowledge base hooked as the
  module hooks it). The tests of what Core does with a stored recording (the summary round, delete, retention, the
  commit and its recovery, lists, search, a hand edit) stay in `internal/kb` and store their recordings with a
  test-only plain upload through the same commit (`recording_seed_test.go`); the module-off gates in
  `internal/assistant` seed through the port (`kb.RecordingCore(e).CommitRecording`).

## Decision (2026-11-25): a module that is off must not hide or change what the user already has

The owner decided a rule for every module move (https://github.com/nwverse/persona/issues/13430#issuecomment-7149471360, "A rule for every module move in this tracker", posted 2026-11-25): **switching a module off must
not hide or change data the user already has.** Read access to existing data is Core; the write side, the background work and
the entry points are the module's; a module being off never triggers deletion or rewriting. First application: recordings.

For recordings this means, with the recording module off or built out: existing recordings, transcripts, minutes, day recaps
and photo texts stay readable (the knowledge list, a recording's detail, `kb_get`) and searchable, byte for byte unchanged.
Only new recording, upload, transcription, minutes and recap writing, the background lanes and the recording entry points go.

**What this supersedes** in the first version of this document (the old text is kept, marked, in "Superseded text" at the end):
old section 3 "Absent module" (a nil provider made lists, `kb_get` and search empty), old section 4 "Switched off" (files hidden
from lists and search, retention paused, recovery at module start), and old open question 9 ("kb_get and search with the
module off change from found to not found; confirm with the owner"). The owner's answer to 9 is the opposite.

**Other decisions already made**, which this revision builds on:

- `recordingwire` counts as Core. The contract package (1,805 non-test lines, `git ls-files server/internal/recordingwire`
  without tests) is the Core's vocabulary for recordings; moving types into it is not a Core reduction.
- The extension write path to recording files stays in Core (`ApplyExtension`, `server/internal/assistant/kb_rpc.go:259`;
  `editTagging`, `server/internal/kb/attribution.go:420`). It was open question 6; it is closed.
- Crash recovery: see section 3.2 (iii). It stays in Core and keeps running in `NewStore` as today, so the old plan's decision
  "recover at module start plus lazily" is not needed (open question 4 is closed).

## Goal

Meeting recording's storage still lives in `server/internal/kb`. The module (`server/internal/modules/recording`) may import
it only through one debt line, `recording internal/kb` in `server/internal/archguard/testdata/module_services.txt:24`. This
plan moves the **write side** of the storage (and the background work on it) into the module in a few pure-move PRs behind a
narrow port, and keeps the **read side**, the search index and what retention and delete need in Core, so that existing
recordings behave the same with the module off. By the owner's definition of Core (UI-free capabilities and interfaces) the
knowledge base's files, reading and search are Core's; creating, uploading, transcribing and summarising recordings are the module's.

The earlier blocker is true and is still the starting point: the writers share the store's private locks and are reached from
the core (https://github.com/nwverse/persona/issues/13407#issuecomment-7121773826).

## 0. State of the code today

- Step 1 is done. #15022 added `server/internal/recordingwire` and #15086 made the method-carrying types movable and moved
  them: every recording type now lives in `recordingwire` (13 non-test files, 1,805 lines) with same-name aliases in `internal/kb`
  (57 `type X = …` lines across the 19 recording files, counted in section 1), and 39 kb-bound methods became `kb` functions (the landing comment on #13407 says 39; the brief said 44)
  (`kb.CleanRecordingUpload`, `recording_space.go:3` in `internal/kb` then, is one). **The old plan's PR 1 is done.**
- The 19 recording files in `internal/kb` are now **10,233 non-test lines** (they were 11,500 before #15086). Command, from
  `server/internal/kb`: `wc -l recordings.go minutes.go minutes_overview.go minutes_timeout.go day_recap.go day_recap_write.go
  recording_day.go recording_photos.go recording_audio.go recording_commit.go recording_title.go recording_trim.go
  recording_space.go live.go meeting.go meeting_material.go transcription.go hub_segments.go echo.go`. These are raw `wc -l`
  lines (comments and blanks included), as `core_size.py` counts them; I did not run `core_size.py` itself (no Go toolchain
  on the machine I worked on).
- **Today the module being off only removes the module's own seam.** The recording lanes are not gated on it: `WriteMinutes`,
  `TitleRecordings` and `WriteDayRecaps` run from `server/internal/assistant/kb_lanes.go:118,121,124` and `PruneRecordings`
  and `SummarizeRecordings` from `server/internal/assistant/kb.go:538,543` whether or not the module is loaded; the module
  only fills the engine's callbacks (`Layer.Hook`, `server/internal/modules/recording/layer.go:25-30`; `recordingHook`
  calls it only when `s.rec != nil`, `server/internal/assistant/recording.go:133`). Reads, the index and the web detail
  differ: the server answers them with the module off; the web hides them (section 8). So "stop the lanes with the module off"
  is a behaviour change this plan makes on purpose, in PR 2. I did not check whether anything else gates the lanes.

## 1. Inventory: read path and write side, per file

Method. For each of the 19 files I listed every top-level `func`/`type` with its line span (doc comment included; a script in
`/tmp`, not committed: it finds a declaration's first `func|type|var|const` line, takes the doc comment above it, and ends at the
closing brace at column 0) and assigned each function to one of three columns. R and K are assigned by name from the lists in
the table; **W is everything else in the file**, so W also holds each file's imports, constants, vars, aliases and blank lines
(the "declarations" that readers share are estimated in the totals, not separated per file).

- **R, the read path (stays in Core).** What the Core callers of section 2a reach, plus the functions those call: the
  recording list and get, transcript text, minutes and day-recap readers, the detail's snapshot helpers, the day readers the
  briefing uses, the search view and the undo overlay. Roots: `Store.Recording`, `Recordings`, `RecordingID`, `dayDetail`,
  `metaWithMeeting`, `metaWithNameSource`, `minutesWithSpan`, `nonNilSegments`, `recordOf`, `recordSnapshot`,
  `recordingSearchView`, `recordingSnapshot`, `readEntityJSON` (called from `kb/update.go:215-285`, `kb/items.go`, `kb/find.go`,
  `kb/attribution.go`), and the outside-kb reads `ReadMinutes`, `ReadDayRecap`, `TodaysDayRecordings`, `DaySpan`,
  `PhotosMarkdown`, `HoldsRecording`, `RecordingZone`, `RecordingSegments`. R is these roots plus a hand-pruned call
  closure (identifier matching over function bodies, then I removed name collisions such as `at`, `end`, `head`, `text`).
- **K, a write that must stay in Core anyway.** What reads, delete, retention and recovery need: the undo-journal commit and
  recovery, `DeleteRecording`, the retention round (`PruneRecordings` and what it calls: the trim of a recording, of its photos
  and of its transcription, the meeting.json update primitive the trim uses, the tombstones, the state planners
  `planMinutes`/`planRecap` that the trim calls), and the photo book code the trim needs.
- **W, the write side (moves).** Everything else: uploads, appends, audio, photos intake, live log, meeting writers,
  transcription, minutes and recap writers and their prompts, titles, summaries, the hub transcript writers.

| File | Non-test lines | R (Core, reads) | K (Core, kept write) | W + declarations (moves) | R functions | K functions |
| --- | --- | --- | --- | --- | --- | --- |
| `recordings.go` | 914 | 141 | 165 | 608 | `RecordingID`, `badRecording`, `recordingDays`, `Store.Recording` (:129), `Recordings`, `RecordingSegments`, `recordingName`, `renderTranscript`, `nonNilSegments`, `RecordingDays`, `MinutesDays`, `MinutesDaysFor` | `tombstones`, `tombstonesPath`, `DeleteRecording` (:517), `unlinkRecording`, `PruneRecordings` (:593), `pruneRecordings` (:605), `trimPruned`, `unindex` |
| `minutes.go` | 1,320 | 64 | 27 | 1,229 | `ReadMinutes` (:120), `minutesWithSpan`, `minutesSpan`, `minutesSpanOf`, `recordingEnd`, `clockAt`, `calendarTitle` | `planMinutes`, `isMeeting` |
| `minutes_overview.go` | 494 | 0 | 0 | 494 | none (the model-answer builders) | none |
| `minutes_timeout.go` | 77 | 0 | 0 | 77 | none | none |
| `day_recap.go` | 580 | 56 | 25 | 499 | `hasRecap`, `readRecap`, `ReadDayRecap` (:138), `dayDetail` (:553) | `planRecap`, `recapFailed` |
| `day_recap_write.go` | 1,406 | 40 | 0 | 1,366 | `dayStart`, `daySpan`, `recordedWithin`, `earlier`, `later` (the day span arithmetic `DaySpan` uses) | none |
| `recording_day.go` | 542 | 70 | 0 | 472 | `dayLocation`, `RecordingZone`, `Engine.now`, `coverageTo`, `seconds`, `finite`, `dayRecordings`, `DayRecordings`, `TodaysDayRecordings` (:520), `DaySpan` | none |
| `recording_photos.go` | 975 | 15 | 251 | 709 | `PhotosMarkdown` (:503) | the photo book: `photoBook`, `storedPhoto` and its methods, `readPhotosLocked`, `putPhotosLocked`, `putPhotosTextLocked`, `photoLine`, `photoWords`, `minutesPhotos`, `citedPhotos`, `trimPhotosLocked` (:851) |
| `recording_audio.go` | 940 | 0 | 0 | 940 | none | none (the two functions retention calls, `audioDroppedLocked` :110 and `audioFileClosed` :122, become a hook, section 3.3) |
| `recording_commit.go` | 272 | 101 | 137 | 34 | `recordingSnapshot` (:25), `recordingRead` (:42), `readEntityJSON`, `recordingSearchView` (:75), `recordingDiskSnapshot`, `readRecordingUndo` | `syncRecordingDir`, `writeRecordingFile`, `recoverRecordingLocked`, `recoverRecordings` (:186), `commitRecording` (:197) |
| `recording_title.go` | 256 | 19 | 0 | 237 | `metaWithNameSource` | none (`RenameRecording` :75 moves, 3.2) |
| `recording_trim.go` | 110 | 0 | 110 | 0 | none | whole file: `trimRecording` (:43), `putTrimmedMeta`, `hasMinutes` |
| `recording_space.go` | 18 | 15 | 0 | 3 | `HoldsRecording` (:9), `CleanRecordingUpload` | none |
| `live.go` | 713 | 5 | 0 | 708 | `jsonNull` | none |
| `meeting.go` | 423 | 60 | 109 | 254 | `readMeeting` (:89), `metaWithMeeting` | `meetingUpdateLocked` (:227), `putMeeting`, `meetingParts`, `roomShown`, `meetingSame`, `bumpRevs`, `meetingChanged` |
| `meeting_material.go` | 334 | 0 | 0 | 334 | none | none |
| `transcription.go` | 305 | 0 | 41 | 264 | none | `trimTranscription` (:283), `transcriptionShown` |
| `hub_segments.go` | 363 | 116 | 0 | 247 | `readHubLocked`, `Engine.HubTranscript`, `TranscriptOfRecord`, `recordChars`, `hubMode`, `recordSnapshot` (:248), `recordOf` | none |
| `echo.go` | 191 | 0 | 0 | 191 | none | none |
| **Total** | **10,233** | **702** | **865** | **8,666** | | |

Reading it:

- **R is 702 lines**, more than the task leader's rough 300 to 500: the search overlay (`recording_commit.go`, 101) and the
  detail snapshot with the hub transcript (`hub_segments.go`, 116) are bigger than the list/get functions the estimate had in mind.
- **K is 865 lines**, and it is the surprise: Core keeps retention (the user's configured `recording_days` and `minutes_days`),
  and a trim is not a delete. It keeps minutes, the photos the minutes cite, meeting.json's private mark and the room's end
  (`recording_trim.go:39-86`, `recording_photos.go:851`, `transcription.go:283`), so the photo book (251 lines) and
  the meeting.json update primitive (109) come with it. Section 3.2 gives the alternative and why not.
- **The files that split** (R or K and W in one file): `recordings.go`, `minutes.go`, `day_recap.go`, `day_recap_write.go`
  (the five day-span helpers), `recording_day.go`, `recording_photos.go`, `recording_commit.go`, `recording_title.go`
  (`metaWithNameSource`, 19), `recording_space.go`, `live.go` (`jsonNull`, 5), `meeting.go`, `transcription.go` and
  `hub_segments.go`. In PR 2 every one of them is split by function, as a pure move inside the package.
- **W includes declarations.** Of the 8,666 lines, 57 are `type X = recordingwire.X` aliases (they stay in Core: the core's
  code and tests use `kb.Recording`) and some constants and vars are read by R and K code. I estimate 150 to 350 more lines
  stay, so **about 8,300 to 8,500 non-test lines leave `internal/kb`**; the count is an estimate, PR 2 measures it.
- The five files marked "none" for both R and K (`minutes_overview.go`, `minutes_timeout.go`, `recording_audio.go`,
  `meeting_material.go`, `echo.go`) move whole. The `recording_audio.go` retention coupling is two calls (section 3.3).

Tests: 24 files, about 12,300 lines, 158 Test functions (the earlier count in #13407); I did not re-measure them. They use
kb's unexported fixtures. The tests of R and K code stay in kb; the tests of W code move with it, with an exported fixture
(PR 3).

Not counted as recording: `attribution.go` (extension write attribution; `editTagging` takes `recFilesMu` for recordings,
`:420-446`; stays in Core, section 3) and `day_identity.go` (no recording reference).

## 2. Coupling

### 2a. Core to recording (what the core needs of recordings)

"Read" rows are the owner's rule's subject: they must work with no module and are what R is for.

| Where | What it needs | Kind | After this plan |
| --- | --- | --- | --- |
| `kb/store.go:213-221,258` | the recording locks live in `Store`; `NewStore` runs `recoverRecordings` (`recording_commit.go:186`) | state, once at open | stays; recovery keeps running at open (3.2 (iii)) |
| `kb/find.go:95-112,181,226,265` | `searchable` classifies a recording's files (journal, `minutes.json`, recap, live, meeting, photos files are not searched; `minutes.md`, `recap.md`, `photos.md`, `transcript.md` are plain searched files); `recordingSearchView`; lists recordings as known entries | read | unchanged (R, Core) |
| `kb/find_cache.go:108-110`, `kb/find_matches.go` | `meta.json`/`transcript.md` handling, `RecordingID` normalising | read | unchanged |
| `kb/items.go:359-367,403,520-546` | `kb_list`/`List` of recordings newest first; `recordingEntry` (:542); `recordingViewFile` check in the walk | read | unchanged |
| `kb/search_index_update.go:70-85` | `indexedFile` and `recordingViewFile`: meta.json and transcript.md are read from the committed snapshot, not the disk | read | unchanged |
| `kb/update.go:215-285` | `Get`/`Detail` of a recording: `recordSnapshot`, `metaWithMeeting`, `minutesWithSpan`, `dayDetail` | read | unchanged (R) |
| `kb/update.go:765-775` | `Update` of a recording by hand (tags, pinned facts, projects) under `recMu` | write, a user edit | stays (3.2) |
| `kb/dedupe.go:518-527` | project membership of a recording under `recMu` | write | stays |
| `kb/edit.go:73-95,233-247`, `kb/attribution.go:420-446` | `putEntity`, `putSummary`, `editTagging`: for a recording they take `recFilesMu` and run `recoverRecordingLocked` | write, generic | stays: every meta.json writer goes through `putEntity` |
| `kb/readonly.go:49,80` | a read-only archive refuses a pending journal (`:49`) and decodes `Recording` (`:80`) | read | unchanged: precedent for reading without the writer |
| `kb/archive.go` (callback fields `RecordingDaysFunc`, `MinutesDaysFunc`, `MeetingChanged`, `MinutesWritten`, `DayRecapWritten`, `DayRecordingName`) | settings and callbacks | callbacks | stay; nil callbacks are no-ops today |
| `assistant/kb.go:538,543` | the retention tick `PruneRecordings` and `SummarizeRecordings(ctx, 3)` | write, every tick | prune stays (3.2); summarise is the module's (gated) |
| `assistant/kb_lanes.go:118,121,124` | lanes `WriteMinutes(ctx,2)`, `TitleRecordings(ctx,2)`, `WriteDayRecaps(ctx,1)` | background jobs | the module's (gated by the port) |
| `modules/briefing/day.go:33` | `Day` and its recording/recap reads for the evening briefing | read | unchanged (R) |
| `assistant/tools_apps.go:80-81,168-190,743-789` | `kb_list`/`kb_get` of recordings: `Recordings()`, `Recording(id)`, `PhotosMarkdown`, the trimmed-transcript note | read | unchanged (R) |
| `assistant/space_recordings.go:160` | `HoldsRecording` | read | unchanged (R) |
| `assistant/profile.go`, `assistant/settings_tool.go` | `kb.MinutesDaysFor` | pure | unchanged (R) |
| `assistant/kb_rpc.go:259` | `ApplyExtension` | write, extension | stays (decided) |

The core callers that only read are exactly the ones the review found: `kb/items.go`, `find.go`, `update.go` (Detail),
`assistant/tools_apps.go`, `briefing_day.go`. The core sections that write under recording locks are `kb/edit.go:73-95,233-247`
and `kb/attribution.go:420-446` (`recFilesMu`), `kb/update.go:771` and `kb/dedupe.go:519` (`recMu`).

### 2b. Recording to core kb (what the module uses of kb)

The module (`server/internal/modules/recording`, 25 non-test files import `internal/kb`) names 144 distinct `kb.` identifiers.
Most are types, constants and errors that are `recordingwire` aliases (`kb.Recording` 54 uses, `kb.LiveAsk` 43, `kb.Live` 36,
`kb.MeetingRoomLean` 22, …); `kb.Engine` is named 73 times. A few are not recording vocabulary and would have to move to a contract or be
reached through the port: `kb.ErrNotFound` (19), `kb.ErrReadOnly` (9), `kb.KindRecording` and `kb.KindProject`. I did not
enumerate the Engine and Store methods the module calls beyond the earlier count (50 distinct of the 64 exported recording
methods); PR 3 starts by listing them.

Shared state and the documented order: `recMu` → `meetMu` → `liveMu` → audio rec mutex → `recFilesMu` → `metaMu`
(`recording_audio.go:32`; the field comments at `kb/store.go:213-221`). Analysis in section 4.

The writers also reach out through the `Model` and `ModelFor` interfaces (`kb/archive.go`), `prompts`, `lazyre` and `kbwire`.

## 3. The port, redesigned

The first version made the module the owner of everything and let Core ask it for reads. That is what the owner's rule rules
out. The new shape: **Core owns the reader and the few writes that exist without the module; the module owns the writers
and the background work, and reaches Core's storage primitives through a port.**

### 3.1 What Core keeps and what the module's provider shrinks to

**Core (always present, no module needed):**

- A recording **reader**: `Store.Recording`, `Recordings`, `RecordingSegments`, the transcript renderer, `ReadMinutes`,
  `ReadDayRecap`, `dayDetail`, `PhotosMarkdown`, `HubTranscript`, `recordSnapshot`, `TodaysDayRecordings`/`DaySpan`, and the
  **search view** (path classification `searchable`, `recordingViewFile`, `indexedFile`, `recordingSearchView`, `recordingEntry`)
  with the **snapshot/undo overlay** (`recordingRead`, `recordingSnapshot`, `recordingSearchView` under `recFilesMu.RLock`,
  `recording_commit.go:25-101`). Section 1's R column; 702 lines. Nothing in it is new: it is the code that is there now,
  and the Core call sites in section 2a do not change.
- The **undo-journal commit and recovery**: `commitRecording` and `recoverRecordings` (K column). The review's finding (b)
  is confirmed by the code: the pending journal is a read overlay, so a reader sees the last committed view even when a commit
  was interrupted (`recordingRead`, `recording_commit.go:42`), and `NewStore` recovers at open (`store.go:258`).
- **Delete, retention, hand edits**: 3.2.

**The module's provider (shrunk).** `recordingwire.Provider` is now only the write side and the rounds the module owns:

```go
// package recordingwire (contract). kb holds Engine.Recordings Provider; it is never nil: with the module absent it is a
// no-op value, so a call site never checks.
type Provider interface {
	// Background rounds, same cadence and lane names as today (assistant/kb_lanes.go, assistant/kb.go).
	WriteMinutes(ctx context.Context, max int) int
	TitleRecordings(ctx context.Context, max int) int
	WriteDayRecaps(ctx context.Context, max int) int
	SummarizeRecordings(ctx context.Context, max int) int
	// AfterPrune is the module's part of the retention tick: the sweep of hub transcripts (sweepTranscriptions,
	// recording_audio.go:883: mends transcript.md after a crash, makes minutes again after a final pass, drops audio).
	AfterPrune(now time.Time)
	// The audio state's two touch points with retention and delete (recording_audio.go:110,122).
	AudioDropped(id string, why error) // under recFilesMu (write): note that the audio dir is going
	AudioClosed(id string)             // no lock held: close the open chunk file
}
```

Everything the module does on the HTTP side (upload, append, audio socket, photo intake, meeting and live routes, rename,
the minutes and recap routes) is the module's code on the module's routes; those routes answer 404 with the module off, as
today, so they need nothing from the provider. They call Core's primitives (3.5) and, until PR 5, the kb Engine methods
that are still in kb.

**What Core returns for each write or lane call when the module is absent** (built out, or switched off, #14442): the no-op
`Provider` returns 0 and does nothing: no model call, no log line, no file touched (`WriteMinutes`, `TitleRecordings`,
`WriteDayRecaps`, `SummarizeRecordings`, `AfterPrune`); `AudioDropped`/`AudioClosed` do nothing because no audio writer exists. Nothing
panics, because the no-op is a value (the same shape as `recordinghost`'s present/absent check), not a nil check at each
call site. The module's own routes, tools, jobs and the audio socket are absent as today (`TestRecordingFollowsItsModule`,
`server/internal/assistant/recording_integration_test.go:1016`). **Reads are unaffected**: they never go through the provider.

### 3.2 Where edit, delete, retention and recovery live with the module off

**(i) Edit and delete of an existing recording.**

- **Delete stays in Core.** The owner's rule says old data stays and is the user's to remove; deleting is an explicit user
  action. `DeleteRecording` (`recordings.go:517`, 37 lines, K) writes a tombstone, removes the files, unlinks projects and
  purges the index. Today its only HTTP route is the module's (`modules/recording/kb_recordings.go:120`), which is 404 with
  the module off, so **PR 2 adds a Core route (or kb tool) for it**; the name is chosen there. The audio hook (`AudioDropped`,
  `AudioClosed`) is the only module touch point and is a no-op when the module is off.
- **Tags, pinned facts and projects stay in Core** (`Update`, `update.go:765-775`; `dedupe.go:518-527`; `editTagging`). They are
  the generic knowledge-base edit, not the recording module's.
- **Rename and the other edits go with the module.** `RenameRecording` (`recording_title.go:75`, 38 lines), `SetMinutesTodos`,
  `SetDayRecapTodos`, photo notes and deletes, minutes asks and handoff are entry points of the recording UI, and each rewrites
  data (a title with its name source, minutes, a recap). The rule keeps data readable and unchanged with the module off; it does
  not promise the user can edit it. Recommendation: with the module off the user can read, search and delete, not rename.
  This is a product call; flagged in section 7, question 2.

**(ii) Retention.** Retention the user configured is not a side effect of the switch (the rule: "a module being off never
triggers deletion", and equally never *stops* a deletion the user asked for). Recommendation: **`PruneRecordings` keeps
running in Core with the module off**, exactly as today. What it costs in lines: section 1's K column, **865 lines stay in
Core** (`pruneRecordings` 70, `trimPruned` 22, the trim 110, the photo book and `trimPhotosLocked` 251, the meeting.json update
primitive 109, the commit primitive 137, the planners and the tombstones). It runs, in the order of today's tick
(`assistant/kb.go:538`), `pruneRecordings`, then the module's `AfterPrune` (the hub transcript sweep) only when the module is on.
What retention does not do with the module off: it does not run `sweepTranscriptions`'s repairs and "make minutes again after
the final pass" (they write minutes, a module job); an unfinished final pass leaves its audio on disk until the trim removes
the whole audio directory, which `trimRecording` does for every recording past `recording_days`
(`recording_trim.go:43-86`). The alternatives and why not:

- Retention paused with the module: a transcript would outlive `recording_days` because the user switched a module off, a
  privacy regression, and it contradicts "retention is not a side effect".
- A smaller retention (delete only, no trim): recordings between `recording_days` and `minutes_days` that today are trimmed
  to their minutes would be deleted with their minutes, or kept whole; both change data.

**Decided by the owner on 2026-11-25** (section 7, question 1): retention keeps running in Core as configured
(`kb.RetentionWithoutRecordingModule = true`).

**(iii) Crash recovery.** The undo-journal primitive is needed by readers (they overlay a pending journal) and by every
meta.json writer (`putEntity`, `putSummary`, `editTagging` call `recoverRecordingLocked` first, `edit.go:73-95,233-247`). So
`recordingRead`, `recordingSnapshot`, `commitRecording`, `recoverRecordingLocked` and `recoverRecordings` all stay in Core and
**`NewStore` keeps running `recoverRecordings` at open (`store.go:258`)**, module or not. The old plan's decision "run recovery
when the module starts, or lazily on the first provider call" is therefore **unnecessary and withdrawn**: there is no window in
which the core can see a half-committed upload, because recovery is the same call at the same place as today. Cost: none; these
lines are in R and K already.

### 3.3 The hook retention needs for audio

Retention and delete reach the audio writers' memory in exactly two places: `audioDroppedLocked(id, why)` (`recording_audio.go:110`,
called with `recFilesMu` held for writing: `recordings.go:541,646`, `recording_trim.go:62`) and `audioFileClosed(id)`
(`recording_audio.go:122`, called with no `recFilesMu`: `recordings.go:544,649`, `recording_trim.go:47`). Both are the
provider's `AudioDropped` and `AudioClosed` above. With the module off no audio state exists; the no-op is correct and the audio
mutexes move with the audio writers (section 4).

### 3.4 Types, files and the contract

Unchanged from the first version and already done: the types are in `recordingwire`, pure, listed in
`server/internal/archguard/testdata/contracts.txt:20`, and `internal/kb` aliases them (the pattern of `kb/fields.go:15`).
The new interfaces (`Provider` above, and the storage interface in 3.5) go into `recordingwire` too, so
`TestContractsArePure` holds (standard library and other contracts only).

### 3.5 What the module asks of Core (the storage port)

The module's writers need Core's primitives: read a recording, commit files under the undo journal, update meta.json through
`putEntity`, update meeting.json, notify the change feed, mark the index, take `recMu`, and the clock, zone and model. A Core
implementation (an adapter in `internal/kb`, handed to the module through `recordinghost.Layer`, `Hook(e *kb.Engine)` becoming
`Hook(c recordingwire.Core)`) satisfies a consumer-defined interface. Shape, not final signatures:

```go
// Core is what the module's writers call. Reader is the read path of 3.1 (the module reads existing recordings the
// way every other caller does).
type Core interface {
	Reader
	// UnderRecording runs fn holding recMu: a recording's read-compare-write. Permanent: Core's own update.go:771 and
	// dedupe.go:519 take the same lock, so the module cannot own it (section 4).
	UnderRecording(fn func() error) error
	// CommitRecording is commitRecording: write a set of files under the undo journal, atomically for readers.
	CommitRecording(r *Recording, files map[string][]byte, remove []string) error
	// PutMeta is putEntity for a recording's meta.json (recFilesMu, recovery, metaMu in order).
	PutMeta(r *Recording) error
	// UpdateMeeting is meetingUpdateLocked: meeting.json's read-modify-write under meetMu, returns the changed parts.
	UpdateMeeting(id string, fn func(m *Meeting, r Recording) error) (Meeting, []string, error)
	Tombstoned(id string) bool
	Changed(id string, parts ...string) // store.notify
	Index(r *Recording)                 // e.index, e.unindex
	Now() time.Time
	Zone() *time.Location
	ReadOnly() bool
	Model(id string) Model
}
```

**This is the permanent Core lock exposure** the brief asked about: `UnderRecording` takes `recMu`, a Core lock, for the
module's writers for as long as the module exists.

### 3.6 Where it lives and why archguard passes

Same as before: the interfaces and types are in the contract package, pure; `internal/kb` imports the contract, never the
module (`TestCoreImportsNoModule`); the module imports the contract and the library packages it already imports. The adapter
that implements `Core` is in `internal/kb` (a recording-port file). With no import of `internal/kb` left in the module, the
`recording internal/kb` pair is deleted by the PR that removes the last import (a stale pair fails the guard). The
`knowledge-ui internal/kb` line is a different module and is not touched. Whether this design gets there: section 6, PR 5.

## 4. Locks and data

Rule, unchanged: no change of behaviour, no change of lock order `recMu` → `meetMu` → `liveMu` → audio rec mutex →
`recFilesMu` → `metaMu`.

| Lock | Where it goes | Why |
| --- | --- | --- |
| `metaMu` | Core | also guards the project log; recording code takes it through `putEntity` (the module no longer takes it directly). Its recording takers today: `recording_commit.go:199`, `recording_trim.go:91`, `minutes.go:151`, `day_recap.go:199,235` (the writers among them move and call `PutMeta`/`CommitRecording`) |
| `recFilesMu` | Core | takers that stay in Core: `putEntity`, `putSummary` (`edit.go:73-95,233-247`), `editTagging` (`attribution.go:420-446`), the commit, recovery, the readers' `RLock`, delete and the trim. The module no longer takes it: it calls `CommitRecording`/`PutMeta`. Audio writers hold `recFilesMu` for reading per append (`recording_audio.go:32`): they move with the audio writers and take it through a Core read-lock method or a Core-side `AudioAppend` wrapper (PR 4 decides; this is the one place the module takes a Core lock directly besides `recMu`) |
| `recMu` | Core, with `UnderRecording` | takers outside the recording files: `update.go:771` (hand edit of a recording), `dedupe.go:519`, and Core's `DeleteRecording` and `pruneRecordings` (K). The writers (minutes, recap, photos, titles, audio) take it too, so it cannot move without making the Core call the module for a hand edit. This is the permanent port method of 3.5 |
| `meetMu` | Core, with `UpdateMeeting` | `trimTranscription` (K, `transcription.go:283`) takes it inside `meetingUpdateLocked`; the module's meeting writers (`MeetingUpdate`, `putMeetingFields`, `refreshGaps`: `meeting.go:218,299`, `recording_audio.go:481`) call `UpdateMeeting`. Holding `meetMu` inside the primitive means the module must not take `liveMu` and then call it (order); I did not verify exhaustively that no live writer calls a meeting update under `liveMu`; PR 5 adds a lock-order test |
| `liveMu` | the module | only `live.go` writers take it (`live.go:226,291,362,448`); retention does not (`trimRecording` removes `live.json` by directory listing under `recFilesMu` only) |
| `audioMapMu` and each `audioRec.mu` | the module | audio writers only; retention reaches them through `AudioDropped` (leaf `audioMapMu`, under `recFilesMu` write) and `AudioClosed` (`audioRec.mu`, no `recFilesMu`), both consistent with the order |

With this split the order is kept by construction: the module takes `recMu` via `UnderRecording`, then (if it needs the live
log or audio) its own `liveMu` and audio mutex, and each Core primitive it calls takes `meetMu`/`recFilesMu`/`metaMu`
inside it, in order. The exception to check in PR 4 and PR 5 is a module path that holds `liveMu` or an audio mutex and calls
`UpdateMeeting` (needs `meetMu`, which sits before them).

On-disk layout does not change: `<data>/recordings/<id>/...` (meta.json, transcript.md, minutes.json, live and meeting files,
photos, `attachments/audio/`) stays where it is, "files first". No database migration: the knowledge base keeps no
recording state in Postgres. After the moves the module owns the writing of those files; Core owns the directory, the
reading, the index, delete and retention. Switched off: **nothing is hidden or rewritten**. Files stay, lists and search keep
them (the index keeps indexing them), `kb_get` reads them, retention keeps its schedule, and only new writing and the
module's lanes and routes are gone.

## 5. In-flight state when the module goes off

All of this is stored data the reader shows **as stored, nothing rewritten**. What the stored state says and what happens:

| State on disk | Where | With the module off | When the module comes back |
| --- | --- | --- | --- |
| An upload not finished: `complete: false` | `recordingwire` `Recording.Complete` (`recordings.go:36`) | listed and readable with the text so far; nothing completes it | the phone's next upload completes it, as after any gap |
| Minutes planned, not written: `minutes_state: "pending"` | `Recording.MinutesState` (`recordings.go:38`), set by `planMinutes` (`minutes.go:98-117`) | shown as pending; no lane writes them (the lane is the module's) | `WriteMinutes` takes it up: the state is re-derived from stored fields (`planMinutes`), nothing was reset |
| Minutes that already failed: `failed` | same | shown as failed | unchanged; the existing retry rules apply |
| Day recap pending: `recap_state: "pending"` | `Recording.RecapState` (`recordings.go:49`) | shown as pending | `WriteDayRecaps` resumes |
| Hub transcription in progress: `live`, `finishing` (meeting.json `transcription.state`) | `recordingwire` `TranscriptionLive`, `TranscriptionFinishing` (`transcription.go:31`) | shown as stored; no final pass runs; the audio stays on disk | the final pass and the sweep resume |
| A day recording being recorded; a meeting Persona attends: `attendance` | `Recording.Attendance`, `AttendingSession` (`recordings.go:111-112`) | shown as stored; no segments arrive | resumes if the phone is still sending; `AttendResume` (`assistant/recording.go`) runs again |

**Does any stored state show "in progress" forever?** Yes, for as long as the recording lives: a pending or live or attending
state is never settled by a Core that does not write it. With retention in Core it is bounded: `pruneRecordings` deletes, or
trims to the minutes, every recording past `recording_days` (default 14, `recordings.go:38`), and `putTrimmedMeta`
(`recording_trim.go:88-110`) clears attendance and re-plans minutes and recap (`planMinutes`, `planRecap`), so a trimmed
recording settles. Without retention in Core they would never settle (another reason for 3.2 (ii)).

**The smallest fix that rewrites nothing:** the reader marks what the module would have settled, at read time, in the response
and not in the file. When the provider is the no-op, `Detail` and the list entry carry a derived `paused: "module_off"` for a
recording whose stored state is in flight (not complete, pending minutes or recap, a live or finishing transcription, attending).
It is computed from the same fields (about 20 lines in the Core reader), lives on the response types (`Detail`, `Entry`),
never on `Recording`, so `putEntity` cannot persist it, and clients that ignore it behave as today. The clients' wording
for it is the follow-up in section 8. I did not check how the clients draw the stored pending states today.

## 6. PR plan, revised

PR 1 is **done**: #15022 (the `recordingwire` contract) and #15086 (the method-carrying types made movable, 44 methods into kb
functions, types moved). The first version's PR 2 (the port with every Core call site rerouted) is replaced; read call
sites do not change now because the reader stays in kb, so **the new PR 2 is smaller than the old one**: it touches the four
write/lane call sites in `assistant/kb.go` and `assistant/kb_lanes.go`, not about fifteen files.

Each PR is a pure move or an interface extraction; with the module on, the default build and the wire stay byte-identical.
**Verification for every PR:** the kb and module test suites with a test database under the default build, `persona_without_recording`
and `persona_core_only` (tests of the module skip without it, `needsRecordingModule`); `go build` and `go vet` for the three tag
sets (default, `persona_without_recording`, `persona_core_only`); `go test ./internal/archguard/...`;
`python3 scripts/ci/module-e2e.py --modules recording` (close and reopen); `python3 scripts/ci/core-only-smoke.py` and
`--switched-off recording`; and the **new check**, in every PR from PR 2 on: **with the module off, an existing recording is
still listed, opened and found by search, unchanged** (a Go test in `internal/assistant` that seeds a recording, switches the
module off, and compares `kb_list`, `kb_get` and `kb_search` output and the bytes of every file under `recordings/<id>/`
before and after; plus the script checks below).

| # | PR | Files | Non-test lines out of Core | Tests that move | Conflicts |
| --- | --- | --- | --- | --- | --- |
| 1 | done: `recordingwire` and movable types (#15022, #15086) | | 0 net (contracts count as Core) | | |
| 2 | **Reader/writer split and the write-side port.** Split the mixed files by function into reader, kept-write and writer files (a move inside the package); add `Provider` with a no-op value and gate the lanes, summarise and after-prune on it; the audio hook; a Core delete route; the derived `paused` response field; the new module-off checks | `recordings.go`, `minutes.go`, `day_recap.go`, `day_recap_write.go`, `recording_day.go`, `recording_photos.go`, `recording_commit.go`, `recording_title.go`, `recording_space.go`, `live.go`, `meeting.go`, `transcription.go`, `hub_segments.go` (split only); `assistant/kb.go`, `kb_lanes.go`; `modules/recording/layer.go` | 0 (adds about 250 to 350 in Core: the port types, the no-op, the route, the paused field; estimate) | the module-off test is new; no test moves | `assistant/kb_lanes.go`, `kb.go`; the split touches the files PRs 3 to 5 move |
| 3 | **done**: the minutes and day-recap writers, into `server/internal/modules/recording/writers`, behind the storage port `recordingwire.Core` (32 methods; see "PR 3 has landed" at the top) | `minutes.go`, `minutes_overview.go`, `minutes_timeout.go`, `day_recap.go`, `day_recap_write.go` (moved whole; the declarations readers name stayed in the `_read.go` and `_keep.go` files) | about 3,570 out of `internal/kb`; about 500 into `recordingwire` (Core) | the minutes and day-recap tests and their prompt goldens, and the tests of other files that ran the minutes round; `internal/kb` keeps the reader, trim and module-off tests with minutes written as files | the port is the one PR 4 and PR 5 extend |
| 4 | **done**: photos, audio, titles, transcription, the hub's transcript (see "PR 4 has landed" at the top; `echo.go` stayed) | `recording_photos.go` W side, `recording_audio.go`, `recording_title.go` W side, `transcription.go` W side, `hub_segments.go` W side | estimated about 2,580 (709 + 940 + 237 + 264 + 247 + 191 = 2,588, less declarations); measured 2,189 out of `internal/kb`, 321 into `recordingwire`, a net 1,867 | `recording_photos_test.go`, `recording_audio_test.go`, `recording_title_test.go`, `transcription*_test.go`, `hub_segments_test.go`, `echo_test.go`, `recordings_echo_test.go` | the audio socket and `meeting_audio.go` in the module; decide the audio `recFilesMu` read-lock wrapper (section 4) |
| 5 | **done**: the store's write half (see "PR 5 has landed" at the top: measured 2,379 out of `internal/kb`, 145 into the contracts, a net 2,230; the debt line kept, shortened). As planned: | `recordings.go` W side (608), `recording_day.go` W side (472), `live.go` (708), `meeting.go` W side (254), `meeting_material.go` (334), `recording_commit.go` W side (34), `recording_space.go` (3), `recording_upload_rest.go` (124) and the decision on `echo.go` (180); the module's imports of `internal/kb` replaced by the contract | about 2,400 (2,413, less declarations) | `recordings_test.go`, `live_test.go`, `meeting*_test.go`; kb keeps black-box tests with a fake provider and the reader/commit/retention tests | `store.go`, `edit.go`; the `liveMu`/`meetMu` order test |

PRs 3 and 4 are independent after 2 and may run in parallel; PR 5 comes last. After each, the Core reader/K code stays in the
same kb files it was split into by PR 2, so a later PR is a deletion plus a module addition.

**What happens to `recording internal/kb` in `module_services.txt`.** It can go only when the module imports no `internal/kb`
at all (a stale pair fails the guard, so the PR that removes the last import removes the line). This design gets there only if
three things hold, which PR 5 must show and I could not verify from reading alone: (a) every `kb.` identifier the module names
(144 today, most already `recordingwire` aliases) is switched to a contract; the non-recording ones (`kb.ErrNotFound`,
`kb.ErrReadOnly`, `kb.KindRecording`, `kb.KindProject`, …) are added to `recordingwire`; (b) the `kb.Engine` uses become `Core`
(3.5) and `Provider`, which means the port's `Core` interface has to cover every Engine/Store method the module still calls
(about 50 today, of which the writers' own methods move into the module and the rest go through `Core`); (c) the 25 module
files' tests and fixtures stop importing kb. If (b) leaves a long tail of Engine methods, the honest outcome is a smaller
pair, a debt line with a reduced reason ("recording reads the core's reader"), not zero; I would rather keep the line with a
short reason than widen `Core` into a copy of `Engine`. Decision for PR 5, flagged in section 7, question 5.

### Script checks that assume the old behaviour

- `scripts/ci/module-e2e.py`, the `recording` row of `SERVER` (`:83-89`) seeds a recording while the module is on and checks only
  the route `/api/v1/recordings` (404 off). That stays correct (the route is the module's). **Add (PR 2):** an off-phase
  check that the seeded recording is still listed, opened and found by search through the Core knowledge routes and that the
  body equals the on-phase body.
- The `recording` row of `WEB` (`:176-184`): the entry "Knowledge › Meetings recording" with `text: "Module gate sample
  recording"` is, by the script's own legend (`:105-109`), "visible while on, **absent off**", and the chunk
  `RecordingDetail-*.js` "none may be requested while off". Both assert the old rule. They stay green until the web follow-up
  (section 8) because the web still hides recordings off; that PR must **flip the entry to "visible off" and keep the module
  chunk absent while off** (the read-only detail is Core's). Until then the row is wrong by the owner's rule, not by the code.
- `scripts/ci/core-only-smoke.py`: `MODULE_ROUTES` lists `/api/v1/recordings` as 404 off (`:19,125`), which stays correct.
  **Add (PR 2):** write a recording into the data directory before the hub starts and, in the `core-only` and
  `--switched-off recording` runs, check that it is listed, readable and found, and unchanged after the restart; today the
  smoke seeds nothing and so cannot tell.
- I searched the scripts for these two assertions; I did **not** search the Go tests for tests that assert a recording absent
  with the module off (beyond `TestRecordingFollowsItsModule`, whose recording-related lines I saw only as far as it says the
  stored ones stay); PR 2 must grep for them and flip them.

## 7. Revised totals, risks and open questions

**After PR 3 (measured).** The port is 32 methods, not about 15, and the pure state rules live in `recordingwire`,
so the Core side of the move is larger than estimated below: of the five files' 3,577 lines that left `internal/kb`,
about 500 lines came back as Core in `recordingwire` (the port's interfaces, the rules, the transitional provider
part) and about 110 as the adapter in `internal/kb`. PRs 4 and 5 extend the same port; expect a similar ratio.

**After PR 4 (measured).** The port is 40 methods. Of the 2,189 lines that left `internal/kb`, 321 came back as Core
in `recordingwire` (the photo book's types, the span arithmetic and four more rules, the port's ten new methods, the
provider), so PRs 3 and 4 together took 4,827 net lines out of Core (2,960 and 1,867). What is still in
`internal/kb` for PR 5 is the files of its row in section 6.

**After PR 5 (measured), and the series.** PR 5 took 2,379 lines out of `internal/kb` and 2,230 net out of Core
(`core_size.py`: 845,825 to 843,595 on the day's `origin/main`). The whole series, PR by PR, in Core non-test lines:
PR 2 +441 (the port's provider, the no-op, the delete route, the paused field), the photo image route +59, PR 3
-2,960, PR 4 -1,867, PR 5 -2,230: **a net 6,557 lines out of Core**, against the revised estimate of 7,800 to 8,200
below. Out of `internal/kb` alone the writers' moves took 8,138 lines (3,570, 2,189, 2,379), against the estimate
of 8,300 to 8,500; the difference to the net is what came back as Core by design: `recordingwire` grew from 1,805 to
2,898 lines (the port's two interfaces, the provider, the rules and types both sides use) and the adapter
`recording_core.go` is 191. What is left of recordings in `internal/kb` is 2,566 lines: the readers (`_read.go`), the
kept writes (`_keep.go`, `recording_trim.go`), the adapter and the provider's Core side (`recording_core.go`,
`recording_port.go`, `recording_writers.go`) and the summary round (`recordings_summary.go`). The module is 18,096
non-test lines (9,369 before PR 3). The port ended at 46 methods where section 3.5 sketched about 12, and the
provider at 7.

**Totals (estimates marked).** First version: about 10,000 to 10,500 net (11,500 lines leaving, 12,331 of tests moving).
Revised: of the 10,233 lines now in `internal/kb`, **702 (R) and 865 (K) stay by design**, plus an estimated 150 to 350
declaration lines, so **about 8,300 to 8,500 non-test lines leave `internal/kb`** (estimate), and the new Core code is **about 250
to 350 lines for the port, the no-op, the delete route and the paused field, plus about 150 to 250 for the storage adapter
(`Core`)** (estimates). With contracts counted as Core (decided, and the 1,805 lines of `recordingwire` already count there),
the **net Core reduction is about 7,800 to 8,200 lines (estimate)**, against 10,000 to 10,500 before: roughly 2,000 to 2,500
less, of which 1,567 is exactly what the owner's rule keeps in Core. The test lines that move are the tests of W code (the
first version's 12,331 includes tests of code that now stays; not re-measured).

Risks and open questions, each with my recommendation. Dropped because this design removes them: the old `OwnsPath` in the index
walk (risk 1), the index purge on a path class change (the search view does not change with the module), the recovery window
(3.2 (iii)), and the hidden `kb_get` (the rule now requires the opposite).

1. **Retention with the module off. DECIDED by the owner on 2026-11-25: retention keeps running as configured.**
   `PruneRecordings` runs in Core, 865 lines stay (3.2 (ii)); it matches `kb.RetentionWithoutRecordingModule = true`
   (the alternatives change data or leave transcripts past the user's setting).
2. **Rename and other edits with the module off.** The user can read, search, delete, and edit tags and projects; not rename.
   Recommend this for PR 2; the owner's rule does not require more, but the user's wish "my old recording has a wrong title"
   is a legitimate follow-up (a Core `rename` using the same `putNamed` path would cost about 40 lines).
3. **Photo reads and audio playback with the module off. DECIDED by the supervisor (the owner's rule: what the user
   has stays readable): photo lists and images are readable through Core's read-only routes; stored audio is not applicable.**
   - Photos, implemented: `GET /api/v1/recordings/{id}/photos` and `GET …/photos/{n}` are served by Core
     (`assistant/kb_recording_media.go`, reading `kb.Engine.PhotosRead` and `PhotoImage` in `kb/recording_photos_read.go`)
     with the module on and off: the same paths, full credentials only, the existing JSON and image responses, and the
     existing `410`/`404` and knowledge-base `503` errors. They write nothing. Adding and deleting photos stay the
     module's routes; the module no longer registers its copies of the GETs.
   - Audio, no code: no client-facing route has ever served stored audio. `kb.AudioRead` is used only by the module's
     final transcription pass; the chunk files (`attachments/audio/<from>.pcm`) are raw PCM16 and are deleted after the
     final pass; the only audio routes are the phone's upload socket and the gap upload. There was nothing to keep readable,
     and a playback route would be a new feature (a separate request). What does apply is that with the module off the
     chunk files of a recording in flight stay on disk byte for byte untouched, and the module resumes from them when it
     is back: asserted by `TestStoredAudioStaysUntouchedWithoutTheModule`, `scripts/ci/core-only-smoke.py` and
     `scripts/ci/module-e2e.py`.
4. **The retention tick and the lanes' behaviour change** (section 0): the lanes stop with the module off; today they run.
   Recommend: yes, as the rule says; document it in the PR and in `docs/architecture/core-and-modules.md`.
5. **Whether `recording internal/kb` can reach zero** (section 6): decide in PR 5 on the measured Engine/Store tail, and
   prefer a short remaining line over a wide `Core`.
6. **Live transcription hot path.** `live.go` writers take `liveMu` many times a second while a meeting runs; `Core` calls
   from them must stay plain method calls, no goroutine hop (PR 5; benchmark with `live_test.go`). The reader is untouched.
7. **The search index.** Nothing to port, since the index and the recording path classification (`find.go:95-112`,
   `search_index_update.go:70-85`) stay in Core. PR 2's module-off test is the guard; the index's walk cost does not change
   (`find_bench_test.go` as a check).
8. **Test fixture.** An exported kb fixture for the moved tests (PR 3) is a new stateful package under `internal/`; check it
   passes `TestStandInsHoldNoModuleCode` (stand-ins must not import the module).
9. **Read-only knowledge base** (`readonly.go:49,80`): it already decodes recordings and refuses a pending journal
   without the module. Nothing changes; the new test covers it.
10. **Core size accounting.** The owner counts `recordingwire` as Core (decided). `core_size.py` reads
    `internal/archguard/testdata/contracts.txt` only if it does; I did not check how it classifies the contract package,
    and did not run it. PR 2 should state the Core delta from the script, not from `wc -l`.

## 8. Clients (outside this plan)

Out of scope here, but the owner's rule fails on clients today and the server work alone does not fix it. Web, checked in the code:

- `web/src/modules/recording/module.ts:17` declares `kbKinds: ["recordings"]`; the detail's recording parts (minutes,
  conversation, transcript, day recap, photos, rename, handoff) are drawn by the module's code (`web/src/modules/recording/`,
  `RecordingDetail.tsx` and neighbours) through the `kb.detail` slot (`web/src/core/ModuleKBDetail.tsx`).
- `useKBKindShown` (`web/src/core/ModuleKBDetail.tsx`) answers false for a kind only an off module names; the Meetings list drops
  recordings and the search leaves them out (`web/src/views/knowledge/KBList.tsx:57-59`), and a recording's detail page is
  "not there" (`web/src/views/knowledge/KBDetail.tsx:129-130`).

So with the module off, the web shows no old recording even though the server will list, open and find it. For the rule to hold on
clients, **the default panel must keep a read-only recording detail**: title, summary, the minutes text, the day recap, the
transcript and the photo texts (what `kb_get` prints), with the list and search entries shown, and the module's controls (rename,
ask, conversation, live view, handoff) staying gated. That also means `kbKinds` and `useKBKindShown` need a split between "the kind is
shown" (always) and "the module adds parts" (when on). The same shape is probably true of the Apple and Android knowledge
pages; **I did not check them** (the web is the only client I read). **Follow-up issue to open, not designed here:** "Keep a
read-only recording detail in the default knowledge panel when the recording module is off", with `module-e2e.py`'s web row
flipped in the same PR (section 6) and the derived `paused` field (section 5) drawn.

## Not in scope

The Apple, Android and web recording clients (section 8 records their consequence), the wire (JSON shapes stay byte-identical;
the one addition is the derived `paused` field on responses, additive), prompts (they stay under `server/internal/prompts/`),
`recordinghost.Host`'s 75 methods (a separate narrowing, #13407), the hub's meeting glue (`hub/meeting_*.go`) and
`server/scripts/transcribelive`.

## Superseded text (kept for the record)

From the first version (2026-11-25, 4f5ff696c), replaced by the Decision note at the top and sections 3 to 5:

- Old section 3, "Absent module": *"`Engine.Recordings == nil`: `List`/`Get`/`Detail` answer empty or not found (so `kb_get`
  returns the ordinary not-found, the Knowledge page's Recordings list is empty), `OwnsPath` is false, so the finder and the
  index treat the files as unknown and skip them (they do not appear in search or `kb_list`) … `Edit`/`Delete` return
  `ErrNotFound`, `Recover` is skipped (pending commits stay on disk until the module is back)."* Replaced by: reads, the index,
  delete and recovery stay in Core; only writes and lanes are absent.
- Old section 4, "Switched off": *"files stay, hidden from lists and search … no new minutes or recaps are written, retention
  does not run, and turning the module back on picks the files up again; crash recovery runs at that start."* Replaced by
  section 4's last paragraph and 3.2.
- Old open question 9: *"The `kb_get`/search behaviour with the module off changes from 'recordings found' to 'not found'. This
  is intended (hidden when off) but visible; confirm with the owner."* The owner confirmed the opposite.
- Old open question 4 (recovery order: lazily or accept the window): withdrawn, section 3.2 (iii).
- Old open question 6 (may extensions write recording files at all): closed, they may and it stays.
- Old `Provider` with `List`, `Get`, `Detail`, `OwnsPath`, `Recover`, `Edit`, `Delete`, `Prune`, `TodaysDayRecordings`, `DayRecap`,
  `PhotosMarkdown`: the read half is now Core's reader; `Prune`, `Edit` and `Delete` are Core's; the write rounds are what is left
  (3.1).
- Old PR 2 ("the port; core call sites go through `e.recordings()`"): replaced by the new PR 2, which does not change read call sites.
