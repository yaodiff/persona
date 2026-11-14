# Home Screen and Lock Screen widgets (#5664)

Persona's own widgets for iPhone and iPad. Each widget does one job, in the manner of the system's own (Calendar,
Reminders, Fitness, Batteries). The persona chosen in **Settings › Appearance › Avatar** is only an accent. The Live
Activity, the Control Center controls and the extension widget are documented elsewhere (`docs/apple/live-activity.md`,
`docs/apple/intents.md`, `docs/extensions/extensions.md`). The Mac app is frozen and gets none of these widgets. The
watch has its own complications (`docs/apple/persona.md`, "The Milk Frog on the watch").

**Who it serves.** Everyday people first: **Calendar**, **To-dos**, **Record**, **Health** and **Inbox** need no
knowledge of Agents and use plain words. **Agents**, **Usage** and **Computers** serve people who work with Agents.
They show titles and state words, never engine names, session IDs, paths or model IDs (DESIGN_SYSTEM.md 5.6).

## The widgets

Each widget has one hero (a number, a ring, a chart, a button) and one color that says which widget it is.

| Widget | Its color | Small | Medium | Large | Lock Screen | Edit Widget |
| --- | --- | --- | --- | --- | --- | --- |
| **Agents** | Needs You's blue (the lead state's) | how many need you, big; the other counts in a line | + the one waiting longest | the three counts side by side, then five agents | circular (count, or the face), rectangular, inline | Show: All Agents · Agents on Your Computers · Persona's Tasks; Computer |
| **Calendar** | the icon's coral | weekday, the day big, the next event | Today, Week or Month (the switch at its top right): the date and the next events; the week with a dot under each day that has events, the chosen day's events under it; the month with its dots, the chosen day's beside it | Today: the rest of the day as a day view, a red line at now; Week: the week, then the days from the chosen one on; Month: the month, then the chosen day's events | circular (the date), rectangular, inline | View |
| **To-dos** | orange | the list's disc and how many, "1 overdue" | + four to-dos to tick off | eight to-dos to tick off | circular, rectangular, inline | none |
| **Usage** | by how full: green, orange ≥ 80 %, red ≥ 95 % | the fullest window as a ring | each account as two rings, one inside the other | today's tokens, money spent, tokens by the hour, the accounts' rings | circular gauge, rectangular, inline | Account |
| **Computers** | green; a computer's agents color its ring | four rings, a computer each | four rings with names and states | how many online, then each computer with its load | circular, rectangular, inline | Computer |
| **Record** | the system's recording red | one big button | the button and what the recording is named | — (one action does not fill it) | circular and rectangular buttons | none |
| **Health** | pink | today's steps, big, against the 7-day average | + sleep and resting heart rate | + the week's steps as bars | circular gauge, rectangular, inline | none |
| **Inbox** | Needs You's blue | how many need you, big, and who sent the newest | + the newest message | the five newest | circular, rectangular, inline | none |

A small widget never holds a list; lists start at medium. The Calendar widget was called Today until it had a week and a
month (#6946); its kind stays `persona.home.today`, so the ones already placed keep their place. A new widget needs no
editing: every default works on a fresh install (principle 1). Edit Widget holds every choice and changes it back.
Nothing about the widgets lives in the app's Settings (principles 2 and 3).

## The look

- **Surface**: the system widgets' white and their dark gray (`HomeWidgetTokens.surface`). No card inside a card.
  Words use the system's primary, secondary and tertiary levels.
- **Name line**: the widget's name at the top left in its color (as Calendar's weekday), the persona at the top right.
- **Hero**: big rounded numerals (`HomeWidgetTokens.hero…`), rings with a track (`HomeRing`), bar charts. Words are
  text styles and follow the text size. Widget sizes are the system's, so heroes, rings and faces are fixed sizes, the
  documented exception in DESIGN_SYSTEM.md 2.2, like `IslandMetrics` and `WatchFaceTokens`.
- **Tinted and clear (`accented`) and the Lock Screen (`vibrant`)**: the system paints every opaque pixel in one
  color, two with the accent group. Every colored part (heroes, rings, the event bars, the Record button, the To-dos
  disc) goes in the accent group (`homeTone` / `homeAccent()`). Marks on a filled disc are knocked out of it, so they
  still read in one color. Mochi becomes a line drawing (`MochiGlyph`), and a package's picture is shown desaturated.
