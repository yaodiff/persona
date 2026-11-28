// The minutes as one summary card (#6563, docs/assistant/meeting-minutes.md "Summary card (#6563)"): the headline and
// the core conclusions, real disagreements (#16728), the topic cards under their group headings, then the action items,
// the open questions and a note that AI wrote it. RecordingMinutes shows it only for minutes whose cleaned overview has
// a bullet, and reuses MinutesDebatesSection in the legacy layout as well.
import { useEffect, useId, useMemo, useState } from "react";
import { t } from "../../i18n";
import { formatSpan, type MinutesPoint, type RecordingMinutes } from "../../lib/kb";
import { boldParts, TOPIC_BULLETS_SHOWN, TOPIC_GLYPH, TOPIC_TINTS, type MinutesOverview, type OverviewBullet, type OverviewCite, type OverviewLine, type OverviewTopic } from "./minutes-overview";
import { decisionAnchor, overviewDecisionTargets, type DebateStatus, type MinutesDebate } from "./minutes-debates";
import { formatPositionDisplays, resolvePositionDisplays, type DetailSpeakers } from "./speakers";
import { Button, cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Card, TonePill } from "../../views/knowledge/KBParts";
// Its tokens and own rules (the tints, the grid, sizes in em) load with this page, not with every first screen.
import "./minutes-overview.css";

type Cite = (item: OverviewCite) => React.ReactNode;

/** The summary card. `cite` draws a line's source as the page's items do (the photo it cites, else its transcript
 *  time); `time` draws a transcript timestamp for disagreement positions and outcomes; `actions` and `open` are the
 *  page's own action items and open-question list. */
