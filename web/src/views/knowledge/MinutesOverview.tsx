// The minutes as one summary card (#6563, docs/assistant/meeting-minutes.md "Summary card (#6563)"): the headline and
// the core conclusions, the topic cards under their group headings, then the action items, the open questions and a
// note that AI wrote it. RecordingMinutes shows it only for minutes whose cleaned overview has a bullet.
import { useId, useState } from "react";
import { t } from "../../i18n";
import { formatSpan, type RecordingMinutes } from "../../lib/kb";
import { boldParts, TOPIC_BULLETS_SHOWN, TOPIC_GLYPH, TOPIC_TINTS, type MinutesOverview, type OverviewBullet, type OverviewCite, type OverviewLine, type OverviewTopic } from "../../lib/minutes-overview";
import { Button, cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Card } from "./KBParts";
// Its tokens and own rules (the tints, the grid, sizes in em) load with this page, not with every first screen.
import "./minutes-overview.css";

type Cite = (item: OverviewCite) => React.ReactNode;

/** The summary card. `cite` draws a line's source as the page's items do (the photo it cites, else its transcript
 *  time); `actions` and `open` are the page's own action items and open-question list. */
export function MinutesOverviewCard({ minutes, overview, cite, actions, open }: {
  minutes: RecordingMinutes;
  overview: MinutesOverview;
  cite: Cite;
  actions: React.ReactNode;
  open: React.ReactNode;
}) {
  const base = useId();
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
        {minutes.partial && <p className="mt-2 text-label leading-relaxed text-ink-3">{t("knowledge-kbdetail.minutes_partial")}</p>}
        {overview.conclusions.length > 0 && <div data-minutes-conclusions className="mt-3 rounded-card bg-ai-tint px-3.5 py-3">
          <h4 id={`${base}-conclusions`} className="flex items-center gap-1.5 text-meta font-semibold leading-snug text-ink">
            <Icon name="light" size={15} stroke={2} className="minutes-glyph shrink-0 text-ai" />{t("knowledge-kbdetail.minutes_conclusions")}
          </h4>
          <ul aria-labelledby={`${base}-conclusions`} className="mt-2 space-y-1.5">
            {overview.conclusions.map((c, i) => <li key={i} data-minutes-conclusion className={cx("flex flex-wrap gap-x-3 gap-y-0.5 text-meta leading-relaxed", c.photo ? "items-center" : "items-baseline")}>
              <p className="minutes-words min-w-0 grow break-words text-ink"><Bold line={c} /></p>
              <Source>{cite(c)}</Source>
            </li>)}
          </ul>
        </div>}
        <div className="mt-4 space-y-4">
          {overview.groups.map((group, g) => {
            // The group's tint by its position, four in turn (minutes-overview.css --topic-N).
            const tint = (g % TOPIC_TINTS) + 1;
            // Keyed by the minutes' version (a count of rewrites) and the title: a topic's fold survives a confirmed
            // to-do, which keeps the version, and starts closed again once Regenerate or a photo change rewrites them.
            const key = (i: number, title: string) => `${minutes.version}:${i}:${title}`;
            const topics = (level: 4 | 5) => group.topics.map((topic, i) => <TopicCard key={key(i, topic.title)} id={`${base}-t${g}-${i}`} topic={topic} level={level} cite={cite} />);
            if (!group.title) return <div key={key(g, "")} data-minutes-group={g} data-minutes-tint={tint} className="minutes-topic-grid">{topics(4)}</div>;
            return (
              <section key={key(g, group.title)} aria-labelledby={`${base}-g${g}`} data-minutes-group={g} data-minutes-tint={tint}>
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
        <h4 className="mb-2 text-meta font-semibold text-ink">{t("knowledge-kbdetail.minutes_open")}</h4>
        {open}
      </div>}
      <div data-minutes-ai-note className="flex gap-1.5 px-4 py-3 text-label leading-relaxed text-ink-3">
        <span aria-hidden className="flex h-[1lh] shrink-0 items-center"><Icon name="sparkles" size={13} className="minutes-glyph text-ai" /></span>
        <p className="min-w-0 break-words">{t("knowledge-kbdetail.minutes_ai_note")}</p>
      </div>
    </Card>
  );
}

/** One topic's card: its icon and title, its first bullets and "Show N more" for the rest. */
function TopicCard({ id, topic, level, cite }: { id: string; topic: OverviewTopic; level: 4 | 5; cite: Cite }) {
  const [open, setOpen] = useState(false);
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
        {shown.map((bullet, i) => <Bullet key={i} bullet={bullet} cite={cite} />)}
      </ul>
      {/* Its name says which topic it opens (the visible words first), so the buttons of several topics differ. */}
      {more > 0 && <Button tone="quiet" size="sm" expanded={open} aria-controls={`${id}-bullets`} className="mt-1 ml-2" onClick={() => setOpen((o) => !o)}
        aria-label={open ? t("knowledge-kbdetail.minutes_show_less_in", { topic: topic.title }) : t("knowledge-kbdetail.minutes_show_more_in", { count: more, topic: topic.title })}>
        {open ? t("knowledge-kbdetail.minutes_show_less") : t("knowledge-kbdetail.minutes_show_more", { count: more })}
      </Button>}
    </article>
  );
}

/** A point (a dot, and who said it when known) or a decision (the flag and the word Decision), with its source. */
function Bullet({ bullet, cite }: { bullet: OverviewBullet; cite: Cite }) {
  const source = cite(bullet);
  const label = bullet.decision ? t("knowledge-kbdetail.minutes_decision") : bullet.who;
  return (
    <li data-minutes-bullet={bullet.decision ? "decision" : "point"} className="flex gap-2 text-meta leading-relaxed">
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