- **StandBy**: the system removes the background and scales a small widget up. Every widget reads on black, and at
  night the system draws it in red.

## Calendar: Today, Week and Month

The Calendar widget (#6946) shows today's page, the week or the month on the medium and large sizes; the small one is
today's page alone, since a switch, a week or a month grid do not fit beside each other there, and a small widget never
holds a list.

- **Today** is the page it always had: the date, the next events (medium), the rest of the day as a day view (large).
- **Week**: the week that holds the chosen day, from the phone's first weekday, as a strip of seven days: the
  weekday's letter, the number, a dot under a day that has events. Under it the chosen day's events (medium), or that
  day and the days after it that have events, for a week, as many as fit (large).
- **Month**: the month as the calendar draws it, a dot under each day that has events; the chosen day's events under
  the grid (large) or beside it (medium, a smaller grid).
- Today is in the widget's coral: a coral disc when it is the chosen day, its number in coral when another is. A chosen
  day is a filled disc (black in light, white in dark), as the phone's Calendar marks them. Days gone by and their dots
  step back. Today's list is the rest of the day; another day's is the whole day, with "N more" when it has more.
- **The switch** (Today · Week · Month) sits at the top right, beside the persona: a segmented control drawn in the
  widget's tokens (`HomeCalendarSwitch`). Each part is a widget button (`ShowCalendarViewIntent`): a tap changes only
  this widget and redraws it at once, without opening the app or reaching the network. The view stays where it was left.
- **A day** in the week or the month is a button too (`PickCalendarDayIntent`): it picks that day in the widget and
  lists its events, without the app (Persona has no calendar page of its own, and the To-dos calendar shows to-dos, not
  the phone's events). A tap on today, or on the day already picked, goes back to today. A picked day goes back to
  today by itself after 10 minutes (`HomeCalendarState.pickLasts`) and at midnight, so the next glance shows today.
  The rest of the widget opens the To-dos page, as before.
- **Edit Widget › View** (Today, Week, Month) sets the view a widget starts on; the default is Today, so the widgets
  placed before the switch existed still show today's page.
- **Two widgets alike**: WidgetKit gives a widget no identity of its own. What the switch and a tapped day leave is kept
  in the App Group (`Widgets/calendar.json`, `HomeCalendarState`) under the widget's size and Edit Widget › View. Two
  sizes always keep their own; two of the same size and View switch together, and giving one of them another View in
  Edit Widget keeps them apart.
- **Why a day lists nothing** (`HomeDayNone`): no calendar access ("Allow Calendars in Persona to see your events"),
  nothing left today, only today known (the hub's meetings: "Allow Calendars in Persona to see other days"), a day
  the snapshot does not cover yet ("Open Persona to see this day"), or a free day ("No events"). A day the snapshot
  does not cover has no dot: an unknown day is never shown as a free one in its list.
- **Sizes**: the switch's words, the grids' numbers and letters are fixed sizes (`HomeWidgetTokens.segment…`,
  `week…`, `month…`), as the heroes are; the events' words follow the text size, and fewer of them fit.
- **The timeline** has at most four entries: now, when a picked day goes back to today, midnight (the date and the grids
  move on without the app; the snapshot already holds the next days, so the new day's events show too), and when the
  snapshot turns stale.

## Lock Screen

Every widget but Record comes in the Lock Screen's three families; Record in the circle and the rectangle (one action).

- **Circular**: the hero alone. Agents: how many need you (the face when none). Calendar: the weekday and the day. To-dos,
  Inbox: the count under the widget's mark. Usage: the fullest window as a gauge. Computers: online of all. Health: a
  gauge of the steps against the average. Record: the button.
- **Rectangular**: the widget's line in its accent ("2 need you", the next event, "6 to-dos today", "3 of 4 online")
  and one or two lines under it (the agent waiting longest, the event after, two to-dos, the windows, two computers,
  last night's sleep, the newest message). Agents keeps the face beside it, Record its button.
- **Inline**, beside the clock: one line after the widget's mark.

The system draws them in one color (`vibrant`): every part is in the accent group or a level of the primary color.
Before pairing a Lock Screen widget shows its mark and "Open Persona to connect"; when the snapshot is an hour old its
content steps back, as on the Home Screen. Inbox and Health stay `privacySensitive`.

## Edit Widget

Four widgets have choices, on the widget itself (touch and hold, Edit Widget); nothing about the widgets is in the
app's Settings (principles 2 and 3). Every choice starts at a default that works on a fresh install.

| Widget | Choice | Default | The others |
| --- | --- | --- | --- |
| Agents | Show | All Agents | Agents on Your Computers (the coding agents), Persona's Tasks |
| Agents, Computers | Computer | Any Computer | each computer in the snapshot |
| Calendar | View | Today | Week, Month: where the widget starts; its switch changes it from there |
| Usage | Account | The Fullest | each subscription account in the snapshot (up to 8) |

- The lists come from the snapshot the app last wrote (`HomeChoices`), so Edit Widget never calls the hub and names
  everything as the widgets do. The choices are `HomeWidgetEdit` intents whose entities (`HomeComputerEntity`,
  `HomeAccountEntity`) keep the computer's or the account's id; the empty id is the default.
- A narrowed widget counts only what it shows: the Agents counts, the corner's mood and the Smart Stack's score are its
  rows' (the rows past the snapshot's cap count only with no choice). Usage's tokens stay every computer's. A computer
  or an account that is gone leaves the widget's empty state until another is chosen.
- A widget placed before Edit Widget existed keeps its kind and takes the defaults.

## The persona, as an accent

The persona never takes the hero's place. It appears in three places:

- **The corner**: a 22 pt face at the top right (`HomeWidgetTokens.faceCorner`) whose mood says how this widget's
  subject is. Agents: needs you, working, done (an agent finished, unseen). Calendar: working when an event starts within
  15 minutes. To-dos: needs you when one is overdue, done when all are done. Usage: working over 80 %, needs you over
  95 %. Computers: what their agents do, offline when all are. Health: done once the steps pass the average. Inbox:
  needs you when a message does.
- **Empty states**: the face, calm, and one plain line ("Nothing needs you", "All done for today").
- **Before pairing**: the face and "Open Persona to connect".

| Mood | Mochi | A package (Milk Frog, uploads) |
| --- | --- | --- |
| Needs you | the needs-you ring | its `needs_you` frame |
| Working | the working arc, eyes aside | its `working` frame |
| Done | idle with the green check (happy eyes on the line drawing) | `idle` frame + the check |
| Thinking | eyes up, the thought bubble | its `thinking` frame |
| Idle | as the icon | its `idle` frame |
| Offline | gray, eyes closed: the snapshot is over an hour old | its `offline` frame |

Mochi is drawn in code (`PersonaFace` in full color, `MochiGlyph` otherwise). A package's pictures are written by the
app into the App Group at the widget sizes, as the Live Activity's are.

## What each number counts

The same definition as the app, from the same code (principle 4):

| Widget | Number | Definition | Source in the app |
| --- | --- | --- | --- |
| Agents | Needs You | sessions that wait for you, each once (DESIGN_SYSTEM.md 4.8, `AgentsNeedsYou`, the Agents tab's badge) | `AppModel` sessions, the rule of `TabBadges` |
| Agents | Working, Just Finished | the Agents list's states (finished = unread) | the same |
| Calendar | events | the rest of today's events: the phone's calendar when Persona already has full access (`MeetingCalendar`'s); else the hub's Today meetings; else none, and the widget says to allow Calendars in Persona. The widget never asks itself | EventKit / `TodayBody.meetings` |
| Calendar | a day's dot, its events | the phone's calendar only, the same events as today's (declined and untitled ones left out): every event on that day, all-day ones first, an event over several days on each; today's dot also counts the ones already over | EventKit (`HomeDays.build`) |
| To-dos | the count | the To-dos page's Today filter: due today, plus open overdue ones | `TodoStore`, `TodoListLogic` |
| Usage | a window's percent, reset time | `GET /api/v1/usage/windows`, the Machines page's numbers | `UsageWindowsStore` |
| Usage | tokens today, by the hour | the island's Tokens Today; the hours from `GET /api/v1/usage`'s `hourly` (#5721) | `FleetState.stats`, the usage summary |
| Usage | money spent today | only what a gateway reports: the `cost_usd` of the quota report's usage entries whose `period` is `day`, over the launchers with a gateway, a key two launchers share counted once (`HomeUsage.spentToday`). Never estimated from prices, never an entry known only by its label. With no gateway saying, the widget shows no money | launcher quotas (`GET /api/v1/launchers/{name}/quota`) |
| Computers | online, load | `Machine.online`; load from `FleetState.stats.loads` | `AppModel.machines` |
| Health | steps, sleep, resting heart rate | Health's own numbers, with the user's permission | `HealthTool.daily` / `.summary` |
| Inbox | Need You, Unread | open items the triage marks as yours (`InboxStore.actionCount`); the Inbox badge's number | `InboxStore` |

**Private content**: Inbox and Health are `privacySensitive`. While the phone is locked the system blurs them on the
Lock Screen, in StandBy and in the Smart Stack.

## Ticking off a to-do

The box beside a to-do is a button (`CompleteTodoIntent`). The intent runs in the app, never in the extension, as the
Live Activity's buttons do. The app marks the to-do `ticking` in the snapshot and reloads the widget, so it shows
done at once (principle 7). Then it tells the hub (the To-dos page's own call). On success the to-do leaves the list.
On a refusal or no answer it becomes `failed`: a red mark and "Couldn't complete — tap to try again", and a tap tries
again. Nothing fails silently. On the Lock Screen the intent asks for Face ID first.

## Data: the snapshot

The widget extension never talks to the hub and never reads the keychain (it cannot on a phone,
`docs/apple/live-activity.md`, "Why the avatar pack was still wrong in build 103"). The app writes one file into the
App Group, and the widgets draw only that:

```
Widgets/snapshot.json        HomeSnapshot: counts, the top rows, events and days, to-dos, windows, computers, health, inbox
Widgets/calendar.json        HomeCalendarState: the Calendar widgets' views and picked days, written by their buttons
Widgets/faces/<id>_<rev>/    the chosen package's pictures, one per state, at the widget sizes (as Fleet/faces/)
```

- **What it holds** (`apple/Shared/HomeWidgets/HomeSnapshot.swift`): only what a widget draws. Up to 40 agent rows
  (the rest counted), today's events, the days around today (below), 8 to-dos, the accounts, 8 computers, 5 messages
  and a week of steps. About 6 KB, with the days up to about 20 KB. Times are Unix seconds. The app and its widgets
  ship together: a file of another shape (`HomeSnapshot.version`, written before an update) reads as none until the
  app writes again. A new optional field (the days) reads as absent from an older file and needs no new version, so
  the widgets keep their content across the update.
- **When the app writes it**: on every change a widget shows, coalesced to at most one write every 5 seconds (P7);
  when it goes to the background; at least every 20 minutes while it runs (`HomeWidgetWriter.heartbeat`); and when the
  hub's widget push wakes it (#5721). That push is sent within seconds when Needs You changes; other changes are coalesced to at most one
  every 15 minutes per device. The app writes only when the bytes changed, then reloads only the kinds whose part
  changed (`WidgetCenter.reloadTimelines(ofKind:)`): WidgetKit budgets reloads. The writer is `HomeWidgetWriter`; what
  the watch's summary follows asks it for a write (`AppModel.summariesChanged`), and a recording that starts or stops
  writes at once (`RecordingActivity`).
- **Waking the app** (#5721): from the foreground the app tells the hub whether any of the widgets is on a screen
  (`widgets` in its device prefs, `HomeWidgetWriter.tellHub`); the hub's silent `persona_wake: "widgets"` push then
  connects it for 25 seconds, and it writes before it lets go.
- **The days** (`HomeSnapshot.Today.days`, `HomeDays`): every day of this month's grid and the 14 days after today,
  whichever ends later (at most about seven weeks), each with how many events it has and its first four (titles cut
  to 60 characters); days without events are left out, and the range is written, so a day outside it reads as not
  known rather than free. Only from the phone's calendar: the hub's meetings are today's alone.
- **What the app reads itself** (`HomeWidgetData`): the calendar's events over those days, in one query (EventKit, off
  the main actor, only with the access Persona already has; today's rest and the days come from the same read), Health's
  numbers (`HealthTool.daily`: steps for 8 days, sleep, resting heart rate, active energy), the subscription windows
  (`UsageWindowsStore`), today's tokens by the hour (`GET /api/v1/usage`'s `hourly`) and today's money at the gateways
  (each gateway launcher's quota, at most every 15 minutes, never sooner when the hub wakes the app; a money read on
  another day is not shown). Each is read only while a widget that shows it is on a screen (`WidgetCenter`'s
  configurations, from the foreground), at most every 10, 15 and 5 minutes, and again when the hub wakes the app; a read
  that fails (Health is locked with the phone) keeps the last.
- **Off the main thread** (P1): the file is written on a background task, protected until first unlock as
  `FleetLookStore`'s; only encoding the few kilobytes runs on the main actor, at most every 5 seconds.
- **Stale**: the timeline adds an entry at `HomeSnapshot.staleAfter` (an hour after the write). From then on the
  content steps back (`staleOpacity`), the face is offline and the widget says "Updated … ago" (principle 7).

## Taps

A widget opens its page (`persona://widget/<kind>`): the Agents list, To-dos, Machines, Recordings, Health, Inbox;
Calendar opens To-dos too (Persona has no calendar page of its own; its switch and its days are buttons that change only
the widget), and Usage opens Machines, where the windows are. Agents opens the most urgent agent's chat
(`persona://open?machine=…&session=…`, the Live Activity's link). Each row of the large Agents widget opens its own
agent. The Record button runs the Record control's intent without opening the app, and the recording's Live Activity
confirms it at once.

## Smart Stack

Each entry carries a `TimelineEntryRelevance` (`HomeRelevance`), scored on what the widget shows after its Edit Widget choices: highest when something needs you (agents, messages, an overdue to-do)
or a recording is running, high when a meeting starts within 15 minutes, low otherwise.

## Screenshots

The gallery draws every widget at its Home Screen sizes in light and dark with made-up data (`HomeSamples`):
`-showHomeWidgets YES` in a Debug build. A view outside a widget gets no container background, family, margins or tint
from the system, so the gallery supplies stand-ins for them. For the Home Screen itself, `-showHomeWidgetSample YES`
writes the same made-up snapshot instead of the app's, so the widgets added to a simulator's Home Screen show it
without a hub (`-showHomeWidgetSample ticked`: two to-dos just ticked, one on its way and one the hub refused); those
shots, on each PR, are the proof (both DEBUG launch arguments, in `release.sh`'s list). `-showHomeWidgets calendar:<page>`
draws the Calendar widget's views a page at a time (`medium`, `large`, `large2`, `more`; an iPad's screen holds one).

## Phases

1. **The snapshot and the first widgets**: `HomeSnapshot` and its writer in the app, the package pictures at the
   widgets' sizes (the Live Activity's faces, `FleetFaces.widgetSizes`), the Agents, Inbox and Record widgets (Home
   Screen), the gallery, `HomeWidgetTests` (Needs You replayed from `spec/fixtures/needs-you/cases.json` against the
   Agents badge, rows past the cap, the file read back only in its shape, which kinds reload, writes only on change,
   moods).
2. **The rest of the Home Screen**: Today, To-dos (with ticking off, `CompleteTodoIntent`, `HomeWidgetActions`), Usage,
   Computers, Health; their data (`HomeWidgetData`, `HomeToday`, `HomeTodos`, `HomeUsage`); taps to the To-dos,
   Machines and Health pages.
3. **Lock Screen, Edit Widget and the Smart Stack**: the accessory families (with the states before pairing and when
   stale), Edit Widget (`HomeWidgetEdit`, entities for computers and accounts read from the snapshot), relevance
   (`HomeRelevance`) with its tests. The hub's push (#5721) and the app's refresh on it came with part 2.
4. **The gateways' money for Usage**: an optional `period` on a quota report's usage entries (`day`, `week`, `month`;
   `server/internal/quota`), and the widget's money as the sum of the `day` entries' `cost_usd` over the gateway
   launchers (`HomeUsage.spentToday`), read at most every 15 minutes.
5. **The Calendar widget's week and month** (#6946): Today renamed Calendar; the switch and the day buttons
   (`ShowCalendarViewIntent`, `PickCalendarDayIntent`, `HomeCalendarState`), Edit Widget › View, the days in the
   snapshot (`HomeDays`), the midnight entry, `HomeCalendarWidgetTests`.
