// A recording's parts of its knowledge page (/kb/recordings/<id>), through the `kb.detail` slot (core/module-host.ts,
// core/ModuleKBDetail.tsx): Rename and Hand to Agent beside "Ask Persona", the latest hand-off first, then its facts,
// private mark, minutes (or an all-day recording's recap), live view, conversation, asks and photos after the entry's
// own facts, and its transcript after the code directories. docs/assistant/meeting-minutes.md, docs/assistant/day-recap.md.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ModuleKBDetailProps } from "../../core/module-host";
import { sessionTitle } from "../../lib/format";
import { conversationRoute } from "../../lib/inbox";
import { dayTitle, formatAt } from "../../lib/kb";
import { recordingView, type RecordingView } from "./recording-view";
import { machineWithHub } from "../../lib/mesh";
import { href, onLinkClick, type Route } from "../../lib/router";
import { useStore } from "../../lib/store";
import { Button } from "../../ui/atoms";
import { scrollBehavior } from "../../ui/stream/motion";
import { Card } from "../../views/knowledge/KBParts";
import { t } from "../../i18n";
import { hasMaterial, meetingRunning } from "./meeting-thread";
import { RecordingAsks } from "./RecordingAsks";
import { MeetingPrivate, RecordingConversation, useRecordingMeeting } from "./RecordingConversation";
import { RecordingDayRecap } from "./RecordingDayRecap";
import { RecordingHandoffDialog, RecordingHandoffRow, useRecordingHandoff, type MeetingHandoff } from "./RecordingHandoff";
import { RecordingLiveView } from "./RecordingLiveView";
import { RecordingMinutes } from "./RecordingMinutes";
import { DeleteSpeakersConfirmDialog, RecordingSpeakersDialog, useRecordingSpeakers } from "./RecordingSpeakersDialog";
import { RecordingTranscript } from "./RecordingTranscript";
import { RecordingPhotos } from "./RecordingPhotos";
import { namedBy, RecordingRenameDialog } from "./RecordingRename";