export function MinutesOverviewCard({ minutes, overview, debates = [], speakers, cite, time, actions, open, highlighted, onJump }: {
  minutes: RecordingMinutes;
  overview: MinutesOverview;
  debates?: readonly MinutesDebate[];
  speakers?: DetailSpeakers | null;
  cite: Cite;
  time: (at: number) => React.ReactNode;
  actions: React.ReactNode;
  open: React.ReactNode;
  highlighted?: string;
  onJump?: (id: string) => void;
}) {
  const base = useId();
  // Keyed by the minutes' version (a count of rewrites) and the topic: a topic's fold survives a confirmed to-do, which
  // keeps the version, and starts closed again once Regenerate or a photo change rewrites them.
  const [opened, setOpened] = useState<{ version: number; keys: Set<string> }>(() => ({ version: minutes.version, keys: new Set() }));
  const [pendingJump, setPendingJump] = useState<string | null>(null);
  const decisionTargets = useMemo(() => overviewDecisionTargets(overview, minutes.decisions), [overview, minutes.decisions]);
  const bulletDecisionIds = useMemo(() => {
    const map = new Map<string, string>();
    decisionTargets.forEach((target) => {
      map.set(`${target.groupIndex}:${target.topicIndex}:${target.bulletIndex}`, target.anchor);
    });
    return map;
  }, [decisionTargets]);

  useEffect(() => {
    if (!pendingJump) return;
    setPendingJump(null);
    onJump?.(pendingJump);
  }, [pendingJump, onJump]);

  const topicKey = (g: number, i: number, title: string) => `${minutes.version}:${g}:${i}:${title}`;
  const isTopicOpen = (key: string) => opened.version === minutes.version && opened.keys.has(key);
  const toggleTopic = (key: string) => {
    setOpened((prev) => {
      const next = prev.version === minutes.version ? new Set(prev.keys) : new Set<string>();
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return { version: minutes.version, keys: next };
    });
  };
  const jumpToDecision = (idx: number) => {
    const target = decisionTargets.get(idx);
    if (!target) return;
    const topic = overview.groups[target.groupIndex]?.topics[target.topicIndex];
    if (!topic) return;
    const key = topicKey(target.groupIndex, target.topicIndex, topic.title);
    if (target.bulletIndex >= TOPIC_BULLETS_SHOWN && !isTopicOpen(key)) {
      setOpened((prev) => {
        const next = prev.version === minutes.version ? new Set(prev.keys) : new Set<string>();
        next.add(key);
        return { version: minutes.version, keys: next };
      });
      setPendingJump(target.anchor);
      return;
    }
    onJump?.(target.anchor);
  };

  const facts = [
    (minutes.start || minutes.when) && <span key="when" data-minutes-when>{formatSpan(minutes.start, minutes.end, minutes.when)}</span>,
    minutes.where && <span key="where">{minutes.where}</span>,
    !!minutes.attendees?.length && <span key="who">{minutes.attendees.join(" · ")}</span>,
  ].filter(Boolean);
  return (
    <Card>
      <div data-minutes-overview className="px-4 py-3.5">
        <h3 className="break-words text-card-title font-semibold leading-relaxed text-ink">{overview.headline ?? minutes.title}</h3>
        {/* The separators stay readable, so a screen reader hears where one fact ends and the next begins. */}
        {facts.length > 0 && <p className="mt-1 break-words text-label leading-relaxed text-ink-2">
          {facts.flatMap((fact, i) => i ? [` · `, fact] : [fact])}
        </p>}
        {minutes.partial && <p className="mt-2 text-label leading-relaxed text-ink-3">{t("recording.minutes_partial")}</p>}
        {overview.conclusions.length > 0 && <div data-minutes-conclusions className="mt-3 rounded-card bg-ai-tint px-3.5 py-3">
          <h4 id={`${base}-conclusions`} className="flex items-center gap-1.5 text-meta font-semibold leading-snug text-ink">
            <Icon name="light" size={15} stroke={2} className="minutes-glyph shrink-0 text-ai" />{t("recording.minutes_conclusions")}
          </h4>
          <ul aria-labelledby={`${base}-conclusions`} className="mt-2 space-y-1.5">
            {overview.conclusions.map((c, i) => <li key={i} data-minutes-conclusion className={cx("flex flex-wrap gap-x-3 gap-y-0.5 text-meta leading-relaxed", c.photo ? "items-center" : "items-baseline")}>
              <p className="minutes-words min-w-0 grow break-words text-ink"><Bold line={c} /></p>
              <Source>{cite(c)}</Source>
            </li>)}
          </ul>
        </div>}
        <MinutesDebatesSection
          debates={debates}
          decisions={minutes.decisions}
          speakers={speakers}
          time={time}
          onDecisionJump={onJump ? jumpToDecision : undefined}
          canJumpDecision={(idx) => decisionTargets.has(idx)}
          className="mt-4"
        />
        <div className="mt-4 space-y-4">
          {overview.groups.map((group, g) => {
            // The group's tint by its position, four in turn (minutes-overview.css --topic-N).
            const tint = (g % TOPIC_TINTS) + 1;
            const groupKey = `${minutes.version}:${g}:${group.title}`;
            const topics = (level: 4 | 5) => group.topics.map((topic, i) => {
              const tKey = topicKey(g, i, topic.title);
              return (
                <TopicCard
                  key={tKey}
                  id={`${base}-t${g}-${i}`}
                  topic={topic}
                  level={level}
                  cite={cite}
                  open={isTopicOpen(tKey)}
                  onToggle={() => toggleTopic(tKey)}
                  bulletId={(b) => bulletDecisionIds.get(`${g}:${i}:${b}`)}
                  highlighted={highlighted}
                />
              );
            });
            if (!group.title) return <div key={groupKey} data-minutes-group={g} data-minutes-tint={tint} className="minutes-topic-grid">{topics(4)}</div>;
            return (
              <section key={groupKey} aria-labelledby={`${base}-g${g}`} data-minutes-group={g} data-minutes-tint={tint}>
                <h4 id={`${base}-g${g}`} className="mb-2 flex items-center gap-1.5 break-words text-label font-semibold leading-snug text-ink-2">
                  <span aria-hidden className="minutes-group-dot shrink-0 rounded-full" />{group.title}
                </h4>
                <div className="minutes-topic-grid">{topics(5)}</div>
              </section>
            );
          })}
        </div>
      </div>
      {actions}
      {open && <div className="px-4 py-3.5">
        <h4 className="mb-2 text-meta font-semibold text-ink">{t("recording.minutes_open")}</h4>
        {open}
      </div>}
      <div data-minutes-ai-note className="flex gap-1.5 px-4 py-3 text-label leading-relaxed text-ink-3">
        <span aria-hidden className="flex h-[1lh] shrink-0 items-center"><Icon name="sparkles" size={13} className="minutes-glyph text-ai" /></span>
        <p className="min-w-0 break-words">{t("recording.minutes_ai_note")}</p>
      </div>
    </Card>
  );
}

/** Exhaustive status wording for a disagreement: unknown values show no status word. */
function debateStatusLabel(status: DebateStatus): string | null {
  switch (status) {
    case "open":
      return t("recording.minutes_debate_open");
    case "settled":
      return t("recording.minutes_debate_settled");
    case "unknown":
      return null;
  }
}

/** Disagreements section (#16728), shared by the summary card (after Core conclusions, before topic cards) and the
 *  legacy layout (after Action items, before Open questions). Returns null when there are no disagreements, leaving
 *  minutes without disagreements untouched. */
