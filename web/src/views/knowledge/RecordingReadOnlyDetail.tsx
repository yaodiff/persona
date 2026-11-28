// The default Knowledge panel's read-only view of a recording that already exists. It stays available when the
// recording module is off; the enabled module supplies the full interactive detail instead. Only Core read endpoints
// are used here, and this file never imports the recording module.
import { useEffect, useMemo, useState } from "react";
import type { ModuleKBDetailProps } from "../../core/module-host";
import { t } from "../../i18n";
import { api, apiBlob, enc, isAbort } from "../../lib/api";
import { knownZone, recordingPaused, wallClock, type KBSegment, type RecordingPhoto } from "../../lib/kb";
import {
  decodeSpeakers,
  groupLinesBySpeaker,
  hasVisibleSpeakers,
  speakerAvatarGlyph,
  speakerLabel,
  waitingSpeakerMinutes,
  type DetailSpeakers,
  type SpeakerFailReason,
} from "../../lib/recording-speakers";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { Card, Marked, SectionTitle } from "./KBParts";

type Props = ModuleKBDetailProps;
type Auth = Props["auth"];

/** Stored content only: no record, upload, rename, transcription, regeneration, live view, speaker edit or handoff actions. */
export function RecordingReadOnlyDetail({ detail: d, query, auth, current, children }: Props) {
  const paused = d ? recordingPaused(d.paused) : null;
  const speakers = useMemo(() => decodeSpeakers(d?.speakers), [d?.speakers]);
  return <>{children({
    hideProfile: !!d?.recap,
    lead: paused === "module_off" ? (
      <p className="flex items-start gap-2 rounded-control bg-inset px-3 py-2 text-label leading-relaxed text-ink-2 shadow-hairline" role="note" data-recording-paused="module_off">
        <Icon name="info" size={13} className="mt-0.5 shrink-0 text-ink-3" />
        <span className="min-w-0">{t("knowledge-kbdetail.recording_paused_module_off")}</span>
      </p>
    ) : undefined,
    sections: d && (
      <>
        <StoredMarkdown title={t("knowledge-kbdetail.recording_minutes")} markdown={d.minutes?.markdown} />
        <StoredMarkdown title={t("knowledge-kbdetail.recording_day_recap")} markdown={d.recap?.markdown} />
        <StoredPhotos id={d.id} kind={d.meta?.kind} auth={auth} current={current} />
      </>
    ),
    tail: d && <Transcript meta={d.meta} segments={d.segments} speakers={speakers} query={query} />,
  })}</>;
}

function StoredMarkdown({ title, markdown }: { title: string; markdown?: string }) {
  if (!markdown?.trim()) return null;
  return (
    <section>
      <SectionTitle title={title} />
      <Card>
        <div className="px-4 py-3.5">
          <Markdown text={markdown} small />
        </div>
      </Card>
    </section>
  );
}

function readOnlyFailText(reason: SpeakerFailReason): string {
  switch (reason) {
    case "too_long":
      return t("knowledge-kbdetail.recording_speakers_failed_too_long");
    case "timeout":
      return t("knowledge-kbdetail.recording_speakers_failed_timeout");
    case "engine_unavailable":
      return t("knowledge-kbdetail.recording_speakers_failed_engine_unavailable");
    case "no_audio":
      return t("knowledge-kbdetail.recording_speakers_failed_no_audio");
    case "gave_up":
    case "audio_gone":
    case "error":
      return t("knowledge-kbdetail.recording_speakers_failed_other");
    case "unknown":
      return "";
  }
}

