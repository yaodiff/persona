# Phone data tools

`apple/Persona/PhoneTools/`: wraps iPhone capabilities as MCP tools so Claude on a computer can operate phone data. Toggles and authorization are under Settings › Connectors › This iPhone (called "Phone data" before 2026-11-01).

Route: Claude calls a `phone_*` tool → `personad mcp` → node.sock → hub → an online device (iPhone, Android, or Mac, WebSocket `tool_call`) →
the app queries/writes locally → `tool_result` returns along the same route.

Device tool descriptions and schemas are registered by the app (iOS `apple/Persona/PhoneTools/`, Android `android/platform/phonetools/` (`PhoneToolCatalog.kt`)). The hub adds an optional `queue_if_offline` parameter to confirmed writes and supplies its own queue management tools from `server/internal/prompts/mcp/phone/` (`docs/assistant/prompts.md`, "MCP tools"). `personad mcp` adds `file` and `trim` to `phone_clipboard_set` (see "Copying a file to the clipboard").
A conversation lists its tools once (`personad mcp` says `listChanged: false`): when the manager can't reach the hub for the phone's tools then, it asks again briefly, then lists the ones the hub last gave, so a hub restart does not leave a conversation without them (#6422).
Descriptions and parameters for tools with the same name must match exactly on both platforms (the hub merges tools from all phones by name; `PhoneToolCatalogTest` compares against the catalog exported by iOS).

Mail tools `mail_*` use the same `personad mcp`, but execute on the hub without going through the phone; see "Mail tools (mail_*)" below.

## Registration and toggles

| Entry | Purpose |
| --- | --- |
| `PhoneToolRegistry.all` | All tools (ignoring toggles), used by tests and settings |
| `PhoneToolRegistry.enabledSpecs()` | Only data sources that are on and allowed, or on and not asked yet where the first use asks (`PhoneDataSource.offered`); sent in `register_phone` after connecting to the hub and after a call asked iOS |
| `PhoneToolRegistry.call(name:args:confirm:)` | Executes one call, never throws: failure becomes `isError=true` + explanation |
| `PhoneToolRegistry.authorizationStatus(_:)` / `requestAccess(_:)` | Displays status and requests permission in settings |

Each data source (contacts / calendar / reminders / location / health / photos / device / clipboard / notifications, and on iPhone the system
integrations alarms / messages / calls / shortcuts / media / home) can be disabled individually in settings. Disabled sources
no longer appear in `enabledSpecs()`, so the model cannot see their tools, and calls are rejected too (preventing old sessions from continuing to call them).
The iPhone integrations act outside Persona, so they start **off** (`PhoneDataSource.iPhoneOnly`): the user turns each one on; the older sources start on.
A source that is on but refused is not offered either. One that iOS hasn't asked about yet is offered when its first use asks
(see "Permissions"), so the switch in Settings shows it on: the switch always says what the hub is offered.

The table above is for iOS (Mac uses the same code). **Android** uses `PhoneToolRegistry.kt` in `android/platform/phonetools/`, also with `all`, `enabledSpecs()`, and `call(…)`; all calls pass through this single gate:

- **Toggles** are under Settings › Phone data (Android has no "Connectors" group), one per data source (contacts / calendar / location / health / photos / clipboard / device information / notifications, no reminders). All default to off and are stored separately per pairing (`PhoneToolSettings`).
- **Foreground restrictions** are declared in the tool catalog: `PhoneToolCatalog.kt` marks `phone_location_current` and the two clipboard tools with `foreground = true`. When the app is not in the foreground, `enabledSpecs()` omits them and calls are rejected, telling the model to ask the user to open Persona and retry. Moving to the background during execution also interrupts the call.
  - **iOS** has the same rule for the two clipboard tools (#4192): iOS gives an app in the background no clipboard, so a write
    woken by a silent push was dropped while the tool answered ok, and a read came back empty. `ClipboardTool` refuses both
    while Persona is not the active app, with the "open Persona and try again" answer of the other tools that need it on screen
    (`Foreground`), and answers a write ok only once the pasteboard shows it landed (`changeCount` moved and `hasStrings` is true,
    or the text was empty). iOS keeps both registered, since a suspended app cannot withdraw them, so the hub sends no silent push
    for them (`phoneOnScreenTools` in `hub.go`): with no connected phone offering one it answers "open Persona" at once, as it ends
    up doing for an Android phone that stopped offering them. A phone still connected in the background gets the call and refuses it.
- **Hub registration** is handled by `PhoneToolsRuntime.kt`: send `register_phone` after connecting. When toggles, foreground/background state, or the persistent service change (permissions are also rechecked on every return to the foreground), recalculate `enabledSpecs()` and resend only if it changed. Send an empty list on disconnect or unpairing. Tools are offered only while the app is in the foreground or the persistent service is enabled; with neither, none are offered.
- Before execution, check toggles, permissions, foreground state, and online status in order; writes requiring confirmation (only event creation) recheck after the user taps Allow. Duplicate delivery of the same `call_id` returns the previous result without executing again.

See `docs/android/android.md` ("App shell and direct page navigation," the Settings › Phone data item; "Phone data tools aligned with iOS (2026-11-02)") and `android/platform/phonetools/README.md` for wiring and details.

## Tool catalog

| Tool | Permission required | Confirmation | Description |
| --- | --- | --- | --- |
| `phone_contacts_create {given_name?, family_name?, company?, job_title?, phones?, emails?}` | Contacts | **Yes** | New contact (#2286, `CNSaveRequest`); needs a name or a company; phones / emails are `{value, label?}` |
| `phone_contacts_update {id, …, replace?}` | Contacts (full; limited sees only shared contacts) | **Yes** | Adds phones / emails next to the existing ones (duplicates skipped); a field with another value is overwritten, and phones / emails replaced, only with `replace: true`. Notes are not written (`CNContactNoteKey` needs a special entitlement) |
| `phone_contacts_search {query, limit?}` | Contacts | No | Fuzzy search by name, company, job title, email, or phone number (numbers may contain spaces/dashes); returns name, company, job title, labeled phone numbers and emails, and birthday (`--MM-DD` if no year) |
| `phone_calendar_events {start?, end?, query?, limit?}` | Calendar (full access) | No | Defaults to midnight today through 7 days later; returns id, title, start/end, all-day flag, calendar name, location, first 300 characters of notes, and attendees |
| `phone_calendar_create_event {title, start, end, location?, notes?, all_day?, calendar?}` | Calendar | **Yes** | Creates an event, selects a calendar by name, falls back to the default calendar |
| `phone_calendar_update_event {id, occurrence_start?, span?, title?, start?, end?, location?, notes?, all_day?}` | Calendar | **Yes** | Changes or reschedules an event (#2276); a new start alone keeps the length. A repeating event (`recurring: true` in `phone_calendar_events`) needs `occurrence_start`, and `span` `this` (default) or `future`; without it the tool refuses rather than guess. The card shows before → after |
| `phone_calendar_delete_event {id, occurrence_start?, span?}` | Calendar | **Yes** | Deletes (cancels) an event or one occurrence, same rules |
| `phone_reminders_list {list?, include_completed?, limit?}` | Reminders | No | Defaults to incomplete only; returns id, title, due date, completion, priority, list, and first 300 characters of notes |
| `phone_reminders_create {title, due?, notes?, list?, priority?, location?}` | Reminders | **Yes** | Creates a reminder; priority 0/1–4/5/6–9. `location {title, latitude, longitude, radius_meters?, trigger: arrive\|leave}` makes it go off on arriving at or leaving a place (#6564, "Reminders by place" below) |
| `phone_reminders_update {id, title?, due?, notes?, priority?, list?, location?}` | Reminders | **Yes** | Changes only the fields given; `due: null` removes the due date, `notes` replaces the notes (`null` removes them), `list` moves it, `location: null` stops it going off at a place; the card lists every field that changes (#2277) |
| `phone_reminders_complete {id}` | Reminders | **Yes** | Marks complete (using the id returned by `phone_reminders_list`) |
| `phone_reminders_delete {id}` | Reminders | **Yes** | Deletes a reminder the user no longer needs |
| `phone_location_current {}` | Location (while in use) | No | One-shot location, waits up to 15 seconds; returns latitude/longitude, accuracy, altitude, time, time zone, and reverse-geocoded address |
| `phone_location_search {query, limit?}` | Location | No | Finds a place by name or address (MapKit): name, address, latitude, longitude, up to 10 |
| `phone_maps_search_nearby {query, latitude?, longitude?, radius_meters?, limit?}` | Location | No | Places around the current fix (or the given point), nearest first: name, address, coordinates, distance, category, phone, website and the source (#2281). MapKit gives apps no opening hours; the result says so |
| `phone_maps_navigate {latitude, longitude, name?, mode?}` | None (foreground) | No | Opens directions in Apple Maps (`driving` / `walking` / `transit`) and returns `opened: maps`: navigation runs in Maps, not in Persona |
| `phone_maps_route {latitude, longitude, name?, mode?, from_latitude?, from_longitude?}` | Location | No | Works out a route with `MKDirections` and opens nothing (#5544): from Maps' own idea of where the phone is (`MKMapItem.forCurrentLocation()`) or the start given, to the destination, by `driving` / `walking` / `transit`. Returns the distance in meters, the expected time in seconds, the route's name and its line simplified to at most 48 points (`RouteLine`, Douglas–Peucker); transit has the time and distance only, as Apple Maps gives apps no transit line. The prompt asks for it on "how far / how long" and keeps `phone_maps_navigate` for when the user wants to go |
| `phone_health_summary {metrics?, days?}` | Health (read) | No | Defaults to steps over the last 7 days; steps / distance (m) / active_energy, resting_energy, dietary_energy (kcal) / protein, carbs, fat (g) / heart_rate, resting_heart_rate (bpm) / sleep (hours) / weight (kg). Energy and nutrients are daily sums; today is partial. See "Daily health summaries" below |
| `phone_health_write_sample {metric, value?, start, end?}` | Health (write) | **Yes** | Writes one sample: steps (integer) / distance (meters) / active_energy (kilocalories) / weight (kilograms, one instant) / sleep (an asleep interval, no value); intervals at most 24 hours, not in the future; one sample at a time, no bulk import or deletion |
| `phone_workouts_recent {days?, activity?, limit?}` | Health (read) | No | iPhone (and Vision Pro). Recent workouts from Apple Health, newest first (default 30 days, max 365; default 20, max 50), of one activity if given. Each row has the id, activity, source app, start and end, duration, distance, active energy, elevation gain, average and maximum heart rate, and pace for runs, walks, hikes and rides. See "Workouts" below |
| `phone_workout_detail {id}` | Health (read) | No | One workout by id: the same row plus per-kilometre splits (pauses left out) and minute-average heart rate |
| `phone_photos_recent {limit?, days?, screenshots_only?}` | Photos (read) | No | Recent photo/video metadata: local id, time, type, dimensions, screenshot/favorite flags, duration, latitude/longitude |
| `phone_photo_get {id, max_size?}` | Photos (read) | No | Returns the JPEG image itself (default longest edge 1280, quality 0.7) + metadata |
| `phone_device_info {}` | None | No | Model identifier, iOS version, battery and charging state, Low Power Mode, storage, network type, time zone, language, thermal state, the app's `app_version`, `app_build` and `app_channel` (`debug` / `release`; the app carries no commit) |
| `phone_clipboard_get {}` | None (foreground; system paste prompt) | No | Reads clipboard text |
| `phone_clipboard_set {text}` or `{file, trim?}` | None (foreground) | No | Replaces clipboard text; `file` copies a text file from the agent's computer without the model reading it (see "Copying a file to the clipboard") |
| `phone_notify {title, body, delay_seconds?}` | Notifications | No | Local notification, optionally delayed up to 24 hours |
| `phone_alarms_list {}` | Alarms (AlarmKit) | No | Persona's own alarms and timers (#2278): id, label, state, time / repeat days, timer length. The Clock app's alarms are invisible to apps |
| `phone_alarm_create {time, repeat?, label?}` | Alarms | **Yes** | `time` is `HH:mm` (next occurrence, or weekly on `repeat` days) or one ISO 8601 moment; rings through silent mode and Focus |
| `phone_timer_start {seconds, label?}` | Alarms | **Yes** | Countdown timer, 1 s – 24 h |
| `phone_alarm_cancel {id}` / `phone_alarm_pause {id}` / `phone_alarm_resume {id}` | Alarms | Cancel only | Only ids of Persona's own alarms |
| `phone_messages_compose {recipients, body}` | None (foreground) | System sheet | Prefills `MFMessageComposeViewController` (#2279); the user sends or cancels there and the result is `sent` / `cancelled` / `failed`. Nothing is sent silently, nothing is read |
| `phone_call_start {number, kind?, name?}` | None (foreground) | **Yes** | `tel:` / `facetime:` / `facetime-audio:` (#2280); one number only (letters or two numbers are refused), then iOS asks again. The result says the call was handed to iOS, not that it connected |
| `phone_shortcuts_list {}` / `phone_shortcuts_forget {name}` | None | No | The Shortcuts the user allowed (#2283), kept on the phone |
| `phone_shortcuts_register {name, purpose?}` | None | **Yes** | Allowing a shortcut is the confirmation; after that it runs without asking |
| `phone_shortcuts_run {name, input?}` | None (foreground) | No | Only an allowed shortcut; `shortcuts://x-callback-url/run-shortcut` with `x-success` / `x-cancel` / `x-error` back to `persona://shortcut-callback/…` (`ShortcutCallbacks`, handled first in `onOpenURL`). Waits 90 s for the callback, then says no answer came |
| `phone_media_play {query, kind?}` | Apple Music (MusicKit) | No | Searches the catalog and plays the best song / album / playlist in the Music app (`SystemMusicPlayer`, #2284); without a subscription that can play the catalog it says so |
| `phone_media_control {action}` | Apple Music | No | play / pause / next / previous / status of the Music app's player; other apps cannot be controlled |
| `phone_music_create_playlist {name, songs, description?, play?}` | Apple Music (MusicKit) | **Yes** | Searches the catalog for each song (one search per entry), creates the playlist in the user's library (`MusicLibrary.createPlaylist`, iOS 16+) and, unless `play` is false, plays it with `SystemMusicPlayer`; returns the playlist, the songs added and the searches that found nothing; needs a subscription that can play the catalog |
| `phone_music_snapshot {recent_limit?, playlist_limit?}` | Apple Music (MusicKit) | No | Returns one JSON object with now playing, recently played songs and library playlists, for the Music extension's `applemusic_iphone_sync`; reads only, never changes the library or playback |
| `phone_media_open {service, query}` | None (foreground) | No | Opens a Spotify or YouTube search (their universal links); the result says `playing: false` |
| `phone_home_list {}` | Home (HomeKit) | No | Homes, rooms, accessories (reachable, security-sensitive, controls with last known values) and scenes (#2285) |
| `phone_home_control {accessory_id, control, value}` | Home | **Yes** | power / brightness / target_temperature / lock / door, one change per call; locks, garage doors, security systems, doors and windows get a warning line on the card; an unreachable accessory fails without writing |
| `phone_home_run_scene {scene_id}` | Home | **Yes** | Runs a scene; the card says when it touches a security accessory |
| `phone_notifications_list {limit?}` | None | No | **Android only**: Persona's own currently displayed notifications (id, title, body, time), newest first, default 50, maximum 100; excludes confirmation-card notifications and cannot see other apps' notifications |

In the Persona chat on iPhone, the results of `phone_location_current`, `phone_location_search` and `phone_maps_search_nearby` are also drawn as a map card under the turn's work line, with numbered pins and Directions to Apple Maps (#5543), a route `phone_maps_route` worked out as its line with the mode, time and distance and a labeled Start in Maps (#5544, #6177), and directions `phone_maps_navigate` opened in Maps as the destination's card with its mode and a button that opens them again (#5545; instant result cards in `docs/assistant/unified-entry.md`). The tool calls stay folded.

Coordinates are WGS-84 everywhere, mainland China included: Core Location, MapKit's search (`MKLocalSearch`, `CLGeocoder`), `MKMapSnapshotter` and `MKDirections` take and give WGS-84, and MapKit shifts the mainland map (GCJ-02) itself. Nothing in Persona converts, so a place found by search, the phone's own fix and a saved place agree, and a reminder's circle is where the user meant (`docs/lessons/ios.md`, "MapKit speaks WGS-84 in mainland China too").

### Reminders imported as to-dos (#4009)

Under Settings › Phone data, once Reminders is on, **Import into To-dos** (off by default) sends the incomplete
reminders of the lists the user leaves on (all of them at first) to the hub, which makes to-dos of them, one way
(`RemindersImport`, `RemindersImporter`; `docs/assistant/todos.md`, "Imported from Reminders"). It runs on launch, on
return to the foreground and when the Reminders database changes while the app is in front, and sends nothing when the
batch is the same as the last one the hub took.

### Workouts

`phone_workouts_recent` and `phone_workout_detail` (#2526) read workouts that the Workout app or any other app saved to Apple Health.
They let a workout report compare a workout with recent ones of the same activity (`docs/assistant/health.md`, "Automatic workout
reports (iOS)"), and let later questions ("how was last week's run?") use the same data. `WorkoutReader` holds the queries that the
reports share.

- **What is read**: the workout itself and its heart rate, distance (on foot or by bike) and active energy samples. No route or
  location is read.
- **Activity names**: a report's activity names (`Running`, `Strength training`, …), lowercased with `_` for spaces, are the
  tool's `activity` values. Other HealthKit activities are listed but cannot be filtered on.
- **Splits**: whole kilometres in order (`WorkoutSplits`). Each distance sample's meters are spread evenly over its own interval.
  The pauses from the workout's events (the user's own, and the Watch's automatic ones) are left out, as the Workout app does.
  The last part kilometre is dropped. Samples that cannot give a sensible split (a kilometre in no time, a sample outside the
  workout) give no splits at all, since the hub refuses a whole report over one bad split. Swimming and other activities have
  none.
- **Elevation gain** comes from the workout's `HKMetadataKeyElevationAscended`. The average and maximum heart rate come from the
  workout's own statistics. Values outside sensible bounds are left out.
- **Pace** is the duration over the distance, for a run, walk, hike or ride of at least 500 m.
- **Permission**: workouts and cycling distance join `HealthTool.readTypes`. Someone who allowed Health before these types
  existed is asked again by the tools and the reports, through `statusForAuthorizationRequest`. That happens only while Persona
  is in front, where the Health sheet can show. Health never says what was allowed, so a refused type reads as no data.

### Daily health summaries

`phone_health_summary` reads the requested metrics and returns one row per phone-local day; `days: 1` starts at midnight today and ends at the query time. The default is `metrics: ["steps"]`, `days: 7`; the maximum is 90 days. Days or metrics without data are omitted, never filled with zero.

- `dietary_energy` is energy consumed (kcal); `protein`, `carbs`, and `fat` are grams consumed. Each is summed over the day. The result contains daily totals, not foods or meals.
- `resting_energy` is resting energy burned (kcal), summed over the day; `active_energy` remains the separate activity-energy total (kcal).
- iOS reads `.dietaryEnergyConsumed`, `.dietaryProtein`, `.dietaryCarbohydrates`, `.dietaryFatTotal`, and `.basalEnergyBurned` through `HealthTool.quantityIdentifiers`. These join `HealthTool.readTypes`, so the Health authorization request includes them. People who allowed Health before these types were added must also grant the new read permissions.
- The hub advertises the app's registered description and metric enum. Open/reconnect the updated phone app so its new catalog reaches the hub; a hub update alone cannot add metrics to an older phone app.
- HealthKit does not reveal read authorization status: an empty result can mean no source data or denied read access. Check that the source app wrote today's entries and that Persona may read those categories.
- **Locked phone (#7677)**: while the iPhone is locked, HealthKit's database is closed and every query fails with `HKError.errorDatabaseInaccessible`. The app then never starts a query: it answers from a snapshot of every metric over 90 days, written to Application Support with `completeFileProtectionUntilFirstUserAuthentication` while the app was in the foreground (refreshed on foreground and after successful live reads, throttled to 15 minutes). A snapshot answer keeps every field of the live answer and adds `"source": "snapshot"` plus `"as_of"`; the hub prepends a plain-text note with the same `as_of`. When no snapshot exists yet, the tool fails with a bilingual `device_locked` message instead of putting the English HealthKit error into the day fields.

For the iPhone acceptance steps using 薄荷健康, see `docs/assistant/health.md` ("How to verify").

### Reminders by place (#6564)

"Remind me to buy milk on the way home": the assistant writes a reminder with a location alarm (`EKAlarm.structuredLocation` and `proximity`), and iOS watches the circle itself, like a reminder made in the Reminders app. Persona needs no background location and no "Always" permission; the alarm goes off on the phone and the watch even when Persona is closed. A location alarm goes off once.

- **When it goes off is the assistant's call** (`server/internal/prompts/assistant/claude_md.md`): something to do on the way is leaving where the user set out from; something to do once there is arriving. It says which it chose, and `phone_reminders_update` changes it.
- **Places** (home, office) are the user's saved places, below; the assistant reads them with `list_places` and never takes a place's coordinates from memory.
- **No duplicates**: the assistant lists reminders first and changes the matching one. "Done" completes it and "not needed" deletes it.
- The radius defaults to 150 m (100–2000): fixes indoors are often 50–100 m off, so a smaller circle misses.
- Not yet: going off near any shop of a kind ("when I pass a supermarket"), and Android, which has no reminder tools.

### Saved places (#1598)

The places reminders by place go off at belong to the user, not to the assistant's memory: **Settings › Assistant › Saved Places** lists them and lets the user add, change and delete them (`SavedPlacesView`, `SavedPlacesModel`). A place is a name (what the user calls it in a chat), an address, coordinates and the reminder's radius (150 m by default, 100–2000). The editor (#4248) puts a map first, there from the start: a saved place opens centered on it, and a new one on the user when location access is already allowed (the editor never asks by itself; Use Current Location does), else wherever MapKit opens. The place is set in three ways:

- **Search**: suggestions follow what is typed (`MKLocalSearchCompleter`, addresses and places, favoring the area the map shows); picking one looks up its coordinates and address (`LocationTool.find(_:)` for a suggestion). The keyboard's Search key runs a full search (`LocationTool.find(_:limit:near:)`, the same search as `phone_location_search`, which passes no region so its answers are unchanged). MapKit's "nothing there" says no places were found; any other failure says the search couldn't run.
- **Use Current Location**, which asks for When In Use access once.
- **A tap on the map** moves the place there and looks up its address. The map pans and zooms; a tap on a far-out map (much more than the place's own span) zooms in on the place, otherwise the user's zoom stays.

The circle on the map follows the reminder's radius. Delete from the list (swipe or context menu) asks first, like the editor's Delete, and a full swipe does nothing.

- **Kept by the manager** in its settings (`assistant.places`, `server/internal/assistant/places.go`), per manager. `GET/POST /api/v1/assistant/places`, `PUT/DELETE /api/v1/assistant/places/{id}` (`docs/protocol/protocol.md`); names are unique (case aside), writes need full credentials.
- **The assistant** reads them with `list_places` before a reminder by place, and before a route or directions to or from a place the user names ("how long to get home", "directions to the office": the saved place's coordinates go to `phone_maps_route` or `phone_maps_navigate`, #6386) instead of searching Apple Maps for "home". When a place is missing, it finds it with the user (`phone_location_current` there, or `phone_location_search` and the user's pick) and keeps it with `save_place`, which adds the place or changes the one of the same name; so whatever it uses, the user sees and can correct.
- Changing a saved place does not move the reminders already set: each reminder keeps the coordinates it was made with.

Tools by platform:

- **iOS**: everything above except `phone_notifications_list`. The Home tools only in a build with the HomeKit capability ("iPhone system integrations" below).
- **Mac** (`PersonaMac`): no health tools (Mac has no health data, `PhoneToolRegistry.healthTools`) and none of the iPhone system integrations
  (alarms, messages, calls, shortcuts, media, home, Maps' nearby search and navigation); otherwise the same as iOS. Vision Pro: the same as the Mac plus health.
- **Android**: no reminder tools, `phone_health_write_sample`, `phone_location_search`, or any tool of #2276–#2286 yet; adds `phone_notifications_list`.
  `phone_location_current` is available only while the app is in the foreground. `scripts/assets/export_ios_phone_tools.py` leaves these out of the Android fixture
  (`IOS_ONLY`, `IOS_ONLY_TOOLS`).

### iPhone system integrations (#2276–#2286)

Code: `CalendarTool`, `RemindersTool`, `ContactsTool` (changes), `MapsTool`, `AlarmsTool`, `MessagesTool`, `CallTool`, `ShortcutsTool`, `MediaTool`,
`HomeTool`, shared helpers in `SystemTools.swift` (`SystemAccess`, `Foreground`, `CardTime`). Tests: `PhoneSystemToolsTests`.

- **Done means done**: a tool that only hands over to another app says so (`opened: maps`, `handed_to_ios`, `playing: false`, `result: cancelled`);
  the assistant's prompt tells the model to say it the same way (`server/internal/prompts/assistant/claude_md.md`).
- **No guessing**: a repeating event without `occurrence_start`, a call string with two numbers, a contact field that already has another value
  (without `replace`), a shortcut the user did not allow and an id that is not one of Persona's alarms are refused with a sentence the model can act on.
- **Foreground**: messages, calls, Maps, Shortcuts and Spotify / YouTube need Persona on screen (`Foreground.require`): iOS only opens another app, or
  shows a sheet, for the app in front. In the background they fail and ask the user to open Persona.
- **The hub waits for the user** on every tool with a card and on `phone_messages_compose` (`phoneConfirmedTools`, 30 minutes).
- **Alarms** need `NSAlarmKitUsageDescription` and iOS 26 (the deployment target). Not yet: a Live Activity for `AlarmAttributes<PersonaAlarmData>` in the
  widget extension, so a timer's countdown is not drawn on the Lock Screen / Dynamic Island; the alert itself rings.
- **Apple Music** needs `NSAppleMusicUsageDescription` and the MusicKit app service on the App ID (developer account); without the service catalog
  requests fail and the tool reports it.
- **Home** needs the HomeKit capability on the App ID and the `com.apple.developer.homekit` entitlement, a signing change for the owner of the
  developer account. Until a build sets both and `PERSONA_HOMEKIT=YES` (Info.plist `PersonaHomeKit`, `HomeAccess.isBuilt`), the Home source is not
  shown and its tools are never offered. `HMHomeManager` (which asks for permission) is only made after the user turns Home on.
- **Share Extension** (#2282): files and choosing the conversation are #1460; nothing changed here.

Results are consistently pretty-printed JSON text (`ToolContent.text`), with all timestamps in ISO 8601 including the local time-zone offset; date fields use
`yyyy-MM-dd`. `phone_photo_get` also includes a `ToolContent.image`.

## Confirmation flow

Writes (create / change / delete event, create / change / complete / delete reminder, write health data, create / change contact, alarms and timers,
calls, allowing a shortcut, Home changes and scenes) carry a `confirmation` closure; before execution, the app shows a confirmation card (Android only confirms event creation, since it lacks the other three write tools), with a summary such as "Create event ‘…’ 14:00 → 15:00"
or "Write health: weight 72.5 kg". If the user denies, the tool returns `isError=true` with `The user declined on the phone.`. Confirmation covers only one call;
if Claude calls again, it asks again.

- **One at a time** (#4456, `PhoneConfirmations`): a write that comes while another is on screen waits behind it and shows once that one is answered.
  Each answer goes to the confirmation it names: dismissing the dialog denies only the one it showed, and an answer counts once.
- **How long the hub waits** (`phoneConfirmWait` in `server/internal/hub/hub.go`, #4427): a confirmed write waits up to 30 minutes, other phone calls 2 minutes.
  The clock starts when the hub sends the `tool_call` to the phone (after waking it, if it had to), not when the dialog shows: a write queued behind another
  has less of its 30 minutes left when its turn comes, and the hub may have stopped waiting already. An approval after that is still carried out on the phone,
  and the agent was told to check before calling again. Claude Code's own limit for a silent MCP call is also 30 minutes; `personad mcp` sends progress
  while the call waits, so the hub's reply is the one the agent gets (#1498, `docs/protocol/protocol.md` "MCP tools").
- **Android** (#1875, `docs/android/android.md`): the phone takes a confirmation back after 90 seconds and answers that nobody approved or declined it
  (nothing saved, not a refusal), or that it couldn't be shown (Persona in the background with notifications off). Only Deny returns the decline sentence.
- **A result while the socket is down** (#4456, `ToolResultOutbox`): Allow tapped just as the app comes back from the background can land before the socket
  has reconnected. The result is kept (only tool results, at most 16 per hub group) and sent once that group connects again; the hub matches it to the call
  by `call_id` whatever connection it comes on. One older than 30 minutes is dropped: the hub no longer waits for it.

Reads do not interrupt the user, but every call leaves a `tool_call` / `tool_result` record in the session.

`phone_clipboard_set` and `phone_notify` also change things on the phone, but do not show confirmation. Confirmation happens in the app; the hub's `connector_gate` does not govern phone tools. `mail_send` confirmation is requested by the hub under different rules; see the next section.

## Offline write queue

A caller can opt into hub storage by adding `"queue_if_offline": true` to one of the six confirmed writes:
`phone_calendar_create_event`, `phone_reminders_create`, `phone_reminders_update`, `phone_reminders_complete`,
`phone_reminders_delete`, or `phone_health_write_sample`. The hub adds the optional boolean and its description to the
registered schema, then removes it before forwarding arguments to the app, so existing apps need no update.
Read tools, `phone_clipboard_set`, and `phone_notify` cannot be queued. A write must also appear in a retained phone
registration; the hub refuses tools that no registered phone offers.

- **Default unchanged**: omitting the parameter, or setting it to false, uses the existing call and background wake path;
  an unreachable phone still returns an error. With true and no connected device offering that tool, the hub saves the
  request immediately and returns its queue record and opaque `id`. After admission, it also attempts a silent push
  in the background using the same device selection and 30-second throttle as ordinary calls. The caller waits for
  neither the push nor reconnection. Without push configuration this attempt does nothing; the entry waits for the
  app to connect. With a connected device, the call runs normally and waits for its result. An error after sending
  never turns into a new queue entry.
- **Confirmation still required**: accepting a queue entry does not mean the write happened. When a phone registers
  the needed tool, one delivery loop sends queued entries in their original order among the currently available tools,
  waiting for each result before sending the next. Every delivery uses the normal app confirmation flow and starts its
  own `phoneConfirmWait` deadline (30 minutes). Registration does not wait for queue delivery, and a tool unavailable
  on every connected device stays queued.
- **Deduplication before delivery**: a queued entry with the same tool and normalized JSON arguments returns the existing
  ID. Object key order and whitespace do not matter. Once delivery starts, this is no longer a deduplication guarantee:
  query the ID instead of submitting the operation again.
- **Durable, with no automatic resend**: queued entries survive hub restarts. A delivered request whose confirmation
  times out or whose connection closes becomes `uncertain`; the same applies to entries still `delivering` at hub startup.
  The hub never automatically resends these entries. A later app result cannot turn an uncertain write into a retry.
  Check the phone's actual calendar, reminders, or health data before deciding whether another write is needed.
- **Bounded storage**: the hub accepts at most 100 pending entries (`queued` plus `delivering`), with at most 64 KiB of
  JSON arguments per entry. Cleanup runs only when admitting a write or recording a delivery result: it removes
  terminal records older than 30 days and keeps at most 1,000 terminal records. Reads, cancellations, and startup
  recovery do not delete records, so older or excess terminal records remain visible until the next cleanup; the
  total stays bounded at 1,100 records. Results are limited to 64 KiB; a larger result is replaced by an explanation
  while preserving its success, decline, or failure state. Queued entries do not expire. A full pending queue rejects
  new entries; it does not discard accepted writes.

Queue management is available to computer sessions through the same MCP route, including while every phone is offline:

| Tool | Result |
| --- | --- |
| `phone_write_queue_list {id?}` | `{items: [...], pending_count: N}`; omit `id` for all retained records, or supply it to inspect one; `pending_count` counts all queued and delivering entries |
| `phone_write_queue_cancel {id}` | Cancels only a still-queued entry and returns its record; repeating an already-cancelled ID is safe, and an already-sent confirmation cannot be recalled |

Each record contains `id`, `tool`, `args`, `state`, `created_at`, `updated_at`, and an optional `result` in `ToolResult`
format (the phone reply or a hub explanation). States are `queued`, `delivering`, `succeeded`, `declined` (the user explicitly denied it), `failed` (a tool
error, such as missing permission), `uncertain`, and `cancelled`. Preserve the distinction between `declined` and an
unanswered confirmation; only the explicit phone decline result means the user refused.

The authenticated hub API exposes the same list and cancellation operations at `GET /api/v1/phone/write-queue` and
`POST /api/v1/phone/write-queue/{id}/cancel` (see `docs/protocol/protocol.md`, "Phone write queue"). Clients can poll the
list and use `pending_count` for a pending-write badge. The iOS / Android queue screen and badge are not implemented;
existing apps continue to show the usual per-write confirmation cards.

## Copying a file to the clipboard

`phone_clipboard_set` takes either `text` or `file` (#3947). `file` is the absolute path of a text file on the computer
the agent runs on; for the assistant, that is the hub machine. It is for secrets (API keys, tokens, passwords): the model
sends only the path and never sees the text, so the text stays out of its context, its transcript and Claude Code's
safety checks. `trim` (default true) removes line breaks at both ends, since key files usually end with one.

- **Who reads it**: `personad mcp` (`server/internal/mcpserver/clipboard_file.go`), the process Claude Code starts for
  the agent. It runs as the agent's user, inside the agent's sandbox when there is one (`docs/server/agent-sandbox.md`),
  so it reads only what the agent could read itself. It sends the phone an ordinary `{text}` call, so the iOS and
  Android apps and the protocol are unchanged. Not the node: it is not sandboxed, so a sandboxed agent could have it
  copy `hub.json` to the clipboard and read it back with `phone_clipboard_get`. Not the hub: it may run in a container
  or on another machine than the agent.
- **What it accepts**: an absolute path (`~` is not expanded) that resolves, after symlinks, inside the user's home
  directory. The file must be a regular file the same user owns (not checked on Windows), at most 64 KiB, and UTF-8
  text with no NUL or other control characters except tab, LF and CR. A leading byte order mark is dropped. The last
  part of the path is opened without following symlinks, and the opened file is checked again. A guest conversation's
  server neither lists `file` nor reads one. These rules keep the argument to its purpose. They are not a wall against
  the agent, which runs as the same user anyway.
- **What comes back**: `Copied the text of <path> to the phone's clipboard: N bytes, M characters, ending in "wxyz".`
  The last 4 characters are shown only for text of 16 characters or more. Refusals name the path and the rule, never
  the content. A phone answer that quotes the text is replaced. Nothing on the way (node, manager, hub) logs a call's
  arguments; the phone gets the text, as it does with `text`.
- `personad mcp` alone lists the two arguments (in `tools/list`); runtimes that reach phone tools another way
  (`docs/integrations/runtimes.md`) see the phone's own schema with `text` only.

## Mail tools (mail_*)

Mail tools are supplied by the hub's mail connector (`mailConnector` in `server/internal/extmgr/mail_connector.go`, with descriptions and parameters in `server/internal/prompts/mcp/mail/`). They connect to IMAP / SMTP on the hub, without going through the phone.
Both the assistant and Agents can use them (connector `AgentTools: mail_*`). See `docs/extensions/mail.md` for accounts and credentials.

| Tool | Confirmation | Description |
| --- | --- | --- |
| `mail_accounts {}` | No | Lists configured email accounts (id, name, address, provider); call before search or sending to get `account_id` |
| `mail_search {account_id?, mailbox?, query?, limit?, before_uid?}` | No | Searches mail, returning uid, subject, sender, time, and summary; omitting `account_id` searches all accounts' inboxes; `mailbox` defaults to INBOX; `limit` defaults to 20, maximum 200; `before_uid` pages backward. Gmail uses Gmail search syntax; other providers use server-side full-text search. If server search is unavailable, the hub matches subject, sender, and recipients in a recent batch (not body), returning `before_uid` for backward pagination |
| `mail_read {account_id, uid, mailbox?}` | No | Reads one message in full: headers, plain text, HTML, attachment list; does not mark read |
| `mail_send {account_id, to, cc?, bcc?, subject, text, html?, in_reply_to?, references?}` | **Yes** (hub asks) | Sends mail; `to` / `cc` / `bcc` are arrays of `{name?, email}`; for replies, set `in_reply_to` to the original Message-ID and `references` to its original References plus that ID |

`mail_send` confirmation is separate from phone writes: it is a connector write passing through the hub gate (`server/internal/connector/gate.go`).
If no rule matches, the default is confirm: the hub sends an approval card to the phone ("Send mail to …: subject"), returning `The user declined mail_send.` if denied;
`connector_gate` rules in the manager's manager.json can set allow (send without asking) or deny (reject outright). Writes in health chats always ask; allow rules do not bypass confirmation (`docs/assistant/health.md`).

## Permissions

System permissions are requested at two points: when the user turns a source on in Settings (`PhoneToolRegistry.requestAccess`), or on the first
actual use of a tool (#7659).

- **First use asks** for contacts, calendar, reminders, location, photos and notifications (`PhoneDataSource.asksWhenUsed`, iOS only). Their
  tools are offered while the source is on and iOS hasn't asked yet, so "remind me to buy milk when I get home" works for someone who never
  opened Settings. Before 2026-11-14 they were offered only once allowed: the model never saw them and could only guess that something was off.
  - `PhoneToolRegistry.call` checks the switch, then shows a write's confirmation card, then iOS's own permission dialog
    (`askOnFirstUse`), then runs the tool. A write the user turned down never asks iOS.
  - Allowed, the call goes on. Refused, it fails with where to allow it ("Permission denied … iOS Settings › Persona").
  - iOS shows the dialog only for the app in front. While Persona is not, the call fails with "open Persona on the iPhone and try again"
    and nothing is asked.
  - Calls that arrive together wait for one dialog.
  - Either answer posts `phoneAccessAnswered`, and `AppModel` registers again, so a refused source leaves the hub's list.
- **Settings only** for Health and the iPhone integrations. Health's sheet lists every type, and iOS never says which reads were allowed
  (see below). The integrations act outside Persona and start off. Health only requests read permission again after a grant, to add newly
  introduced types. Their tools that meet "Not requested" return an error and tell the model to ask the user to turn them on.
- The Mac app (frozen) and Vision Pro keep asking from Settings only. Android has its own rules (above).
- Previously denied permissions can only be changed in iOS Settings.

| Data source | Info.plist key | Authorization |
| --- | --- | --- |
| Contacts | `NSContactsUsageDescription` | `requestAccess(for: .contacts)`, full or limited |
| Calendar | `NSCalendarsFullAccessUsageDescription` | iOS 17+ `requestFullAccessToEvents()` |
| Reminders | `NSRemindersFullAccessUsageDescription` | iOS 17+ `requestFullAccessToReminders()` |
| Location | `NSLocationWhenInUseUsageDescription` | `requestWhenInUseAuthorization()` |
| Health | `NSHealthShareUsageDescription`, `NSHealthUpdateUsageDescription` (+ entitlement) | `requestAuthorization(toShare: five write types, read: …)` |
| Photos | `NSPhotoLibraryUsageDescription` | `.readWrite`, possibly limited (only user-selected photos are readable) |
| Notifications | None (runtime authorization) | `requestAuthorization([.alert, .sound, .badge])` |
| Alarms | `NSAlarmKitUsageDescription` | `AlarmManager.shared.requestAuthorization()` |
| Apple Music | `NSAppleMusicUsageDescription` (+ MusicKit service on the App ID) | `MusicAuthorization.request()` |
| Home | `NSHomeKitUsageDescription` (+ HomeKit entitlement, see above) | the first `HMHomeManager` |
| Messages, calls, Shortcuts | None | The user acts in iOS's own sheet or prompt |

Health writes write only the one sample confirmed on the phone: the confirmation summary and written data come from the same argument parsing (`HealthSample`, values to at most two decimal places; what the card shows is what gets written). Invalid arguments make the card say "Invalid parameters; nothing will be written." Samples are marked as manually entered (`HKMetadataKeyWasUserEntered`). Unlike read access, the system reports write permission to the app: users who previously granted only read access (before writes existed) see another system health permission prompt after confirming their first write. Denial returns an error telling them to enable access in iOS Settings › Health › Data Access & Devices › Persona.

Health has a hard iOS limitation: **the system never tells an app whether read permission was granted** (to prevent information leakage). Settings therefore show whether permission has
been requested; denial appears as empty query results. The tool description explains this, so the model cannot infer anything beyond "no data" from empty results.

Android's `phone_health_summary` (implemented in #278, omitted when #632 merged into the integration branch, rebuilt in #820) reads Health Connect; its name, description, and parameters match iOS exactly (including
"read permission is never reported to the app," because the hub stores a single tool catalog). The difference: Health Connect tells the app which read permissions were granted,
so settings can show "Only some data allowed," and unauthorized metrics appear in `not_granted`; if none are granted, return an error rather than an empty result.
Health Connect is built into Android 14 and later; on 8–13 it must be installed separately (usually absent on phones without Google services), in which case the tool returns "This phone has no Health Connect."

## Implementation notes

- Connection reuse per account does not apply: each tool creates its own query. EventKit's `EKEventStore`, `CNContactStore`,
  and `HKHealthStore` are created on demand to avoid passing non-Sendable objects across actors.
- Convert all network/system callbacks to async with `withCheckedContinuation` + a one-shot `SingleResume` wrapper, ensuring callbacks triggered multiple times
  (Photos degraded image → original, HealthKit error callbacks) do not resume twice.
- Blocking or delegate-based operations such as location, network state, and contact enumeration live in helpers outside MainActor
  (`OneShotLocation`, `NetworkProbe`, `ContactsTool.fetch` use background queues).
- EventKit objects are not Sendable; reminders are converted to value-type `ReminderRow` in the callback before being returned.
- Image scaling uses `UIGraphicsImageRenderer`, keeping the longest edge within `max_size` (default 1280, maximum 2048).

## iOS limitations

- **No API to read SMS / iMessage / call history** (`MessageUI` can write, not read). `phone_messages_compose` only prefills the system sheet.
- **No API to read Notes, WeChat, or other apps' data**. The only routes are "screenshot + `phone_photo_get` to inspect the image" or the user manually pasting into the clipboard.
- **The app must be connected to the hub**: the hub can send `tool_call` only to a phone that has sent `register_phone`. In the foreground or just after entering the background, the WebSocket
  remains available to receive calls; when iOS suspends the iPhone app, the hub first sends a silent push to wake it in the background and waits up to 20 seconds for connection (`hub.go`, `wakePhone`). Only the most recently registered device is woken; if it was woken within 30 seconds, skip the push but still wait 20 seconds. Hub push configuration is required; Android is not woken.
  If waking fails, return an error asking the model to have the user open Persona and retry. The clipboard tools are never woken for: they answer that at once ("Foreground restrictions"). A call waits at most 2 minutes for the phone (a confirmed write 30 minutes, see "Confirmation flow"); timeout also returns an error.
- **Background location is unavailable**: only "While Using" permission is requested, so location fails outside the foreground.
- **Clipboard reads trigger a system prompt**: when reading content copied in another app, iOS asks "Allow Paste?" and the user must allow it.
- **Health data is aggregated by day only**: no individual-record queries; steps/energy are totals, heart rate is daily average/minimum/maximum, and sleep is daily time asleep
  (the `asleep` types, excluding "In Bed").
- **Photos access may be limited**: if the user grants only selected photos, `phone_photos_recent` returns only those.
- **Notifications can be sent only when the app has execution time**: delayed notifications are scheduled by the system and arrive on time, but the call itself must succeed, meaning the app is connected to the hub or can be woken.
- Returned content (including mail/photo metadata) enters Claude's context and is therefore given to the model for processing.
