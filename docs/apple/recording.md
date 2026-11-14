# Recordings in the knowledge base

Recordings feed spoken notes, meetings, and information captured on site to Persona; this is not a second recording product. Both iPhone and Apple Watch record in segments; the watch sends them to the phone via WatchConnectivity, and the phone transcribes locally.
The resulting text (timestamped segments) automatically syncs into `recordings` in the knowledge base, where the assistant and Agents can find it with `kb_search` / `kb_list` / `kb_get`. The knowledge base retains only the most recent N days (14 by default).

Since 2026-11-05 (#3235), there is no longer a "Composer + › Recording context" entry: user feedback was that this entry was wrong. Recordings should be stored in the knowledge base and retrieved by search and queries, not manually inserted into the composer each time.

Rolling recording (the watch keeps recording and only retains the last 6 hours, with health bookmarks) is a mode of this same system — see `docs/apple/recording-rolling.md`. Pulled ranges are transcribed on device and sync into Knowledge like any other recording; there is no composer "as context" path.

All-day recording (the watch records the whole day, the phone transcribes it as it arrives and appends the text to the hub, which writes a daily recap) is the recording kind `day`; its contract, the watch-to-phone transfer included, is `docs/assistant/day-recap.md`. How the watch and the phone carry it out is "All-day recording (Apple)" below.

## Why send only text

- Audio is roughly 14 MB / hour; the knowledge base searches text, so text is enough. The assistant needs "who said what at that day's meeting," not the audio itself.
- Privacy: by default original audio stays on the phone (watch audio is still cleaned up after transfer to the phone) and the hub holds only transcripts. A meeting has two exceptions, each said where it applies, in every language, as the contract's "Privacy per mode" Wording has it ([meeting-minutes.md](../assistant/meeting-minutes.md)): while the owner holds Talk, what is said then goes to the voice service (#3862 phase 4); and a meeting transcribed on the hub (hub mode, phase 5: picked at the start, or the default in Settings › Meetings) sends its audio, Talk included, through the hub to the speech service the hub is set up with (named when the owner chose another extension), and the hub deletes that audio once the meeting's minutes are written. The phone keeps its own audio as before, until `days` or the owner's delete. The texts: the microphone usage description (`apple/project.yml`, `apple/Persona/Resources/InfoPlist.xcstrings`), the meeting intent's description (`apple/LiveActivity/RecordingIntents.swift`), the line under Record and Settings › Meetings (`MeetingTranscriptionWords`), the recording page's footer, and the site's privacy page (`site/public/privacy/index.html`).
- Recording entries are separate from meetings (`meetings`): a meeting is an external item from the Lark calendar, stored as one JSON document and batch-tagged by a model; a recording has a full transcript, its own retention period, and UUID-based writes and deletes from the phone. Their lifecycles differ, and combining them complicates cleanup and display.

A recording marked as a meeting (`kind: "meeting"`, with the phone calendar's event in `calendar`) also gets structured minutes from the hub: [meeting-minutes.md](../assistant/meeting-minutes.md).

## Synchronization

See [protocol.md](../protocol/protocol.md) ("Recording synchronization") for the API and [knowledge.md](../assistant/knowledge.md) ("Recordings") for storage in the knowledge base.

- The phone uses each recording's UUID as the key for `PUT /api/v1/recordings/{id}`, sending the complete state so far each time (full replacement). `revision` increases monotonically: replayed or out-of-order uploads retain only the newest copy.
- Upload available segments before transcription finishes (`complete: false`), then upload again as transcription progresses, recording ends, or a language change triggers retranscription; once `complete: true`, the hub generates a summary and tags.
- Deleting a recording on the phone → `DELETE /api/v1/recordings/{id}`, deleting it from the knowledge base too; the hub retains a tombstone, returns 410 for late uploads, and the phone discards the pending upload.
- When offline, after hub restarts, or when the manager is disconnected (503), the phone queues retries; `GET /api/v1/recordings` reconciles state (uploading recordings absent from the hub or with an older revision there, and removing those deleted on the hub).
- Retention: the assistant setting `recording_days` (14 days by default, 1–365) can be read or changed through `assistant.recording_days` in `persona_settings`; the hub deletes expired files and index entries together, and recordings older than the retention window are no longer uploaded.
  A meeting with minutes is the one exception, and only on the hub: past `recording_days` it keeps the minutes (not the transcript) according to `minutes_days` (forever by default: unset or `0`, otherwise 1–3650 days, never shorter than `recording_days`; the user’s delete still removes everything; #3780 / #3862, [meeting-minutes.md](../assistant/meeting-minutes.md) "Privacy"). Such a recording stays in reconciliation's `recordings` at its revision, with `trimmed_at`, and an upload of it is `expired`.
  The phone uses `days` from reconciliation to remove expired local recordings as well (audio and text together, leaving active recordings untouched): the hub is authoritative for text, and local retention never exceeds knowledge-base retention. `days` stays `recording_days`, never `minutes_days`: the phone's own copies (audio and transcript) follow the recordings' retention, not the minutes'.
- The iOS implementation is `RecordingSync` (`apple/Persona/Recording/`): every `RecordingLibrary.save` triggers a round (1-second debounce); `revision` is this state's `updatedAt` (milliseconds, guaranteed to increase on save).
  Uploaded revisions, pending deletions, rejections (400), and `days` are stored in `sync.json` in the recording directory; after restart, only changed recordings are uploaded. Transient connection failures retry with exponential backoff starting at 5 seconds, up to 10 minutes; returning to the foreground or a new save triggers an immediate retry.
  `kb_disabled` / `kb_read_only` pause automatic retries without dropping uploads or pending deletions. Fix the manager configuration, then reopen the app to retry; another transcript save cannot enable knowledge. Authentication, permission and unsupported-route failures also pause. Failed reconciliation stops the pass before writes. A DELETE 404 does not confirm deletion: the remote copy stays pending, and the list explains that it has not yet been removed.
  Reconcile on startup and return to the foreground (at most once every 10 minutes). A 410 or an entry in `deleted` during reconciliation means it was deleted elsewhere (such as the web), so delete it locally too. Recordings without any text (no recognized speech) are not uploaded, except a meeting that Persona attends, which is uploaded once when it starts ([meeting-minutes.md](../assistant/meeting-minutes.md) "Attending a meeting").

Phone recordings can use the microphone on glasses or a Bluetooth headset (Bluetooth HFP); see `docs/apple/meta-glasses.md`.

## Migration sources and boundaries

Reuse Hourhand's `Shared/SegmentRecorder.swift` (segmented AAC), `HourhandWatch/TransferQueue.swift` (persistence and transfer backoff), and `Hourhand/Transcription/SpeechEngines.swift` (on-device engines and timestamps). After migration, standardize on the `Recording` prefix and remove product accounts, payments, cloud services, location, and separate recording-library navigation.

- Audio: 16 kHz mono AAC, 32 kbps, roughly 14 MB / hour; segments are 30 seconds in the foreground and 60 seconds in the background, and 15 seconds in both while Persona attends a meeting started with the live view ("Live view (phase 2)" below). watchOS 11 and later finalize with `AVAudioFile.close()`; watchOS 10 releases the last file reference before the callback and must not retain a local reference inside the write loop.
- Storage: Application Support/Recordings on each device, one UUID directory per recording, with atomic updates to `recording.json`. This is not a cache and must not be deleted by cache cleanup.
- Abnormal exit: retain finalized segments and delete the unfinished tail; do not automatically restart the microphone. At most the current 30–60 seconds are lost.
- Privacy: no location collection and no audio uploads; transcript text syncs into the hub's knowledge base ("Synchronization" above) and is not automatically sent to any chat, except to the session attending a meeting the user chose to attend (#2981). Transcription uses only Apple's on-device models, with no implicit fallback to Apple servers or cloud APIs.
- The initial version has no recording summaries, speaker identification, route maps, payments, or cloud services. The user explicitly selects the recording language.

## Incremental commits

1. Shared recording format, segmentation engine, persistence, and recovery after abnormal exits.
2. On-device transcription, timestamps, resumability, and context generation.
3. Watch recording, persistent transfer, end messages, and cleanup after acknowledgment.
4. Phone recording page, preview / deletion, screenshots, and regression verification.
5. Knowledge-base synchronization (#3235): hub `recordings` and APIs, retention, web / Android knowledge pages; remove the iOS composer entry and switch to automatic sync.

## Meeting recordings

In-person meetings and hallway discussions went unrecorded: the recorder sat four taps deep (Knowledge › Recordings, the last kind chip), and nobody remembered it in the moment. A meeting recording starts in one step from wherever the user is, and every entry point that starts one has the same name, **Record** (zh-Hans 录音, ja 録音), and does the same thing: it starts a meeting recording, or opens the one running (#5243). The drawer's row is the Recordings page itself, a place like the rows under it, so it opens the page and leaves the start to its Record button (#5517: as Record it started at a tap, and in zh-Hans and ja it reads exactly as the page's name). Record always records a meeting (`kind: meeting`, minutes, and Persona can attend with the hub's default role), since impromptu talks are meetings too; the calendar only names it.

| Where | How | Code |
| --- | --- | --- |
| The drawer | **Recordings**, first among the places: opens the Recordings page, whose Record starts a recording (the row starts nothing); while a recording runs the row reads "Recording… Open" and opens it | `AssistantSidebar.meetingRow`, `ShellView.openRecordingsPage` |
| Persona's main conversation | **Record** in the composer's + list (the iPhone's touch and hold on +, the attach panel's More tab, the iPad's + menu; #3723): starts recording and opens Recordings; while a recording runs it reads "Recording… Open" and opens it. Not in other conversations | `AttachMoreSection.common(meeting:)`, `Composer.attachMoreSections` |
| Recordings page | On the iPhone, **Record** (prominent, the page's one start button) sits in a bar at the bottom of the page, under the thumb, with the calendar line under it (#5522); while a recording runs, the bar holds its now-recording card instead. The options stay at the top of the list, one row each, every row with its icon and its words under the title: **Type** (Meeting / Note), then for a meeting Persona, Private Meeting (the system switch) or Who Attends, then language and microphone. At accessibility text sizes the calendar line moves from the bar to the list's first row. The options and Record share one `RecordingStartForm`. Knowledge › Recordings (and visionOS) keep Record at the top of the section. While the start runs (calendar, microphone) the button keeps its label and shows a spinner | `RecordingCaptureControls` (`Part`), `RecordingStartForm`, `RecordingStart`, `RecordingsSection`, `RecordingsPage` |
| Home screen icon's menu | **Record** | `QuickAction.meetingRecording` |
| Control Center, Lock Screen, Action button | the **Record** control, without opening the app | `MeetingRecordingControl` → `StartMeetingRecordingIntent` |
| Siri, Shortcuts, Spotlight | **Record**: "Record with Persona", "Record a meeting with Persona" and their zh-Hans / ja phrases | `PersonaShortcuts`, `StartMeetingRecordingIntent` |
| Apple Watch (#5493) | **Recordings** on the home list opens the recording page, whose **Record** starts, with Type (Meeting / Note) under it; while a recording runs the row reads "Recording… Open". The **Record** complication starts a meeting at a tap ("Apple Watch" below) | `WatchMeetingEntries`, `WatchRecordingView`, `MeetingRecordingComplication` |

- **Meeting or Note** (`RecordingKindChoice`): a note has no minutes and no Persona (`kind: note`). It is a secondary choice, never a second button. Before the start, the Recordings page's **Type** row is Meeting for every start; Note, picked for that one start, hides the meeting's options (and the calendar line) and the same Record button records a plain note ("No minutes, and Persona stays out."). It is never remembered, and every other entry point starts a meeting. While it records, the live sheet's page menu (`RecordingLiveView`'s, beside Hide, and the meeting page's) has the same choice, Meeting and Note with their marks, under the menu's other items (`RecordingKindPicker` → `MeetingRecorder.setMeeting`). Meeting → Note: `meeting` goes (the upload says `kind: note` with no attendance or `room`, so the hub turns Persona off and writes no minutes), the role goes off from that moment and Persona stops speaking; the attendance stays on the phone, off, so the live log the hub has, Persona's spoken stretches and a room meeting's room are kept. The sheet then shows the live view. Note → Meeting: named after the calendar event on now if it has no name yet (else "Meeting · <date time>"), Persona's part as a fresh start would get it (`MeetingAttendees.startAttendance`, attending from that moment: `MeetingAttendees.resumed`; a meeting that had a room keeps it), and the hub hears of it at once (`RecordingSync.reupload`, so live lines and asks wait for the new upload's `200`). Either way what was said before stays the recording's transcript. The Live Activity follows (Meeting or Recording, and Persona's role only while it attends).
- The identifiers stay what they were, so shortcuts, controls, home screen items and tests made before keep working: `StartMeetingRecordingIntent`, the control's kind `persona.control.meeting-recording`, the item type `persona.meeting-recording`, `sidebar-meeting-recording`, and `recording-meeting-start` for Record (the old plain start, `recording-start`, is gone on the iPhone; visionOS keeps its earlier controls). The watch has the same one entry since #5493 ("Apple Watch" below).

- `StartMeetingRecordingIntent` is an `AudioRecordingIntent`: the system lets it open the microphone in the background, on condition that a Live Activity shows for as long as the recording runs (`RecordingActivity` starts one before `perform` returns). Before the microphone was ever allowed it continues in the foreground (`continueInForeground`) so the system can ask.
- **The calendar names it** (`MeetingCalendar`): the event on now (all-day, declined and untitled ones aside; one starting within 5 minutes counts; of overlapping ones, the one that started last) gives the title, and a copy of the event goes into `RecordingEntry.meeting` (`RecordingMeeting`: event id, title, start / end, location, attendees by name). It reads only with full access already granted, so a start never waits on a prompt; a meeting recording started in the app asks once, after the recording has begun, and renames it if an event turns up. Without an event the title is "Meeting · <date time>" (`RecordingEntry.displayTitle`).
- A watch recording made during a calendar event is that meeting's too: the phone tags it when its first segment or end arrives (`MeetingRecorder.tagWatchRecording`, by the watch's `createdAt`). The watch has no calendar.
- Language: every start uses the one last picked on the Recordings page (`RecordingDefaults.localeKey`), Chinese until then. The on-device Chinese model writes the English words said among Chinese in Latin letters; there is no mixed-language model on device (the hub's realtime service is #2083).
- **Live Activity** (`docs/apple/live-activity.md`, "Recording"): any recording on the phone, a meeting's or a note, shows its name, what it is, bookmarks and the time on the Lock Screen and in the Dynamic Island, with **Bookmark** and **Stop** (they work locked, as a voice memo's). Tapping it opens Recordings (`persona://record`).
- **A call**: the interruption pauses the recording and closes the open segment at once (`RecordingSegmentWriter.cut`), so an app stopped during a long call keeps everything said before it. When the system ends the interruption the recording resumes by itself (whether or not it says `shouldResume`: the meeting goes on). If the microphone can't be opened again in the background, the phone posts "Recording Paused" (`MeetingRecorder.notifyPaused`), the activity says "Paused by a call · Open Persona to continue", and coming back to the app resumes it (`resumeAfterInterruption`, on `didBecomeActive`). A media services reset while paused stops the recording (its engine is gone) and keeps what was saved.
- **Stopped in the background** (the activity's Stop with the phone locked): the audio session ends, and with it the app's right to run. `MeetingRecorder.finishInBackground` holds a background task for up to 30 s, until the last segment is transcribed and uploaded, so the minutes don't wait for the next launch.

## Apple Watch

The watch records a meeting in one step too (#2720), and shows its minutes once the phone has them.

- **Starting** (#5493, as on the iPhone since #5243 and #5517): **Recordings** on the home list (`WatchMeetingEntries`, `WatchRecordingsLabel`) is a place, like the iPhone drawer's row: it opens the recording page (`WatchRecordingView`, titled Recordings) and starts nothing; while a recording runs it reads "Recording… Open" and opens it. All-day recording has its own card, so the row stays Recordings then, under Mark Meeting (all-day recording's meeting marks, below). The unpaired page has the same row. On the page, **Record** is the one prominent button (Double Tap on watchOS 11 and later): always a meeting, marked as one for the phone to name after the calendar (`RecordingKindChoice.watchMeeting`; the watch reads no calendar). Under it **Type: Meeting** is a text button with the kind's mark (`.borderless`, as the iPhone's Type row shows the mark), Record's qualifier and never a second start button: a tap makes it **Type: Note**, which says "No minutes, and Persona stays out.", and the same Record then records a plain note. It is Meeting for every start and never remembered. Rolling and All-Day Recording stay under them as before. The **Record** complication (watch face corner / circular / rectangular, with "Meeting" under Record / inline, and the Smart Stack; `MeetingRecordingComplication`) is an explicit start, like the iPhone's Control: one tap opens the app straight into a meeting recording (`persona://record?meeting=1`, `WatchRecordingView(startsMeeting:)`). Its kind, `persona.watch.meeting-recording`, stays, so faces set up before keep it. The watch has no Control or Siri intent of its own; the Ultra's Action button isn't wired yet: it needs an App Shortcut in the watch app, with its own phrase catalog.
- Identifiers: `watch-meeting-recording` (the Recordings row, kept from when it was Meeting Recording), `watch-recording-start` (Record), `watch-recording-kind` and `watch-recording-kind-detail` (Type), `watch-recording-meeting` (a meeting being recorded), `watch-meeting-mark` (Mark Meeting). The watch has no UI tests; `RecordEntryTests` checks what Meeting and Note mark.
- **The meeting mark**: a meeting recording carries `meeting: true` in every `RecordingTransfer` (each segment and the end message); a watch with an older app sends none, which reads as a note. The phone keeps the mark when the recording arrives (`RecordingLibrary.receive`), names it after the calendar event at the recording's start (`MeetingRecorder.tagWatchRecording`; the watch reads no calendar), and uploads `kind: meeting` with the event as `calendar` when one matched. A note made on the watch during a calendar event becomes that meeting's too, as the contract allows.
- **Minutes on the watch**: the phone relays a short version of its newest meetings' minutes (at most 8 meetings from the last week; 4 points, 4 decisions, 4 action items each, 120 characters an item; `WatchMinutes`) in the WatchConnectivity application context, and the watch shows them under **Meeting Minutes** (`WatchMinutesList`, `WatchMinutesView`): ready, "Writing the minutes…", or failed. The watch never talks to the hub for them. `MinutesRelay` reads them from the hub when the phone app comes to the front (again every 3 minutes while it stays there and some are still being written) and whenever a recording's page loads them, so minutes written while the phone app is closed reach the watch the next time it is opened (the hub's "minutes ready" notification is the usual reason to open it).
- **Long recordings**: the watch app records with the `audio` background mode, so recording goes on with the wrist down and the screen off; an hour is about 14 MB (32 kbps AAC). Segments are 30 s while the phone is reachable and 60 s otherwise; the watch sends them in batches every 3 minutes (5 in Low Power Mode) and keeps each until the phone confirms it is saved. It stops by itself, keeping what was recorded, under 5 % battery off the charger or under 150 MB of free space. A call or Siri pauses it like the phone's (the open segment is closed at once) and it resumes when they end. Battery use is not measured yet: continuous microphone capture, AAC encoding and the transfers are the costs; check it on a real watch with a one-hour recording.
- **Verified in the simulator**: the watch app builds; screenshots of the home entry, the recording page (`-openRecording YES`, with `-showWatchNote YES` for Note picked and `-showRecordingEnd YES` for its end), a meeting being recorded (its page, or with `running` the home list's "Recording… Open") and the short minutes (`-showMeetingSample recording|running|minutes`, `-openWatchItem minutes|minutes-writing`), and the Record complication in every family (`-openWatchItem record-complication`). The real watch checks are in the device checklist: wrist down for an hour, the screen off, the phone out of range and back, and minutes reaching the watch.

## The live recording sheet and the wrap-up (#4240)

Design and reasons: `docs/design/proposals/meeting-recording.md`. On iPhone a running recording has one place, a sheet (`RecordingLiveSheet`, large detent, own navigation stack) that `.recordingLiveSheet()` presents from both hosts of `RecordingsSection` (Knowledge's list and `RecordingsPage`): when a recording starts from this page, when the page appears while one runs (Live Activity tap, drawer, Siri, notification: `MeetingRecorder.openRecordings` bumps `RecordingLivePresenter.serial`), and from the now-recording card. **Hide** (or a swipe down) keeps recording and leaves the card, one row with dot, title, time and state that is not repeated in Recent Recordings.

- Top to bottom: title (with the page menu, Meeting / Note, and Hide), Meeting chip and state line (dot, or a pause glyph, never color alone), the timer (`Text(timerInterval:)`, the system ticks it), a level meter (`RecordingLevelMeter`, 30 fps clock, one still frame with Reduce Motion or Low Power; the level is `RecordingCapture.inputLevel`, the writer's own smoothed level), the Persona strip when the hub offers attending (role menu, Who Attends…, problem text as a `NoticeBanner`), and one timeline (`RecordingTimeline`): transcript sentences, bookmarks and Persona exchanges in time order. Sentences arrive per finished segment (about every 30 s), so the audio newer than the last sentence shows as "Transcribing the last N s" and an empty timeline says "Listening…". The timeline follows the newest item unless the reader scrolled up; then **Jump to Latest** appears with the count of new items (`RecordingFollow`).
- The bottom bar stays under the thumb: **Ask Persona** (prominent, only when Persona attends; keyboard button for a typed question, written in a row of the bar), then Pause / Continue, Stop and Bookmark. At accessibility sizes it stacks, holds Ask Persona and Stop, and a More menu has Pause, Bookmark and Type a Question.
- **Stop asks first** ("Stop Recording?", the audio is saved on the device and its text goes to Knowledge, a stopped recording can't be continued). The Live Activity's, Control's and Siri's Stop stay one tap.
- **Wrap-up** (`RecordingWrapUp`): after Stop the same sheet cross-fades to the recording's steps, worked out from state the phone already has (`RecordingPhase`): Saved, Transcribed, Syncing to Knowledge (with "Waiting for the hub" when unpaired or offline), Writing minutes (meetings), Minutes ready. A failed step shows its reason and the existing action (Resume Transcribing, Retry for minutes). **Open Minutes** / **Open Recording** (the one solid button) pushes the recording's page inside the sheet, **Done** (top trailing, where Hide is while recording) closes it; everything goes on in the background.
- DEBUG: `-showMeetingSample flow` (no microphone) records a made-up meeting by itself: the clock and level move, a line is heard every 6 s, Pause, Bookmark and Stop work through DEBUG sample paths in `RecordingCapture` (the entry ends, `onSaved` runs), so the wrap-up reaches the minutes against the mock hub (`POST /mock/recording_minutes {"mode":"ready"}` before the launch; the app asks again every 15 s). The existing values (`recording|paused|interrupted|listening|taking-part|answered|minutes|pending`) work as before, and their Stop now ends the sample too. UI test: `MeetingRecordingUITests.testLiveSheetFlowReachesTheMinutes`.
- The recording's page (`RecordingDetailView`): title "Recording", a compact header (title, when and where on one line, attendees, one source line), the minutes straight after it, the transcript, then the actions and, last, the retention paragraph. "Writing the minutes…" is a row with a spinner; a failed write shows the reason and a **Retry** button (`minutes-retry`); the sections unfold with `Theme.Motion.reveal` (not with Reduce Motion); time chips have a 44 pt hit area; transcript rows are identified by where their sentence starts.
- visionOS keeps the earlier in-list controls; the new views are iOS only.

## Minutes

The hub writes minutes for a meeting recording once its text is complete (#2710; the hub side and the field contract are `docs/assistant/meeting-minutes.md`). Every upload says `kind: "meeting"` or `"note"`, and a meeting the device matched to a calendar event adds the event as a hint, `calendar: {title, attendees, location}` (no event id: the hub can't see the phone's calendar). `RecordingUpload` is the one place that writes them. A phone recording is a note only when Note was picked for it (at the start or on its live page); a watch recording becomes a meeting only when the phone matches it to an event.

- On iPhone, once they are ready, the recording's page gets a segmented switch right under its header, **Summary | Transcript** (#5026). **Summary** is the default and takes the place of the minutes' sections; the choice lasts while the page is open (no setting). Before the minutes are ready (still recording, waiting for the text, being written, failed, not a meeting) the page is as it was, with no switch.
  - **Summary** (`RecordingMinutesSummary`): a header card (the minutes' title when it differs from the recording's, when · where, the attendees; "Nothing to note" when every group is empty; Updating, a failed rewrite, an expired transcript and **Regenerate**, below), then one card per group with something in it, in this order: Key Points, Decisions, Open Questions, Action Items, each headed by its icon, title and count. An item shows its words, for an action who and by when, the meeting photo it cites (below), and its time as a chip: a tap switches to **Transcript**, scrolls to that line and lights it. Its chevron opens, in place, the transcript lines around its time (the line `RecordingMinutes.sentenceIndex` picks, `Theme.Recording.contextBefore` before it and `contextAfter` after it), so the details read without leaving the Summary. The Action Items card carries **Add to To-Dos** / **Added** on each item and ends on **Add All** (below).
  - **Transcript**: the transcript as before, without the minutes' sections above it.
  - What the Summary shows is worked out by `RecordingMinutesCards` (`apple/Shared/Recording/RecordingMinutes.swift`) when the minutes or the transcript change, not in `body`; tests: `apple/PersonaTests/MinutesCardsTests.swift`, `apple/PersonaUITests/MinutesCardsUITests.swift`.
  - **The summary card** (#6563, `RecordingMinutesOverview`; the contract is [meeting-minutes.md](../assistant/meeting-minutes.md) "Summary card (#6563)"): minutes that carry an `overview` show it instead of the four cards. `RecordingMinutes` reads `overview` on its own, as a JSON value cleaned by the contract's "Clients" rules (`RecordingMinutes.Overview.clean`), so a broken one never fails the lists; nil when no bullet is left, and then the page is exactly as before. The Summary then holds the summary card (the headline, else the minutes' title; when, where and the attendees only where the page's header does not show them; the Core Conclusions box on the AI tint with the lightbulb; Updating and Regenerate), one card per topic under its group's heading (the group's tint by its place, `Theme.Hue.minutesGroups`; the icon by `icon`, `general` for an unknown one), Action Items, Open Questions and the AI note. A bullet is an item as the four cards' (its words with the `strong` places in bold by the contract's rule, `RecordingMinutes.boldParts`, never Markdown; its photo; its time and its transcript lines), after a dot, or the decision mark and the word Decision for a decision; a topic shows four and folds the rest under Show N More. An added action whose to-do the app holds shows that to-do's done circle, which shows and ticks it as the To-dos list does (a failed start still waiting on the owner reads open), named for VoiceOver by its action. Copy and Share send the hub's `markdown` in this layout (`RecordingMinutes.shareText`). Design: docs/design/DESIGN_SYSTEM.md "Minutes cards (#5026)"; tests: `MinutesCardsTests` (and the shared fixture `spec/fixtures/minutes-overview/cases.json`), `MeetingRecordingTests`, `apple/PersonaUITests/MinutesOverviewUITests.swift`.
- visionOS shows them above the transcript (`RecordingMinutesSections`): the title when it differs, when and where, the attendees, then key points (who said it, when known), decisions, action items (who, by when, as said) and open questions. Each item carries `at`, seconds into the recording; tapping the time scrolls the transcript to that line and highlights it for two seconds (`RecordingMinutes.sentenceIndex`: the last line that started by then).
- When they were is written from `start` / `end`, in the header's date style through `RecordingFormat.span` (the end's time alone on the same day, its date too on another; the hub's `when` only from a hub without them, #4875), in the Summary's header card and in the sections alike (`RecordingMinutes.whenLine`).
- **Copy** and **Share** send them as Markdown rendered on the phone (`RecordingMinutes.text`), every item with its time; with the summary card (#6563), the hub's own `markdown`, which follows its layout. On iPhone they sit under the switch, the same in both views.
- **Action items as to-dos** (#4062, as the web and Android): each action item has **Add to To-Dos**, or **Added** once it is one (`todo_id` set). **Add All** appears when more than one is left. Both send `POST /api/v1/recordings/{id}/minutes/todos` with the `version` shown, and the page reads the recording back before another write is allowed. `409 minutes_stale` (rewritten since they were shown) shows the new minutes and says they changed; nothing is sent again. A hub whose minutes have no `version` or action `id` offers neither. On iPhone they are in the Summary's Action Items card (Add All at its end).
- **Regenerate** sits under Copy and Share (on iPhone in the Summary's header card, `RecordingMinutesUpdates`) when the whole transcript is on the hub, nothing is being written and the transcript hasn't expired (`trimmed_at`). The minutes shown stay while the hub writes new ones ("Updating the minutes…", read again every 15 s). If the new ones fail, the shown ones stay and the page says so. An expired transcript keeps its minutes and says so, with no Regenerate.
- Until they exist the section says what they wait for: the recording to end and be transcribed, the text to reach the hub, or "Writing the minutes…" while `detail.meta.minutes_state` is `pending` (asked again every 15 s while the page is open: `RecordingMinutesModel`). `failed` offers **Retry** (the wrap-up says the same) (`POST /api/v1/recordings/{id}/minutes`; a `409` means the hub doesn't have the complete transcript yet). A note shows minutes only if the hub wrote some.
- Read from `GET /api/v1/kb/recordings/{id}`: `detail.minutes` (`version`, `title`, `when`, `start` / `end` (RFC 3339 with the user's offset), `where`, `attendees`, `points`, `decisions`, `actions` with `id` / `who` / `what` / `by` / `due_date` / `todo_id`, `open`, and since #6563 `overview` and `markdown`), `detail.meta.minutes_state` and `detail.meta.trimmed_at`. Sections a hub leaves out are empty.
- Mock hub: `MOCK_RECORDING_MINUTES=ready|pending|failed` (`scripts/mock/mock_hub.py`; `POST /mock/recording_minutes` switches it) writes made-up minutes in that shape from a meeting's own sentences when it is stored complete (`when`, `start` and `end` from its start and length, as the hub writes them), leaves them pending, or fails them. `POST /mock/recording_trim {"id"}` turns a stored one with minutes or a day recap into a trimmed recording (its transcript expired); `MOCK_RECORDING_DAYS` and `MOCK_MINUTES_DAYS` start the two retentions. Its minutes carry `version` and action ids. `POST …/minutes/todos` makes the to-dos (`409 minutes_stale` for an old version), `POST …/minutes` keeps the shown minutes while the next version is written (3 s later in `ready` mode), and `POST /mock/recording_minutes_rewrite {"id"}` writes the next version at once. `MOCK_RECORDING_MINUTES_SHAPE=legacy|overview|malformed` (`POST /mock/recording_minutes {"mode", "shape"}`; default `legacy`, which `MinutesCardsUITests` keeps as its baseline) picks the minutes' shape (#6563, `docs/assistant/meeting-minutes.md` "Summary card (#6563)"): `overview` adds the summary card, `malformed` a broken one beside the legacy minutes (the app shows the four cards), and every shape carries the hub's `markdown`. In `overview` every sentence that neither decides, assigns nor leaves something open is a point, and each transcript minute is a topic titled by the subject its first line names before a colon (`导出：…`), so a sample with lines in at least four minutes gets four topics in two groups (`docs/operations/testing.md` "Mock-data screenshots" has the rest). `-showMeetingSample overview` opens a five-minute sprint review written for it (`RecordingSamples.installOverviewMeeting`); the `minutes` sample stays the four cards' baseline. Day recordings are on by default (`MOCK_NO_RECORDING_DAY=1` disables them); `MOCK_DAY_RECAP=ready|pending|failed|none` sets their recap mode (default `ready`), and `POST /mock/recording_day {"on": true, "recap": "ready"}` switches it (`GET` reads it back).

## Persona in meetings

Persona can attend a meeting being recorded on the iPhone (#2981; the iPhone side is #3375). The contract, with every field and error, is [meeting-minutes.md](../assistant/meeting-minutes.md) "Attending a meeting"; this is what the app does with it. Only a hub that lists the `meeting_attendance` feature gets any of it: with another hub the Recordings page, the upload and the Live Activity are exactly as before. The redesign (#3862) replaces the attending session with the meeting's own room conversation and a meeting page, on a hub that lists `meeting_conversation`: [meeting-minutes.md](../assistant/meeting-minutes.md) "Meeting conversation (#3862)". The iPhone does phase 1 of it, phase 2, the live view, phase 3, photos, phase 4, realtime voice, and phase 6, talking about any recording ("The meeting conversation (#3862)" below); a meeting started on a hub without it, or before the hub listed it, keeps everything this section describes.

- **Role, chosen as the recording starts**: **Off** (the hub writes the minutes after the meeting, as before), **Listen** (Persona follows the meeting; answers show on screen only), or **Take Part** (they are also said aloud). The Recordings page shows the choice under **Record** for a meeting (`RecordingCaptureControls.personaRow`), pre-selected from the hub's `meeting_attendance_default`, which **Settings › Meetings** changes (`MeetingSettingsView`, `PUT /api/v1/assistant`). A pick on the page is for that one recording. The drawer, the home screen's menu, the Control, Siri and the Action button start with the default and the session picked last unless that was the main chat, else with Persona off, so they stay one tap (never the main chat by default, #3862); the last word from the hub (whether it offers attending, its default) is kept for a start without the app in front (`MeetingAttendanceDefaults`).
- **Who attends**: one session per recording (`MeetingAttendees`): the main chat (sent by leaving `attending_session` out), Persona's other open chats, or an Agent that takes messages; never one the hub would refuse ("Sessions that never attend": a work computer's, another manager's, the health space's, a chat whose replies leave the apps or that an extension opened, one in `bypassPermissions`). The one picked last is pre-selected while it can still attend, unless it was the main chat: the main chat attends only when picked for that meeting (`RecordingAttendance.mainPicked`; owner decision in #3862, since every line fed to a chat fills its context). With nobody chosen the role shows Off; Listen or Take Part shows Who Attends: Choose with "Choose who attends. Until then Persona stays out of the meeting.", a start without a choice starts with Persona off, the picker never pre-selects the main chat, and turning Persona on mid-meeting (the strip's Turn on Persona or its role menu; `MeetingRecorder.setRole` returns false) opens the picker first and turns on once someone is picked. The upload holds it whatever built the attendance: an attending role with no session named and the main chat not picked goes as `off` (`RecordingAttendance.sentRole`, which the stored mark, live lines and asks follow too), so `attending_session` is never left out by default. Kept on the recording as `RecordingEntry.attendance` (`RecordingAttendance`), on this phone only.
- **The upload** (`RecordingUpload`, `RecordingSync`): an attending recording goes at once, ahead of the others and without the save debounce, before it has any text, and every later upload carries the same `attendance` and `attending_session`. A meeting never attended carries neither. The `200` says what the hub took (`attendance`, `attendance_problem`); a `200` without `attendance` is an older manager, and the phone stops attending that meeting (`stopped: unsupported`).
- **Live lines** (`MeetingLive`): every finished sentence that ended while Persona attended (`RecordingAttendance.periods`) and is not Persona's echo (below) joins the recording's append-only live log, once per segment, and goes to `POST …/live` from the hub's `next`, at most 200 a request, only after the hub stored the current attendance. `409 live_gap` asks where the hub is and goes on from there; `404 recording_not_found` and `409 not_attending` upload first; a `404` without that code is an older manager (stop attending); `413 live_full` stops the lines but not the questions; `503` and a dropped connection back off (2 s … 60 s). The hub's `problem` shows on the recording.
- **While it records** (`MeetingAttendanceSection`, below the recording's controls): "Persona is listening" / "Persona is taking part" as the visible notice, the role (changeable for the rest of the meeting: turning it on mid-meeting sends only what is said from then on), who attends, **Ask Persona** and **Type**, and the last three answers with what they drew on (`sources`).
- **Asking, by a tap only** (no wake phrase; `MeetingAsk`): **Ask Persona** asks about what was just said: the open segment is cut at the tap (`RecordingCapture.flushSegment`), transcribed and its lines sent, then `POST …/ask` goes with `at` and `live_next`, and the question the hub heard replaces "What was just said". **Type** opens a question row in the live sheet's bottom bar, not a system alert (#4486, DESIGN_SYSTEM.md 4.4): the composer's field, which grows to a few lines, and its send button, which waits for words; Return sends too, and the keyboard button becomes Cancel. The timeline and the earlier answers stay in sight while typing. Each question has the phone's own id; a `202`, a timeout or a server error sends the same request again a few seconds later, for up to 4 minutes. A failure says what happened in plain words and offers Retry with the same id. An answer names the earlier meetings and projects it drew on (`sources`), a meeting with when it was in the minutes' date style (its `start` / `end`; `when` from a hub without them, #4875).
- **Spoken answers** (`MeetingSpeaker`): an answer with `speak` (taking part) is said at most once, only for a question asked in this run, only while the recording runs and the role still speaks (checked again just before the first sound), and only when it came within a minute of the tap. It is said in the realtime voice of voice chats when the hub offers it (#5004): the voice extension runs, is set up and lists `speak` in `/info` (`MeetingVoiceRoutes`, asked again after a minute). The phone sends the model of the use `plugin.<id>` and the voice picked on this device (`VoiceTimbre`) to `POST …/speak` and plays the PCM as it streams in (`MeetingVoicePlayer`, an engine of its own in the recording's session, holding the first 0.3 s). Without that voice, on an error (offline, `no_upstream`, `upstream_*`, an older hub's 404), with no audio at all, or when no audio has come within 8 seconds, the phone's on-device voice for its `lang` says it instead (`AVSpeechSynthesizer`, `MeetingDeviceVoice`). Either way it plays in the recording's own audio session, so the recording goes on. A realtime answer cut off midway plays what came and is not said again. Where it plays follows the voice call's route rule (`VoiceRoute.output`, #3015): a connected headset, else the loudspeaker, never the earpiece; the recording keeps its microphone. A recording that starts as Take Part lets a Bluetooth headset play (`allowBluetoothA2DP`, listening only; `RecordingCapture.playsAloud`), which an Off recording's session never does. **Stop Speaking** cuts it short.
- **Its echo is not the meeting's** (#5905, [meeting-minutes.md](../assistant/meeting-minutes.md) "Persona's echo"): every stretch the phone speaks in is kept on the recording with the answer said in it (`RecordingAttendance.spoken`, `RecordingSpan.text`), from when its audio actually begins (the realtime voice may take a moment to start; what the room says meanwhile is the room's). A sentence is Persona's echo only when it overlaps a stretch (0.5 s more on each side, `MeetingEcho.margin`) and its words are the answer's (at least 60% of its character bigrams after normalizing, `MeetingEcho.containment`; `RecordingSpan.echoes`), so what anyone else says while Persona answers stays the meeting's. A stretch saved before has no text and keeps the old rule, at least half of the line inside it. Echo lines stay on the recording, marked: hidden wherever the transcript is shown (`RecordingEntry.sentences` is the lines that are not echo; the timeline and Talk skip them too), never in the live log, and uploaded with `echo: true` beside the stretches (`spoken`) to a hub that lists `meeting_echo`, read from its `hello` at each upload (`RecordingEntry.markedSentences`, `RecordingUpload`); to a hub without it they are left out of the upload. A line transcribed while the answer is still being said is checked against the stretch so far (`MeetingSpeaker.openSpan`, which carries the whole answer). Persona's answers stay on the hub as the meeting's `asks`, not transcript lines.
- **The Live Activity**: under the name, "Persona listening" or "Persona taking part"; the latest answer, cut to two lines (the hub posts no notification for answers); **Ask Persona**, and **Turn Off Persona** (`TurnOffMeetingAttendanceIntent`) beside Bookmark and Stop, which give up their words for their marks so the four fit. Ask and Turn Off work from the Lock Screen without unlocking, as Stop does: the hub answers a meeting only from meeting material.
- In the attending session the meeting's batches show as messages from **Meeting** (`MessageSource` kind `meeting`).
- Screenshots and UI tests: `-showMeetingSample listening|taking-part|answered` (a made-up meeting Persona listens to, takes part in, or takes part in with two answers, the newest being said); `-openMeetingSettings YES` for Settings › Meetings. The mock hub serves the whole contract (`scripts/mock/mock_hub.py`, "Attending a meeting"); `POST /mock/meeting_attendance {"on", "default"}` switches the feature and the default before a launch, and `GET /mock/meeting_attendance` reads back each recording's role, live log and questions (`MeetingAttendanceUITests`).

### The meeting conversation (#3862)

Phase 1 of the contract ([meeting-minutes.md](../assistant/meeting-minutes.md) "Phase 1: the meeting conversation"), on a
hub that lists `meeting_conversation` beside `meeting_attendance`. The page's design and its identifiers are in
[DESIGN_SYSTEM.md](../design/DESIGN_SYSTEM.md) "Meeting page (#3862)".

- **A room meeting**: a phone meeting recording started while the hub listed the feature (`MeetingAttendees.conversation`,
  from the last `hello`; every hub `hello` keeps the meeting flags in `MeetingAttendanceDefaults` for a start without the
  app in front, so a cold one-tap start goes by the last hub it heard). The recording keeps
  what it started with (`RecordingAttendance.room`, `photos`, `privateMark`; `RecordingEntry.roomMeeting`), whatever the
  hub lists later.
- **Meeting content never reaches the main chat**: a room meeting's uploads carry its role only inside `room`, never the
  top-level `attendance` or `attending_session`, whatever its attendance holds (`RecordingUpload`), so the hub feeds its
  lines and asks to the room alone and an older manager after a rollback sees a meeting that is off; only Just me (the
  owner's explicit switch) sends to the main chat. One-tap starts on such a hub are room meetings too. A meeting started
  on an older hub follows "Persona in meetings" above, where the main chat attends only when picked. Pinned by
  `MeetingConversationTests` (`aRoomMeetingNeverNamesAnAttendingSession`, the one-tap start),
  `MeetingAttendanceTests` (the old hub's defaults and the upload's guard) and
  `MeetingConversationUITests.testRoomMeetingAnswersATypedQuestionInTheThread` (the room holds the meeting's lines; the
  main chat's items are the same before the meeting and after its lines and a question).
- **Starting**: the Recordings page shows the role and **Private Meeting** (off; "Its minutes are never used when Persona
  answers in later meetings."), and no Who Attends: the meeting's own room conversation attends. One-tap starts (drawer,
  home screen menu, Control, Siri, Action button) use the default role and not private (`MeetingAttendees.startAttendance`).
  A room meeting stores its attendance whatever the role, Off included, so it uploads at once.
- **The upload**: every upload of a room meeting carries `room: {"attendance": <role>}` (`off` too) and `private`, never
  the top-level `attendance` or `attending_session`. Live lines and asks wait for a `200` that carries `room` (the stored
  mark is `room|<role>`); its `room` goes to `MeetingConversation` (`RecordingSync.onRoom`), and an upload with `ended_at`
  that the hub stored is noted (`RecordingSync.endedStored`).
- **An older manager after a rollback**: a `200` without `room` is not permanent. The recording goes on uploading `room`,
  no `…/live` or `…/ask` goes, the page says the hub can't take part right now (`RecordingSync.roomMissing`), and the
  Live Activity shows Persona as not attending, until a `200` carries `room` again. One rule decides whether Persona can
  be asked (`MeetingAsk.canAsk`: it attends and the room isn't missing): the page's subtitle and header, the bar (the field
  asks nothing of the room, [+] goes), `MeetingAsk.ask` and `retry` (no exchange, so no forced upload) and the Live
  Activity follow it. Just me stays. After the meeting such a hub has no follow-up: the composer says so.
- **The hub's state of the meeting** (`MeetingConversation`): the room (`MeetingRoom`: state, ids, problem; a state the
  app doesn't know reads as `starting`), the private mark, the follow-up and the asks, from the upload's `200`, the `200`
  of `POST …/live` and `GET …/live`. A page on screen watches its recording (one watch per page) and reads it whole once;
  a `meeting_live` event reads only the parts it names that are newer than those read (`?parts=`), and only the parts
  the page shows (phase 1's `room`, `asks`, `private`, `followup`; `topics` with the live view, `highlights` with the
  highlights, `photos` with photos); a reconnect reads every watched meeting again. An upload's or a
  live POST's late answer never takes the room back to an earlier state. `404` / `410` mean the hub has no such recording.
- **The meeting page in the sheet**: `RecordingLiveSheet` shows `MeetingPage` for a room meeting in place of the live
  view, and after Stop in its after mode in place of the wrap-up; every way in (the drawer row, the Live Activity, a
  notification, Recordings) still opens the sheet. The page has the state line, a subtitle with the role, "On this
  iPhone" and "Private", the room's problem (`runtime_unsupported`, `node_too_old`, `machine_offline`, `start_failed`: then
  questions are answered from the meeting so far), **Topics** for a meeting started with the live view ("Live view (phase
  2)" below), the top card, the thread, and the bar: Just me, the composer and its `[+]` menu (**What Was Just Said**,
  the heard ask), Pause, Bookmark, Stop. The top card is **Highlights** for a meeting started with them (the room's
  highlights, or a waiting line before the first while Persona attends, and **Full Transcript**; "Highlights (phase 2b)"
  below), or else the **Live** box (the last three lines, how far behind the transcript is, **Full Transcript**); while
  the keyboard is up, either keeps only its newest line or highlight, or the waiting line.
- **The thread** (`MeetingThreadView`, read apart by `MeetingThread`, a port of the web's `meeting-thread.ts`): once the
  room has ids the page subscribes to it like any chat (WS `subscribe`, **Show Earlier** pages back) and shows its items:
  Persona joined, batches as "N lines from the meeting", heard questions as "What was just said (m:ss)", the owner's
  questions, Persona's answers (consecutive replies merged), the end. An ask the room's thread doesn't hold yet, a failed
  one and one answered from material keep the `MeetingExchangeRow` look; without ids ("Persona is joining the meeting…",
  or a room that never came up) every ask shows that way.
- **Asking**: the composer asks the room (`…/ask` as #3746 does, one at a time); the answer records how it went (`via`).
  A room ask the hub fails at once with `room_unavailable` (the room went unavailable) is asked once more under a new id,
  which the hub answers from the meeting's material; `meeting_ended`, `room_unavailable` and `no_answer` have their own
  words.
- **Just me**: with the switch on, the composer sends `POST …/just-me` (id, text, images) and the thread shows those
  questions and the main chat's answers after each one (by its `client_msg_id`) until the end of its turn: the next user
  text of any kind, one the hub puts in (a task's notice, a scheduled run) included (`MeetingThread.justMeAnswer`). The
  main chat's reply being written shows only while the question is in the chat and its turn the newest; a card the turn
  raises (since the question, while its turn is the newest) is answered in the main chat, which the thread says. Just me lives only in
  `MeetingConversation`: never in `MeetingAsk`, the Live Activity, `MeetingSpeaker`, VoiceOver announcements or the room.
  Retries follow the hub's receipts: the same id only when no answer came, on `503 assistant_offline` or a pending
  receipt; any other answer ends that id and **Retry** sends the words under a new one. A `404 recording_not_found`
  uploads the recording first and sends once more.
- **The private mark**: the page menu's **Private Meeting** (`PUT …/private`); shown as the hub has it once it said
  anything (a read of `private`, a change, an answer carrying it: `Meeting.privateRead`), absent counting as private; the
  start's choice stands in only until then. The recording's page has the same switch for every meeting on such a hub
  (hidden after `409 not_meeting`). A refused change says "Couldn't change Private Meeting. Try again." unless the hub
  names a reason (`MeetingConversation.privateMessage`).
- **After the meeting**: the after page shows the status card (when, the minutes' state, **Open Minutes** / **Open
  Recording** in the sheet and where the minutes push opened it, kept above the thread), the room's thread with the asks answered from material where they
  were asked (or the hub's `asks` when the room never came up), **After the Meeting**, then the follow-up. Its composer
  waits while the hub would answer `409 meeting_running` (`MeetingConversation.running`): the recording runs, an upload
  on its way may carry the `ended_at` the hub doesn't have yet (within 12 hours of the last upload it stored, after which
  the hub ends the meeting itself), the room is still starting, ready or unavailable, or a room meeting's room isn't read
  yet. The hub's word goes first: a room `ended` or `deleted` has passed the end, and a whole read without a room, like
  an older manager, has no room to wait for. A refused upload or a paused sync isn't on its way: the composer says the
  sync's problem instead of "in a moment" until those 12 hours are over (`followupHold`). The first message goes
  to `POST …/followup` (it makes the follow-up with that message), the next ones to the follow-up's
  `POST /api/v1/sessions/{mid}/{sid}/messages` with the message's id as `client_msg_id` and op id; a send keeps the route
  it started on. A `400` / `409` / `413` ends that id (Retry sends the words under a new one); no answer, a pending
  receipt, a 5xx or a `404` keep it. A sent message leaves the pending list once the follow-up's items hold its
  `client_msg_id` (`followup-<id>` or the id). Just me and `…/followup` take at most 8000 characters: over that Send
  stays off with a caption saying so, and nothing is cut (the follow-up's own messages have no such limit).
- **The recording's page**: for a meeting on such a hub a **Private Meeting** row, and for any finished recording with
  something to talk about **Talk about this meeting**, which pushes the after page ("Talk about this meeting (phase 6)"
  below).
- **Screenshots and tests**: `-showMeetingSample room-listening|room-taking-part` (the made-up meeting as a room meeting,
  listening in or taking part). The mock hub serves the phase (`POST /mock/meeting_conversation {"on", "room",
  "end_seconds"}`, `GET` reads back each meeting's room, mark and follow-up; the fresh start `POST /mock/recording_minutes`
  also takes Just me's messages and their replies back out of the main chat). UI tests: `MeetingConversationUITests` (the
  start's choices on a hub with and without the feature, a typed question answered in the room's thread, Just me reaching
  only the main chat, the private switch, the follow-up after Stop); `MeetingAttendanceUITests` turns the feature off to
  keep covering #3746. Unit tests: `MeetingConversationTests` (the upload and its `200`, the hub's state and events, the
  private mark and when the hub's word replaces the start's, Just me and the follow-up with their retries and the 8000
  limit, when the follow-up waits, asks while the room is missing, `room_unavailable`, the words) and `MeetingThreadTests`
  (the web's cases, the mock's items, where a Just me answer ends, asks answered from material in place).

#### Live view (phase 2)

The contract's "Phase 2: live view" ([meeting-minutes.md](../assistant/meeting-minutes.md)), for a room meeting started
while the hub listed `meeting_live_view` beside `meeting_conversation` (`RecordingAttendance.liveView`, kept for life like
`photos`: a hub that lists it later adds nothing to a meeting already running; `MeetingAttendanceDefaults.liveViewKey`
keeps the last hello's flag for a start without the app in front, `MeetingAttendees.liveView`, `startAttendance`, the
Recordings page's start through `RecordingStart.make`). The Topics row is in [DESIGN_SYSTEM.md](../design/DESIGN_SYSTEM.md)
"Meeting page (#3862)"; the Live Activity's topic and line in [live-activity.md](live-activity.md) "Recording".

- **15-second segments**: while Persona attends such a meeting and its lines reach the room
  (`RecordingAttendance.shortSegments(roomMissing:)`: a room meeting with the live view whose role isn't off, not stopped
  by the hub (`live_full`, `unsupported`) and not taken by an older manager without the room, `RecordingSync.roomMissing`),
  its segments are `RecordingAudioSpec.meetingLiveSegmentSeconds` (15 s, above the
  writer's 10 s minimum) in the foreground and the background alike, so a line reaches the page and the room within about
  15–30 seconds. `MeetingRecorder` hands it to the capture at the start (`RecordingCapture.start(…, shortSegments:)`)
  and again when the role changes (the page's role, the activity's Turn Off) or the recording turns into a note or back
  (`RecordingCapture.setShortSegments`; the open segment closes as soon as it is longer), and whenever a save or an
  upload's answer changes whether the lines go (`RecordingSync.onSegments` → `MeetingRecorder.followSegments`). Every other recording keeps
  30 s in front and 60 s behind (`RecordingCapture.segmentLength(short:watched:)`), all-day recording its own chunks.
- **Topics**: `MeetingConversation` reads `topics` and `topics_at` with the rest of `GET …/live` (`LiveRead`, leniently:
  an unknown state reads as `open`, a topic without a title leaves the list, at most 20 are kept) and follows `topics` in
  `meeting_live` (`partsRead(for:)` adds it for such a meeting only), reading `?parts=topics` when its revision is newer.
  A read of other parts keeps the topics; a meeting started without the live view keeps none (`applyTopics`). The page
  shows them as chips under the header, above the Live box, once the room set any. In the background the WebSocket is
  closed, so no `meeting_live` comes: while this phone records such a meeting, an accepted `POST …/live` reads
  `?parts=topics` at most every 20 s (`MeetingConversation.liveAccepted`, only with the socket closed), so the Live
  Activity's topic follows.
- **The Live box keeps the phone's own lines** (its transcript): the phone never reads `lines` of `GET …/live`, which is
  for other devices.
- **The Live Activity**: the current topic (`topic`, at most 60 characters) and the live log's latest line (`line`, at
  most 120), both only while Persona attends; the third line is the latest answer for 60 seconds after this phone had it,
  then the latest line (`RecordingActivity.content`); updates go at once for an answer or a phase and at most every 5
  seconds for a new line or topic (`RecordingActivity.hold`). Just me never reaches it.
- **Screenshots and tests**: the room samples (`-showMeetingSample room-listening|room-taking-part|room-whiteboard`) start
  with the live view. The mock hub serves the phase (`POST /mock/meeting_live_view {"on"}`, `MOCK_NO_MEETING_LIVE_VIEW=1`;
  its room sets made-up topics after each batch). UI tests `MeetingLiveViewUITests` (the topics on the page and the
  current one on the Lock Screen, shots `live-<appearance>-topics|island-expanded|lock`; none from a hub without the
  phase). Unit tests `MeetingLiveViewTests` (the flag and its snapshot, which meetings write 15-second segments, the
  topics' decoding, merging and event, the chips' words, the activity's topic and line, the third-line rule, the
  throttle, the state's size).

#### Highlights (phase 2b)

The contract's "Phase 2b: highlights" and "The meeting card" ([meeting-minutes.md](../assistant/meeting-minutes.md)),
for a room meeting started while the hub listed `meeting_highlights` beside `meeting_live_view`. The card's design and
identifiers are in [DESIGN_SYSTEM.md](../design/DESIGN_SYSTEM.md) "Meeting page (#3862)".

- **The flag**: kept from every hello only with the conversation and the live view
  (`MeetingAttendanceDefaults.highlightsFeature`, `highlightsKey` for a start without the app in front;
  `MeetingAttendees.highlights`), and snapshotted at the start as `RecordingAttendance.highlights` (decoded leniently,
  false in a file from before; `.room(…)` sets it only beside `liveView`), through `startAttendance` and the Recordings
  page's `RecordingStart.make`. A meeting keeps it for life, as it keeps `liveView`.
- **Reading them**: `MeetingConversation` reads `highlights` and `highlights_at` with the rest of `GET …/live` (`LiveRead`,
  leniently into `MeetingHighlight`: trimmed words, none without them; an `at` that isn't a finite time from 0 drops the
  highlight; an `end` missing, of another shape or before `at` is `at`) and follows `highlights` in `meeting_live`
  (`partsRead(for:)` adds it only for such a meeting), reading `?parts=highlights` when its revision is newer.
  `applyHighlights` keeps them unless the field came or the part was asked for (then an absent list is none), keeps
  none for a meeting started without them, and keeps at most four, earliest first. The background read after an
  accepted batch (`liveAccepted`) stays `?parts=topics`: the Live Activity never shows highlights, and its
  `ContentState` is as phase 2 has it; a page back on screen reads them again.
- **The card** (`MeetingTopCard`, `MeetingHighlightsCard.swift`): where the Live box was, both layouts. With the flag it
  is **Highlights**, the gist in the room's words: each highlight's time as the minutes cards' chip and its words
  (`Theme.Recording.highlightLines` lines at the regular sizes), earliest first, then **Full Transcript** at the trailing
  side. It shows no transcript lines: during the meeting the owner reads the points, and the words as said are on the
  Full Transcript page (the owner's call). Before the first highlights, while Persona attends and its room still gets
  the meeting (`MeetingHighlights.attending`: the role isn't off and the hub took it, the phone still sends the meeting's
  lines, and the room isn't `unavailable`, nor missing on an older manager; built on `MeetingAsk.canAsk`, the page's own
  rule, plus `stopped` nil, no `RecordingSync.attendanceProblem` and the room's state, so it never sits under a banner
  saying Persona can't take part), it holds one waiting line instead, "Persona is listening. Highlights come after a few
  minutes." (secondary), above Full Transcript. The Live box stays only where there can be no highlights: a meeting
  started without the flag, or one Persona doesn't attend that has none; highlights already shown stay after the role
  turns off. The Live box is phase 2's, pixel for pixel; only its **Full Transcript** is a view of its own now
  (`MeetingFullTranscriptLink`), shared with the Highlights card. The card takes only what it draws: the recording's id,
  not the recording, so a save of the transcript doesn't redraw it (P5). While the keyboard is up
  at the regular sizes the card keeps only its newest highlight (the latest `at`), or the waiting line, without Full
  Transcript (the Live box folds to its newest line as phase 2 has it); at the accessibility sizes nothing folds. The page
  reads the highlights and the room's state with the rest of the meeting and hands them to the card
  (`MeetingPage.topCard`), so the card observes nothing of `MeetingConversation`; the card lists them as it is made
  (`MeetingHighlights`: the mode, `live`, `waiting` or `highlights`, each `folded` with the keyboard; the rows with their
  ids and clocks; the newest; at most four), so its first frame is already the right card and nothing animates as the
  page opens or changes layout. The pushed transcript is the page's state, kept while the page is open, across the switch
  between the regular and the accessibility layouts. A change of mode animates on `Theme.Motion.state` (at once with
  Reduce Motion): the Live box and the Highlights card cross-fade in place, one over the other (a `ZStack` from the top),
  and inside the card the waiting line and the first highlights cross-fade the same way; nothing about them is announced.
  After Stop the page goes on as the thread and shows no highlights.
- **A highlight's time** pushes `MeetingTranscriptPage` with a `MeetingTranscriptFocus` (`at`, `end`; the live page's
  `.navigationDestination(item:)`): 100 ms after it appears it scrolls to the first of the phone's lines whose start is
  from `at` to `end` and lights every such line for two seconds (`Theme.accentTint`, cleared on `Theme.Motion.standard`;
  VoiceOver reads them as selected meanwhile), or, with none there, the line `RecordingMinutes.sentenceIndex` picks for
  `at` (`RecordingTimeline.focus`, compared in milliseconds). `RecordingTimelineRows` takes the lit rows (none for every
  other page) and gives each row its id.
- **Screenshots and tests**: `-showMeetingSample room-highlights` (the room meeting started with the live view and the
  highlights, 24:20 in, a line in each of fourteen minutes). The mock hub serves the phase (`POST /mock/meeting_highlights
  {"on"}` and `{"id", "highlights": [{"text", "at"}]}`; its room makes highlights after a batch; `POST
  /mock/meeting_conversation {"room": "slow", "room_seconds"}` keeps the room starting, so none come yet, and `{"room":
  "unavailable"}` keeps it from starting; `{"on": false}` before the room starts gives it no highlights' tool). UI tests
  `MeetingHighlightsUITests` (the card in time order above the thread with Full Transcript at its trailing side and no
  Live box, a time opening the transcript with its minute lit and selected and Back, only the newest highlight without
  Full Transcript while the keyboard is up and every one back once it is down, a hub without the flag (where a start
  snapshots none, so the meeting starts with the live view alone), a meeting started without it on a hub with it after
  its room made a highlight, the card at the largest text drawn as at xxxLarge, the interface's cap (#6813), each time
  beside its words, the waiting line before the slow room's first
  highlight and the highlight in its place after, the Live box under the problem of a room that is unavailable, the
  waiting line alone without Full Transcript while the keyboard is up and the Live box once Persona is turned off before
  any highlights; on every card no Live Transcript and none of the lines only the transcript has; shots
  `highlights-<look>-card|waiting|waiting-keyboard|role-off|transcript-lit|keyboard|room-unavailable|no-highlights|started-without`
  in light, dark and `large` (`card` also at `cap`, xxxLarge);
  every call to the mock must answer 2xx, so they need the mock with the phase); `MeetingLiveViewUITests` turns the
  highlights off to keep covering phase 2. Unit tests
  `MeetingHighlightsTests` (the flag and its snapshot, the decoding, the merge, the cap and the order, the parts, the
  card's mode for each case and folded with the keyboard, who attends (also with the role still on after the phone
  stopped attending, after `live_full`, and with the role the hub didn't take), its rows and newest highlight, which
  lines a time lights).

#### Talk about this meeting (phase 6)

The contract's "Phase 6: talk about this meeting" ([meeting-minutes.md](../assistant/meeting-minutes.md)): the follow-up
from any recording, by text and voice, through phase 1's `POST …/followup`; no flag of its own, so it shows on a hub that
lists `meeting_conversation`, and an older hub keeps today's page.

- **Which recordings** (`MeetingTalk.offered`): any recording on this phone that ended (not a rolling or an all-day one),
  whatever recorded it (a note, a meeting no room attended, a #3746 meeting, the watch's), with words in its transcript,
  minutes ready, or a conversation the hub holds for it (a follow-up, or a room that came up). Whether the transcript has
  words is worked out when the recording changes (`MeetingTalk.hasText`, stopping at the first line; performance.md P4),
  not on every redraw. Past retention (`RecordingSync` status `expired`) the words still on this phone don't count: the
  hub has cut the transcript (and the room with it) or deleted the recording, so only minutes, a follow-up or a room
  whose conversation the hub still has do. Its page has one row,
  **Talk about this meeting** (`recording-talk`), in the section under its header beside Private Meeting; while it
  records a room meeting's page is the live sheet instead.
- **The thread without a room**: the row pushes `MeetingPage` in its after mode, which reads `GET …/live` (any recording
  answers it) and never waits on a room: a recording with no room, or whose room is `none`, `ended` or `deleted`, writes
  its follow-up as soon as the hub has its end (`MeetingConversation.running`). The thread is the hub's `asks`, if any, then
  the follow-up; with nothing before it there is no **After the Meeting** divider, and until the follow-up starts a line
  says what to ask ("Ask Persona about this meeting: what was decided, who does what, or a follow-up email."), once the
  hub has answered for the meeting (`loaded` or `missing`): before that read the follow-up and the room aren't known even
  where they exist, and the line would flash. The first
  message makes the follow-up (`…/followup`), the next go to its messages, as after a room meeting.
- **Voice**: once the follow-up exists, the composer has **Voice Chat** before the field (`meeting-voice`), where the hub
  has a voice extension that speaks protocol 2 (`VoiceSupport`, asked at most once a minute) and nothing records. The call
  is an ordinary one bound to the follow-up (`target` the follow-up, no meeting mode, `VoiceEntry.start`); its bar takes
  the composer's place while it runs (`VoiceBar`, as in a chat), with the line a call that ended by itself leaves, and
  leaving the page ends it. With no follow-up yet there is no voice: the first request is typed, so no turn runs on the
  meeting's material alone. Dictating that first request is not done yet.
- **The minutes push** carries the recording's id, `recording_id`, at the top of its payload beside `machine_id` and
  `session_id` (a field older hubs leave out); a tap opens Recordings with that recording's thread
  pushed over it (`MeetingRecorder.openMeeting`, `MeetingPageOpener`; a request no Recordings page took within 30 seconds is
  dropped). Its Back returns to Recordings, so the page keeps **Open Minutes** / **Open Recording** to the recording's page
  (`opensRecording`, with no Done), as in the sheet. With another recording running, the live sheet doesn't present
  itself as Recordings appears for the push (`RecordingLivePresenter.holdOnAppear`, once, within those 30 seconds), so it
  doesn't cover the meeting; the now-recording card is the way back to it. A push without the field, a recording that isn't on this phone (made on another device) and a dismissal open
  what they opened before: the main chat (`MeetingTalk.threadToOpen`).
- **Tests**: `MeetingTalkTests` (which recordings offer it, past retention too, `hasText`, a note writing its follow-up at
  once, the push's field and where a tap goes, the opener, the live sheet held for the page the push opened); `TalkAboutMeetingUITests` (a finished meeting no room attended: the row, the thread without a
  divider, a question, the mock's follow-up answer; screenshots `talk-<appearance>-detail` and `-thread`).

#### Photos (phase 3)

The contract's "Phase 3: photos" ([meeting-minutes.md](../assistant/meeting-minutes.md)), for a room meeting started
while the hub listed `meeting_photos` (`RecordingAttendance.photos`, kept for life like the rest of the start; a hub that
lists it later adds nothing to a meeting already running). The row, its states, the delete confirmation and the viewer
are in [DESIGN_SYSTEM.md](../design/DESIGN_SYSTEM.md) "Meeting page (#3862)". The core case is a whiteboard: shoot it,
see it in the thread at once, then "Used as material", then the room's note on what in it matters.

- **Adding**: while the meeting records, the composer's `[+]` menu has **Take Photo** (only where `CameraPicker.isAvailable`)
  and **Choose Photos** (`PhotosPicker`, images only, at most `Theme.Recording.photoPickLimit` at once). Not with Just me
  on (photos are the meeting's material, never private), not after the meeting, not for a meeting started without the
  feature. A photo's `at` is the recording's clock (`RecordingCapture.shared.timeline`) when it was taken or picked.
  Picked photos show at once as Preparing… rows (`MeetingPhotos.pick`); Photos hands each one's bytes over when its
  turn to be prepared comes, and one it can't (still downloading from iCloud) leaves again and is said in a line under
  the field (at accessibility sizes among what the page scrolls). Adding a photo plays no haptic.
- **Prepared on the phone, off the main actor, one at a time** (`MeetingPhotoPipeline`, the store's queue; the original
  is let go of once downscaled, before Vision reads the small copy): ImageIO downscales it to at most 1600 px on its
  long side (never up) and encodes a JPEG at quality 0.75, then 0.6, 0.45 and 0.3 until it is at most 1 MiB; a PNG's
  transparency goes onto white. Vision (`VNRecognizeTextRequest`, `.accurate`, language correction) reads its words in
  the recording's languages (`zh-CN`: Simplified Chinese and English, `ja-JP`: Japanese and English, otherwise English);
  the lines go as `text`, cut to 8000 characters, and no `text` when nothing was read. A camera picture becomes JPEG
  bytes off the main actor before it goes in; nothing decodes an image with `UIImage(data:)`
  (`scripts/test/test_apple_perf_patterns.py`).
- **The upload** (`MeetingPhotos`): `POST …/photos` with `id`, `at`, `mime`, `data` and `text`, at once, under the photo's
  own id (lowercase). No answer, a 5xx, `408` or `429` sends the same id again after 2, 4, 8, 16, 30 and 60 s, then it
  fails and **Retry** sends the same id. `404 recording_not_found` uploads the recording first and sends the same id once
  more; `409 op_id_reused` sends it under a new id; `410 photo_deleted` (stored, then deleted elsewhere) and `410
  recording_deleted` drop it for good (its JPEG and its entry in `photos.json` too). Any other refusal fails at once with
  its own words (`photos_full`, `transcript_expired`, `not_meeting`, `kb_read_only`, `forbidden`, a picture the hub can't
  read); a `404` without a code is a hub without the photo routes, said so and not tried again on its own. Whether Retry
  can help is kept with the failure (`Local.State.failed(…, retry:)`, `MeetingPhotos.canRetry(after:)`), over a relaunch
  too: not for a picture the hub can't take, `not_meeting`, `transcript_expired`, `forbidden` or a hub without the
  routes, which show Delete Photo alone; yes after no answer, a 5xx, `kb_read_only` or `photos_full`. A `200` stores it:
  its photo joins the hub's list, which is read again soon after, and the local copy leaves once the list has it (the
  thread keys photo rows by the lowercase id, so the row stays where it is). **An answer lost**: a list that has a photo
  still uploading or failed here (its first `POST` stored, the answer lost, every try since failed or waiting out the
  backoff) settles it: the local copy leaves (its JPEG stays for the thumbnail) and `photos.json` forgets it, so the
  row reads Used as material, the next try never goes, and a try still on its way changes nothing when it comes back
  (nor does a late `200` bring back a photo deleted meanwhile). A relaunch whose list came first doesn't show it failed
  again.
- **Kept over a relaunch**: a photo not stored yet keeps its JPEG at `<recording folder>/photos/<id>.jpg` and its state in
  `<recording folder>/photos.json`, written atomically off the main actor. `MeetingRecorder.start()` calls
  `MeetingPhotos.shared.start()`, which sends again those that were on their way, keeps a failed one (with Retry when it
  can help) and drops one still being prepared (its original is gone). A stored photo's JPEG stays for the thread's
  thumbnail while the hub lists it: once a list read whole (every entry read, or at least named) no longer has it
  (deleted elsewhere, cut with the transcript), its JPEG and its thumbnails go too. The folder is checked against the
  list after each such read, once the launch read of `photos.json` is in, so this holds after a relaunch, and a photo
  still to send keeps its JPEG.
- **The hub's list**: `GET …/photos` (`MeetingPhotos.refresh`, one read per recording at a time, calls meanwhile sharing
  the next; it says whether a list was applied) is the truth for every stored photo: its number, the room's `note`,
  `cited`, `forwarded`. For a meeting that takes photos `MeetingConversation` also reads the `photos` revision of `GET
  …/live` (`?parts=photos` carries only the revision): a `meeting_live` event or a `…/live` read with a newer one reads
  the list again, as does a page that starts watching, and the revision counts as read only once that list applied, so
  a read that failed (a timeout, a hub switch) is tried again by the next event or `…/live` read. Only `404
  recording_not_found` and `410 recording_deleted` list nothing; any other refusal (a `404` without a code: a manager
  without the routes) leaves what was listed, and a list never read stays unread.
- **The pages' view** (`MeetingPhotos.Album`, one per recording): each photo's row content worked out once per change
  (its number, time, first lines, note, `added_at` read once, whether it has a picture and whether Retry can help), the
  order of those the room's part doesn't hold, the numbers for the minutes, whether the list was read, and the deletes
  running and their problems. The thread reads only the order; each photo row is its own view that reads the rest
  (docs/apple/performance.md P4, P5).
- **In the thread** (`MeetingThread`): the room's two items of a forwarded photo (the image and the line asking for its
  note, both `client_msg_id` `photo-<id>`) are one `.photo` entry where the room got it, never an image, a batch or a
  question. The hub's line withdrawing a deleted photo (`photo-withdrawn-<id>`) shows nothing; the end batch is only the
  room's last line from the meeting, and only once the meeting ended (`room(…, ended:)`). Photos the room thread doesn't
  hold (not forwarded: the role off, the room not up; forwarded with its message not among the room's pages loaded; or
  still on their way) follow the room's entries, oldest `at` first, those the hub doesn't list yet last, and move to
  their message once a page with it is loaded; none is ever hidden. A `photo-<id>` the hub's list no longer has, once it was read, is **Photo
  removed**. The room's note shows under its photo; "Persona is looking at the photo…" shows while a forwarded photo has
  no note and the room is ready, for at most `Theme.Recording.photoNoteWait` after it was added. After the meeting the
  page shows the same photos from the list, with their notes and **Cited in the minutes**; it adds none.
- **Thumbnails and the viewer**: from the JPEG kept on the phone, else `GET …/photos/{n}`, downsampled off the main actor
  to `Theme.Recording.photoThumbSide` points at the screen's scale and kept in a memory cache that counts bytes. The viewer
  shows the whole picture from the same sources.
- **Deleting** (`MeetingPhotos.remove`): the photo's long-press menu, a failed photo's Delete Photo button or its
  accessibility action, then a confirmation that says whether Persona already saw it, then `DELETE …/photos/{n}`. A
  failed photo whose upload may have been stored (no refusal that comes after the hub's check for a stored repeat:
  `kb_read_only`, `not_meeting`, `transcript_expired`, `photos_full`) reads the list first: one it has is deleted there
  by its number, one it doesn't only leaves this phone, and with no list (offline) it stays and says so. `404
  photo_not_found`, `404 recording_not_found` and `410 recording_deleted` count as done; the JPEG on the phone goes too;
  a refusal says "Couldn't delete the photo. Retry." unless it has words of its own (`kb_read_only`, no connection).
- **Cited in the minutes**: a minutes item may cite a photo (`RecordingMinutes.Item.photo`, its `n`). The recording page's
  minutes (on iPhone the Summary's cards, #5026) show that photo's thumbnail under the item (`MinutesPhoto`, iPhone only;
  a tap opens the viewer), Photo removed once the list was read and no longer has it, the thumbnail's place while it
  isn't; Copy and Share add "(photo N)" to the item as the hub's Markdown does.
- Nothing of photos reaches the Live Activity. The camera's purpose string (`NSCameraUsageDescription`) names a meeting's
  whiteboard beside pairing and chat photos.
- **Screenshots and tests**: `-showMeetingSample room-whiteboard` is `room-taking-part` started with photos; once its
  upload's `200` carried `room` it draws a made-up whiteboard (2000×1500 px) and adds it as the camera does, so the real
  pipeline, upload and note run. The mock hub serves the photo routes (`POST /mock/meeting_photos {"on", "note",
  "note_seconds"}`; `MOCK_NO_MEETING_PHOTOS=1` is a manager without them), forwards each photo to a ready room and
  writes the room's note `note_seconds` later; the fresh start `POST /mock/recording_minutes` forgets every photo. UI
  tests in `MeetingConversationUITests`: the whiteboard's note (`testWhiteboardPhotoGetsTheRoomsNote`, which also checks
  what the hub stored), a delete leaving **Photo removed** (`testDeletePhotoShowsPhotoRemoved`), and the photo items
  only in the meeting started with photos, both ways round (`testPhotoMenuFollowsTheMeetingsStart`: `room-whiteboard`
  offers Choose Photos, `room-taking-part` doesn't). Unit tests: `MeetingPhotosTests` (the pipeline and recognition, the
  upload body, retries under the same id, a new id after `op_id_reused`, each refusal's words and whether Retry can help,
  the list and which refusals empty it, an answer lost and settled by the list (after a relaunch, during the backoff, a
  late failure or `200`), the delete of a failed photo, the JPEGs of photos that went, one photo prepared at a time, rows
  at once for a pick, the album per recording, `photos.json` over a relaunch, thumbnails), `MeetingConversationTests`
  (the photos revision kept only once its read applied; a start takes photos only when the hello listed
  `meeting_photos` with the conversation), `MeetingRecordingTests` (a minutes item citing a photo) and
  `MeetingThreadTests` (photo entries, the withdrawn line, the end batch).

#### Realtime voice (phase 4)

The contract's "Phase 4: realtime voice in the meeting" ([meeting-minutes.md](../assistant/meeting-minutes.md)): Talk to
Persona by voice during a room meeting, with no hub flag of its own. The voice extension says it takes meeting calls with
`meeting` in its `GET /info` features (`VoiceMeeting.feature`, from `VoiceSupport`). While a meeting that wants a call
has none, the phone asks as it starts wanting one, then 30 seconds, 2 minutes and 5 minutes later while the answer says
nothing either way (the ask failed, the extension isn't running at that moment), and never again for that recording once
the hub said no (`MeetingCallRule.probeAgain`, `declines`; P9: not once a minute for hours with the screen locked); what
else asks meanwhile (a spoken answer's route, a chat's voice button) counts too. The Talk button
and its states are in [DESIGN_SYSTEM.md](../design/DESIGN_SYSTEM.md) "Meeting page (#3862)", the Live Activity's in
[live-activity.md](live-activity.md) "Recording". The pure rules are `MeetingTalkRules.swift`, the call and Talk
`MeetingCall`, the audio `MeetingCallAudio`.

- **Who has a call** (`MeetingCallRule.wanted`): a room meeting this phone records, Persona taking part (`participant`,
  from the start or a later change), the hub took the room (`MeetingAsk.canAsk`: not `RecordingSync.roomMissing`), the
  room `ready` with its ids (`MeetingConversation.roomKey`), and a voice extension that runs, speaks protocol 2, is set up
  and lists `meeting` (`MeetingCallRule.extensionID`). `MeetingCall` follows the recording, its role, the room and
  `VoiceSupport` with `withObservationTracking` (as `RecordingActivity` does) and starts or ends the call as they change.
- **The call** is a `VoiceConversation` in meeting mode (`VoiceMeeting`): protocol 2 `start` with `mode: "meeting"`,
  `recording_id` (lowercase), `target` the room's key, `echo_cancellation` (`RecordingCapture.cancelsEcho`), the model and
  voice as any call has them, no `recent` (the extension reads the room's own turns), and no `speech_gate` (#5904: the
  extension keeps the user-speech gate on for a meeting call, whose questions go to the room as the owner's words, and the
  meeting page has no Send Anyway; Only Send What I Just Said never reopens it); `history` and `resume` on a reconnect, as
  protocol 2 has them. A `ready` without `meeting` ends it (`incompatible`): an ordinary call bound to the room
  would put its tools in front of the room. It is not a phone call: it never claims the microphone
  (`RecordingMicrophone`: the recording holds it), keeps no screen on, never ends after 10 idle minutes, and isn't ended
  by the background (it is not one of `VoiceCalls`). It reconnects 1, 4 and 10 seconds after a drop, then every 30 seconds
  (`VoiceMeeting.retryEvery`) for as long as the meeting records; a call that ended by itself (a fatal `not_meeting_room`,
  a refused handshake but 401 and 403) starts again 30 seconds later while the meeting wants one, unless the end says it
  never can (`MeetingCallRule.retries`: 401, 403, `incompatible`, a model that can't be used). It ends with Stop
  (`RecordingCapture.onStopping` → `MeetingCall.ended`), another role, the room going, or the hub saying for sure that
  its extension no longer takes meeting calls (`MeetingCallRule.declines`: no voice extension, `/info` without
  `meeting`, `404`, `403`); an ask that failed, or an extension restarting, keeps a running call, whose own `ready` and
  close decide. While the recording is paused (by the owner or a call) its socket stays and nothing plays: what the voice
  had queued is dropped, its stretch closes and the voice service is told to stop (`interruptSpeech`), so nothing stale
  plays after Continue; an answer playing on the recorder's engine (below) stops too.
- **One capture** (`MeetingCallAudio`, the `VoiceAudio` of a meeting call): the recorder's one input tap, already at 16 kHz,
  feeds the segment writer and, through `RecordingCapture.listen` (`RecordingSegmentWriter.listener`, called on the tap's
  thread, with where each buffer's first sample lies on the timeline), `MeetingTalkTap`, which hands the call's uplink
  PCM16 and keeps the newest buffer's level for the talk window, with the room's over the last 5 seconds in pieces of
  about 100 ms (`MeetingTalkTap.room(before:)`). A buffer reaches the tap only once it is full (4096 input frames: about
  85 ms at 48 kHz, 512 ms at a Bluetooth headset's 8 kHz), so the span is placed by where its samples were captured, not
  when they arrive: the press and the release are `RecordingCapture.listenPosition` (the timeline plus what the tap's
  next buffer has gathered so far, `RecordingSegmentWriter.elapsedNow`), only the samples between them go up, and
  `talk off` waits for the buffer that holds the release (at most 0.7 s, `MeetingCall.tailWait`). Each edge is placed to
  its own safe side (`RecordingSegmentWriter.placed`): the press never before it happened, however long the buffer or
  late its delivery (all of the time since the newest buffer's last sample, plus 50 ms for the converter's delay and a
  buffer without a host time), so a stall only cuts the owner's first words; the release at most the longest buffer seen
  since the tap was installed plus 100 ms of delivery past the timeline, so a stall never runs the span past the lift.
  The uplink of a meeting call is gated
  (`VoiceUplink(gated: true)`): nothing is held before `ready`, nothing goes outside a span, and the `talk` frame goes
  under the uplink's lock (`VoiceUplink.talk`), so on the socket no buffer of a span is before its `talk on` or after its
  `talk off`. The voice plays through a player node on the recorder's own engine
  (`RecordingCapture.callPlayer`, attached before the engine first starts for every room meeting, `callFormat` 24 kHz mono
  float, a call at another rate resampled), behind the phone call's jitter buffer (`VoicePlayout`), so the system's echo
  cancellation takes it out of the microphone. Beside it a second player (`RecordingCapture.answerPlayer`) takes
  Persona's other spoken answers in the realtime voice (`MeetingVoicePlayer`, the extension's `speak`: an ask about what
  was just said, Talk without a call), resampled to 24 kHz when they come at another rate, so they are echo-cancelled too;
  while the recorder's engine is paused an answer gets an engine of its own, as before. The on-device voice
  (`AVSpeechSynthesizer`) still plays outside that I/O: it isn't in the echo canceller's reference, and its stretch
  (below) is what keeps its echo out of the transcript. A recording of any other kind gets no player and no listener: notes,
  all-day and rolling recordings, #3746 meetings and the watch are as they were.
- **Echo** (`MeetingRecorder.echoCancellation`): a room meeting that starts as `participant` asks for voice processing on
  the recorder's input as recording starts (`RecordingCapture.start(…, echoCancellation:)`,
  `RecordingSegmentWriter.voiceProcessing`), and never switches it while it records, whatever the role does later; where it
  won't turn on, or the engine won't start with it, the recording goes on without (as the phone call's audio does), and
  `echo_cancellation` says `false`. Not on the simulator, where voice processing doesn't run (docs/assistant/voice.md 6.4).
  Voice processing ducks every other sound the phone plays by default, heavily: the recorder sets the least ducking and
  no advanced ducking (`voiceProcessingOtherAudioDuckingConfiguration`, iOS 17), so an answer in the on-device voice
  still plays at its level. Either way every stretch the call's voice or a spoken answer (`MeetingSpeaker`) plays in joins
  `RecordingAttendance.spoken` as it played, never wider, with the words said in it: a spoken answer's text, and for the
  call's voice its answers, the voice's own captions not kept with an earlier stretch, the one being said included
  (`MeetingCallEcho`; with no caption at all a stretch has no text and keeps the old time rule). Persona's echo is then
  #5905's rule ("Its echo is not the meeting's" above;
  [meeting-minutes.md](../assistant/meeting-minutes.md) "Persona's echo"), which widens the stretch by `MeetingEcho.margin`
  itself: a sentence that overlaps a stretch and says its words is marked, hidden in the transcript, kept out of the live
  log and uploaded marked (left out, to a hub without `meeting_echo`), while someone talking over the voice stays the
  meeting's. A sentence transcribed while the voice still plays is checked against the open stretch, which carries what is
  being said (`MeetingCall.openSpan`, `MeetingSpeaker.openSpan`; both, when both play).
- **Talk** (`MeetingCall.beginTalk` / `endTalk`, from a `MeetingTalkSource`): offered while a room meeting Persona can be
  asked in records (`MeetingCallRule.talkOffered`, listener or participant, not paused). Held on the page; a hold also
  ends when the page's Talk leaves the screen with the finger still down (Just Me turned on, the page left, a word typed:
  `MeetingTalkButton`'s disappear, `MeetingCall.endHold`), at the Live Activity's Done, and after 60 seconds whatever the
  finger does (`MeetingCall.longestHold`). On the Lock Screen and in the island a tap opens a talk window
  (`TalkInMeetingIntent`, `MeetingCall.activityTap`), which ends on Done, on 2 seconds of silence after speech, after 20
  seconds without speech or after 60 seconds (`MeetingTalkWindow`, ticking every 100 ms on the tap's level). Speech is the
  owner's, not the room's: the microphone hears everyone, and a room talking at a normal level would otherwise keep a
  window open for its 60 seconds, all of it going to the voice service. So as the window opens it takes the room's level
  from what the tap heard in the 5 seconds before (`MeetingTalkWindow.Room`; the 25th percentile of its 100 ms pieces,
  so the owner's own words just before the tap hardly raise it), and a buffer is speech only 15 dB above that
  (`aboveRoom`: the owner speaks into the phone, others are further away) and never below -40 dBFS. With less than
  2 seconds of the room heard (the recording just started or continued), -40 dBFS alone, as before. The activity's
  buttons carry what they showed (`open`: Talk true, Done false), and
  `MeetingCallRule.activityTap` acts only on a tap that matches what is open: Talk opens a window when no span is, Done
  ends any open span (a window, or a hold the page lost), and a Done tapped just after the window closed on its own
  opens nothing. VoiceOver on the page opens a window too (`pageWindow`). A span opening stops what Persona is saying (the
  call's voice, a spoken answer). With the call live, the span is `talk on`, its audio, `talk off`; the call's answer comes
  as captions and its voice, and in the room's thread as the room's turn. No span goes to the call before the owner was
  told what Talk sends (`MeetingCallRule.toCall`, `MeetingTalkPrivacy`, below): one held or opened on the page goes only
  once the Wording was seen with an earlier span or is on screen under Talk as it opens, and one from the Lock Screen or
  the island (which never show it) only once seen; any other is asked without the call, its audio staying on the phone
  (a meeting started from Siri, the Action button or the Control, whose page showed no Wording while the call was still
  connecting, a VoiceOver window under a hint that wasn't spoken). A span the call loses midway (its socket dropped, the
  call ended) goes on without it
  (`MeetingCall.Talk.lost`): the segment is cut there, and on release the whole span is asked. Without it (a listener, no
  `meeting`, the call connecting, reconnecting or down), the segment is cut at the span's start and end
  (`RecordingCapture.flushSegment`, also while paused, `cutsOnFlush`), the
  span is transcribed on the phone as every segment is, and once its segments have their text (at most 12 s) the
  sentences at least half inside it (`MeetingTalkWords.question`) go as a spoken question: `…/ask` with `question` and
  `spoken: true` (`MeetingAsk.ask(_:text:spoken:)`), answered as any ask (aloud for a participant, `MeetingSpeaker`). Let
  go in under half a second, the page says to hold Talk while speaking; a span asked without the call that held no words
  says Persona heard nothing. A span closes with the recording's pause or the role going off (what was said is still
  asked: the pause's open segment is cut while paused); Stop drops it.
- **Typed questions** go as the call's `text` frame while it is live (`MeetingCall.say`; the fast layer hands them to the
  room, and the answer comes in the room's thread), otherwise through `…/ask` as before; the room's one-question-at-a-time
  rule is the asks', not the call's.
- **The privacy texts**: where Talk sends its spans to the voice service, a room meeting taking part on a hub whose voice
  extension takes meeting calls (`MeetingCallRule.talkSendsAudio`), the line under Record and Talk's first-use note (and
  its spoken hint) say "Audio stays on this iPhone, except while you hold Talk: what is said then goes to the voice
  service." (the contract's Wording). The note is a line of its own under the composer's caption, beside a word after a
  span (hold longer, heard nothing) rather than replaced by it, until a span went to the call while it showed
  (`MeetingTalkPrivacy`, written once). The line tells `MeetingCall` when it is on screen (`showingPrivacy`, from its
  appearing and leaving), and it counts as shown for a span once it has been there a second (`MeetingTalkPrivacy.shown`):
  drawn in the same instant as the finger went down (the call came up just then) is not shown. VoiceOver's window counts
  the line on screen too, not Talk's spoken hint, which hints off, Voice Control, Switch Control and a keyboard never
  speak; a Lock Screen window never marks it. Everywhere else the texts are as before: a
  listener's Talk stays on the phone. Record's Siri and Shortcuts description (`StartMeetingRecordingIntent`) says the
  Wording too, since it may start a meeting with Talk, with "this device" for "this iPhone": App Store Connect refuses
  App Intent metadata that names the iPhone (#6471).
- **No voice in a room opened as a chat**: a conversation whose origin is `meeting` offers no Voice Chat
  (`VoiceAvailability.meetingRoom`; the + menu's item says to hold Talk on the meeting's page), since the extension refuses
  an ordinary call bound to a room (`meeting_room`).
- **What the simulator cannot verify**: voice processing (it doesn't run there), how much it changes the recording of a
  room's far voices, and the voice's echo through the loudspeaker; measure on a device before relying on the echo rule alone.
  The same goes for the talk window's speech against the room (`MeetingTalkWindow.aboveRoom`, `roomPercentile`): voice
  processing's gain control narrows the gap between the owner and the room, and a phone lying on the table as far from
  the owner as from the others can't tell them apart by level at all (the window then ends after 20 seconds, or on its
  limit if the room is loud enough to count). Measure a real room before changing them.
- **Screenshots and tests**: the room samples (`-showMeetingSample room-listening|room-taking-part|room-whiteboard`) feed
  canned audio into the recorder's tap (`RecordingCapture.feedSample`: 3 s of a hum, 2 s of quiet), so Talk has something
  to send with no microphone; a span asked without the call gets made-up words as its transcript
  (`RecordingSamples.transcribeTalkSample`). `room-talk-held` is `room-taking-part` with Talk held for 10 seconds once the
  call is up. The room samples show Talk's first-use note again. The mock hub with `MOCK_VOICE=1` serves meeting calls
  (`scripts/mock/voice_meeting_mock.py`: `meeting` in the features, the start checked against its room, audio counted
  inside and outside spans, an answer after each span in the room's thread; `GET /mock/voice_meeting`, `?meeting=off` an
  extension from before phase 4). `room-talk-held` holds Talk only once the page has shown the Wording for a moment, as a
  finger under it would. UI tests `MeetingTalkUITests` (the first-use note on screen before the first hold, a held Talk
  reaches the call inside its span only and its answer the thread, the note gone after it; Talk held and the Lock
  Screen's talk window, a Done after the
  window closed opening no other span and no audio outside spans; a listener's Talk asked through `…/ask` as `spoken`; the
  start's privacy line with and without meeting calls; shots `talk-<appearance>-page|speaking|answered|held|lock-before|lock|listener-asked|start`).
  `MeetingConversationUITests` and `MeetingLiveViewUITests` switch the mock's meeting calls off, so their questions keep
  going through `…/ask`. Unit tests `MeetingCallTests` (who has a call, the extension's feature, only a definite no ending
  a running call, asking for meeting calls stopping, where Talk is and where it sends audio, which ends start again, the
  start frame, the talk frames, a `ready` without `meeting`, only the span's audio going up and in order, a drop ending the
  span, the tap's PCM16 and level, only the samples inside the span with buffers straddling the press and the release, a
  late 512 ms buffer sending nothing from before the press, a span the call loses asked without it, a paused recording
  still cutting the span's segment, reconnecting every 30 s, no idle end, the talk window's four ends, a room talking
  keeping no window open, a silent owner in a talking room ending at 20 s, what counts as speech, the tap hearing the
  room, the activity's tap doing what its button showed, no span from any source going to the call before the Wording
  showed or was seen, the Wording counting only once it was on screen, the span's words, a spoken ask on the wire, a
  stretch of the voice carrying its answers and not those kept with an earlier one, its echo marked by the words and kept
  on the recording while someone talking over it stays, never widened twice, the open stretch marking the upload and
  keeping its echo out of the live log, a meeting call's start saying nothing of the speech gate, the Live Activity's Talk, the Lock Screen showing the call's answer, a call keeping its latest answer, no
  voice in a room opened as a chat); `MeetingSpeakerTests` (an answer's stretch as it played, with its words, a voice
  resampled for the recorder's player); `RecordingSegmentWriterTests` (a press never placed before it happened, a release held
  back).

#### Hub transcription (phase 5)

The contract's "Phase 5: hub transcription" ([meeting-minutes.md](../assistant/meeting-minutes.md)): in hub mode the
meeting's audio goes to the hub, which transcribes it, and the phone's own transcript fills where the hub has nothing.
It has two parts: the data plane, what the phone sends and keeps and the one store the meeting page and the Live Activity
read (`MeetingTranscription`); and the UI that shows it and the choices (`MeetingTranscriptionViews.swift`, the start,
Settings › Meetings, the meeting page, the Live Activity, the recording's page). The wire and every rule are pure in `MeetingAudioWire.swift`, held to the contract by
`MeetingTranscriptionTests`; the socket session is `MeetingAudioUplink`, the settings `MeetingTranscriptionSettings`.

- **The flag** (`meeting_transcription`, only beside `meeting_conversation` and `meeting_live_view`;
  `MeetingAttendanceDefaults.transcriptionFeature`) is snapshotted as the meeting starts, as `liveView` and `photos` are:
  `RecordingAttendance.transcription` is the mode the meeting asked for, nil when the hub didn't list the flag (its
  uploads then name no mode). A start asks for `MeetingAttendees.startTranscription`: the start sheet's pick (`hub` only
  while `meeting_transcription_ready`), else the hub's `meeting_transcription_default`, `device` while the hub isn't
  ready (`MeetingAttendanceDefaults.startMode`; one-tap starts read the default and readiness kept from the last
  settings read, as the role's default is). A meeting that asked for a mode also keeps every role change with its time
  (`RecordingAttendance.roles`), for the timeline.
- **Hub-mode time** (`RecordingAttendance.hubTime`, kept with the recording): the stretches of its clock that were
  hub-mode meeting time. One opens as a meeting that asked for `hub` starts, or as a recording becomes one (a Note turned
  into a meeting, `MeetingAttendees.resumed`: from the switch, never from the recording's start), and closes when it
  turns into a Note (`MeetingRecorder.switched`), when an older manager rolls it back, when hub transcription stops for
  the meeting (`RecordingAttendance.stopHub`, below), and at Stop (`MeetingTranscription.ended`). Only audio inside it
  ever goes up after the meeting: whatever the hub lacks where the recording was a Note (before it became a meeting, or
  between two meeting stretches) stays on the phone, and the hub's gaps there keep the phone's text (the hub's final
  pass then starts 30 minutes after the `complete: true` upload). Turning into a Note also ends the uplink at the same
  sample, in the same step: `MeetingRecorder.setMeeting` tells `MeetingTranscription.becameNote` before the switch is
  saved, the uplink takes no sample from there on (`MeetingAudioUplink.cut`, also a buffer the tap hands over a moment
  later) and comes off the tap at once, so no buffer of the Note reaches the hub while the switch's observers catch up.
- **The upload** (`RecordingUpload`): such a meeting's every upload names its mode (`transcription`; the hub takes the
  first and ignores the rest, so a lost upload changes nothing), and in hub mode carries `timeline`
  (`MeetingTimeline`: the role changes and the spoken stretches that ended, `{roles: [{at, attendance}], speaking:
  [{start, end, text?}]}`, the `complete: true` upload included). The timeline always fits the hub's limits: the first
  100 role changes and 1000 stretches, malformed entries left out, a text cut at 4000 bytes on a character, so the hub
  never ignores it whole. The `200`'s `transcription` and `transcription_problem` reach the store
  (`RecordingSync.onTranscription`) before the attendance it carried counts as stored. Device transcription and
  15-second segments keep running in hub mode: they are the fallback, they fill the gaps, they are the `segments`.
- **The mode in effect** (`MeetingTranscriptionRule.decide`) is the `200`'s word, and `hub` only when the phone asked for
  it, so a meeting's audio never leaves the phone unasked. A `200` without `transcription` is an older manager: `device`
  for the rest of the recording (`olderManager`), a running uplink stopped, whatever a later `200` says. The rollback is
  kept with the recording (`RecordingAttendance.hubRolledBack`, its hub-mode time closed there), so after a relaunch with
  the newer manager back nothing streams and no gap goes up. In hub mode no
  `POST …/live` goes (`MeetingLive`, `hubTranscribes`; a `409 hub_transcribes` stops them too) and a heard ask sends no
  `live_next` (`MeetingAsk`: the segment is still cut, for the fallback).
- **The uplink** (`MeetingAudioUplink`): the second consumer of the recorder's one tap (`RecordingCapture.listenUplink`,
  beside the meeting call's `listen`), stamped with the writer's frame position. It listens from the start of a meeting
  that asked for `hub`, before the mode is in, into the last 60 seconds kept in memory whatever the hub acknowledged
  (`MeetingAudioRing`, PCM16 16 kHz); a tap buffer costs a conversion and a copy under a lock on the tap's thread, the
  sends run on a queue of their own, and nothing per buffer reaches the main thread (performance P1). Once the mode is
  `hub` the socket opens (`GET …/audio`, `MeetingAudioEndpoint`, the device's bearer token, `HubVoiceSocket` with its
  20-second ping): `start {at, timeline}` at once (`at` the oldest second held), then `speaking` on for a stretch Persona
  is saying, then `role`; no audio before `ready`, then frames of about 100 ms (at most 64 KiB, whole samples) from
  `ready.from`, a `seek` first when the hub's end is older than the oldest sample held. `role` goes on every change and
  `speaking` on and off as Persona's answers (`MeetingSpeaker`) and the meeting call's voice (`MeetingCall`) start and
  stop: one on while either says something, off once the last stops. An answer's `on` carries its whole text; the meeting
  call's carries none (`MeetingTranscription.spokenText`): its captions stream in with its audio, so at playback's start
  they hold only the answer's first words, and the hub judges the sentences it held against the `on` frame's text alone.
  Without one the hub applies the time rule to the stretch; the call's whole answer reaches the hub's timeline with the
  stretch once it ended (the next socket's `start`, the uploads' `timeline`). It runs whatever the role, `off` included.
- **The socket's life** (`MeetingTranscription.follow`): closed while the recording is paused, opened again on Continue;
  after a drop opened again after 1, 2, 4, 8, 16, then every 30 seconds, at once when the network comes back, sending
  again from the new `ready.from`; audio that fell out of the 60 seconds unacknowledged is a gap (`lost`). The failures
  in a row (the backoff) and the `bad_frame`s count across sockets until one is healthy: its first `ack`, or 30 seconds up
  (`MeetingTranscriptionRule.healthyAfter`), never at `ready`, so a hub that fails right after `ready` is not tried every
  second with the 60 seconds resent each time. At Stop the rest of the audio goes, then `end` at the last sample; from
  then on the uplink takes no audio and comes off the recorder's tap (`RecordingCapture` drops its listener at Stop too,
  so it never hears the next recording), and the hub's last lines are read for up to 20 seconds before the socket counts
  as ended. Hub frames: `ready` (its `gaps`, and the positions missed while away read with `GET
  …/live?parts=lines&from=`), `ack`, `interim`, `line` (kept by position), `problem` (`code: null` clears it) and `error`,
  acted on by its code (`MeetingTranscriptionRule.reaction`): `recording_not_found` uploads first and tries again,
  `bad_frame` and `hub_failed` try again (three `bad_frame`s in a row stop it), `replaced` (another socket has the
  meeting; the hub's run goes on through it) yields: this phone opens no other socket while it runs, so two never
  replace each other in turn, and the meeting's hub-mode time stays, so what the hub lacks still goes up as gaps. Every
  other code (`audio_full`, `recording_deleted`, `kb_read_only`, `not_hub`, `transcript_expired`,
  `transcription_final`, an unknown one) and a refused handshake (401, 403, 404, 405, 410) stop hub transcription for the
  meeting for good while the phone's own goes on: the uplink comes off the tap at once and the stop is kept with the
  recording (`RecordingAttendance.hubStopped`, where on its clock and the code), its hub-mode time ended there, so no
  socket opens for it again, also after a relaunch or a Note turned back into a meeting, and no audio recorded from the
  stop on goes up as a gap (`hubStretches` clips at it); what came before it may.
- **Wi-Fi only** (`MeetingNetwork`, one `NWPathMonitor`; `MeetingTranscriptionRule.mayStream`): cellular data is
  allowed unless the iPhone's **Hub transcription on Wi-Fi only** is on (`MeetingAttendanceDefaults.wifiOnlyKey`, off by
  default, kept on the phone). Then on cellular or a network marked expensive no socket opens and no audio goes
  (`waitingForWiFi`); the audio is still kept, so a return to Wi-Fi within the minute resends it, and the rest goes up as
  gaps. The monitor's view decides when to try (its latest answer, `MeetingNetwork.latest`, read right before every
  socket, a retry's included, and every gap piece); the system enforces the rule for a hub reached straight: with the
  switch on, the socket's request and every gap `PUT` set `allowsCellularAccess` and `allowsExpensiveNetworkAccess` to
  false (`URLRequest.keepOnWiFi`, `APIClient.putBytes(wifiOnly:)`), so a path that changes after the check (Wi-Fi
  Assist, Wi-Fi gone during a retry) fails the request instead of carrying the audio. The network counts as unknown, not
  Wi-Fi, until the monitor's first answer. Turning the switch on opens a socket opened without it again under it and
  stops a gap `PUT` under way (each runs as a task of its own), and the pass starts again under the rule; with the switch
  on, the path leaving Wi-Fi closes the socket and stops the `PUT` the same way.
- **Through a tunnel** (the hub's SSH tunnel, local SOCKS5 proxy or in-app tunnel, `HubTransport`): the request goes to
  the tunnel's loopback address, so the system's guard covers only that hop; the connection that carries the bytes is
  the tunnel's own, which stays on the path it was made on (a tunnel dialled on cellular keeps running over cellular
  after the phone joins Wi-Fi). Dialling a separate tunnel connection that refuses cellular isn't cheap here: the SSH
  session is shared with everything else the app sends, a local proxy is another app's, and the in-app tunnel's core
  keeps its own connection. So with the switch on and the hub routed through a tunnel, audio goes only while the phone's
  path is Wi-Fi and has been off cellular and expensive networks since the tunnel began (`HubTransport.since`,
  `MeetingNetworkState.meteredUntil`, `MeetingTranscriptionRule.mayStream(wifiOnly:network:tunnel:meteredUntil:)`),
  checked right before each socket and each gap piece; the path leaving Wi-Fi closes the socket and stops the gap `PUT`,
  and the same tunnel isn't used for audio again until it is opened anew on Wi-Fi (the app opens its tunnels again as it
  comes to the front). What the phone can't see stays a limit: a local proxy's app, or the in-app tunnel's core, may
  choose a path of its own (Wi-Fi Assist included) behind a phone that is on Wi-Fi.
- **The gaps after the meeting** (`MeetingTranscription.kickGaps`): for every ended room meeting that asked for `hub`,
  within the hub's retention, once its last revision (the `complete: true` upload) is stored, the phone reads
  `transcription` (`GET …/live?parts=transcription`) and, while the hub is `live` or `finishing` with gaps, uploads them
  (`PUT …/audio?at=`, `MeetingGapUpload.plan`: at most 300 seconds a request, only what lies inside the phone's audio,
  `at` a plain decimal exact to the sample, `MeetingAudioClock.query`), decoded from the phone's own segments off the
  main thread (`MeetingSegmentAudio`), until no gap is left or the final pass started (`409 transcription_final`);
  refusals in the contract's order each have their answer (`MeetingGapUpload.reaction`), and the Wi-Fi rule holds here
  too. Every piece is clipped to the recording's hub-mode time (`MeetingTranscription.uploadable`, `MeetingGapUpload.plan`
  `within:`); a recording with none uploads nothing. The recording and the mode in effect are read again before each
  recording of a pass and before each piece (and once more after a piece's audio is decoded): a rollback, a stop, a
  delete, a Note or the retention reached since the pass began ends its uploads there (`stillUploading`), and an older
  manager's `200` stops a `PUT` of that recording under way. It runs at launch, after an upload of an ended meeting is
  stored, when the network changes and when the app comes back, under background time (`beginBackgroundTask`, so the
  pass right after Stop goes on as the app goes behind); a pass that left a meeting `later` (a `503`, a timeout, a
  read-only knowledge base) brings another after 15, 30, 60, 120, 240, then every 300 seconds
  (`MeetingGapUpload.retryDelay`) while it still needs gaps, so they reach the hub before its final pass. The background
  time is held across that wait and the retry while the system grants it, and ends once no meeting is left `later`
  (or when the system takes it back; the next pass then asks again): let go before the wait, iOS could suspend the app
  during it, and the retry would run only on its next launch, maybe after the final pass.
- **Retention on the phone**: the phone keeps a recording's audio until the hub's `days` or the owner's delete, whatever
  the hub's state (`RecordingSync.purgeExpired`), so a hub-mode recording's audio is there for gap uploads at least until
  `final` or `failed`, and never past `days`: an expired one is never uploaded (`MeetingTranscription.needsGaps`).
- **The store** (`MeetingTranscription`, `@Observable`, `meeting(id)`): per recording `asked`, `decided` (`mode`,
  `problem`, `olderManager`), `socket` (`off`, `waitingForMode`, `waitingForWiFi`, `connecting`, `ready`, `down`,
  `paused`, `ending`, `ended`), `everUp`, the hub's `interim` and `lines` (`MeetingHubLine`, by position, `source` `hub`
  or `device` when read from the live log), the standing `problem`, `stopped` (the code that stopped the uplink),
  `acked`, the hub's `gaps`, the phone's `lost`, `hub` (`transcription` of `GET …/live` and `detail.meta`:
  `MeetingTranscriptionView`, its `mode`, `state`, `gaps`, `problem`, `fallback`; `MeetingConversation` reads the
  `transcription` part on `meeting_live` for a meeting that named a mode, never `interim`, which comes on the socket)
  and `gapUpload`. `shown(id, role:)` (`MeetingTranscriptionRule.shown`) says what the page and the Live Activity show:
  the hub's caption and lines while the socket is up and no problem other than `no_interim` stands, the phone's own
  lines otherwise, with a notice once it has fallen back: the uplink stopped, a problem other than `no_interim` stands,
  the phone waits for Wi-Fi, a socket never up is down, or one that was up has been away (down or opening again) for
  more than 5 seconds (`reconnectGrace`, `Meeting.away`). Paused, at its end after Stop and ended are no fallback, nor are
  the first connect and a brief reconnect. With the role `off`, the hub's caption and the phone's own lines. A `line`
  frame or a `?from=` read that adds positions brings `MeetingConversation.liveAccepted`'s throttled topics read, as an
  accepted `POST …/live` does in device mode, so the Lock Screen's "Now:" follows the room behind the app. `MeetingTranscriptionView(meta:)` reads `detail.meta` for the recording's detail.
- **Settings** (`MeetingTranscriptionSettings`, load → show → edit → save): the hub's `meeting_transcription_default`,
  `meeting_glossary` (cleaned and refused as the hub would: one line each, repeats dropped case-insensitively, at most 200
  terms of 40 characters) and `meeting_transcription_extension` (`choices`: the built-in contribution first, then every
  installed extension's `transcription` contribution; shown only with more than the built-in one), the read-only
  `meeting_transcription_ready`, and the Wi-Fi switch; a hub without them offers none (`offered`).
- **The start** (`RecordingCaptureControls`, docs/design/DESIGN_SYSTEM.md "One Record entry (#5243)"): a room meeting's
  options have **Transcription** (On This iPhone / On the Hub, a menu row like Persona's, `meeting-transcription-start`)
  only while `MeetingAttendees.offersHub` (the flag and `meeting_transcription_ready`, read again as the page opens), the
  hub's default shown until picked, the pick for that start only (`RecordingStartForm.pickedTranscription`, passed to
  `startTranscription(picked:)`). One-tap starts (the Control, Siri, the activity) use the default, `device` while the hub
  isn't ready. The line under Record says what the mode about to start sends (`MeetingTranscriptionWords.startPrivacy`):
  hub mode's Wording, naming the extension the owner chose (`meeting_transcription_extension`, by its name from `GET
  /api/v1/plugins`), else privacy mode's as before.
- **Settings › Meetings** (`MeetingSettingsView`, `MeetingTranscriptionSettingsSections`), from a hub that has the fields:
  **Transcription** with On This iPhone / On the Hub (the default, `PUT meeting_transcription_default`), under it what the
  default sends (`modePrivacy`) and, while the hub can't transcribe, that meetings stay on this iPhone until it can;
  **Speech Service** only when an installed extension contributes `transcription` beside the built-in one; **Glossary**
  (its count) pushing `MeetingGlossaryPage`: a field and Add, the newest term first, a swipe deletes, each change saved
  at once, a term over 40 characters or a 201st said and never sent; one change at a time (Return and a swipe do nothing
  while a change saves, the field keeps its text and empties only once the hub kept the term); **Hub Transcription on
  Wi-Fi Only** in a section of its own, with what it does and the data an hour costs.
- **The meeting page** (`MeetingPage`): the subtitle adds "Transcribed on the hub" in hub mode (`MeetingPageSubtitle`, a
  view of its own so the store's changes redraw only it). The Live box follows `shown`: the hub's newest lines (by
  position) and its caption under them in the secondary color (read as "Being said: …"; nothing "behind" while one
  shows) while the hub is up and healthy; else the phone's own lines with a notice under the box's title, why in words
  (waiting for Wi-Fi, the hub stopped for this meeting, or not transcribing right now), the iPhone mark in the caution
  color; with Persona off, the phone's own lines and the hub's caption. With the Highlights card on top (phase 2b), which
  shows no lines, the same rule moves to the Full Transcript page (`MeetingTranscriptPage`): the hub's lines are the
  timeline's rows in place of the phone's (`RecordingTimelineRows(hubLines:)`, `RecordingTimeline.sentences(hub:)`, each
  row's id its position, `.line(n)`; no gap marker under them), its caption under the last row
  (`MeetingTranscriptCaption`, never cut there), and the notice above them (`MeetingTranscriptionFallbackRow`), which also
  sits under the card's title so the page says it without a tap; a highlight's time lights the hub's lines in its minute
  then. The page reads none of the store itself: `MeetingTranscriptHubFeed` hands it the hub's lines only when they
  change, and the caption and the notice are views of their own, so a caption a second redraws only its row (P5). The
  Live box and the transcript share the caption's row (`MeetingCaptionRow`) and the notice
  (`MeetingTranscriptionFallbackNotice`). The line beside Talk (`MeetingPageBar`,
  `MeetingTranscriptionWords.pagePrivacy`) says hub mode's Wording in a meeting the hub transcribes (its audio, Talk
  included, goes through the hub), naming the extension the start or the settings read (`MeetingTranscription.service`),
  and privacy mode's otherwise. Views that only ask whether the hub transcribes read `hubMeetings`, which changes with the
  mode alone, never with a caption.
- **The Live Activity** (`RecordingActivity.content`, `transcribed`): in hub mode the line is the hub's latest while the
  page shows the hub's lines, else the phone's own with `fellBack` (the Lock Screen says "On iPhone: …" with the iPhone
  mark, on the line's own lines, so the height is unchanged); `fellBack` goes at once, the line at most every 5 seconds as
  before.
- **The recording's page** (`RecordingTranscriptionRow`): for a meeting that named a mode, `detail.meta.transcription`
  (read with the minutes, `RecordingMinutesModel.transcription`, polled while the hub is finishing) says "Transcribed on
  the hub" with the hub's state ("The hub is finishing the transcript…" with a spinner until `final`, then final or
  failed), the stretches it has no audio for, and the gap uploads (sending, or waiting for Wi-Fi); a meeting that asked
  for the hub and stayed on the phone says so. Its footer says the audio went through the hub, not that it stayed here
  (`RecordingRetentionFooter`, `MeetingTranscriptionWords.audioWentToHub`): from `detail.meta` once read, before that
  from the mode the upload's `200` decided, else from the meeting having asked for the hub with hub-mode time, so it
  never says the audio stayed here while the phone still transcribes its backlog or is offline.
- **Checking it**: `MeetingTranscriptionTests` (the frames, `?at=` as a plain decimal, the hub's frames read leniently,
  PCM16 little-endian, `transcription` from `…/live` and `detail.meta`; the ring: nothing before `ready`, whole frames of
  at most 100 ms and 64 KiB, the resend from `ready.from`, a seek when the hub is behind the ring, what falls out
  unacknowledged as a gap, a jump in the clock; the uplink on a fake socket: start first, the open stretch and the role,
  no audio before `ready`, a stale `ready`, the resend on a new socket, `end` after the last audio; the mode in effect,
  never `hub` unasked, an older manager for good; every error code and close, the backoff; the Wi-Fi rule; what the page
  shows; the start's mode; the flag only beside the conversation and the live view; the timeline's limits, cut texts and
  malformed entries, the role changes kept; the upload's `transcription` and `timeline`; the gap plan, the refusals and
  which meetings still need gaps; the store: listening before the mode, the socket's request, lines and problems,
  missed lines read from the live log, an unknown error stopping it, a drop tried again, `recording_not_found`
  uploading first, a refused handshake, an older manager, a device meeting, Wi-Fi only, pause and Continue, role and
  speaking frames, `end` and the last lines, the `…/live` read, gap uploads until none is left, `transcription_final`,
  Wi-Fi only holding them; after the review: hub-mode time (a Note turned into a meeting, a Note between two meeting
  stretches, a rollback kept across a relaunch, Stop), gaps clipped to it, the requests' cellular flags and the network
  unknown at first, the call's `speaking` without its partial words, a socket healthy only after its first `ack`, nothing
  after `end` and the uplink off the tap, the reconnect grace and no fallback while paused or after Stop, the topics read
  on new hub lines, a gap pass left `later` tried again under background time, the footer before `detail.meta`, a term
  submitted while a save runs; after the privacy review: a fatal error ending hub-mode time for good (kept across a
  relaunch, gaps only before the stop, a Note turned back into a meeting opening none), `replaced` yielding with
  hub-mode time kept, a refused socket kept, a rollback or a delete during a gap pass, Wi-Fi Only turned on with a `PUT`
  under way (a fake `PUT` stopped halfway), the tunnel rule (the decision, the path's last moment on cellular, the
  socket and a gap `PUT` through a tunnel), nothing from a Note's first sample on and the uplink off the tap at once, the
  background time held across a retry's wait and asked for again once the system took it; no `POST …/live` in hub
  mode; the settings: the glossary's cleaning, the extension choice,
  load and save, a refused change put back, a hub without them, the Wi-Fi switch; what the UI says: the privacy texts per
  mode, the extension named only when chosen, the fallback notice's reasons, the recording page's facts, the Lock Screen's
  hub line and the phone's own said so, `detail.meta.transcription` read leniently, the extensions read for the choice).
  The UI test
  `MeetingHubTranscriptionUITests` runs `-showMeetingSample room-hub` (a room meeting listening in that asked for `hub`,
  canned audio into the tap) against the mock hub's hub mode ([testing.md](../operations/testing.md) "Meeting
  transcription"): the upload asks for `hub` with its timeline, the socket sends `start`, the role and audio, the mock's
  lines reach the store (read with `-probes YES` as `probe.meeting-transcription`), and an upstream problem makes it
  fall back and the hub's sentences coming again bring it back. Its UI tests: the start offers On the Hub with the hub
  ready, and its line says hub mode once picked, but not with `ready: false` or without hub transcription (`on: false`);
  Settings › Meetings saves the default and the glossary (the mock's `GET /api/v1/assistant` has them) and refuses a
  long term; the meeting page shows the hub's lines, then the fallback notice on `{"upstream": "failed"}`, the Lock
  Screen the hub's line, then "On iPhone: …", and after Stop the recording's page says it was transcribed on the hub
  and how far the hub is; with the highlights (`-showMeetingSample room-hub-highlights`, `room-hub` started with them
  too) the card holds no line and no Live box, Full Transcript shows the hub's lines, then on `{"upstream": "failed"}`
  the phone's own under the notice, and back on the page the notice under the card's title.

## All-day recording (Apple)

The contract (the wire between watch, phone and hub, retention, the recap) is `docs/assistant/day-recap.md` (#4741); this is how the Apple apps carry it out. The rules are pure and unit tested on the phone's target (`apple/Shared/Recording/`: `RecordingDay.swift` for the contract's values and wire types, `RecordingDayOutbox` for what the watch sends and how, `AllDayAlertPolicy` for what the wrist, Notification Center and the face show, `AllDaySnapshot` for the complication). On the watch `WatchAllDay` carries them out, `RecordingCapture` records in day mode and `WatchDayTransfer` sends; on the phone `RecordingDayReceiver` hands chunks to `RecordingDayStore`, then `RecordingDayTranscriber`, `RecordingDaySync`, `RecordingDayKeeper` and `RecordingDayCatchUp` take them on (`apple/Persona/Recording/`).

### On the watch

- **The mode**: All-Day Recording on the home list (`WatchAllDayCard`), on the recording page, on its own page (`WatchAllDayView`: state, time since it last started, chunks waiting for the iPhone, Pause, Bookmark, Mark Meeting, Erase Last 10 Minutes, Stop for Today, Continue) and the complication. It shows only once the phone set `recordingDay`; while it is `0` and nothing records, the page says "Agree on your iPhone to start" in place of its controls, and a short version of the consent (`WatchAllDayExplanation`) is on the page whatever the state: the watch never asks for consent itself (`AllDayAlertPolicy.access`). The phone's own start request stands for its consent, which its application context may bring later. One microphone: it doesn't start while another recording runs ("Stop the other recording first.").
- **First start**: the first start ever asks once to allow notifications (with the screen off a pause reaches the wrist only as one). The first start of a local day makes the day recording before the microphone opens (`RecordingCapture.openDay`): a UUID, the IANA zone the watch is in, the date there and `recording_started_at`, saved with its seq and event counters. Starting again the same day (after Pause, Stop for Today or a halt) continues the same recording and counters. `RecordingCapture.allDayIntentKey` keeps the wish to record from a start until Stop for Today, so a launch tells a day the app lost from one the user ended.
- **Chunks**: 10 s while the phone is reachable, 60 s otherwise (`RecordingDayContract.liveChunkSeconds`, `backlogChunkSeconds`); the day transfer pushes reachability into the capture (`linkReachable`), which changes the writer's length on the fly. Each chunk carries its mean RMS level over all its samples (`RecordingDayLevel`) for the phone's silence skip. AVAudioFile's m4a writer leaves a `free` box of about 23 KB between `moov` and `mdat`, so 10 s of 32 kbps AAC came to about 65 KB, over the live envelope's 48 000 bytes: `RecordingSegmentWriter` drops it from each finished all-day chunk (`compactsFiles`, `RecordingM4A`, the chunk offsets moved back), which leaves about 42 KB. Every other recording keeps its files byte for byte.
- **Seqs and crash recovery**: the writer takes each chunk's seq from the recording's counter just before it opens the file (`RecordingLibrary.allocateSeq`), and the counter is on disk before the seq is returned, so no seq is handed out twice. At launch, before any seq is taken, `RecordingLibrary.recover` keeps the finished chunks, deletes the half-written one and reports every seq handed out without a chunk as `lost` (with its times when it had a file); a recording whose microphone was open also gets the `paused` (`stopped`) event the exit never wrote. The day stays open under the same id.
- **Midnight in the recording's zone**: moving to another zone never ends a day, and a 23- or 25-hour day ends where it does (`RecordingDayCalendar`). While recording, the writer cuts at the exact sample without closing the microphone: the old day gets `closed`, the next day's recording (named in advance, made with its first sample) a `started`. A day left open while nothing records is closed by a background refresh about 5 minutes after its midnight (`AllDayAlertPolicy.closeRefreshDate`; `WatchBackgroundRefresh` shares the app's one request with the Milk Frog's) or by whatever opens the app next; a pause through the night leaves no empty day.
- **Control events**: `started`; `paused` with its reason, `paused` (Pause, Stop for Today), `interrupted` (a call, Siri, another app, the microphone changing, a resume that failed) or `stopped` (battery, storage, a failed write, an audio services reset, a crash); `lost`; `erase`; `closed`; and the app's own `mark` ("Beyond the contract"). Each is saved with the entry in the same write and kept until acknowledged.
- **Erase** (Erase Last 10 Minutes, while recording or after Stop for Today while the day is open): the chunk being written is cut first (`cutForErase`), then every day recording the window touches (both, just after midnight) gets its `erase` event, naming the chunks it deletes before the phone acknowledged them, and only then are they deleted. An erase the phone asks for: the watch cuts the open chunk when the range reaches it, deletes what it holds in the range and answers with those seqs, kept 7 days (`dayErases`) so a repeated request gets the same answer.
- **Deleted on the phone**: the watch deletes every chunk and the entry (leaving a tombstone). When it was recording into that day it stops (no event: the recording is gone), says so (a stop haptic in front, "Recording Stopped: Today's all-day recording was deleted on your iPhone.", the page's line) and is off, not stopped: a start later that day opens a new recording.
- **Interruptions**: a call or Siri closes the open chunk and pauses. The watch plays a failure haptic only while the app is in front (watchOS ignores haptics otherwise), posts a local notification itself (`WatchAllDayNotices`: "Recording Paused" with **Continue**, or "Recording Stopped" for battery, storage, a failed write, an audio reset), writes the complication's snapshot and hands everything waiting to the system's transfers before it is suspended. One tap continues: **Continue** on the notification, the complication, the card or the page brings the app to the front, the only place the microphone can open (`AllDayNotice`, `WatchAppDelegate`). Coming to the front also resumes any recording a call paused, unless the call's audio still holds the session. In front, a notice goes to Notification Center without a banner; recording again withdraws it.
- **The dead-man's switch**: while recording, a "Recording Stopped" notification is scheduled 10 minutes ahead (`AllDayAlertPolicy.deadManSeconds`) and moved on by the saved chunks once less than 5 minutes is left (`deadManRenewBelow`), so if the app dies it fires 5 to 10 minutes later. At the next launch a day the app ended in while recording gets its `paused` (`stopped`) event and, unless the switch already spoke, the same notice (`alertsOnLaunch`).
- **watchOS / iOS 27**: `RecordingSessionObserver` observes `AVAudioSession`'s `didBecomeInactive` (a system interruption told apart from the app's own deactivation), `resumptionRecommendation` and `didBecomeActive` behind `#if compiler(>=6.4)` and `#available(… 27.0)`, and `AVAudioSession.interruptionNotification` everywhere else; only one of the two, so nothing is handled twice. Every recording uses it, on the phone and the watch.
- **Low Power Mode**: chunk messages wait until the oldest of them ended `RecordingDayOutbox.lowPowerHoldSeconds` ago and go together, so audio recorded in reach is at most `RecordingAudioSpec.lowPowerTransferBatchSeconds` (5 minutes) behind; a backlog drains as fast as the system delivers. The same constant now also spaces the batches of clips and rolling recordings (`WatchRecordingTransfer`: 3 minutes, 5 in Low Power Mode).
- **Meeting marks**: while all-day recording runs, the home list shows **Mark Meeting** above Recordings, the recording page has it in place of Record, and the Record complication's tap marks the meeting instead of starting: one microphone, so the meeting is a mark in the day, not a second recording. Bookmark adds a plain mark. Both are `mark` events and reach the hub as `bookmarks` (`label` `meeting` or empty), a hint for the recap; a second tap of the same kind within a second adds nothing.
- **The complication** (`AllDayRecordingComplication`: circular, corner, rectangular, inline and the Smart Stack) draws `AllDaySnapshot` (`allday.json` in the watch App Group), which the app writes on every change and every 10 minutes while recording (`heartbeatEvery`); a recording snapshot older than 25 minutes (`staleAfter`) shows "Check recording". A tap opens `persona://allday?action=start|continue|open`.

### Watch to phone

- **Live** (reachable): `WatchDayTransfer` carries out `RecordingDayOutbox`'s plan. A chunk goes by `sendMessageData` in the version 1 envelope (`RecordingDayHeader.frame`: the header's length in 4 bytes big-endian, its JSON, the audio file unchanged), the whole envelope under 48 000 bytes (`maxMessageBytes`), and the reply is its acknowledgement; at most 3 messages wait for a reply at once. Events go as messages.
- **Background**: a chunk over the live limit, or any chunk while the phone is out of reach, goes by `transferFile` with its header under `personaDayChunk` as soon as it is finished; events as `transferUserInfo`. Becoming reachable cancels the outstanding files of chunks that fit a message and sends them live, oldest first; the 60 s chunks stay files. Before the app may be suspended (a pause, a stop, a halt, leaving the front, a background wake while nothing records) everything unacknowledged, messages awaiting their reply included, is handed to the system's queued transfers (`handOff`), which outlive the app.
- **Acknowledgements and retries**: no acknowledgement 30 s after a live send, or an error, sends it again after a backoff of 2 s doubling to 5 minutes; a file delivered without an acknowledgement 60 s later goes again; three chunk messages failing in a row count the live path as down even while the session says reachable; a chunk whose message failed goes as a file once it is 5 minutes old; becoming reachable retries at once.
- **The age limit**: a chunk leaves the watch when acknowledged, erased, or still unacknowledged `recordingDayKeepDays` local days after its day ended (at that midnight in its zone; `RecordingDayWatchLedger.ageOut`), which is reported as `lost`, the watch's only age-based loss. A chunk whose file can't be read 3 times is lost too.
- **The gate**: the watch sends day chunks and its state only while the stored `recordingDay` is 1 or more; a phone from before never sees one. `WatchRecordingTransfer` keeps its WatchConnectivity slots for clips and rolling recordings and fans its events (activation, reachability, a saved chunk) out to `WatchDayTransfer`, which owns the day slots (acknowledgements, the phone's erase and delete requests).
- **Waking the watch**: what the phone sends a suspended or closed watch app goes as user info, and `.backgroundTask(.watchConnectivity)` lets the system wake the app to take it; `WatchAllDay.receiveConnectivity` waits (at most 20 s) until the session has nothing pending and the notices it led to are posted. It never opens the microphone: a start waits for a tap on its notice.

### On the phone

- **Consent** (`RecordingDayConsent`, `RecordingDayConsentView`): five points in the contract's order (everyone around is recorded, so ask them; the audio stays on the watch and iPhone and is deleted within 7 days; the text goes to the hub and its AI model; Agents and extensions that read Knowledge can read it; the text is kept `recording_days`, the recap as long as minutes), then **Agree** and **Not Now**. The agreement is kept with the sheet's version, and a new version asks again. `RecordingDayPhoneCapabilities` tells the watch `recordingDay` (1 once agreed, else 0), `recordingDayHub` (the last hello's `recording_day`, cached by `RecordingDayDefaults` for launches in the background) and `recordingDayKeepDays` (`min(7, recording_days)`, 7 until reconciliation said), when the receiver starts and whenever one changes.
- **Stored before acknowledged** (`RecordingDayIntake`, on WatchConnectivity's queue): a chunk's audio and header (`seg-NNN.m4a`, `seg-NNN.json`) or an event (`events.log`) is on disk before its acknowledgement goes; what can't be stored isn't acknowledged, so the watch sends it again. A duplicate is acknowledged and not stored twice; a chunk of a deleted day, inside an erased range or past the day's audio deadline is acknowledged and dropped (the last settled as `lost`). The day's whole record (`day.json`) is rebuilt from those files, without its transcript, if it is missing or unreadable; the folder layout is described in `RecordingDayStore`. Events apply strictly in `n` order: a later one waits for those before it, and one already applied is acknowledged again, not applied twice.
- **Transcription** (`RecordingDayTranscriber`): a chunk quieter than −50 dBFS (`RecordingDayContract.silenceDBFS`) is skipped without the engine (recorded silence is not a gap). The rest go in runs of contiguous chunks up to 60 s (`runSeconds`), so words at a chunk's edge aren't cut; each sentence is saved on the chunk it starts in, with its hub position in the same write. A missing seq is waited for 2 minutes while later chunks keep coming. A run that fails 3 times leaves its chunks `untranscribed`; a missing speech permission stops the lane without counting a try, and a language with no model (or its model not installed) stops only the days in that language, also without a try, while days in other languages go on (`RecordingSpeechError.isEntryLevel`, `isPerLanguage`). The lane runs only while the ordinary transcription queue is idle, so a recording the user waits for goes first.
- **In the background**: a WatchConnectivity delivery may wake the app; the day's lanes work only inside the background time the system grants (`beginBackgroundTask`) or the catch-up task, never download a model there, and stop when the time ends without losing anything. The catch-up (`RecordingDayCatchUp`) is a `BGProcessingTask` (`persona.recording.day-catchup`) that needs external power but no network (`requiresNetworkConnectivity` is false) and starts 15 minutes after it is asked for at the earliest: when the app goes to the background with a day of the last 8 days, and from arriving chunks at most every 15 minutes. It transcribes, uploads and applies retention, and asks again while work is left.
- **Upload** (`RecordingDaySync`): only to a hub that lists `recording_day`; until then the text stays on the phone. A create `PUT` with no segments, then appends: text placed since the last append waits until 20 s after it while the day records (`appendInterval`; the meta-only updates too), text already waiting then (a backlog going in batches) goes at once, and everything goes at once after the day ended; up to 500 sentences and about 900 KB a request, one request at a time; a sentence over 64 KB is split before it gets its position. A repair `PUT` (every positioned sentence, an empty one in each erased one's place) after an erase, a language change, or whenever the phone's log and the hub's disagree. On `413 day_full` the part closes and the day goes on in a next part (a new id, the same day and clock); `RecordingSync` keeps the parts' ids, so deleting the day deletes all of them. `coverage.to` counts only text the hub holds once the request is stored; the gaps come from the control events, the seqs the watch still holds or lost, the erases and the untranscribed chunks (`RecordingDayCoverage`, `RecordingDayGap`). An older manager's refusal stops a day until the next hello or launch (`unsupportedUntilHello`), each of which lets it try once more.
- **Retention** (`RecordingDayKeeper`): a day's audio goes at the earliest of 3 days after its first final recap (reconciliation's `recap_final_at`), 7 days after its day and `recording_days` after its day, whether or not its text reached the hub; the whole day (audio, text, upload state, its row) goes once it began more than `recording_days` ago, applied only once the hub said its `recording_days`. `RecordingSync` and `RecordingTranscription` leave day recordings to their own lanes, and `MeetingRecorder.tagWatchRecording` never sees one.
- **Today** (Knowledge › Recordings, between the capture controls and Recent Recordings; `RecordingDayTodaySection`): shown with a paired watch that has Persona, whatever the hub supports, or while a day recording of today (the date in its own zone) is on the phone; past days alone don't show it. The **All-Day Recording** switch mirrors the watch (on while it records or is paused, `RecordingDayWatchState.isOn`; off for a day the watch stopped by itself, whose line says "Stopped — tap Continue on your Apple Watch") and sends it a start or stop (`RecordingDaySwitch`); a command on its way holds the switch and says so for up to 2 minutes (a start opens the microphone at once while the watch app is in front, otherwise it waits for a tap on the watch's notice); then the switch follows the watch again and its line says the watch hasn't answered for 10 more minutes. Until the user agreed, **Agree to Start on Apple Watch** opens the consent sheet, and so does turning the switch on (the start goes once they agree). Today's day is a row here and is not repeated in Recent Recordings, where past days are rows (`RecordingDayRow`). A hub without `recording_day` gets a caution banner: the day's text stays on this iPhone.
- **The day's page** (`RecordingDayView`): the date, when it was recorded, how much is transcribed, its state word and its gaps by reason; the recap in the minutes' layout (summary with Copy and Share, whether it is final, the timeline under Morning / Afternoon / Evening with meetings marked, the suggested to-dos read-only, confirmed on the web), every time chip opening the transcript at its line, lit for 2 s; before a recap, what it waits for, "Writing the recap…" (looked at again every 30 s), or a failure with **Retry** (`POST …/minutes`). Then one section: a **Transcript** link to the day's transcript by the hour on its own page (`RecordingDayTranscriptView`), **Erase Last 10 Minutes** (today's open day only, asked first) and **Delete Recording**.
- **Erase and delete**: Erase removes the last 10 minutes from every day it touches on the phone, keeps the request for the watch until it confirms or 8 days pass (`RecordingDayWatchRequests.lifetime`, `maxKeepDays` + 1: by then the watch holds nothing of the day; `day-requests.json`, sent again at launch and when the watch shows up) and repairs the hub. Delete removes the day on the phone, the hub (every part) and the watch; when the watch records into it, the confirmation says All-Day Recording stops there. A day deleted on the hub (reconciliation's `deleted`, a `410`) is deleted on the phone and the watch too.
- The Mac compiles this code but never shows a day; visionOS receives days but works on them only in front (it grants no background time).

### Beyond the contract

Apple-only additions, marked `[ext]` in `apple/Shared/Recording/RecordingDay.swift`; a phone or watch that doesn't know them ignores them.

| Addition | Way | What for |
| --- | --- | --- |
| `mark` event (`at`, `label` empty or `meeting`) | watch → phone, `personaDayEvent` | Bookmark and Mark Meeting; uploaded as `bookmarks` |
| `personaDayCommand` (`command` `start` or `stop`, `sent_at`) | phone → watch, message or user info | The Today switch. The watch acts only on a command newer than the last one it acted on and at most 10 minutes old (`WatchDayCommandOrder`); a start while the app isn't in front becomes a notice with **Start** |
| `recordingDayWatch` application context (`RecordingDayWatchState`: `recording`, `day`, `state`, `since`, `next`, `pending`) | watch → phone | The Today switch and row; a new state goes at once, progress at most once a minute |
| coverage gap reason `untranscribed` | phone → hub | Chunks the phone could not transcribe in 3 tries; the hub keeps a reason it doesn't know |

### What the simulator cannot verify

Paired simulators don't deliver `transferFile` and never run background tasks, and a simulator can't stand in for a real microphone or a day on the wrist, so the stream is checked in process (below) and these need a real watch and iPhone:

- Wrist down and screen off for hours (the `audio` background mode); battery, heat and storage over a whole day.
- The phone out of reach and back: the 60 s backlog as files, the small chunks taken back and sent live, the age limit.
- A call or Siri: the notice reaching the wrist with the screen off, Continue taking one tap, the complication showing Paused.
- A force quit or crash while recording: the dead-man's notice within 5 to 10 minutes, and the next launch's report.
- Midnight while recording and while paused (when the background refresh really comes).
- Low Power Mode batching; the iPhone woken by WatchConnectivity and transcribing within its grants; the catch-up while charging.
- The switch starting the watch from its notice; a delete on the phone stopping the watch.
- The watchOS 27 / iOS 27 session messages, which need a 27 system and an Xcode with Swift 6.4.
- On-device speech models for the day's language, and envelope sizes from the watch's own encoder.

### Screenshots and tests

- DEBUG launch arguments, phone: `-showDaySample today|paused|recap|pending|failed|consent` (yesterday's day in Recent Recordings and today's in Today, transcribed and stored as the watch would send them, so the phone uploads them as real days; `paused`: waiting for Continue after a call; `consent`: a user who hasn't agreed), `-openDayRecap YES` (with `-showDaySample`: the past day's page), `-openDayCatchUp YES` (runs the catch-up now). Watch: `-showAllDaySample recording|paused|interrupted|stopped|off|consent|deleted` (no microphone, 3 h 25 min in, 4 chunks waiting), `-openWatchItem allday` (its page), `-openWatchItem allday-complication` (every family in every state), `-recording.phoneDayHub NO` (the "needs a hub update" line), `-showLargeText YES` (an accessibility text size).
- Unit tests: `RecordingDay*Tests` (the contract's values, store, transcriber, upload, coverage, retention, status, recap, writer, outbox) and `AllDayAlertPolicyTests`. `RecordingDayStreamTests` runs the watch's real writer, the outbox's plan, the live envelope and the files, and the phone's real intake, store, transcription lane and upload, over an in-memory WatchConnectivity link to a fake hub, with a fake engine answering after 5 ms and one wall clock the test moves: every chunk stored once and in order, events applied in order whichever way they came, erases reaching the phone. Its latency is the in-process work alone (no radio, no system scheduling, no speech model): per chunk, from finished to stored and acknowledged, to transcribed and to on the hub, printed by the test as `LATENCY` lines (p50, p95, max, n). It fails above its bounds: for live chunks a p95 of 0.5, 1 and 2 s; for a backlog a maximum of 5, 6 and 8 s from each chunk's end, and 2, 3 and 5 s from the phone being back in reach.
- UI: `RecordingDayUITests` against the mock hub (`docs/operations/testing.md`, "All-day recording").

## Entry points

- Phone: Knowledge › "Recordings" (the last type capsule; screenshot arguments `-tab assistant -openAssistantPage knowledge -showKnowledgeKind recordings`, with `-showRecordingSample YES` to add a fictional recording (`second` adds the same one under another id, for a test that deletes one) and `-openRecording YES` to open it).
  On the phone, this category lists local recordings (including those being recorded or transcribed), not hub copies; Mac has no recording feature and lists hub `recordings`. Existing interface styles and layout are preserved.
- Apple Watch: "Recordings" on the home screen opens the recording page, whose Record starts a meeting (Type: Note for a plain note; "Apple Watch" above), and the Record complication starts one from the watch face; recording can start even when unpaired or the hub is offline. Record, pause / resume, bookmark, stop; watchOS 11 and later support Double Tap to start or add a bookmark.
- Phone recording section: choose Chinese / English / Japanese (and Meeting or Note), then tap Record; recording continues after leaving the page or locking the screen, and is stopped on return. Each list row shows source, duration, and state: Recording, Waiting for watch transfer, Transcribing, Waiting to sync, Syncing, Synced to knowledge base (Partially synced), or Not accepted by knowledge base; when the hub is unreachable, the reason appears below the list.
- Details: bookmarks and timestamped transcript; resume transcription, change the language and retranscribe (resync replaces the old version), or delete (immediate local deletion after confirmation, synchronized deletion from the knowledge base, queued offline). A row in the list also deletes with a left swipe, with the same confirmation and the same path; a recording still being made on the watch has no Delete. Original audio is not uploaded.
- Knowledge base: iOS, web, and Android knowledge pages all have a "Recordings" category. Lists show name, time, duration, and source device; details show summary, bookmarks, and full timestamped text.
- The watch cleans up transferred audio only after receiving the phone's application-level persistence acknowledgment; segments whose transfer succeeded but have not been acknowledged remain. The end message transfers independently to avoid displaying Recording forever when stopped at a segment boundary.
- iOS uses `SpeechTranscriber`, trying `DictationTranscriber` if the language is unsupported; system models download on demand. If no model is available, show the reason and retain the recording for retry, without using the cloud.
- Dictation and long recordings share one microphone ownership lock, so neither can reconfigure or close the other's audio session. Deletion retains a UUID tombstone so late watch segments cannot recreate the recording.

## Verified (from #3235 onward)

- iOS / watch simulator and Mac (`CODE_SIGNING_ALLOWED=NO`) builds; `RecordingSyncTests`: upload only once until changed, queued offline retries, eventual deletion after deleting offline, local deletion following knowledge-base deletion, reconciliation restoring recordings lost by the hub, expiry removing audio too, and no upload without text.
- End-to-end UI (`RecordingKnowledgeUITests`, `TEST_RUNNER_PERSONA_MOCK_HUB`, read back through mock hub `/api/v1/recordings` and `/mock/recordings`): a fictional recording appears in Knowledge › Recordings and automatically syncs to the hub (only text and timestamps); deleting from details also deletes it from the hub, with no later upload; no recording entry in the + menu of the new-chat composer.
  `TEST_RUNNER_PERSONA_SHOTS=<directory>` and `TEST_RUNNER_PERSONA_SHOTS_APPEARANCE=light|dark` save three screenshots: list, details, and + menu.

## Verified (composer-entry era, before #3235)

- Phone and watch simulator builds; Mac shared composer build.
- Unit tests for recording format, out-of-order and duplicate segments, abnormal-exit recovery, transcription checkpoints, timestamps, and XML context.
- End-to-end UI: selecting "Use as context" sends no message to the mock hub; tapping Send delivers timestamped text without audio.
- Phone transcript preview screenshots in dark / light mode; watch entry screenshot. A watch Picker compressed into a narrow strip was replaced with single-line language-switch buttons.

## Verification limits

Paired simulator `transferFile` does not deliver, and simulator microphones and speech models cannot replace real-device acceptance. Unit tests cover out-of-order delivery, duplicates, stopping at segment boundaries, recovery, and transcription checkpoints; phone and watch pages were screenshotted on dedicated simulators. Final real-device checks are required for continued recording while locked, incoming calls, transfer of watch offline backlogs, long recordings, model downloads, and permission denial.

All-day recording has its own device checklist: "What the simulator cannot verify" under "All-day recording (Apple)".