export function MinutesDebatesSection({
  debates,
  decisions,
  speakers,
  time,
  onDecisionJump,
  canJumpDecision,
  className,
}: {
  debates: readonly MinutesDebate[];
  decisions?: readonly MinutesPoint[] | null;
  speakers?: DetailSpeakers | null;
  time: (at: number) => React.ReactNode;
  onDecisionJump?: (decisionIndex: number) => void;
  canJumpDecision?: (decisionIndex: number) => boolean;
  className?: string;
}) {
  const base = useId();
  if (!debates.length) return null;
  const headingId = `${base}-debates`;
  const activeSpeakers = speakers && speakers.state === "ready" && speakers.quality !== "unknown" ? speakers : null;
  return (
    <section aria-labelledby={headingId} data-minutes-debates className={className}>
      <h4 id={headingId} className="mb-2 flex items-center gap-1.5 text-meta font-semibold leading-snug text-ink">
        <Icon name="arrowLeftRight" size={14} stroke={2} className="minutes-glyph shrink-0 text-ink-2" />
        {t("recording.minutes_debates")}
      </h4>
      <div className="space-y-2">
        {debates.map((debate, i) => {
          const qId = `${base}-d${i}`;
          const statusLabel = debateStatusLabel(debate.status);
          const decisionIdx = debate.outcome?.decision;
          const linkedDecision = decisionIdx !== undefined ? decisions?.[decisionIdx] : undefined;
          const hasDecisionLink =
            decisionIdx !== undefined &&
            !!linkedDecision &&
            !!onDecisionJump &&
            (!canJumpDecision || canJumpDecision(decisionIdx));
          return (
            <article
              key={i}
              aria-labelledby={qId}
              data-minutes-debate
              data-debate-status={debate.status}
              className="minutes-debate min-w-0 rounded-card bg-inset px-3.5 py-3 shadow-hairline"
            >
              <div className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-1">
                <h5 id={qId} className="minutes-words min-w-0 grow break-words text-meta font-semibold leading-snug text-ink">
                  {debate.question}
                </h5>
                {statusLabel && (
                  <TonePill tone={debate.status === "open" ? "orange" : "neutral"}>
                    {statusLabel}
                  </TonePill>
                )}
              </div>
              <ul className="mt-2 space-y-1.5">
                {debate.positions.map((pos, p) => {
                  const attribution = activeSpeakers
                    ? formatPositionDisplays(resolvePositionDisplays(activeSpeakers.people, pos, activeSpeakers.quality))
                    : pos.who ?? "";
                  return (
                  <li key={p} data-minutes-debate-position className="flex gap-2 text-meta leading-relaxed">
                    <span aria-hidden className="minutes-mark flex h-[1lh] shrink-0 items-center justify-center">
                      <span className="minutes-mark-dot rounded-full bg-ink-3" />
                    </span>
                    <div className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-3 gap-y-0.5">
                      <p className="minutes-words min-w-0 grow break-words text-ink">
                        {pos.stance}
                        {attribution && <>{" "}<span className="inline-block max-w-full break-words pl-0.5 text-label text-ink-3">{attribution}</span></>}
                      </p>
                      {pos.at !== undefined && <Source>{time(pos.at)}</Source>}
                    </div>
                  </li>
                  );
                })}
                {debate.outcome && (
                  <li data-minutes-debate-outcome className="minutes-debate-outcome flex gap-2 border-t border-line pt-2 text-meta leading-relaxed">
                    <span aria-hidden className="minutes-mark flex h-[1lh] shrink-0 items-center justify-center">
                      <Icon name="flag" size={12} stroke={2.1} className="minutes-mark-flag text-ink-2" />
                    </span>
                    <div className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-3 gap-y-0.5">
                      <p className="minutes-words min-w-0 grow break-words text-ink">
                        {t("recording.minutes_debate_outcome", { text: debate.outcome.text })}
                      </p>
                      {(hasDecisionLink || debate.outcome.at !== undefined) && (
                        <Source>
                          <span className="inline-flex flex-wrap items-center gap-2">
                            {hasDecisionLink && (
                              <button
                                type="button"
                                data-minutes-debate-decision={decisionIdx}
                                aria-controls={decisionAnchor(decisionIdx)}
                                aria-label={t("recording.minutes_debate_decision_jump", { decision: linkedDecision.text })}
                                onClick={() => onDecisionJump(decisionIdx)}
                                className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap rounded-control text-submeta font-medium text-accent-ink hover:bg-hover"
                              >
                                <Icon name="flag" size={11} stroke={2.1} />
                                {t("recording.minutes_decision")}
                              </button>
                            )}
                            {debate.outcome.at !== undefined && time(debate.outcome.at)}
                          </span>
                        </Source>
                      )}
                    </div>
                  </li>
                )}
              </ul>
            </article>
          );
        })}
      </div>
    </section>
  );
}