/** Read-only speaker summary/status strip (zero edit buttons) shown when the recording module is off. */
function ReadOnlySpeakersBar({ speakers }: { speakers: DetailSpeakers }) {
  if (speakers.state === "waiting") {
    const progress = waitingSpeakerMinutes(speakers.done, speakers.end);
    const label = !progress
      ? t("knowledge-kbdetail.recording_speakers_waiting")
      : progress.total !== undefined
        ? t("knowledge-kbdetail.recording_speakers_waiting_progress", { done: progress.done, total: progress.total })
        : t("knowledge-kbdetail.recording_speakers_waiting_done", { done: progress.done });
    return (
      <div role="status" data-readonly-speakers="waiting" className="flex items-start gap-2 px-4 py-2.5 text-label leading-relaxed text-ink-2">
        <Icon name="info" size={13} className="mt-0.5 shrink-0 text-ink-3" />
        <span className="min-w-0 break-words">{label}</span>
      </div>
    );
  }
  if (speakers.state === "failed") {
    const msg = readOnlyFailText(speakers.reason);
    if (!msg) return null;
    return (
      <div role="status" data-readonly-speakers="failed" className="flex items-start gap-2 px-4 py-2.5 text-label leading-relaxed text-ink-2">
        <Icon name="info" size={13} className="mt-0.5 shrink-0 text-ink-3" />
        <span className="min-w-0 break-words">{msg}</span>
      </div>
    );
  }
  const formatNum = (n: number) => t("knowledge-kbdetail.recording_speaker_n", { n });
  const unmerged = speakers.people.filter((p) => !p.merged_into);
  const main = unmerged.filter((p) => !p.minor);
  const listed = main.length ? main : unmerged;
  const names = listed.map((p) => speakerLabel(p, formatNum)).filter(Boolean);
  return (
    <div data-readonly-speakers="ready" data-speakers-quality={speakers.quality}>
      {names.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 px-4 py-2.5">
          <Icon name="users" size={14} className="shrink-0 text-ink-3" />
          <span className="shrink-0 whitespace-nowrap text-meta font-medium text-ink">
            {t("knowledge-kbdetail.recording_speakers")}
          </span>
          <span className="min-w-0 flex-1 break-words text-label text-ink-2">
            {names.join(" · ")}
          </span>
        </div>
      )}
      {speakers.quality === "low" && (
        <p role="note" data-speakers-low-quality className="flex items-start gap-2 border-t border-line bg-inset px-4 py-2 text-label leading-relaxed text-ink-2">
          <Icon name="info" size={13} className="mt-0.5 shrink-0 text-ink-3" />
          <span className="min-w-0 break-words">{t("knowledge-kbdetail.recording_speakers_low_quality")}</span>
        </p>
      )}
    </div>
  );
}

/** The transcript already stored on the hub, including its saved clock times and any stored speakers; no live lines
 *  are read or added and no edit controls are shown. */
function Transcript({ meta, segments, speakers, query }: { meta?: Record<string, unknown> | null; segments?: KBSegment[] | null; speakers: DetailSpeakers | null; query?: string }) {
  const lines = (segments ?? []).filter((s) => s?.echo !== true && typeof s?.text === "string" && s.text.trim());
  const visibleSpeakers = hasVisibleSpeakers(speakers) ? speakers : null;
  if (!lines.length && !visibleSpeakers) return null;
  const formatNum = (n: number) => t("knowledge-kbdetail.recording_speaker_n", { n });
  const rows = lines.map((line, i) => ({
    key: i,
    start: line.start,
    text: line.text.trim(),
    speaker: line.speaker,
    speakerUnsure: line.speaker_unsure,
  }));
  const grouped = visibleSpeakers?.state === "ready" && rows.length > 0
    ? (() => {
        const g = groupLinesBySpeaker(rows, visibleSpeakers);
        return g.some((part) => part.kind === "speaker") ? g : null;
      })()
    : null;

  const renderLine = (line: typeof rows[number]) => (
    <li key={line.key} className="flex gap-3 py-1.5 text-meta leading-relaxed">
      <span className="w-14 shrink-0 pt-px text-ink-3 tabular-nums">{transcriptClock(meta, line.start)}</span>
      <span className="min-w-0 flex-1 break-words text-ink select-text">{query ? <Marked text={line.text} query={query} /> : line.text}</span>
    </li>
  );

  return (
    <section>
      <SectionTitle title={t("knowledge-kbdetail.recording_transcript")} count={lines.length || undefined} />
      <Card>
        {visibleSpeakers && <ReadOnlySpeakersBar speakers={visibleSpeakers} />}
        {grouped ? (
          <div data-readonly-transcript-speakers className="space-y-2 px-4 py-2">
            {grouped.map((group, gi) =>
              group.kind === "speaker" ? (
                <div
                  key={`${group.speakerId}-${gi}`}
                  data-speaker-group={group.speakerId}
                  data-speaker-unsure={group.unsure ? "true" : undefined}
                  className="pt-1 first:pt-0"
                >
                  <div className="mb-0.5 flex items-center gap-2 text-label font-medium text-ink-2">
                    <span
                      aria-hidden="true"
                      data-speaker-avatar
                      className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-field text-submeta font-semibold text-ink-2 select-none"
                    >
                      {speakerAvatarGlyph(group.person ?? { id: group.speakerId })}
                    </span>
                    <span className="min-w-0 truncate text-ink">
                      {speakerLabel(group.person ?? { id: group.speakerId }, formatNum)}
                    </span>
                    {group.unsure && (
                      <span
                        data-speaker-unsure-mark
                        role="img"
                        aria-label={t("knowledge-kbdetail.recording_speaker_possibly")}
                        title={t("knowledge-kbdetail.recording_speaker_possibly")}
                        className="shrink-0 font-normal text-ink-3"
                      >
                        ?
                      </span>
                    )}
                  </div>
                  <ol>{group.rows.map(renderLine)}</ol>
                </div>
              ) : (
                <ol key={`plain-${gi}`}>{group.rows.map(renderLine)}</ol>
              ),
            )}
          </div>
        ) : lines.length > 0 ? (
          <ol className="px-4 py-1.5">{rows.map(renderLine)}</ol>
        ) : null}
      </Card>
    </section>
  );
}