export function RecordingDetail({ kind, id, query, detail: d, entry: e, auth, current, manager, reload, onSaved, onOpen, scroller, children }: ModuleKBDetailProps) {
  const [renaming, setRenaming] = useState(false);
  // Hand to Agent (#8852): open, with the failed hand-off tried again when it came from its row.
  const [handing, setHanding] = useState<{ again?: MeetingHandoff } | null>(null);
  // Speakers (#17675): naming dialog and delete confirmation, closed whenever the opened recording changes.
  const [namingSpeakers, setNamingSpeakers] = useState(false);
  const [deletingSpeakers, setDeletingSpeakers] = useState(false);
  const [highlighted, setHighlighted] = useState("");
  const highlightTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(highlightTimer.current), []);
  useEffect(() => {
    setNamingSpeakers(false);
    setDeletingSpeakers(false);
  }, [id]);

  const jumpToTranscript = (lineId: string) => {
    const line = document.getElementById(lineId);
    const page = scroller.current;
    if (!line || !page?.contains(line)) return;
    // Only move the detail's scroller, not the Knowledge list or outer app shell.
    page.scrollTo({ top: page.scrollTop + line.getBoundingClientRect().top - page.getBoundingClientRect().top - page.clientHeight / 3, behavior: scrollBehavior() });
    line.focus({ preventScroll: true });
    clearTimeout(highlightTimer.current);
    setHighlighted(lineId);
    highlightTimer.current = setTimeout(() => setHighlighted(""), 2000);
  };

  const rec = useMemo(() => d ? recordingView(d.kind, d.meta, d.segments) : null, [d]);
  // The meeting conversation (#3862): its private mark, room and follow-up, on a hub that lists meeting_conversation;
  // its photos too where the hub lists meeting_photos (after a change of the photos the minutes may have lost a deleted
  // photo's items or be made again: read them, and again on the next poll, reconnect or event if that read fails).
  const reloadDetail = useCallback(() => reload(), [reload]);
  const meeting = useRecordingMeeting(d, rec, auth, current, manager, reloadDetail);
  // Hub-separated meeting speakers (#17674, #17675): local-first naming, merging, unmerging and clearing.
  const speakersCtrl = useRecordingSpeakers(d, auth, current, manager, reloadDetail);
  // Hand to Agent (#8852): a meeting or note that is over and has something to hand, for someone with an Agent to take
  // it; its latest hand-off shows at the top of the page whether or not another can be made now.
  const handoff = useRecordingHandoff(d?.meta);
  const canHand = !!d && !!rec && !rec.allDay && handoff.hasAgents && !meetingRunning(d.meta, meeting.room) && hasMaterial(d);

  return children({
    // An all-day recording's summary is its recap's, which the recap shows.
    hideProfile: !!(rec?.allDay && d?.recap),
    actions: (
      <>
        {/* A meeting or note (#8877): renamed here, kept over the phone's next upload. Not an all-day recording,
            named by its day. */}
        {kind === "recordings" && d && !rec?.allDay && (
          <Button tone="secondary" icon="pencil" onClick={() => setRenaming(true)}>
            {t("recording.rename")}
          </Button>
        )}
        {canHand && (
          <Button tone="secondary" icon="send" onClick={() => setHanding({})} testId="handoff-open">
            {t("recording-handoff.open_dialog")}
          </Button>
        )}
      </>
    ),
    lead: rec && handoff.latest && (
      <RecordingHandoffRow
        handoff={handoff.latest}
        onRetry={canHand ? () => setHanding({ again: handoff.latest ?? undefined }) : undefined}
      />
    ),
    sections: d && rec && (
      <>
        <RecordingFacts rec={rec} roomMeeting={meeting.roomMeeting} />
        <MeetingPrivate meeting={meeting} />
        {/* An all-day recording (#4741) has a recap instead of minutes. */}
        {rec.allDay ? <RecordingDayRecap
          detail={d}
          rec={rec}
          auth={auth}
          current={current}
          refresh={reload}
          onSaved={onSaved}
          onJump={jumpToTranscript}
          onOpen={onOpen}
        /> : <RecordingMinutes
          detail={d}
          rec={rec}
          speakers={speakersCtrl.speakers}
          auth={auth}
          current={current}
          refresh={reload}
          onSaved={onSaved}
          onJump={jumpToTranscript}
          highlighted={highlighted}
          photos={meeting.photos}
        />}
        {/* While a room meeting records (phase 2): its topics and newest live lines, above its conversation. */}
        <RecordingLiveView rec={rec} meeting={meeting} />
        <RecordingConversation detail={d} rec={rec} meeting={meeting} auth={auth} current={current} manager={manager} />
        {/* A room meeting's questions are in its thread; a #2981 meeting's stay here. */}
        {!meeting.roomMeeting && <RecordingAsks detail={d} rec={rec} auth={auth} current={current} />}
        <RecordingPhotos rec={rec} photos={meeting.photos} highlighted={highlighted} />
      </>
    ),
    tail: rec && (
      <RecordingTranscript
        key={JSON.stringify([id, rec.trimmedAt])}
        rec={rec}
        recordingId={id}
        query={query}
        highlighted={highlighted}
        onJump={jumpToTranscript}
        speakers={speakersCtrl.speakers}
        speakersBusy={speakersCtrl.busy}
        onOpenSpeakers={() => {
          speakersCtrl.clearError();
          setNamingSpeakers(true);
        }}
        onDeleteSpeakers={() => setDeletingSpeakers(true)}
      />
    ),
    dialogs: (
      <>
        {handing && d && rec && (
          <RecordingHandoffDialog
            id={d.id}
            hasTranscript={!rec.trimmedAt && rec.lines.some((l) => !l.bookmark)}
            auth={auth}
            again={handing.again}
            onClose={() => setHanding(null)}
            onDone={(h) => {
              handoff.setMade(h);
              void reload().catch(() => {});
            }}
          />
        )}
        {renaming && d && (
          <RecordingRenameDialog
            id={id}
            name={e.name}
            source={namedBy(d.meta)}
            auth={auth}
            onClose={() => setRenaming(false)}
            onDone={() => {
              void reload().catch(() => {});
              onSaved();
            }}
          />
        )}
        {namingSpeakers && d && rec && speakersCtrl.speakers?.state === "ready" && (
          <RecordingSpeakersDialog
            open={namingSpeakers}
            onClose={() => setNamingSpeakers(false)}
            speakers={speakersCtrl.speakers}
            detail={d}
            rec={rec}
            busy={speakersCtrl.busy}
            lastUndo={speakersCtrl.lastUndo}
            error={speakersCtrl.error}
            onEdit={speakersCtrl.edit}
            onUndo={speakersCtrl.undo}
            onAskDelete={() => {
              setNamingSpeakers(false);
              setDeletingSpeakers(true);
            }}
            onJumpSample={(lineId) => {
              jumpToTranscript(lineId);
              setNamingSpeakers(false);
            }}
          />
        )}
        {deletingSpeakers && d && (
          <DeleteSpeakersConfirmDialog
            open={deletingSpeakers}
            busy={speakersCtrl.busy}
            onClose={() => setDeletingSpeakers(false)}
            onConfirm={() => {
              void speakersCtrl.clear().then((ok) => {
                if (ok) {
                  setDeletingSpeakers(false);
                  setNamingSpeakers(false);
                }
              });
            }}
          />
        )}
      </>
    ),
  });
}