/** One topic's card: its icon and title, its first bullets and "Show N more" for the rest. */
function TopicCard({ id, topic, level, cite, open, onToggle, bulletId, highlighted }: {
  id: string;
  topic: OverviewTopic;
  level: 4 | 5;
  cite: Cite;
  open: boolean;
  onToggle: () => void;
  bulletId?: (index: number) => string | undefined;
  highlighted?: string;
}) {
  const more = topic.bullets.length - TOPIC_BULLETS_SHOWN;
  const shown = open || more <= 0 ? topic.bullets : topic.bullets.slice(0, TOPIC_BULLETS_SHOWN);
  const Heading = level === 5 ? "h5" : "h4";
  return (
    <article aria-labelledby={id} data-minutes-topic className="minutes-topic min-w-0 rounded-card px-3.5 py-3">
      <div className="flex items-center gap-2 text-meta">
        <span aria-hidden className="minutes-tile flex shrink-0 items-center justify-center rounded-chip bg-surface">
          <Icon name={TOPIC_GLYPH[topic.icon]} size={14} stroke={2} className="minutes-glyph" />
        </span>
        <Heading id={id} className="min-w-0 break-words text-meta font-semibold leading-snug text-ink">{topic.title}</Heading>
      </div>
      <ul id={`${id}-bullets`} className="mt-2 space-y-2">
        {shown.map((bullet, i) => <Bullet key={i} bullet={bullet} cite={cite} id={bulletId?.(i)} highlighted={highlighted} />)}
      </ul>
      {/* Its name says which topic it opens (the visible words first), so the buttons of several topics differ. */}
      {more > 0 && <Button tone="quiet" size="sm" expanded={open} aria-controls={`${id}-bullets`} className="mt-1 ml-2" onClick={onToggle}
        aria-label={open ? t("recording.minutes_show_less_in", { topic: topic.title }) : t("recording.minutes_show_more_in", { count: more, topic: topic.title })}>
        {open ? t("recording.minutes_show_less") : t("recording.minutes_show_more", { count: more })}
      </Button>}
    </article>
  );
}

/** A point (a dot, and who said it when known) or a decision (the flag and the word Decision), with its source. */
function Bullet({ bullet, cite, id, highlighted }: { bullet: OverviewBullet; cite: Cite; id?: string; highlighted?: string }) {
  const source = cite(bullet);
  const label = bullet.decision ? t("recording.minutes_decision") : bullet.who;
  return (
    <li
      id={id}
      tabIndex={id ? -1 : undefined}
      data-minutes-bullet={bullet.decision ? "decision" : "point"}
      className={cx(
        "flex gap-2 text-meta leading-relaxed",
        id && "rounded-control outline-none transition-colors",
        id && highlighted === id && "bg-accent-tint",
      )}
    >
      <span aria-hidden className="minutes-mark flex h-[1lh] shrink-0 items-center justify-center">
        {bullet.decision ? <Icon name="flag" size={12} stroke={2.1} className="minutes-mark-flag text-ink-2" /> : <span className="minutes-mark-dot rounded-full bg-ink-3" />}
      </span>
      <div className={cx("flex min-w-0 flex-1 flex-wrap gap-x-3 gap-y-0.5", bullet.photo ? "items-center" : "items-baseline")}>
        <p className="minutes-words min-w-0 grow break-words text-ink">
          {/* The speaker or "Decision" moves to the next line whole while it fits on one, else wraps (a long name). */}
          <Bold line={bullet} />{label && <>{" "}<span className="inline-block max-w-full break-words pl-0.5 text-label text-ink-3">{label}</span></>}
        </p>
        <Source>{source}</Source>
      </div>
    </li>
  );
}

/** A line's time or photo: beside its words while they keep 12em (minutes-words), else under them on the right. */
function Source({ children }: { children: React.ReactNode }) {
  return children ? <span className="ml-auto inline-flex shrink-0">{children}</span> : null;
}

/** A line's words with its `strong` places in bold; nothing in them is read as Markdown. */
function Bold({ line }: { line: OverviewLine }) {
  return <>{boldParts(line.text, line.strong).map((part, i) => part.bold ? <strong key={i} className="font-semibold">{part.text}</strong> : <span key={i}>{part.text}</span>)}</>;
}
