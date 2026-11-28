import { useEffect, useMemo, useState } from "react";
import { assistantEntries, type AssistantEntries } from "../../lib/assistant-entries";
import { isBackgroundSupervisorInteraction } from "../../lib/background-supervisor";
import { directUserInteraction, directUserWaiting, directWaitingCount } from "../../lib/direct-waiting";
import { loadFeed, useFeed, type FeedItem } from "../../lib/feed";
import { useShownManager } from "../../lib/manager-choice";
import { taskSessionLookup, useStore } from "../../lib/store";
import type { TaskCardMetadata } from "../../lib/task-cards";
import { useRestoredTodos } from "../../lib/todo-restore";
import type { Interaction, Session } from "../../lib/types";
import { answeredLookup, answeredSessions } from "./answered-lookup";

const EMPTY_FEED: FeedItem[] = [];
const EMPTY_CARDS: TaskCardMetadata[] = [];
const EMPTY_ASKS: Interaction[] = [];
const EMPTY_ENTRIES: AssistantEntries = { needsYou: [], verify: [], running: [], shelved: [], parked: [], review: [], finished: [] };

/** The main conversation's own waiting list, shared by its sidebar row and the in-chat tray. */
export function useMainWaiting(main?: Session) {
  const conversation = main?.session_id ?? "";
  const manager = useShownManager();
  const plans = useFeed((s) => s.plans);
  const feed = useFeed((s) => conversation ? (s.feeds[conversation] ?? EMPTY_FEED) : EMPTY_FEED);
  const metadata = useFeed((s) => conversation ? (s.taskCards[conversation] ?? EMPTY_CARDS) : EMPTY_CARDS);
  const deferred = useFeed((s) => s.later);
  const todos = useStore((s) => s.todos);
  const sessionRows = useStore((s) => s.features.includes("sessions_list") ? s.sessionSummaries : Object.values(s.sessions).flat());
  const sessions = useStore((s) => s.sessions);
  const sessionSummaries = useStore((s) => s.sessionSummaries);
  const taskRows = useStore((s) => s.taskRows);
  const interactions = useStore((s) => s.interactions);
  const answered = useStore((s) => s.answered);
  const assistantInfo = useStore((s) => s.assistant);
  const mainId = assistantInfo?.main;
  const [restored] = useRestoredTodos();
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const id = window.setInterval(() => setTick((n) => n + 1), 60_000);
    return () => window.clearInterval(id);
  }, []);
  useEffect(() => { if (conversation) void loadFeed(conversation); }, [conversation]);
  const lookup = useMemo(() => answeredLookup(taskSessionLookup({ sessions, sessionSummaries, taskRows }), interactions, answered),
    [sessions, sessionSummaries, taskRows, interactions, answered]);
  const entries = useMemo(() => conversation ? assistantEntries({
    conversation, conversationMachine: main?.machine_id, main: mainId, manager, todos, sessions: answeredSessions(sessionRows, interactions, answered),
    interactions, plans, feed, cards: metadata, deferred, lookup, restored, now: Date.now(),
  }) : EMPTY_ENTRIES,
    // eslint-disable-next-line react-hooks/exhaustive-deps -- tick refreshes time-based buckets once a minute
    [conversation, main?.machine_id, mainId, manager, todos, sessionRows, interactions, answered, plans, feed, metadata, deferred, lookup, restored, tick]);
  const waiting = useMemo(() => directUserWaiting(entries.needsYou, deferred), [entries.needsYou, deferred]);
  const ownInteractions = useMemo(() => {
    if (!main) return EMPTY_ASKS;
    return interactions.filter((ix) =>
      (ix.machine_id === main.machine_id && (ix.session_id === main.session_id || ix.folded_side === main.session_id)) ||
      ((ix.folded_under_machine_id || ix.machine_id) === main.machine_id && ix.folded_under === main.session_id) ||
      isBackgroundSupervisorInteraction(assistantInfo, ix, manager));
  }, [main, interactions, assistantInfo, manager]);
  const ownAsks = useMemo(() => ownInteractions.filter(directUserInteraction), [ownInteractions]);
  const projectedCount = directWaitingCount(waiting, ownAsks);
  // Older hubs and the first snapshot may expose only a scalar. Once cards are
  // available, never let that scalar override the direct-user projection.
  const count = projectedCount === 0 && ownInteractions.length === 0 ? (main?.pending_interactions ?? 0) : projectedCount;
  return { waiting, ownAsks, count };
}