/** A recording's when, how long, where it was made; and whether its transcript is still coming. A room meeting (#3862) is
 *  attended by its own room, which its Conversation section shows, not by a session named here. */
function RecordingFacts({ rec, roomMeeting }: { rec: RecordingView; roomMeeting: boolean }) {
  const rows: [string, string][] = [];
  // An all-day recording says so, with its day (and part, past the first), and its times on its own clock.
  if (rec.allDay) {
    const date = rec.allDay.day ? dayTitle(rec.allDay.day) : "";
    rows.push([t("recording.recording_all_day"), rec.allDay.part > 1 ? t("recording.recording_day_part", { date, part: rec.allDay.part }) : date]);
  }
  if (rec.startedAt) rows.push([t("recording.recording_started"), formatAt(rec.startedAt, undefined, rec.tz)]);
  if (rec.duration) rows.push([t("recording.recording_duration"), rec.duration]);
  rows.push([t("recording.recording_device"), rec.device]);
  if (rec.language) rows.push([t("recording.recording_language"), rec.language]);
  if (rec.attendance) rows.push([t("recording.recording_persona"), t(rec.attendance === "participant" ? "recording.recording_participant" : "recording.recording_listener")]);
  return (
    <section>
      <Card>
        {rows.map(([k, v]) => (
          <div key={k} className="flex items-center gap-3 px-4 py-2.5 text-[13px]">
            <span className="flex-1 text-ink-2">{k}</span>
            <span className="whitespace-nowrap text-ink tabular-nums">{v}</span>
          </div>
        ))}
        {rec.attendance && !roomMeeting && (
          <div className="flex items-center gap-3 px-4 py-2.5 text-[13px]">
            <span className="flex-1 whitespace-nowrap text-ink-2">{t("recording.recording_attending")}</span>
            <AttendingSession session={rec.attendingSession} />
          </div>
        )}
      </Card>
      {rec.partial && <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("recording.recording_partial")}</p>}
    </section>
  );
}

/** The session attending a meeting, opening its chat: a Persona conversation, an Agent, or the main chat when none was
 *  named (docs/assistant/meeting-minutes.md "Attendance fields"). */
function AttendingSession({ session }: { session?: RecordingView["attendingSession"] }) {
  const sessions = useStore((s) => s.sessions);
  const assistant = useStore((s) => s.assistant);
  const machines = useStore((s) => s.machines);
  let to: Route | null = null;
  let label = t("recording.recording_main_chat");
  if (session) {
    const { machineId: mid, sessionId: sid } = session;
    const row = (sessions[mid] ?? []).find((x) => x.session_id === sid)
      ?? (assistant?.machine_id === mid ? assistant.conversations.find((c) => c.session_id === sid) : undefined);
    to = conversationRoute(mid, sid, sessions, assistant);
    label = row ? sessionTitle(row) : t("recording.recording_attending_on", { machine: machineWithHub(mid, machines) });
  } else if (assistant?.main) {
    to = { name: "persona", sid: assistant.main };
  }
  if (!to) return <span className="min-w-0 truncate text-ink">{label}</span>;
  const route = to;
  return (
    <a href={href(route)} onClick={(ev) => onLinkClick(ev, route)} title={label} className="min-w-0 truncate font-medium text-accent-ink hover:underline">
      {label}
    </a>
  );
}