function transcriptClock(meta: Record<string, unknown> | null | undefined, seconds: number): string {
  const started = typeof meta?.started_at === "string" ? meta.started_at : "";
  const start = Date.parse(started);
  if (!Number.isFinite(start)) return offsetClock(seconds);
  const zone = knownZone(meta?.tz) ? meta.tz : undefined;
  const c = wallClock(new Date(start + seconds * 1000), zone);
  return [c.h, c.m, c.s].map((n) => String(n).padStart(2, "0")).join(":");
}

function offsetClock(seconds: number): string {
  const whole = Math.max(0, Math.floor(seconds));
  const h = Math.floor(whole / 3600);
  const m = Math.floor((whole % 3600) / 60);
  const s = String(whole % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

/** A missing photo list (including a hub that has not moved this read into Core yet) hides only this section. */
function StoredPhotos({ id, kind, auth, current }: { id: string; kind: unknown; auth: Auth; current: () => boolean }) {
  const [state, setState] = useState<{ id: string; items: RecordingPhoto[] }>({ id, items: [] });
  const meeting = kind === "meeting";
  useEffect(() => {
    setState({ id, items: [] });
    if (!meeting) return;
    let live = true;
    const ac = new AbortController();
    api<{ photos?: RecordingPhoto[] | null }>(`/recordings/${enc(id)}/photos`, { ...auth, signal: ac.signal }).then((r) => {
      if (!live || ac.signal.aborted || !current()) return;
      const items = (r.photos ?? []).filter((p) => p && Number.isInteger(p.n) && p.n > 0 && typeof p.id === "string" && p.id);
      setState({ id, items });
    }, () => {
      if (live && !ac.signal.aborted && current()) setState({ id, items: [] });
    });
    return () => {
      live = false;
      ac.abort();
    };
  }, [id, meeting, auth, current]);
  const photos = meeting && state.id === id ? state.items : [];
  if (!photos.length) return null;
  return (
    <section>
      <SectionTitle title={t("knowledge-kbdetail.recording_photos")} count={photos.length} />
      <Card>
        <ol className="grid gap-4 p-4 sm:grid-cols-2">
          {photos.map((photo) => (
            <li key={`${id}:${photo.n}`} className="min-w-0">
              <PhotoImage id={id} photo={photo} auth={auth} current={current} />
              {(photo.caption || photo.note) && <p className="mt-2 break-words text-meta leading-relaxed text-ink select-text">{photo.caption || photo.note}</p>}
              {photo.text && <pre className="mt-1 max-h-60 overflow-auto whitespace-pre-wrap break-words rounded-control bg-field px-3 py-2 font-sans text-meta leading-relaxed text-ink-2 select-text">{photo.text}</pre>}
            </li>
          ))}
        </ol>
      </Card>
    </section>
  );
}

function PhotoImage({ id, photo, auth, current }: { id: string; photo: RecordingPhoto; auth: Auth; current: () => boolean }) {
  const key = `${id}:${photo.n}`;
  const [loaded, setLoaded] = useState<{ key: string; url?: string }>({ key });
  useEffect(() => {
    setLoaded({ key });
    let live = true;
    const ac = new AbortController();
    let made: string | undefined;
    apiBlob(`/recordings/${enc(id)}/photos/${photo.n}`, { ...auth, signal: ac.signal }).then((blob) => {
      if (!live || ac.signal.aborted || !current()) return;
      made = URL.createObjectURL(blob);
      setLoaded({ key, url: made });
    }, (err) => {
      if (live && !ac.signal.aborted && !isAbort(err) && current()) setLoaded({ key });
    });
    return () => {
      live = false;
      ac.abort();
      if (made) URL.revokeObjectURL(made);
    };
  }, [key, id, photo.n, auth, current]);
  const url = loaded.key === key ? loaded.url : undefined;
  if (!url) return null;
  const alt = photo.caption || photo.note || t("knowledge-kbdetail.recording_photo_label", { n: photo.n });
  return <img src={url} alt={alt} className="h-48 w-full rounded-control bg-field object-contain" />;
}
