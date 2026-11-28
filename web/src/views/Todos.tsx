import { decisionSourceLabel } from "../lib/todo-decision-label";
import { NewTodoRepositoryBindings, TodoRepositoryBindings, useRepositoryRequestContext } from "../ui/TaskRepositoryBindingsEditor";
import { cleanRepositoryDraft, createTodoWithRepositories, repositoryDuplicateLink, validRepositoryDraft, type CreateRepositoryBindings, type RepositoryCatalog } from "../lib/task-repositories";
import { hasRequestPieces } from "../lib/request-pieces";
import { ExternalSourcesEntry } from "../features/external-sources/ExternalSourcesEntry";
import { defaultManager, MANAGER_HEADER, sameManager, useShownManager } from "../lib/manager-choice";
// Beautiful UI Todos View: left list (sorted by status & created_at), right detail, handover dialog.
import { createContext, lazy, memo, Suspense, useCallback, useContext, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type InputHTMLAttributes, type ReactNode, type RefObject } from "react";
import { api, apiBlob, credentialOwner, enc, errorText, getToken, subscribeCredentialOwner } from "../lib/api";
import { ago, clock, safeWebURL, sessionCan, shortPath, activityOf, tabLabel, uuid } from "../lib/format";
import { foldedState } from "../lib/child-sessions";
import { sessionCard } from "../lib/folded-rows";
import { byTime } from "../lib/times";
import { TaskWaitInfo } from "./assistant/TaskWaitInfo";
import { launchAgent } from "../lib/new-agent";
import { currentHosts } from "../lib/engines";
import { initialLauncher, launcherBlock, usableFirst } from "../lib/launcher-health";
import { defaultLabel, inlineTextStart, machineLaunchers, type LauncherDefault } from "../lib/launcher-models";
import { providerLabel } from "../lib/provider-names";
import { navigate } from "../lib/router";
import { openFromPage } from "../lib/handoff";
import { ONE_PANE, useMedia } from "../lib/viewport";
import { settingsHref } from "./settings/nav";
import { interactionAsk } from "../lib/inbox";
import { usageLimitLabel } from "../lib/usage-limit";
import { plainMarkdown } from "../lib/plain-markdown";
import { composing } from "../lib/send-history";
import { useScrollMemory } from "../lib/scroll-memory";
import { useAgentSessions, useFullAgentSessions } from "../lib/sessions";
import { scoped, useScope } from "../lib/current-assistant";
import { lazyPage } from "../lib/lazy-page";
import { dropTodo, putTodo, refresh, refreshTodos, useStore } from "../lib/store";
import {
  addTodoDecision,
  addTodoExternalPRBlocker,
  cancelTodoBlocker,
  createTodo,
  DECISIONS_FIRST,
  type DecisionsView,
  deleteTodo,
  fetchTodoDecisions,
  fetchTodoMergeTrain,
  setDecisionSupersedes,
  supersedeCandidates,
  shownDecisions,
  isLegacySpacesRouteError,
  makeSessionLookup,
  mostUrgentAgent,
  normalizedPR,
  openAsk,
  orphanedLine,
  reportConcern,
  responsibleOf,
  sortTodos,
  heldDelivery,
  TODO_TONE_CLASS,
  isAutoDone,
  isCancelled,
  isOverdue,
  todoDueDay,
  todoDueText,
  shownStatus,
  strikesThrough,
  todoRestLook,
  todoAgentState,
  todoStatus,
  todoSupervisorLabel,
  unlinkAgentFromTodo,
  unlinkSpaceFromTodo,
  unseeTodo,
  updateTodo,
  type Todo,
  type MergeTrainTodoBadge,
  type PRFacts,
  type TodoActiveBlocker,
  type TodoAgent,
  type TodoImage,
  type TodoPatch,
  type TodoDecision,
  type TodoStatus,
  type SessionLookup,
  taskNow,
} from "../lib/todos";
import { todoLook, waitOf, type WaitReason } from "../lib/todo-wait-reason";
import type { AgentState, Interaction, Launcher, Machine } from "../lib/types";
import { Button, cx, Dialog, IconButton, Loading, Menu, Spinner, toast, useMenu } from "../ui/atoms";
import { contextMenuProps } from "../ui/context-menu";
import { SHEET_QUERY } from "../ui/overlay";
import type { MenuAt } from "../ui/menu-model";
import { SearchField } from "../ui/SearchField";
import { StatusMark } from "../ui/StatusMark";
import { scrollBehavior } from "../ui/stream/motion";
import { Icon } from "../ui/icons";
import { FoldAllButton } from "../ui/FoldAllButton";
import { SESSION_LIST_STYLE } from "../ui/list-metrics";
import { formatDate, t } from "../i18n";
import { treeStateLabel, type TreeState } from "../lib/todo-tree";
import { groupTodosByProject, type TodoProjectGroup } from "../features/todos/project-groups";
import { cancellationReason, formatAgentSource, formatTodoContext } from "../lib/todo-details";
import { useCachedDirectionNames } from "../lib/direction-names";
import { directionTitle, groupsByDirection, refreshDirectionTree, useDirectionTree, type Direction } from "../lib/direction-tree";
import { DirectionCountsText } from "./DirectionMarks";
import { FOLDED_SECTIONS, TRIAGE_SECTIONS, checksAsOpen, countsAsOpen, lastActivity, openMergePRs, sectionStartsFolded, supersededFailedStarts, triage, triageTodos, type OwnerNeed, type Triage, type TriageGroup, type TriageResult, type TriageRule, type TriageSection } from "../lib/todo-triage";
import { orderWaitGroups } from "../lib/task-wait";
import { viewDrawOn } from "../features/todos/model/view-gates";
import { drawnFromView, openAsDrawn } from "../features/todos/model/view-triage";
import {
  TODO_LIST_FILTERS,
  TODO_LIST_SORTS,
  hiddenWaiting,
  isAgentTask,
  isTodoKindFilter,
  isTodoListFilter,
  isTodoListSort,
  listFilterCounts,
  matchesKindFilter,
  matchesListFilter,
  sortTriageGroups,
  type TodoKindFilter,
  type TodoListFilter,
  type TodoListSort,
} from "../lib/todo-list-filter";
import type { MenuItem } from "../ui/menu-model";
import { foldGoalTasks, nestSectionHierarchy, type GoalFoldResult, type GoalSummary, type SectionHierarchyEntry } from "../lib/todo-goal-fold";
import { createEffectiveDirectionResolver, type EffectiveDirection } from "../lib/todo-effective-direction";
import { findSelectedBlocker, matchesSharedBlocker, parseSharedBlockers } from "../lib/todo-shared-blockers";
import { SharedBlockersStrip } from "../components/SharedBlockersStrip";
import {
  eligibleParentTargets,
  reorganizeDirectionOptions,
  reorganizeTodos,
  resolveDirectionSections,
  type ReorganizeDirectionOption,
  type TodoReorganizeRequest,
} from "../lib/todo-reorganize";
import { BatchReorganizeBar } from "../components/BatchReorganizeBar";
import { TodoDirectionPicker } from "../components/TodoDirectionPicker";
import { TodoParentPicker } from "../components/TodoParentPicker";
import {
  classifyTodoDetailTemplate,
  resolveWorkGoalActions,
} from "../lib/todo-detail-template";
import {
  isPersonalSlimRow,
  repoCountOf,
  sectionStartsFoldedV2,
  selectSlimLine2Badges,
  slimSecondaryCue,
  type SlimCueTone,
  type SlimLine2BadgeKind,
} from "../lib/todo-slim-row";
import { DecisionQueuePanel, decisionQueueCandidates } from "../features/todos/DecisionQueuePanel";
import { TodoCausalTraceSection } from "../features/todos/TodoCausalTraceSection";
import { boardAttention, boardSectionMap, consumeBoardDirectionGroups, consumeBoardStatusGroups, isBoardOpen } from "../features/todos/board-consumer";
import { loadOlderClosed } from "../features/todos/closed-history";
import { pref, setPref, useTodoPreference } from "../features/todos/preferences";
import { formatRowRepoGate, formatRowRepoGateTitle, repoGatePRLabel, repoGatePRLook, repoGateReasonLabel, repoGateTone, shouldShowRowRepoGate, type RepoGateTone } from "../features/todos/repo-gate";
import { useRestoredTodos } from "../lib/todo-restore";
import { stepsWords } from "../lib/todo-steps";
import { useOwnerNeeds, useTodoBoard, useTodoLookup, useTodoMinute } from "../lib/todo-owner-needs";
import { ReviewChecklist } from "./assistant/ReviewChecklist";
import { VerificationReport } from "./assistant/VerificationReport";
export { ReviewChecklist } from "./assistant/ReviewChecklist";
import { parkedLine, progressLine, reviewState } from "../lib/supervision";
import { prLink } from "./TodoStageBar";
import { projectChoices, projectNames, useTodoProjects } from "../lib/todo-projects";
import { useFolded } from "../lib/use-folded";
import { stateName, waitSummary } from "../lib/state-labels";
import { Empty, Segmented, Tag } from "./assistant/parts";
import { OfflineState, useShownOffline } from "./ManagerParts";
import { TodoCalendar, type CalendarSlot } from "./TodoCalendar";
import { TodoBoard } from "./TodoBoard";
import { dayKey, isCalendarMode, parseDayKey, startOfDay, type CalendarMode } from "../lib/todo-calendar";
import type { Route } from "../lib/router";
import { LatestReport, ReviewBadge } from "./assistant/Supervision";
import { TodoStageBar } from "./TodoStageBar";
import { TodoAskLine, TodoAskPanel } from "./TodoAsk";
import { TaskCompletionCheck } from "./TaskCompletionCheck";
import { useTodoField } from "./use-todo-field";
import { todoUltracode } from "../lib/ultracode";
import { UltraMark } from "./chat/Ultracode";
import { boundThread, threadLine, threadName, todoThreadsOn, unbindTodoThread } from "../lib/todo-thread";
import { setTodoSeen, todoSeenOn } from "../lib/todo-seen";
import { seenCanFile } from "../lib/todo-details";
import { retryKey, retryTask, useFeed } from "../lib/feed";
import { planTaskOf, retryLauncher, retryOnLauncher } from "../lib/start-retry";
import { dueOn, duePatch, todoRowMenu } from "./todo-row-menu";
import { PageBoundary } from "./PageBoundary";
import { imagePayload, pickAttachment, releaseAttachments, type LocalAttachment } from "../lib/attachments";
import { AttachmentTray } from "../ui/AttachmentTray";

/** What this browser was last on (#2029): the list or the calendar, the calendar's mode, and whether it shows done ones. */
const VIEW_KEY = "persona.todos.view";
const MODE_KEY = "persona.todos.calendar.mode";
const DONE_KEY = "persona.todos.calendar.showDone";
const CALENDAR_DAY_KEY = "persona.todos.calendar.day";
/** The list by status sections (default, #3156), by folder (#2526), by pinned direction (#4352) or as a graph of requests. */
const GROUPING_KEY = "persona.todos.grouping";
/** The list's filter by due date and its order inside each section (#3740), kept per browser like the view. */
const FILTER_KEY = "persona.todos.filter";
const SORT_KEY = "persona.todos.sort";

/** A filter's and a sort's name, as the menu and the chip say them. */
const filterLabel = (f: TodoListFilter) => t(`todos.filter_${f}`);
const sortLabel = (s: TodoListSort) => t(`todos.sort_${s}`);
const kindFilterLabel = (k: TodoKindFilter) => t(`todos.kind_filter_${k}`);

type TodoGrouping = "status" | "folder" | "direction" | "request";

/** Tracks currently expanded & visibly mounted list row IDs while Batch Reorganize mode is active (#17696). */
interface ReorganizeVisibilityRegistry {
  register: (id: string) => () => void;
  currentIds: () => string[];
}

const ReorganizeVisibilityContext = createContext<ReorganizeVisibilityRegistry | null>(null);

const DONE_UNCATEGORIZED_ID = "__uncategorized__";

type CompletedDirectionBucket = {
  id: string;
  label: string;
  uncategorized: boolean;
  todos: Todo[];
  count: number;
};

function mergeTrainPRKey(url: string, pr?: PRFacts): string | undefined {
  if (!url || (pr && (pr.state !== "open" || pr.draft))) return undefined;
  return JSON.stringify([url, pr?.head_oid, pr?.review, pr?.checks, pr?.conflicts]);
}

function mergeTrainKey(todos: readonly Todo[], manager: string): string {
  const keys: string[] = [];
  for (const todo of todos) {
    if (todo.done_at || isCancelled(todo) || !sameManager(todo.manager, manager)) continue;
    const url = normalizedPR(todo.pr_url);
    const facts = normalizedPR(todo.pr_status?.url) === url ? todo.pr_status : undefined;
    const prs = [mergeTrainPRKey(url, facts)];
    for (const pr of todo.other_prs ?? []) {
      const earlier = normalizedPR(pr.url);
      if (earlier !== url) prs.push(mergeTrainPRKey(earlier, pr));
    }
    const open = prs.filter((pr) => pr !== undefined).sort();
    if (open.length) {
      const blockers = (todo.active_blockers ?? [])
        .filter((b) => b.kind === "merge_train")
        .map((b) => JSON.stringify([b.target_ref, b.state, b.attempts])).sort();
      keys.push(JSON.stringify([todo.id, open, blockers]));
    }
  }
  return keys.length ? JSON.stringify(keys.sort()) : "";
}

type ClosedHistory = {
  hasMore: boolean;
  loading: boolean;
  error: string | null;
  onLoad: () => void;
};

/** The grouping saved in this browser; status for none or an unknown one. */
function todoGrouping(saved: string | null): TodoGrouping {
  return saved === "folder" || saved === "direction" || saved === "request" ? saved : "status";
}

/** The calendar's address: its mode, the day it shows, the to-do open beside it. */
function calendarRoute(mode: CalendarMode, day: number, id?: string, manager?: string): Route {
  return { name: "todos", view: "calendar", mode, date: dayKey(day), ...(id ? { id, manager } : {}) };
}

function boardRoute(id?: string, manager?: string): Route {
  return { name: "todos", view: "board", ...(id ? { id, manager } : {}) };
}

type TodosViewProps = {
  selectedId?: string;
  /** The calendar (#2029) or read-only progress board (#12713) rather than the list; calendar mode/day from the address. */
  view?: "list" | "calendar" | "board";
  mode?: CalendarMode;
  date?: string;
  onToggleSidebar?: () => void;
  manager?: string;
};

const DirectionDetail = lazyPage(() => import("../features/external-sources/DirectionDetail").then((m) => m.DirectionDetail), { listed: false });

export function TodosView({ directionId, ...props }: TodosViewProps & { directionId?: string }) {
  return directionId
    ? <PageBoundary page="direction-detail">
        <Suspense fallback={<Loading />}>
          <DirectionDetail id={directionId} manager={props.manager} onToggleSidebar={props.onToggleSidebar} />
        </Suspense>
      </PageBoundary>
    : <TodoListView {...props} />;
}

function TodoListView({
  selectedId,
  manager: selectedManager,
  view,
  mode: routeMode,
  date,
  onToggleSidebar,
}: TodosViewProps) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const repositoryManager = useShownManager();
  // with several assistants, the current one's to-dos (docs/assistant/managers.md "Clients"); a link to another assistant's
  // to-do still opens it, without switching
  const everyTodo = useStore((s) => s.todos);
  const scope = useScope();
  const assistant = useStore((s) => s.assistant);
  const todosV2 = assistant?.todos_v2 === true && sameManager(assistant.manager ?? repositoryManager, repositoryManager);
  const serverBoard = useTodoBoard(scope);
  const legacyNeeds = useOwnerNeeds(!serverBoard);
  const ownTodos = useMemo(() => scoped(everyTodo, scope), [everyTodo, scope]);
  const todos = useMemo(() => {
    if (selectedId && selectedManager) {
      const chosen = everyTodo.find((td) => td.id === selectedId && sameManager(td.manager, selectedManager));
      if (chosen && ownTodos.includes(chosen)) return ownTodos;
      const others = ownTodos.filter((td) => td.id !== selectedId);
      return chosen ? [...others, chosen] : others;
    }
    const linked = selectedId && !ownTodos.some((x) => x.id === selectedId) ? everyTodo.filter((x) => x.id === selectedId) : [];
    return linked.length ? [...ownTodos, ...linked] : ownTodos;
  }, [everyTodo, ownTodos, selectedId, selectedManager]);
  const todosLoaded = useStore((s) => s.todosLoaded);
  const todosError = useStore((s) => s.todosError);
  const todosSupported = useStore((s) => s.todosSupported);
  // the current assistant offline (several assistants): the hub's merged list leaves it out, so none here means it
  // couldn't answer; the page says so instead of the empty one
  const offline = useShownOffline();
  const machines = useStore((s) => s.machines);
  const interactions = useStore((s) => s.interactions);
  // its to-dos' Agents as the sidebar's badge reads them (lib/todo-owner-needs.ts): an open task's Agent outside the list's
  // window too, and a card answered here stops its agent's "Needs you" at once, not at the hub's next push (#4186)
  const lookup = useTodoLookup();

  const [activeId, setActiveId] = useState<string | undefined>(selectedId);
  const [search, setSearch] = useState("");
  const [newOpen, setNewOpen] = useState(false);
  const [handoverOpen, setHandoverOpen] = useState(false);
  const [deleteConfirm, setDeleteConfirm] = useState(false);
  const [reportingConcern, setReportingConcern] = useState<Todo>();
  // Which to-do the hand-over and delete dialogs are for: a row's whose menu chose them (#4429), or for hand-over
  // none, the open one, which it follows. Each stays set after its dialog closes, so the dialog leaves with the to-do
  // it showed.
  const [handoverId, setHandoverId] = useState<string>();
  const [deleteId, setDeleteId] = useState<string>();
  // A row's menu (#4429), where it opens and whether the row's checkbox showed the to-do open; the day picker it opens
  const [rowMenu, setRowMenu] = useState<{ id: string; at: MenuAt; open: boolean } | null>(null);
  const [dueFor, setDueFor] = useState<string>();
  const [deleting, setDeleting] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [showDone, setShowDone] = useState(false);
  const [doneDirectionFilter, setDoneDirectionFilter] = useTodoPreference(`persona.todos.doneDirection:${repositoryManager}`, (value) => value);
  const [doneDirectionOpen, setDoneDirectionOpen] = useTodoPreference(`persona.todos.doneDirectionOpen:${repositoryManager}`, (value) => value === "1" ? "1" : "0");
  const [doneScrollSeq, setDoneScrollSeq] = useState(0);
  const doneFoldRef = useRef<HTMLDivElement>(null);
  const [showCancelled, setShowCancelled] = useState(false);
  const [showReleased, setShowReleased] = useState(false);
  // The calendar (#2029): the address says which mode and day; a click on its empty axis opens New To-Do due there.
  const calendarOn = view === "calendar";
  // Direct view switches draw immediately; the address then becomes authoritative again (including browser Back).
  const [boardOverride, setBoardOverride] = useState<boolean>();
  const boardOn = view === "board" || (!view && boardOverride === true);
  useEffect(() => setBoardOverride(undefined), [view]);
  const storedMode = pref(MODE_KEY);
  const calendarMode: CalendarMode = routeMode ?? (isCalendarMode(storedMode) ? storedMode : "week");
  const preferredDay = (todosV2 ? parseDayKey(pref(CALENDAR_DAY_KEY) ?? undefined) : undefined) ?? startOfDay(Date.now());
  const anchor = parseDayKey(date) ?? preferredDay;
  const listRoute = useCallback((id?: string, manager?: string): Route => ({
    name: "todos", ...(todosV2 && { view: "list" }), ...(id && { id, manager }),
  }), [todosV2]);
  const [calendarDone, setCalendarDone] = useState(() => pref(DONE_KEY) !== "0");
  const [newSlot, setNewSlot] = useState<CalendarSlot | undefined>();
  // /todos with no view: the one this browser was last on
  useEffect(() => {
    if (view || selectedId) return;
    const saved = pref(VIEW_KEY);
    if (saved === "calendar") navigate(calendarRoute(calendarMode, preferredDay), true);
    else if (saved === "board") {
      setBoardOverride(true);
      navigate(boardRoute(), true);
    } else if (todosV2) navigate(listRoute(), true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view, selectedId, todosV2]);
  // on the calendar a new to-do is due on the day shown
  const openNew = () => {
    setNewSlot(calendarOn ? { day: anchor } : undefined);
    setNewOpen(true);
  };
  const showView = (next: "list" | "calendar" | "board") => {
    setBoardOverride(next === "board");
    setPref(VIEW_KEY, next);
    const manager = todosV2 ? selectedManager : undefined;
    navigate(next === "calendar" ? calendarRoute(calendarMode, preferredDay, selectedId, manager) : next === "board" ? boardRoute(selectedId, manager) : listRoute(selectedId, manager));
  };

  // Sync selectedId from URL; on Board, browser Back also closes the wide detail beside it.
  useEffect(() => {
    if (selectedId) setActiveId(selectedId);
    else if (boardOn) setActiveId(undefined);
  }, [selectedId, boardOn]);
  // A link to a to-do the list doesn't have (deleted, or an old link): said once, and the address loses it, so another
  // to-do is not shown under it (web usability audit item 42). Not while the list can't tell: the current assistant
  // offline (the merged list leaves its to-dos out) or a hub without to-dos (an empty list); the link is kept for later.
  const linkGone = todosLoaded && !todosError && todosSupported && !offline && Boolean(selectedId) && !todos.some((td) => td.id === selectedId);
  useEffect(() => {
    if (!linkGone) return;
    toast(t("not-found.todo_gone"));
    navigate(boardOn ? boardRoute() : calendarOn ? calendarRoute(calendarMode, anchor) : listRoute(), true);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per link that is gone
  }, [linkGone]);

  // a minute's tick moves to-dos across the 24 h and 48 h lines (and today's end) while nothing else changes: the
  // badge's clock, so the sections and the badge's marks move together
  const tick = useTodoMinute();
  const [restored, restoreTodo] = useRestoredTodos();
  // whether a failed start was superseded by finished work (#3713) is decided on every to-do: a search or a filter that
  // leaves the finished one out must not put it back in Needs you
  const superseded = useMemo(() => serverBoard ? new Set<string>() : supersededFailedStarts(todos), [serverBoard, todos]);
  const foldChildren = todosV2 && assistant?.todos_v2_fold_children !== false;
  const todosById = useMemo(() => new Map(todos.map((td) => [td.id, td])), [todos]);
  const olderClosed = useStore((s) => s.olderClosedTodos);
  const olderClosedIds = useMemo(() => new Set(olderClosed.map((td) => td.id)), [olderClosed]);
  const { windowTodos, historyTodos } = useMemo(() => {
    if (!serverBoard || !olderClosedIds.size) return { windowTodos: todos, historyTodos: [] };
    const windowTodos: Todo[] = [];
    const historyTodos: Todo[] = [];
    for (const td of todos) (olderClosedIds.has(td.id) ? historyTodos : windowTodos).push(td);
    return { windowTodos, historyTodos };
  }, [serverBoard, todos, olderClosedIds]);
  // Each to-do's own status section (docs/assistant/todo-status-sop.md), whatever the list is grouped by: its checkbox
  // and its page read a failed start waiting on the owner as open (audit item 13).
  const sectionOf = useMemo(() => {
    if (serverBoard) return boardSectionMap(serverBoard, todosById, historyTodos);
    return new Map(todos.map((td) => [td.id, triage(td, lookup, Date.now(), restored.get(td.id) ?? 0, superseded.has(td.id)).section]));
  }, [serverBoard, todosById, historyTodos, todos, lookup, restored, superseded, tick]);
  const projectedNeeds = useMemo(() => serverBoard ? boardAttention(serverBoard, todosById) : undefined, [serverBoard, todosById]);
  const needs = projectedNeeds ?? legacyNeeds;
  // open todos under the knowledge-base project they are in (#2581), else the one their agents work in (#2526)
  const kbProjects = useTodoProjects();
  const projectName = useMemo(() => projectNames(kbProjects), [kbProjects]);
  // Direction metadata shared by the direction grouping, batch reorganize, and todos_v2 slim row meta (#14830).
  const directionTree = useDirectionTree();
  const leaderTitles = useStore((s) => s.assistant?.conversations);
  const directionMenu = useMenu();
  const cachedDirectionNames = useCachedDirectionNames();
  const reorganizeDirections = useMemo(
    () => (todosV2 ? reorganizeDirectionOptions(directionTree, leaderTitles, assistant?.leaders) : []),
    [todosV2, directionTree, leaderTitles, assistant?.leaders],
  );
  const directionLabel = useCallback(
    (id: string) =>
      reorganizeDirections.find((d) => d.id === id)?.label ||
      cachedDirectionNames.get(id) ||
      leaderTitles?.find((c) => c.session_id === id)?.title ||
      undefined,
    [reorganizeDirections, cachedDirectionNames, leaderTitles],
  );
  const knownDirectionIds = useMemo(() => {
    if (!todosV2) return new Set<string>();
    const ids = new Set<string>();
    for (const d of reorganizeDirections) {
      const id = d.id.trim();
      if (id) ids.add(id);
    }
    for (const g of serverBoard?.direction_groups ?? []) {
      const id = g.direction_id?.trim();
      if (id) ids.add(id);
    }
    return ids;
  }, [todosV2, reorganizeDirections, serverBoard]);
  const resolveDirection = useMemo(
    () =>
      todosV2
        ? createEffectiveDirectionResolver({
            todosById,
            board: serverBoard,
            knownDirectionIds,
            directionLabel,
          })
        : undefined,
    [todosV2, todosById, serverBoard, knownDirectionIds, directionLabel],
  );
  // A sessions push builds a new lookup: what it leaves in the same order stays the same list (and the same groups,
  // the same row views below), so the rows and sections it did not touch skip their render.
  const views = useSteady(
    useMemo(
      () => todoRowViews(todos, lookup, interactions, sectionOf, needs, todosV2 ? projectName : undefined, resolveDirection),
      [todos, lookup, interactions, sectionOf, needs, todosV2, projectName, resolveDirection],
    ),
    steadyViews,
  );

  // The filter by due date and the order inside each section (#3740): this browser's, in both groupings
  const [listFilter, setListFilter] = useState<TodoListFilter>(() => {
    const v = pref(FILTER_KEY);
    return isTodoListFilter(v) ? v : "all";
  });
  const [listSort, setListSort] = useState<TodoListSort>(() => {
    const v = pref(SORT_KEY);
    return isTodoListSort(v) ? v : "recent";
  });
  // Lightweight All / To-dos / Tasks filter gated behind todos_v2 (#14642); turning todos_v2 off reverts immediately.
  const [kindFilter, setKindFilter] = useTodoPreference<TodoKindFilter>("persona.todos.kindFilter", (value) => isTodoKindFilter(value) ? value : "all");
  const effectiveKindFilter: TodoKindFilter = todosV2 ? kindFilter : "all";
  // Shared blockers summary strip gated behind todos_v2 (#14728, #17938).
  const rawSharedBlockers = useStore((s) => s.sharedBlockers);
  const visibleTodoIds = useMemo(() => new Set(todos.map((td) => td.id)), [todos]);
  const sharedBlockers = useMemo(
    () => (todosV2 ? parseSharedBlockers(rawSharedBlockers, visibleTodoIds, todosById) : []),
    [todosV2, rawSharedBlockers, visibleTodoIds, todosById],
  );
  const closedCursors = useStore((s) => s.closedCursors);
  const closedLoading = useStore((s) => s.closedLoading);
  const closedError = useStore((s) => s.closedError);
  const hasOlderClosed = useMemo(
    () => Object.entries(closedCursors).some(([manager, cursor]) => cursor.has_more && sameManager(manager, repositoryManager)),
    [closedCursors, repositoryManager],
  );
  const closedHistory = useMemo<ClosedHistory>(() => ({
    hasMore: hasOlderClosed,
    loading: closedLoading,
    error: closedError,
    onLoad: () => { void loadOlderClosed(repositoryManager); },
  }), [hasOlderClosed, closedLoading, closedError, repositoryManager]);
  const [selectedBlockerKey, setSelectedBlockerKey] = useTodoPreference(`persona.todos.sharedBlocker:${repositoryManager}`, (value) => value);
  const activeBlocker = useMemo(
    () => (todosV2 ? findSelectedBlocker(selectedBlockerKey, sharedBlockers) : undefined),
    [todosV2, selectedBlockerKey, sharedBlockers],
  );
  // Batch reorganize mode strictly gated behind todos_v2 (#14758); turning todos_v2 off or leaving list mode resets immediately.
  const [reorganizeMode, setReorganizeMode] = useState(false);
  const [reorganizeSelected, setReorganizeSelected] = useState<ReadonlySet<string>>(() => new Set());
  const [reorganizeSubmitting, setReorganizeSubmitting] = useState(false);
  const [reorganizeError, setReorganizeError] = useState<string | null>(null);
  const [reorganizeFeedback, setReorganizeFeedback] = useState<string | null>(null);
  // Decision Queue modal strictly gated behind todos_v2 (#15757).
  const [decisionQueueOpen, setDecisionQueueOpen] = useState(false);
  // Same-repo merge-train queue positions strictly gated behind todos_v2 (#16433); fires zero requests when off.
  const [mergeTrain, setMergeTrain] = useState<{ manager: string; owner: string; badges: Record<string, MergeTrainTodoBadge> }>();
  const mergeTrainFetchKey = useMemo(
    () => todosV2 ? mergeTrainKey(todos, repositoryManager) : "",
    [todosV2, repositoryManager, todos],
  );
  const mergeTrainByTodo = mergeTrainFetchKey && mergeTrain?.manager === repositoryManager && mergeTrain.owner === owner
    ? mergeTrain.badges : undefined;
  useEffect(() => {
    if (!mergeTrainFetchKey) {
      setMergeTrain(undefined);
      return;
    }
    const ac = new AbortController();
    void fetchTodoMergeTrain(ac.signal)
      .then((res) => {
        if (!ac.signal.aborted) setMergeTrain({ manager: repositoryManager, owner, badges: res.by_todo });
      })
      .catch(() => {}); // Keep the last queue positions if a follow-up read fails.
    return () => ac.abort();
  }, [mergeTrainFetchKey, owner, repositoryManager]);

  const exitReorganizeMode = useCallback(() => {
    setReorganizeMode(false);
    setReorganizeSelected(new Set());
    setReorganizeError(null);
    setReorganizeFeedback(null);
  }, []);

  const toggleReorganizeMode = useCallback(() => {
    setReorganizeMode((on) => {
      if (on) {
        setReorganizeSelected(new Set());
        setReorganizeError(null);
        setReorganizeFeedback(null);
        return false;
      }
      setReorganizeError(null);
      setReorganizeFeedback(null);
      return true;
    });
  }, []);

  const toggleReorganizeSelect = useCallback((id: string) => {
    setReorganizeSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
    setReorganizeError(null);
  }, []);

  const clearReorganizeSelection = useCallback(() => {
    setReorganizeSelected(new Set());
    setReorganizeError(null);
  }, []);

  // Prune deleted to-dos from selection.
  useEffect(() => {
    if (reorganizeSelected.size === 0) return;
    let changed = false;
    const next = new Set<string>();
    for (const id of reorganizeSelected) {
      if (visibleTodoIds.has(id)) next.add(id);
      else changed = true;
    }
    if (changed) setReorganizeSelected(next);
  }, [reorganizeSelected, visibleTodoIds]);

  const applyReorganize = useCallback(async (req: TodoReorganizeRequest): Promise<boolean> => {
    setReorganizeSubmitting(true);
    setReorganizeError(null);
    setReorganizeFeedback(null);
    try {
      const res = await reorganizeTodos(req);
      await refreshTodos();
      if (req.direction_id !== undefined) {
        void refreshDirectionTree().catch(() => {});
      }
      setReorganizeSelected(new Set());
      const updatedCount = res.updated?.length ?? req.ids.length;
      const transferredCount = res.transferred_ids?.length ?? 0;
      setReorganizeFeedback(
        transferredCount > 0
          ? t("todos.reorganize_success_transferred", { count: updatedCount, transferred: transferredCount })
          : t("todos.reorganize_success", { count: updatedCount }),
      );
      return true;
    } catch (e) {
      setReorganizeError(errorText(e));
      return false;
    } finally {
      setReorganizeSubmitting(false);
    }
  }, []);

  // Single-item direction change from TodoDetail under todos_v2 (#15653).
  const applyDetailReorganize = useCallback(async (req: TodoReorganizeRequest): Promise<void> => {
    const res = await reorganizeTodos(req);
    await refreshTodos();
    if (req.direction_id !== undefined) {
      void refreshDirectionTree().catch(() => {});
    }
    const updatedCount = res.updated?.length ?? req.ids.length;
    const transferredCount = res.transferred_ids?.length ?? 0;
    toast(
      transferredCount > 0
        ? t("todos.reorganize_success_transferred", { count: updatedCount, transferred: transferredCount })
        : t("todos.reorganize_success", { count: updatedCount }),
      "ok",
    );
  }, []);

  const chooseFilter = (f: TodoListFilter) => {
    setPref(FILTER_KEY, f);
    setListFilter(f);
  };
  const chooseSort = (s: TodoListSort) => {
    setPref(SORT_KEY, s);
    setListSort(s);
  };
  const inFilter = (td: Todo) =>
    matchesListFilter(td, listFilter, tick) &&
    matchesKindFilter(td, effectiveKindFilter) &&
    matchesSharedBlocker(td, activeBlocker);
  // Filter by search query
  const matchesSearch = (t: Todo) => {
    const q = search.trim().toLowerCase();
    if (!q) return true;
    return (
      t.title.toLowerCase().includes(q) ||
      (t.notes ?? "").toLowerCase().includes(q) ||
      (responsibleOf(t)?.name.toLowerCase().includes(q) ?? false)
    );
  };
  // a search and the filter narrow the to-dos first; the sections (or the folders) place what is left
  const filterTodo = (t: Todo) => matchesSearch(t) && inFilter(t);

  // Hub to-do view projection (#14583, docs/web/web.md "Developing Persona"): preferred by default when the hub lists
  // `todo_view` (with `persona.todos.viewDraw=0` as browser rollback); a row without a readable view keeps today's place.
  const features = useStore((s) => s.features);
  const fromView = useMemo(() => viewDrawOn(features), [features]);
  const hasSearch = search.trim() !== "";
  const hasNonSearchFilter = listFilter !== "all" || effectiveKindFilter !== "all" || Boolean(activeBlocker);
  const hasAnyFilter = hasSearch || hasNonSearchFilter;
  // Under todos_v2 === true with server `board` (#15800), thinly consume `board.status_groups` & `board.counts`
  // directly without re-running client triage, wait-group ordering, or goal-folding heuristics.
  const boardStatus = useMemo(
    () => (serverBoard ? consumeBoardStatusGroups(windowTodos, serverBoard, hasAnyFilter ? filterTodo : undefined, historyTodos, sectionOf) : undefined),
    [serverBoard, windowTodos, historyTodos, sectionOf, hasAnyFilter, search, listFilter, effectiveKindFilter, activeBlocker, tick],
  );
  const triaged = useMemo(
    () => {
      if (boardStatus) return boardStatus.goalFold.triaged;
      const r = orderWaitGroups(triageTodos(todos.filter(filterTodo), lookup, Date.now(), restored, { superseded }), Date.now());
      return fromView ? drawnFromView(r, Date.now(), restored) : r;
    },
    [boardStatus, todos, lookup, restored, search, listFilter, effectiveKindFilter, activeBlocker, superseded, tick, fromView],
  );
  // Under todos_v2 (#14661), fold non-urgent `part_of` child tasks under their top-level parent goal while surfacing
  // `needs_you` / active-ask child blockers in `waiting`; when server `board` is present, reuse its folding directly.
  const goalFold = useMemo(
    () => (boardStatus ? boardStatus.goalFold : foldChildren ? foldGoalTasks(triaged) : undefined),
    [boardStatus, foldChildren, triaged],
  );
  const displayTriaged = goalFold ? goalFold.triaged : triaged;
  const statusSectionOrder = boardStatus ? boardStatus.sectionOrder : TRIAGE_SECTIONS;
  // The legacy header follows the filter but ignores search. The v2 attention total is for the whole board.
  const triageAll = useMemo(
    () => {
      if (serverBoard || !hasSearch) return triaged;
      const r = orderWaitGroups(triageTodos(todos.filter(inFilter), lookup, Date.now(), restored, { superseded }), Date.now());
      return fromView ? drawnFromView(r, Date.now(), restored) : r;
    },
    [serverBoard, todos, lookup, restored, hasSearch, listFilter, effectiveKindFilter, activeBlocker, superseded, triaged, tick, fromView],
  );
  const headerNeedsYouCount = serverBoard?.counts.needs_you ?? sectionSize(triageAll, "waiting");
  // Where the status sections place each to-do the search and the filter leave: its row's section (a merged row takes
  // its most urgent member's), and whether it counts as open (countsAsOpen, the header's Not done, its own reading).
  // Folders and directions share this placement and one number for it; cancelled stays out of both open and Done (#5732).
  const placed = useMemo(() => {
    const row = new Map<string, TriageSection>();
    const open = new Set<string>();
    if (boardStatus) {
      for (const sec of statusSectionOrder) {
        for (const g of boardStatus.goalFold.triaged.sections[sec]) {
          for (const m of g.members) {
            row.set(m.id, sec);
            if (isBoardOpen(sec)) open.add(m.id);
          }
        }
      }
      for (const summary of boardStatus.goalFold.goals.values()) {
        for (const child of summary.children) {
          if (!row.has(child.todo.id)) row.set(child.todo.id, child.section);
          if (isBoardOpen(child.section)) open.add(child.todo.id);
        }
      }
      return { row, open };
    }
    for (const sec of TRIAGE_SECTIONS)
      for (const g of triaged.sections[sec])
        for (const m of g.members) {
          row.set(m.id, sec);
          if ((fromView ? openAsDrawn : countsAsOpen)(m, g.triage.get(m.id)!)) open.add(m.id);
        }
    return { row, open };
  }, [boardStatus, statusSectionOrder, triaged, fromView]);

  // The progress board always shows the open projection, independently of the List's filters.
  const boardProjection = useMemo(() => {
    if (!boardOn) return undefined;
    if (serverBoard) {
      const open = [...sectionOf].flatMap(([id, section]) => {
        const todo = todosById.get(id);
        return todo && isBoardOpen(section) ? [todo] : [];
      });
      return { todos: open };
    }
    const triaged = orderWaitGroups(triageTodos(todos, lookup, Date.now(), restored, { superseded }), Date.now());
    const result = fromView ? drawnFromView(triaged, Date.now()) : triaged;
    const triages = new Map<string, Triage>();
    const open = new Set<string>();
    for (const sec of TRIAGE_SECTIONS) {
      for (const group of result.sections[sec]) {
        for (const member of group.members) {
          const placement = group.triage.get(member.id)!;
          triages.set(member.id, placement);
          if ((fromView ? openAsDrawn : countsAsOpen)(member, placement)) open.add(member.id);
        }
      }
    }
    return { todos: sortTodos(todos.filter((todo) => open.has(todo.id)), lookup), triages };
  }, [boardOn, serverBoard, sectionOf, todosById, todos, lookup, restored, superseded, tick, fromView]);

  const filteredUncompleted = useSteady(
    useMemo(() => sortTodos(todos.filter((td) => {
      const sec = placed.row.get(td.id);
      return placed.row.has(td.id) && sec !== "done" && sec !== "cancelled";
    }), lookup), [todos, placed, lookup]),
    sameList,
  );
  const filteredCompleted = useSteady(
    useMemo(() => sortTodos(todos.filter((td) => placed.row.get(td.id) === "done"), lookup), [todos, placed, lookup]),
    sameList,
  );
  const filteredCancelled = useSteady(
    useMemo(() => sortTodos(todos.filter((td) => placed.row.get(td.id) === "cancelled"), lookup), [todos, placed, lookup]),
    sameList,
  );
  const cancelledCount = boardStatus?.goalFold.triaged.cancelled ?? filteredCancelled.length;
  const projectGroups = useSteady(
    // a folder holding a to-do in Needs you says so, a failed start waiting on the owner too (audit item 13)
    useMemo(() => groupTodosByProject(filteredUncompleted, lookup, Date.now(), projectName, (td) => sectionOf.get(td.id) === "waiting"), [filteredUncompleted, lookup, projectName, sectionOf]),
    steadyGroups,
  );
  // each section gets only its own rows' views, so one row's change re-renders that row's section alone
  const sectionViews = useSteady(
    useMemo((): ReadonlyMap<string, TodoRowView[]> => new Map(projectGroups.map((g) => [g.key, g.todos.map((td) => views.get(td.id)!)])), [projectGroups, views]),
    steadyLists,
  );

  // The list by status sections (#3156, docs/assistant/todo-status-sop.md), or by folder as before
  const [chosenGrouping, setGrouping] = useState<TodoGrouping>(() => todoGrouping(pref(GROUPING_KEY)));
  // "Pinned" (#4352): the hub's direction tree, offered while there is a leader; else the list goes by status and the
  // choice is kept for when there is one. Before the first read lands (undefined) a saved 常驻 choice stays and the list
  // shows its loading state, as the sidebar's Agents block does, rather than flashing the status grouping.
  const hasTreeDirections = groupsByDirection(directionTree);
  const hasBoardDirections = Boolean(serverBoard && serverBoard.direction_groups.length > 0);
  const byDirection = hasTreeDirections || hasBoardDirections;
  const directionPending = chosenGrouping === "direction" && directionTree === undefined && !hasBoardDirections;
  // "By request" is offered while some request is in pieces; else the list goes by status and the choice is kept, as
  // for Pinned. Until the to-dos are read a saved choice stays.
  const byRequest = useMemo(() => hasRequestPieces(todos), [todos]);
  const requestGone = chosenGrouping === "request" && todosLoaded && !byRequest;
  const grouping: TodoGrouping = (chosenGrouping === "direction" && !byDirection && !directionPending) || requestGone ? "status" : chosenGrouping;
  const byStatus = grouping === "status";
  // "By request": the requests graph takes the page's width, and the detail opens beside it only once a piece is picked
  const requestMode = grouping === "request";
  const [renderedReorganizeIds, setRenderedReorganizeIds] = useState<string[]>([]);
  const mountedReorganizeCounts = useRef(new Map<string, number>());
  const reorganizeRegistry = useMemo<ReorganizeVisibilityRegistry | null>(() => {
    if (!reorganizeMode) return null;
    const sync = () => {
      const next = [...mountedReorganizeCounts.current.keys()];
      setRenderedReorganizeIds((prev) =>
        prev.length === next.length && prev.every((id, i) => id === next[i]) ? prev : next,
      );
    };
    return {
      register: (id: string) => {
        const counts = mountedReorganizeCounts.current;
        counts.set(id, (counts.get(id) ?? 0) + 1);
        sync();
        return () => {
          const n = (counts.get(id) ?? 0) - 1;
          if (n <= 0) counts.delete(id);
          else counts.set(id, n);
          sync();
        };
      },
      currentIds: () => [...mountedReorganizeCounts.current.keys()],
    };
  }, [reorganizeMode]);
  useEffect(() => {
    if (!reorganizeMode) {
      mountedReorganizeCounts.current.clear();
      setRenderedReorganizeIds((prev) => (prev.length === 0 ? prev : []));
    }
  }, [reorganizeMode]);
  const visibleReorganizeIds = reorganizeMode ? renderedReorganizeIds : [];
  const selectAllReorganize = useCallback(() => {
    const ids = reorganizeRegistry ? reorganizeRegistry.currentIds() : [];
    setReorganizeSelected(new Set(ids));
    setReorganizeError(null);
  }, [reorganizeRegistry]);

  // Immediately exit organize mode and clear selection if todos_v2 turns off or user leaves list view.
  useEffect(() => {
    if (reorganizeMode && (!todosV2 || calendarOn || requestMode)) {
      exitReorganizeMode();
    }
  }, [reorganizeMode, todosV2, calendarOn, requestMode, exitReorganizeMode]);

  const decisionQueueItems = useMemo(() => {
    if (!todosV2) return [];
    const pending = projectedNeeds
      ? [...projectedNeeds.keys()].map((id) => todosById.get(id)!)
      : triageAll.sections.waiting.flatMap((g) => g.members);
    return decisionQueueCandidates(pending);
  }, [todosV2, projectedNeeds, todosById, triageAll.sections.waiting]);
  useEffect(() => {
    if (decisionQueueOpen && (!todosV2 || boardOn || calendarOn || requestMode)) {
      setDecisionQueueOpen(false);
    }
  }, [decisionQueueOpen, todosV2, boardOn, calendarOn, requestMode]);

  const reorganizeProps = useMemo(
    () =>
      todosV2 && !calendarOn && !requestMode && reorganizeMode
        ? { selectedIds: reorganizeSelected, onToggle: toggleReorganizeSelect }
        : undefined,
    [todosV2, calendarOn, requestMode, reorganizeMode, reorganizeSelected, toggleReorganizeSelect],
  );
  // The tree lists only open to-dos (server/internal/assistant/direction_tree.go, the same countsAsOpen rule), so a
  // direction holds the open ones it lists: one ticked here leaves at once, not when the tree's push comes.
  // Under todos_v2 (#15653), explicit `todo.direction_id` takes precedence over runtime supervision membership so
  // reorganizing updates the direction grouping immediately without interrupting running Agents.
  const directionSections = useMemo(() => {
    if (grouping !== "direction" || !byDirection) return [];
    if (serverBoard) {
      return consumeBoardDirectionGroups({
        todos: windowTodos,
        board: serverBoard,
        matchesFilter: hasAnyFilter ? filterTodo : undefined,
        resolveFallbackName: directionLabel,
        ungroupedLabel: t("todos.direction_uncategorized"),
      });
    }
    if (!hasTreeDirections) return [];
    const openTodos = filteredUncompleted.filter((td) => placed.open.has(td.id));
    return resolveDirectionSections({
      tree: directionTree,
      openTodos,
      todosV2,
      resolveDirectionName: (d) =>
        todosV2 && d.secretary
          ? t("todos.direction_uncategorized")
          : directionTitle(d, leaderTitles?.find((c) => c.session_id === d.id)?.title),
      resolveFallbackName: directionLabel,
    });
  }, [grouping, byDirection, serverBoard, windowTodos, hasAnyFilter, search, listFilter, effectiveKindFilter, activeBlocker, tick, hasTreeDirections, directionTree, filteredUncompleted, placed, todosV2, leaderTitles, directionLabel]);
  // Group completed to-dos by effective direction under todos_v2 By-direction view (#17181).
  const completedDirectionBuckets = useMemo((): CompletedDirectionBucket[] => {
    if (!todosV2 || grouping !== "direction") return [];
    const buckets = new Map<string, CompletedDirectionBucket>();
    const serverCounts = new Map<string, number>();
    const localDirections = new Set([
      ...reorganizeDirections.map((d) => d.id),
      ...directionSections.filter((s) => !s.d.secretary).map((s) => s.d.id),
    ]);
    // The board counts the full history. A local search or filter can only count its loaded matches.
    for (const group of hasAnyFilter ? [] : serverBoard?.direction_groups ?? []) {
      // A merged board's ungrouped total spans assistants, and some named groups belong to other assistants.
      if (scope.several && !localDirections.has(group.direction_id)) continue;
      const id = group.direction_id || DONE_UNCATEGORIZED_ID;
      const uncategorized = id === DONE_UNCATEGORIZED_ID;
      const count = group.counts.done;
      serverCounts.set(id, count);
      if (count === 0) continue;
      const label = uncategorized ? t("todos.direction_uncategorized") : group.label?.trim() || directionLabel(id) || id;
      buckets.set(id, { id, label, uncategorized, count, todos: [] });
    }
    for (const td of filteredCompleted) {
      const dir = resolveDirection?.(td);
      const id = dir?.id || DONE_UNCATEGORIZED_ID;
      const uncategorized = id === DONE_UNCATEGORIZED_ID;
      let bucket = buckets.get(id);
      if (!bucket) {
        bucket = {
          id,
          label: uncategorized ? t("todos.direction_uncategorized") : dir!.label,
          uncategorized,
          todos: [],
          count: 0,
        };
        buckets.set(id, bucket);
      }
      bucket.todos.push(td);
      bucket.count = serverCounts.get(id) ?? bucket.todos.length;
    }
    const activeOrder = new Map<string, number>();
    directionSections.forEach((sec, idx) => {
      if (!sec.d.secretary && !activeOrder.has(sec.d.id)) {
        activeOrder.set(sec.d.id, idx);
      }
    });
    return [...buckets.values()].sort((a, b) => {
      if (a.uncategorized !== b.uncategorized) return a.uncategorized ? 1 : -1;
      const rankA = activeOrder.get(a.id);
      const rankB = activeOrder.get(b.id);
      if (rankA !== undefined && rankB !== undefined) return rankA - rankB;
      if (rankA !== undefined) return -1;
      if (rankB !== undefined) return 1;
      return a.label.localeCompare(b.label);
    });
  }, [todosV2, grouping, filteredCompleted, resolveDirection, directionSections, reorganizeDirections, hasAnyFilter, serverBoard, directionLabel, scope.several]);

  const completedCountByDirection = useMemo(() => {
    const counts = new Map<string, number>();
    for (const bucket of completedDirectionBuckets) {
      counts.set(bucket.id, bucket.count);
    }
    return counts;
  }, [completedDirectionBuckets]);
  const completedCount = todosV2 && grouping === "direction"
    ? completedDirectionBuckets.reduce((total, bucket) => total + bucket.count, 0)
    : boardStatus?.goalFold.triaged.finished ?? filteredCompleted.length;
  const hasCompleted = completedCount > 0 || filteredCompleted.length > 0;

  const activeDoneDirection = useMemo(
    () =>
      todosV2 &&
      grouping === "direction" &&
      doneDirectionFilter !== null &&
      completedDirectionBuckets.some((b) => b.id === doneDirectionFilter)
        ? doneDirectionFilter
        : null,
    [todosV2, grouping, doneDirectionFilter, completedDirectionBuckets],
  );

  const directionDoneFold = todosV2 && grouping === "direction";
  const doneOpen = directionDoneFold ? doneDirectionOpen === "1" : showDone;
  const reviewDirectionCompleted = useCallback((directionKey: string) => {
    setDoneDirectionFilter(directionKey);
    setDoneDirectionOpen("1");
    if (closedHistory.hasMore && !closedHistory.loading) closedHistory.onLoad();
    setDoneScrollSeq((n) => n + 1);
  }, [setDoneDirectionFilter, setDoneDirectionOpen, closedHistory]);

  useEffect(() => {
    if (doneScrollSeq === 0) return;
    doneFoldRef.current?.scrollIntoView?.({ behavior: scrollBehavior(), block: "nearest" });
  }, [doneScrollSeq]);

  // open to-dos no 常驻 section lists: the hub nests a worker's to-do under its leader's Agent instead of listing it
  // (server/internal/assistant/direction_tree.go), so the page says how many and where to see them rather than hide them
  const directionUnlisted = useMemo(() => {
    if (grouping !== "direction" || !byDirection || serverBoard) return 0;
    const listed = new Set(directionSections.flatMap((g) => g.todos.map((td) => td.id)));
    return filteredUncompleted.filter((td) => placed.open.has(td.id) && !listed.has(td.id)).length;
  }, [grouping, byDirection, serverBoard, directionSections, filteredUncompleted, placed]);
  // What 常驻 shows below the directions besides Done: the to-dos outside Done that do not count as open (closed in the
  // last day, accepted, deployed, inactive), which the tree never lists, in their status sections, as Android does
  // (`DirectionTree.todoRows`), so a to-do ticked here moves to Just finished rather than out of the list.
  const directionRest = useMemo(() => {
    if (grouping !== "direction" || !byDirection || serverBoard) return [];
    return TRIAGE_SECTIONS.filter((sec) => sec !== "done" && sec !== "cancelled")
      .map((sec) => ({
        sec,
        groups: sortTriageGroups(
          triaged.sections[sec].flatMap((g): TriageGroup[] => {
            const members = g.members.filter((m) => !placed.open.has(m.id));
            return members.length ? [{ ...g, lead: members[0], members }] : [];
          }),
          listSort,
        ),
      }))
      .filter((s) => s.groups.length > 0);
  }, [grouping, byDirection, triaged, placed, listSort]);
  // what waits on the user that the filter leaves out: the line over the sections says how many (#3740)
  const hiddenWaitingCount = useMemo(
    () => {
      if (listFilter === "all") return 0;
      const searched = todos.filter((td) => matchesSearch(td) && matchesKindFilter(td, effectiveKindFilter));
      const shown = (td: Todo) => matchesListFilter(td, listFilter, tick);
      return projectedNeeds
        ? searched.filter((td) => projectedNeeds.has(td.id) && matchesSharedBlocker(td, activeBlocker) && !shown(td)).length
        : hiddenWaiting(searched, shown, lookup, Date.now(), restored, superseded);
    },
    [todos, lookup, restored, search, listFilter, effectiveKindFilter, superseded, tick, projectedNeeds, activeBlocker],
  );
  const filterCounts = useMemo(() => listFilterCounts(todos.filter((td) => matchesSearch(td) && matchesKindFilter(td, effectiveKindFilter)), tick), [todos, search, effectiveKindFilter, tick]);
  const onRestore = useCallback(
    (id: string) => {
      restoreTodo(id);
      toast(t("todos.restored"), "ok");
    },
    [restoreTodo],
  );
  // Got it on a Just finished row (#5707, docs/assistant/todo-status-sop.md "Seen"): filed under Done at once, the hub
  // keeps the mark for every device, and the toast's Undo takes it off. Only while the hub keeps seen marks.
  const seenOn = useStore((s) => todoSeenOn(s.features));
  // Opening a Just finished to-do is seeing it too: it is filed under Done once the page leaves it (another to-do, the
  // list, another page), so its row does not move while it is read. Nothing is said: the user did not ask for it.
  const seenOnLeave = useRef<string | undefined>(undefined);
  // to-dos the user pressed Got it on here: their own choice (and its Undo) stands, leaving one marks nothing
  const decided = useRef(new Set<string>());
  // what the rows' callback reads when it runs, so it stays the same function and a push re-renders no row
  const seenInputs = useRef({ todos, lookup, restored, superseded });
  seenInputs.current = { todos, lookup, restored, superseded };
  const onSeen = useCallback((id: string) => {
    const failed = (e: unknown) => toast(t("todos.action_failed", { error: errorText(e) }), "error");
    // the mark shown before the hub answers is not earlier than the moment that put it in Just finished
    const { todos: list, lookup: look, restored: back, superseded: sup } = seenInputs.current;
    const td = list.find((x) => x.id === id);
    const after = td ? triage(td, look, Date.now(), back.get(id) ?? 0, sup.has(id)).last : 0;
    decided.current.add(id);
    if (seenOnLeave.current === id) seenOnLeave.current = undefined;
    setTodoSeen(id, true, after).catch(failed);
    toast(t("todos.seen_moved"), "ok", { label: t("lib-agents.undo"), run: () => void setTodoSeen(id, false).catch(failed) });
  }, []);
  const markDecidedUnseen = useCallback((id: string) => {
    decided.current.add(id);
    if (seenOnLeave.current === id) seenOnLeave.current = undefined;
  }, []);
  // Board is read-only: track a to-do opened while on Board so closing it (even via a URL change that drops
  // view=board before selectedId clears) never arms or fires the automatic seen-on-leave write.
  const boardOpenedId = useRef<string | undefined>(undefined);
  if (boardOn && selectedId !== undefined) {
    boardOpenedId.current = selectedId;
    if (seenOnLeave.current === selectedId) seenOnLeave.current = undefined;
  } else if (!boardOn && selectedId !== boardOpenedId.current) {
    boardOpenedId.current = undefined;
  }
  const openedFromBoard = boardOn || (selectedId !== undefined && boardOpenedId.current === selectedId);
  const opened = seenOn && !openedFromBoard && selectedId !== undefined ? todos.find((td) => td.id === selectedId) : undefined;
  const openedJustDone = opened !== undefined && triage(opened, lookup, Date.now(), restored.get(opened.id) ?? 0, superseded.has(opened.id)).section === "just_done";
  useEffect(() => {
    if (openedJustDone && !decided.current.has(selectedId!)) seenOnLeave.current = selectedId;
  }, [openedJustDone, selectedId]);
  useEffect(() => {
    const id = selectedId;
    return () => {
      if (id === undefined || seenOnLeave.current !== id || boardOpenedId.current === id) return;
      seenOnLeave.current = undefined;
      void setTodoSeen(id, true).catch(() => {}); // it stays in Just finished, as if never opened
    };
  }, [selectedId]);

  // List and detail side by side (md and up, Tailwind's max-md: below it they take turns, as on a phone)
  const split = !useMedia(ONE_PANE);
  // Active selected todo
  const activeTodo = useMemo(() => {
    // taking turns, the open to-do is the address's: one left by Back stays closed, and a link opens its own at once
    const id = split ? activeId : selectedId;
    if (id) {
      const found = todos.find((t) => t.id === id);
      if (found) return found;
    }
    // taking turns, nothing is open until a row is: no first row stands in, marked current with its detail loading unseen
    if (requestMode || boardOn || !split) return undefined;
    if (byStatus) {
      // the first row of an open section; none rather than one that would unfold a folded section (Shelved, Done)
      for (const sec of statusSectionOrder) if (!(todosV2 ? sectionStartsFoldedV2(sec) : sectionStartsFolded(sec, displayTriaged.sections[sec], tick)) && displayTriaged.sections[sec].length) return displayTriaged.sections[sec][0].lead;
      return undefined;
    }
    // by 常驻: the first row shown (a direction's, else an unfolded status section's below them), else history
    if (grouping === "direction") return directionSections[0]?.todos[0] ?? directionRest.find((s) => !FOLDED_SECTIONS.has(s.sec))?.groups[0].lead ?? filteredCompleted[0] ?? filteredCancelled[0];
    return filteredUncompleted[0] ?? filteredCompleted[0] ?? filteredCancelled[0];
  }, [activeId, selectedId, todos, filteredUncompleted, filteredCompleted, filteredCancelled, byStatus, requestMode, boardOn, split, statusSectionOrder, todosV2, displayTriaged, grouping, directionSections, directionRest, tick]);

  // The to-do open beside the calendar: the address's, while it is there.
  const selectedTodo = calendarOn && selectedId ? todos.find((td) => td.id === selectedId) : undefined;

  // Active agents of current selected todo
  const activeAgents = useMemo(() => {
    return (activeTodo?.agents ?? []).filter((a) => !a.released_at);
  }, [activeTodo]);

  const releasedAgents = useMemo(() => {
    return (activeTodo?.agents ?? []).filter((a) => a.released_at);
  }, [activeTodo]);

  // Narrow (< md): the list and a todo's detail take turns, as on a phone (iOS pushes TodoDetailView). The URL's ?id=
  // says a todo was opened; back goes to the list.
  const detailOnNarrow = Boolean(selectedId && todos.some((t) => t.id === selectedId));
  // The id a row click in this page pushed (/todos → /todos?id=…). Back from that detail pops the entry, as iOS pops
  // its NavigationStack, so the browser's Back does not return to the detail just left; a deep link replaces instead.
  const pushedId = useRef<string | undefined>(undefined);
  const listColumn = useRef<HTMLDivElement>(null);
  const listBox = useRef<HTMLDivElement>(null);
  const detailColumn = useRef<HTMLDivElement>(null);
  // The to-do a row opened and where the list was scrolled then: on a phone's width the list is hidden under the
  // detail, and the focus on its row goes with it (audit item 74).
  const fromList = useRef<{ id: string; top: number } | undefined>(undefined);
  // stable across pushes, so the memoized rows keep the same props
  const openFromList = useCallback(
    (id: string) => {
      setActiveId(id);
      if (id === selectedId) return; // already this entry (a deep link stays a deep link)
      pushedId.current = id;
      fromList.current = { id, top: listBox.current?.scrollTop ?? 0 };
      navigate(boardOn ? boardRoute(id) : listRoute(id));
    },
    [selectedId, boardOn, listRoute],
  );
  const backToList = () => {
    if (selectedId && pushedId.current === selectedId) {
      pushedId.current = undefined;
      history.back();
    } else navigate(boardOn ? boardRoute() : listRoute(), true);
  };
  const back = useRef(backToList);
  back.current = backToList;
  // On a phone's width a to-do opened from the list takes the focus to the detail's Back, as a page pushed on iOS
  // does; back on the list (Back, Esc, the browser's Back), the list is scrolled where it was and the focus is on that
  // to-do's row again. Side by side, the focus stays on the row and nothing moves.
  // A to-do shown over the hidden list, a deep link's too: back on the list, the focus goes to its row, not the page.
  const shownOverList = useRef<string | undefined>(undefined);
  useLayoutEffect(() => {
    const list = listColumn.current;
    if (!list) return;
    const from = fromList.current;
    if (detailOnNarrow) {
      const hidden = !list.getClientRects().length;
      shownOverList.current = hidden ? selectedId : undefined;
      if (!from || selectedId !== from.id) return;
      if (!hidden) fromList.current = undefined;
      else detailColumn.current?.querySelector<HTMLElement>("[data-testid=todos-back] button")?.focus();
      return;
    }
    const id = from?.id ?? shownOverList.current;
    fromList.current = undefined;
    shownOverList.current = undefined;
    if (from && listBox.current) listBox.current.scrollTop = from.top;
    if (id) {
      const row = [...list.querySelectorAll<HTMLElement>("[data-todo]")].find((el) => el.dataset.todo === id);
      (row?.querySelector<HTMLElement>("[data-testid=todo-row-open]") ?? row)?.focus();
    }
  }, [detailOnNarrow, selectedId]);
  // Esc on a phone's width goes back to the list, as Back does; a dialog or a menu over the page and a field being
  // edited keep it.
  useEffect(() => {
    if (!detailOnNarrow) return;
    const onKey = (e: KeyboardEvent) => {
      const list = listColumn.current;
      if (e.key !== "Escape" || e.defaultPrevented || e.isComposing || e.keyCode === 229 || !list || list.getClientRects().length) return;
      if ([...document.querySelectorAll("[role=menu],[role=dialog],[role=alertdialog]")].some((el) => !el.closest("[data-closing],[inert]")) || (e.target as Element | null)?.closest?.("input,textarea,select,[contenteditable=true]")) return;
      e.preventDefault();
      back.current();
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [detailOnNarrow]);

  // "By request" and Board: the detail closes and the graph or board has the page again
  const closeDetail = () => {
    setActiveId(undefined);
    navigate(boardOn ? boardRoute() : listRoute(), true);
  };

  // `open`: the row's checkbox shows it open (`checksAsOpen`): a failed start waiting on the owner is ticked to close it
  const toggleDone = useCallback(async (todo: Todo, open = !todo.done_at) => {
    if (open && todo.kind === "concern") {
      setReportingConcern(todo);
      return;
    }
    try {
      await updateTodo(todo.id, { done: open });
      await refreshTodos();
      await refresh();
      toast(open ? t("todos.completed") : t("todos.reopened"), "ok");
    } catch (e) {
      toast(t("todos.action_failed", { error: errorText(e) }), "error");
    }
  }, []);

  // a to-do dragged to another time or day on the calendar (#2045): saved at once, said when it fails (it goes back)
  const moveTodo = useCallback(async (id: string, patch: TodoPatch) => {
    try {
      await updateTodo(id, patch);
      await refreshTodos();
    } catch (e) {
      toast(t("todos.calendar_move_failed", { error: errorText(e) }), "error");
    }
  }, []);

  // a priority, due date or reminder chosen in a row's menu: saved at once, as the to-do's page saves its fields; false
  // when it failed (said in a toast)
  const savePlan = async (id: string, patch: TodoPatch): Promise<boolean> => {
    try {
      await updateTodo(id, patch);
      await refreshTodos();
      return true;
    } catch (e) {
      toast(t("todos.save_plan_failed", { error: errorText(e) }), "error");
      return false;
    }
  };
  // `id`: a row's to-do; none: the open one, as the page's Hand to agent
  const openHandover = (id?: string) => {
    setHandoverId(id);
    setHandoverOpen(true);
  };
  const askDelete = (id: string) => {
    setDeleteId(id);
    setDeleteConfirm(true);
  };
  // stable, so the memoized rows keep the same props
  const openRowMenu = useCallback((todo: Todo, at: MenuAt, open: boolean) => setRowMenu({ id: todo.id, at, open }), []);
  const menuTodo = rowMenu ? todos.find((x) => x.id === rowMenu.id) : undefined;
  const handoverTodo = handoverId ? todos.find((x) => x.id === handoverId) : activeTodo;
  const deleteTodoShown = todos.find((x) => x.id === deleteId);
  const dueTodo = todos.find((x) => x.id === dueFor);
  // the to-do went (deleted elsewhere, another assistant): its menu and the dialogs opened for it go with it, and do
  // not come back with it
  useEffect(() => {
    if (rowMenu && !menuTodo) setRowMenu(null);
    if (handoverId && !handoverTodo) setHandoverOpen(false);
    if (deleteId && !deleteTodoShown) setDeleteConfirm(false);
    if (dueFor && !dueTodo) setDueFor(undefined);
  }, [rowMenu, menuTodo, handoverId, handoverTodo, deleteId, deleteTodoShown, dueFor, dueTodo]);

  const retryTodos = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await refreshTodos();
    } finally {
      setRetrying(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTodoShown || deleting) return;
    const id = deleteTodoShown.id;
    setDeleting(true);
    try {
      await deleteTodo(id);
    } catch (e) {
      toast(t("todos.delete_failed", { error: errorText(e) }), "error");
      return;
    } finally {
      setDeleting(false);
    }
    // The todo is gone on the hub: close and drop it now, not after refresh() (seconds on a busy hub), so it cannot
    // be deleted a second time (the hub answers 404 for it).
    dropTodo(id);
    setDeleteConfirm(false);
    // a row's menu can delete another to-do than the one open: that one stays open
    if (id === activeTodo?.id || id === selectedId) {
      setActiveId(undefined);
      pushedId.current = undefined;
      // replace: Back must not land on ?id=<the deleted todo>
      navigate(boardOn ? boardRoute() : calendarOn ? calendarRoute(calendarMode, anchor) : listRoute(), true);
    }
    toast(t("todos.deleted"), "ok");
    // the rest of the page catches up in the background; a failed refresh leaves the list as dropped, the next push fixes it
    void refreshTodos().then(() => refresh()).catch(() => {});
  };

  return (
    <div className="flex h-full flex-col bg-surface">
      {/* Top Header: every page's height and side padding (#16462), so the title and its line stay put between pages */}
      <header className="flex h-14 shrink-0 items-center justify-between border-b border-line bg-surface px-3 sm:px-4">
        <div className="flex items-center gap-2">
          {onToggleSidebar && <IconButton icon="sidebar" label={t("agents-agentsview.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />}
          <div className={cx("flex gap-2", todosV2 ? "items-center" : "items-baseline")}>
            <h1 className="text-[16px] font-semibold whitespace-nowrap text-ink">{t("todos.title")}</h1>
            {/* the counts once the list has its to-dos (audit item 83): not while it loads, nor after an empty failed read */}
            {(!boardOn || serverBoard) && (todosLoaded || todos.length > 0) && !(todos.length === 0 && todosError) && (
              todosV2 ? (
                headerNeedsYouCount > 0 ? (
                  <span
                    data-testid="todos-header-attention"
                    data-count={headerNeedsYouCount}
                    className="inline-flex h-6 shrink-0 items-center gap-1.5 rounded-full bg-orange-tint px-2.5 text-submeta font-medium whitespace-nowrap text-attention tabular-nums max-sm:hidden"
                  >
                    <span className="size-1.5 shrink-0 rounded-full bg-attention" aria-hidden />
                    {t("todos.v2_header_needs_you", { count: headerNeedsYouCount })}
                  </span>
                ) : null
              ) : (
                // a phone's header has the List | Calendar switch to fit (#2029): the counts stay on wider screens
                <span className="text-[12px] whitespace-nowrap text-ink-3 max-sm:hidden" data-testid="todos-summary">
                  {/* one count in every grouping (audit item 12): open as countsAsOpen counts it, section counts as their
                      titles do, with Cancelled always separate from Done (#11085, #5732) */}
                  {t(listFilter === "all" && effectiveKindFilter === "all" && !activeBlocker ? "todos.summary_triage" : "todos.summary_filtered", {
                    filter: [effectiveKindFilter !== "all" ? kindFilterLabel(effectiveKindFilter) : "", listFilter !== "all" ? filterLabel(listFilter) : "", activeBlocker?.label ?? ""].filter(Boolean).join(" · "),
                    open: triageAll.open,
                    parked: sectionSize(triageAll, "parked"),
                    shelved: sectionSize(triageAll, "shelved"),
                    done: sectionSize(triageAll, "done"),
                    cancelled: sectionSize(triageAll, "cancelled"),
                  })}
                </span>
              )
            )}
          </div>
        </div>
        <div className="flex items-center gap-2.5">
          {hasTreeDirections && (todosV2 ? (
            <button
              type="button"
              data-testid="todos-direction-details-trigger"
              aria-label={t("external-sources.direction_details")}
              aria-haspopup="menu"
              aria-expanded={Boolean(directionMenu.anchor)}
              title={t("external-sources.direction_details")}
              onClick={directionMenu.open}
              className={cx(
                "touch-hit inline-flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full border border-line bg-inset px-2.5 text-submeta font-medium text-ink-2 transition-colors hover:bg-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-accent",
                Boolean(directionMenu.anchor) && "bg-hover text-ink",
              )}
            >
              <Icon name="target" size={12} stroke={2} className="shrink-0 text-ink-3" />
              <span className="max-sm:hidden">{t("todos.header_directions")}</span>
              <Icon name="chevron" size={10} stroke={2.2} className="shrink-0 text-ink-3 max-sm:hidden" />
            </button>
          ) : (
            <IconButton icon="target" label={t("external-sources.direction_details")}
              onClick={directionMenu.open} active={Boolean(directionMenu.anchor)} />
          ))}
          {hasTreeDirections && directionMenu.anchor && <Menu anchor={directionMenu.anchor} onClose={directionMenu.close} align="right"
            items={directionTree.directions.filter((direction) => !direction.secretary).map((direction) => ({
              key: direction.id,
              label: direction.name || leaderTitles?.find((row) => row.session_id === direction.id)?.title || t("external-sources.untitled"),
              onSelect: () => navigate({ name: "todos", direction: direction.id, manager: repositoryManager }),
            }))} />}
          {todosV2 && (
            <a
              href={settingsHref("labs", null, false)}
              data-testid="todos-v2-labs-link"
              title={t("todos.v2_labs_badge_hint")}
              onClick={(e) => {
                e.preventDefault();
                navigate(settingsHref("labs", null, false));
              }}
              className="touch-hit inline-flex h-7 shrink-0 items-center gap-1 whitespace-nowrap rounded-full border border-line bg-inset px-2.5 text-submeta font-medium text-ink-2 transition-colors hover:bg-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-accent max-sm:hidden"
            >
              <Icon name="sparkles" size={12} className="shrink-0 text-accent-ink" />
              <span>{t("todos.v2_labs_badge")}</span>
            </a>
          )}
          <Segmented
            label={t("todos.view_label")}
            value={boardOn ? "board" : calendarOn ? "calendar" : "list"}
            options={[
              { v: "list", label: t("todos.view_list") },
              { v: "calendar", label: t("todos.view_calendar") },
              { v: "board", label: t("todos.view_board") },
            ]}
            onChange={showView}
            compact
          />
          {/* on a phone the plus alone, so the label never wraps beside the switch */}
          <IconButton icon="plus" label={t("todos.new_todo")} onClick={openNew} className="sm:hidden" />
          <Button tone="secondary" icon="plus" onClick={openNew} className="h-8 text-[12.5px] max-sm:hidden">
            {t("todos.new_todo")}
          </Button>
        </div>
      </header>

      {calendarOn && offline && todosLoaded && todos.length === 0 ? (
        <div className="flex min-h-0 flex-1 items-start justify-center bg-page">
          <OfflineState offline={offline} className="py-14" />
        </div>
      ) : calendarOn ? (
        <div className="flex min-h-0 flex-1">
          <div className={cx("flex min-h-0 min-w-0 flex-1 flex-col bg-page", selectedTodo && "max-md:hidden")}>
            <TodoCalendar
              todos={todos}
              lookup={lookup}
              mode={calendarMode}
              anchor={anchor}
              showDone={calendarDone}
              todosV2={todosV2}
              selectedId={selectedId}
              onMode={(m, day) => {
                setPref(MODE_KEY, m);
                if (todosV2) setPref(CALENDAR_DAY_KEY, dayKey(day ?? anchor));
                navigate(calendarRoute(m, day ?? anchor, selectedId, todosV2 ? selectedManager : undefined));
              }}
              onAnchor={(day) => {
                if (todosV2) setPref(CALENDAR_DAY_KEY, dayKey(day));
                navigate(calendarRoute(calendarMode, day, selectedId, todosV2 ? selectedManager : undefined), true);
              }}
              onShowDone={(on) => {
                setPref(DONE_KEY, on ? "1" : "0");
                setCalendarDone(on);
              }}
              onOpen={(id) => navigate(calendarRoute(calendarMode, anchor, id))}
              onAdd={(slot) => {
                setNewSlot(slot);
                setNewOpen(true);
              }}
              onShowList={() => showView("list")}
              onMove={moveTodo}
            />
          </div>
          {selectedTodo && (
            <div data-testid="todo-calendar-detail" className="flex min-h-0 w-full flex-col overflow-y-auto border-line md:w-[440px] md:shrink-0 md:border-l">
              <div className="flex shrink-0 items-center justify-between px-2 pt-2">
                <Button tone="quiet" icon="chevronLeft" onClick={() => navigate(calendarRoute(calendarMode, anchor))} className="md:hidden">
                  {t("todos.calendar_back")}
                </Button>
                <IconButton icon="x" label={t("todos.calendar_close")} onClick={() => navigate(calendarRoute(calendarMode, anchor))} className="ml-auto max-md:hidden" />
              </div>
              <TodoDetail
                key={`${owner}/${selectedTodo.id}`}
                owner={owner}
                todo={selectedTodo}
                activeAgents={(selectedTodo.agents ?? []).filter((a) => !a.released_at)}
                releasedAgents={(selectedTodo.agents ?? []).filter((a) => a.released_at)}
                lookup={lookup}
                machines={machines}
                interactions={interactions}
                showReleased={showReleased}
                onToggleReleased={() => setShowReleased((v) => !v)}
                open={checksAsOpen(selectedTodo, sectionOf.get(selectedTodo.id))}
                onToggleDone={(open) => toggleDone(selectedTodo, open)}
                onOpenHandover={() => openHandover()}
                onOpenTodo={(id) => navigate(calendarRoute(calendarMode, anchor, id))}
                onDeleteClick={() => askDelete(selectedTodo.id)}
                todosV2={todosV2}
                allTodos={todosV2 ? todos : undefined}
                parentTodo={todosV2 && selectedTodo.part_of ? todosById.get(selectedTodo.part_of) : undefined}
                childTodos={todosV2 ? todos.filter((td) => td.part_of === selectedTodo.id) : undefined}
                effectiveDirection={todosV2 ? resolveDirection?.(selectedTodo) : undefined}
                resolveChildDirection={resolveDirection}
                directions={todosV2 ? reorganizeDirections : undefined}
                onReorganizeDirection={todosV2 ? applyDetailReorganize : undefined}
                onUnseen={todosV2 ? markDecidedUnseen : undefined}
                mergeTrainBadge={mergeTrainByTodo?.[selectedTodo.id]}
              />
            </div>
          )}
        </div>
      ) : (
      // Main Split Layout: Left List & Right Detail
      <div className="flex min-h-0 flex-1">
        {/* Left Column: Todo List */}
        <div
          ref={listColumn}
          data-testid="todos-list-column"
          data-narrow-hidden={detailOnNarrow || undefined}
          // the edge parts the list from the detail beside it; on a phone the list is the whole page and has none
          className={cx(
            "flex w-full flex-col border-line",
            (requestMode || boardOn) ? "min-w-0 md:flex-1" : todosV2 ? "shrink-0 md:w-[356px] lg:w-[368px]" : "shrink-0 md:w-[380px] lg:w-[400px]",
            (!(requestMode || boardOn) || activeTodo) && "md:border-r",
            detailOnNarrow && "max-md:hidden",
          )}
        >
          {/* Search Bar / Compact Toolbar (List only; Board has no hidden filter) */}
          {!boardOn && (todosV2 ? (
            <div className="flex flex-col gap-1.5 px-2.5 py-2" data-testid="todos-v2-toolbar">
              {!requestMode && (
                <div className="flex items-center gap-1.5">
                  <div className="min-w-0 flex-1">
                    <SearchField tone="inset" value={search} onChange={setSearch} placeholder={t("todos.search_placeholder")} />
                  </div>
                  <TodoListControls filter={listFilter} sort={listSort} counts={filterCounts} onFilter={chooseFilter} onSort={chooseSort} />
                  <button
                    type="button"
                    aria-label={reorganizeMode ? t("todos.reorganize_done") : t("todos.reorganize_toggle")}
                    title={reorganizeMode ? t("todos.reorganize_done") : t("todos.reorganize_toggle")}
                    aria-pressed={reorganizeMode}
                    data-testid="todos-reorganize-toggle"
                    onClick={toggleReorganizeMode}
                    className={cx(
                      "touch-hit flex size-7 shrink-0 items-center justify-center rounded-control transition-colors focus-visible:outline-2 focus-visible:outline-accent",
                      reorganizeMode
                        ? "bg-accent-ink text-surface hover:brightness-105"
                        : "text-ink-3 hover:bg-hover hover:text-ink",
                    )}
                  >
                    <Icon name="layers" size={14} />
                  </button>
                </div>
              )}
              <div className="flex items-center gap-1.5">
                {!requestMode && (
                  <div
                    role="radiogroup"
                    aria-label={t("todos.kind_filter_label")}
                    data-testid="todos-kind-filter"
                    className="inline-flex h-8 shrink-0 items-center gap-0.5 rounded-control bg-inset p-0.5 shadow-hairline"
                  >
                    {(["all", "todos", "tasks"] as const).map((k) => {
                      const active = effectiveKindFilter === k;
                      return (
                        <button
                          key={k}
                          type="button"
                          role="radio"
                          aria-checked={active}
                          data-selected={active}
                          data-kind={k}
                          onClick={() => setKindFilter(k)}
                          className={cx(
                            "touch-hit h-7 rounded-chip px-2 text-submeta whitespace-nowrap transition-colors focus-visible:outline-2 focus-visible:outline-accent",
                            active ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-3 hover:text-ink-2",
                          )}
                        >
                          {kindFilterLabel(k)}
                        </button>
                      );
                    })}
                  </div>
                )}
                <div className={cx("flex shrink-0 items-center", !requestMode && "ml-auto")}>
                  <TodoGroupingMenu
                    grouping={grouping}
                    showDirection={byDirection || directionPending}
                    showRequest={byRequest || (chosenGrouping === "request" && !todosLoaded)}
                    onChange={(next) => {
                      setPref(GROUPING_KEY, next);
                      setGrouping(next);
                    }}
                  />
                </div>
              </div>
            </div>
          ) : (
            <div className="flex items-center gap-2 p-3">
              {/* the graph shows every request: a search or a due-date filter has nothing to narrow there */}
              {!requestMode && (
                <>
                  <div className="min-w-0 flex-1">
                    <SearchField tone="inset" value={search} onChange={setSearch} placeholder={t("todos.search_placeholder")} />
                  </div>
                  <TodoListControls filter={listFilter} sort={listSort} counts={filterCounts} onFilter={chooseFilter} onSort={chooseSort} />
                </>
              )}
              <Segmented
                label={t("todos.grouping_label")}
                value={grouping}
                options={[
                  { v: "status", label: t("todos.grouping_status") },
                  { v: "folder", label: t("todos.grouping_folder") },
                  ...(byDirection || directionPending ? [{ v: "direction" as const, label: t("todos.grouping_direction") }] : []),
                  ...(byRequest || chosenGrouping === "request" && !todosLoaded ? [{ v: "request" as const, label: t("todos.grouping_request") }] : []),
                ]}
                onChange={(next) => {
                  setPref(GROUPING_KEY, next);
                  setGrouping(next);
                }}
                compact
              />
            </div>
          ))}
          {!boardOn && todosV2 && !requestMode && decisionQueueItems.length >= 2 && (
            <div className="px-3 pb-2" data-testid="todos-decision-queue-strip">
              <button
                type="button"
                data-testid="todos-decision-queue-open"
                onClick={() => setDecisionQueueOpen(true)}
                className="touch-hit flex w-full items-center justify-between gap-2 rounded-control border border-attention/30 bg-orange-tint px-2.5 py-1.5 text-left text-submeta font-medium text-attention transition-colors hover:brightness-95 focus-visible:outline-2 focus-visible:outline-accent"
              >
                <span className="inline-flex min-w-0 items-center gap-1.5">
                  <Icon name="hand" size={12} stroke={2.2} className="shrink-0" />
                  <span className="truncate whitespace-nowrap">
                    {t("todos.decision_queue_open", { count: decisionQueueItems.length })}
                  </span>
                </span>
                <Icon name="chevron" size={11} className="shrink-0 -rotate-90 opacity-70" />
              </button>
            </div>
          )}
          {!boardOn && todosV2 && !requestMode && sharedBlockers.length > 0 && (
            <SharedBlockersStrip
              blockers={sharedBlockers}
              selectedKey={activeBlocker?.key ?? null}
              onSelect={setSelectedBlockerKey}
              onOpenTodo={openFromList}
              openableTodoIds={visibleTodoIds}
            />
          )}
          {!boardOn && todosV2 && !requestMode && reorganizeMode && (
            <BatchReorganizeBar
              todos={todos}
              visibleIds={visibleReorganizeIds}
              selectedIds={reorganizeSelected}
              directions={reorganizeDirections}
              submitting={reorganizeSubmitting}
              error={reorganizeError}
              feedback={reorganizeFeedback}
              onSelectAll={selectAllReorganize}
              onClearSelection={clearReorganizeSelection}
              onApply={applyReorganize}
              onExit={exitReorganizeMode}
            />
          )}

          {/* List Content */}
          {boardOn ? (
            <TodoListScroller box={listBox}>
              {!todosLoaded && todos.length === 0 ? (
                <div className="flex justify-center py-16" data-testid="todos-loading"><Loading /></div>
              ) : todos.length === 0 && todosError ? (
                <Empty
                  icon="todo"
                  title={t("todos.load_failed")}
                  action={<Button tone="secondary" icon="retry" busy={retrying} onClick={() => void retryTodos()}>{t("todos.retry")}</Button>}
                >
                  {todosError}
                </Empty>
              ) : offline && todos.length === 0 ? (
                <OfflineState offline={offline} ring="ring-surface" className="py-14" />
              ) : boardProjection && (
                <TodoBoard
                  todos={boardProjection.todos}
                  lookup={lookup}
                  triages={boardProjection.triages}
                  serverBoard={serverBoard}
                  projectedNeeds={projectedNeeds}
                  projectName={projectName}
                  tick={tick}
                  onOpen={openFromList}
                  todosV2={todosV2}
                  allTodos={todosV2 ? todos : undefined}
                  effectiveDirectionOf={todosV2 ? resolveDirection : undefined}
                />
              )}
            </TodoListScroller>
          ) : requestMode ? (
            <Suspense fallback={<div className="flex justify-center py-16"><Loading /></div>}>
              <TodoRequestGraph todos={todos} lookup={lookup} machines={machines} selectedId={activeTodo?.id} onOpenTodo={openFromList} />
            </Suspense>
          ) : (
          <ReorganizeVisibilityContext.Provider value={reorganizeRegistry}>
          <TodoListScroller box={listBox}>
            {(!todosLoaded && todos.length === 0) || (grouping === "direction" && directionPending) ? (
              <div className="flex justify-center py-16" data-testid="todos-loading">
                <Loading />
              </div>
            ) : todos.length === 0 && todosError ? (
              <Empty
                icon="todo"
                title={t("todos.load_failed")}
                action={
                  <Button tone="secondary" icon="retry" busy={retrying} onClick={() => void retryTodos()}>
                    {t("todos.retry")}
                  </Button>
                }
              >
                {todosError}
              </Empty>
            ) : (filteredUncompleted.length === 0 || (grouping === "direction" && directionSections.length === 0 && directionUnlisted === 0 && directionRest.length === 0)) && !hasCompleted && filteredCancelled.length === 0 && !hasOlderClosed ? (
              offline && todos.length === 0 ? (
                <OfflineState offline={offline} ring="ring-surface" className="py-14" />
              ) : search.trim() ? (
                <Empty icon="search" title={t("todos.no_match_title")}>
                  {t("todos.no_match_body")}
                </Empty>
              ) : listFilter !== "all" ? (
                // the filter left nothing (#3740): which, and the way back to every to-do
                <>
                  {hiddenWaitingCount > 0 && <HiddenWaitingLine count={hiddenWaitingCount} filter={listFilter} onShowAll={() => chooseFilter("all")} />}
                  <Empty
                    icon="calendar"
                    title={t(`todos.filter_empty_${listFilter}`)}
                    action={
                      <Button tone="secondary" onClick={() => chooseFilter("all")}>
                        {t("todos.filter_show_all")}
                      </Button>
                    }
                  >
                    {t("todos.filter_empty_body")}
                  </Empty>
                </>
              ) : activeBlocker ? (
                <Empty
                  icon="alertCircle"
                  title={t("todos.shared_blockers_empty")}
                  action={
                    <Button tone="secondary" onClick={() => setSelectedBlockerKey(null)}>
                      {t("todos.shared_blockers_clear")}
                    </Button>
                  }
                >
                  {t("todos.filter_empty_body")}
                </Empty>
              ) : effectiveKindFilter !== "all" ? (
                <Empty
                  icon="todo"
                  title={t(`todos.kind_empty_${effectiveKindFilter}`)}
                  action={
                    <Button tone="secondary" onClick={() => setKindFilter("all")}>
                      {t("todos.filter_show_all")}
                    </Button>
                  }
                >
                  {t("todos.filter_empty_body")}
                </Empty>
              ) : (
                <Empty
                  icon="todo"
                  title={t("todos.empty_title")}
                  action={
                    <Button tone="accent" icon="plus" onClick={() => setNewOpen(true)}>
                      {t("todos.new_todo")}
                    </Button>
                  }
                >
                  {t("todos.empty_body")}
                </Empty>
              )
            ) : (
              byStatus ? (
              <div className="flex flex-col gap-1" data-testid="todos-sections" data-todo-source={serverBoard ? "board" : fromView ? "view" : undefined}>
                {hiddenWaitingCount > 0 && <HiddenWaitingLine count={hiddenWaitingCount} filter={listFilter} onShowAll={() => chooseFilter("all")} />}
                {statusSectionOrder.map((sec) =>
                  displayTriaged.sections[sec].length === 0 && !((sec === "done" || sec === "cancelled") && hasOlderClosed) ? null : (
                    <TodoTriageSection
                      key={sec}
                      section={sec}
                      // Under todos_v2 (#14830), low-noise sections (parked, shelved, done, cancelled) start compactly folded;
                      // legacy mode keeps fresh outcomes open.
                      folded={todosV2 ? sectionStartsFoldedV2(sec) : sectionStartsFolded(sec, displayTriaged.sections[sec], tick)}
                      groups={sortTriageGroups(displayTriaged.sections[sec], listSort)}
                      count={boardStatus ? boardStatus.sectionCounts[sec] : sectionSize(displayTriaged, sec)}
                      views={views}
                      goalFold={goalFold}
                      todosById={todosV2 ? todosById : undefined}
                      reorganize={reorganizeProps}
                      slim={todosV2}
                      mergeTrainByTodo={todosV2 ? mergeTrainByTodo : undefined}
                      selectedId={activeTodo?.id}
                      onSelect={openFromList}
                      onToggleDone={toggleDone}
                      onRestore={onRestore}
                      onSeen={seenOn ? onSeen : undefined}
                      onMenu={openRowMenu}
                      closedHistory={sec === "done" || sec === "cancelled" ? closedHistory : undefined}
                    />
                  ),
                )}
              </div>
              ) : (
              <div className="flex flex-col gap-1">
                {grouping === "direction" ? (
                  <>
                    {directionSections.map((g) => {
                      const dirKey = g.d.secretary ? DONE_UNCATEGORIZED_ID : g.d.id;
                      return (
                        <TodoDirectionSection
                          key={g.d.id}
                          direction={g.d}
                          title={g.title}
                          todos={g.todos}
                          views={views}
                          allTodosById={todosV2 ? todosById : undefined}
                          sectionOf={todosV2 ? sectionOf : undefined}
                          reorganize={reorganizeProps}
                          slim={todosV2}
                          foldChildren={foldChildren}
                          mergeTrainByTodo={todosV2 ? mergeTrainByTodo : undefined}
                          completedCount={todosV2 ? (completedCountByDirection.get(dirKey) ?? 0) : 0}
                          onReviewCompleted={todosV2 ? reviewDirectionCompleted : undefined}
                          selectedId={activeTodo?.id}
                          onSelect={openFromList}
                          onToggleDone={toggleDone}
                          onMenu={openRowMenu}
                        />
                      );
                    })}
                    {directionUnlisted > 0 && (
                      <p data-testid="todos-direction-unlisted" className="px-2 pt-1 text-[12px] text-ink-3">
                        {t("todos.direction_unlisted", { count: directionUnlisted })}
                      </p>
                    )}
                    {directionRest.map(({ sec, groups }) => (
                      <TodoTriageSection
                        key={sec}
                        section={sec}
                        folded={todosV2 ? sectionStartsFoldedV2(sec) : FOLDED_SECTIONS.has(sec)}
                        groups={groups}
                        count={groups.reduce((n, g) => n + g.members.length, 0)}
                        views={views}
                        todosById={todosV2 ? todosById : undefined}
                        reorganize={reorganizeProps}
                        slim={todosV2}
                        mergeTrainByTodo={todosV2 ? mergeTrainByTodo : undefined}
                        selectedId={activeTodo?.id}
                        onSelect={openFromList}
                        onToggleDone={toggleDone}
                        onRestore={onRestore}
                        onSeen={seenOn ? onSeen : undefined}
                        onMenu={openRowMenu}
                      />
                    ))}
                  </>
                ) : projectGroups.map((g) => (
                  <TodoProjectSection
                    key={g.key}
                    group={g}
                    views={sectionViews.get(g.key)!}
                    allTodosById={todosV2 ? todosById : undefined}
                    sectionOf={todosV2 ? sectionOf : undefined}
                    reorganize={reorganizeProps}
                    slim={todosV2}
                    foldChildren={foldChildren}
                    mergeTrainByTodo={todosV2 ? mergeTrainByTodo : undefined}
                    selectedId={activeTodo?.id}
                    hideUnsortedHeader={todosV2}
                    onSelect={openFromList}
                    onToggleDone={toggleDone}
                    onSeen={seenOn ? onSeen : undefined}
                    onMenu={openRowMenu}
                  />
                ))}

                {/* Completed Fold */}
                {(hasCompleted || hasOlderClosed) && (
                  <div ref={doneFoldRef} className="mt-2 border-t border-line/60 pt-2">
                    <button
                      type="button"
                      data-testid="todos-done-fold"
                      aria-expanded={doneOpen}
                      onClick={() => {
                        if (!doneOpen && closedHistory.hasMore) closedHistory.onLoad();
                        if (directionDoneFold) setDoneDirectionOpen(doneOpen ? "0" : "1");
                        else setShowDone((v) => !v);
                      }}
                      className="flex h-8 w-full items-center justify-between rounded-control px-2.5 text-[12px] text-ink-3 transition-colors hover:bg-hover hover:text-ink-2"
                    >
                      <span>{t("todos.done_section", { count: completedCount })}</span>
                      <Icon
                        name="chevron"
                        size={12}
                        className={cx("transition-transform duration-200", doneOpen ? "rotate-180" : "")}
                      />
                    </button>
                    {doneOpen && (
                      <div className="flex flex-col gap-1 pt-1">
                        {todosV2 && grouping === "direction" && completedDirectionBuckets.length > 0 ? (
                          <>
                            <div
                              role="group"
                              aria-label={t("todos.done_direction_filter_label")}
                              data-testid="todos-done-direction-filters"
                              className="flex flex-wrap items-center gap-1 px-1 pb-1"
                            >
                              <button
                                type="button"
                                data-testid="todos-done-direction-chip"
                                data-direction-id="all"
                                aria-pressed={activeDoneDirection === null}
                                onClick={() => setDoneDirectionFilter(null)}
                                className={cx(
                                  "touch-hit inline-flex h-6 max-w-full shrink-0 items-center gap-1 rounded-full px-2 text-submeta whitespace-nowrap tabular-nums transition-colors focus-visible:outline-2 focus-visible:outline-accent",
                                  activeDoneDirection === null
                                    ? "bg-ink font-medium text-surface"
                                    : "bg-inset text-ink-2 hover:bg-hover hover:text-ink",
                                )}
                              >
                                <span>{t("todos.filter_all")}</span>
                                <span className="shrink-0 opacity-70">{" · "}{completedCount}</span>
                              </button>
                              {completedDirectionBuckets.map((bucket) => {
                                const selected = activeDoneDirection === bucket.id;
                                return (
                                  <button
                                    key={bucket.id}
                                    type="button"
                                    data-testid="todos-done-direction-chip"
                                    data-direction-id={bucket.id}
                                    aria-pressed={selected}
                                    title={`${bucket.label} · ${bucket.count}`}
                                    onClick={() => setDoneDirectionFilter(selected ? null : bucket.id)}
                                    className={cx(
                                      "touch-hit inline-flex h-6 max-w-36 shrink-0 items-center gap-1 rounded-full px-2 text-submeta whitespace-nowrap transition-colors focus-visible:outline-2 focus-visible:outline-accent",
                                      selected
                                        ? "bg-ink font-medium text-surface"
                                        : "bg-inset text-ink-2 hover:bg-hover hover:text-ink",
                                    )}
                                  >
                                    <span className="truncate">{bucket.label}</span>
                                    <span className="shrink-0 tabular-nums opacity-70">{" · "}{bucket.count}</span>
                                  </button>
                                );
                              })}
                            </div>
                            {activeDoneDirection === null ? (
                              completedDirectionBuckets.filter((bucket) => bucket.todos.length > 0).map((bucket) => (
                                <div
                                  key={bucket.id}
                                  data-testid="todos-done-direction-group"
                                  data-direction-id={bucket.id}
                                  className="flex flex-col gap-1 pt-1 first:pt-0"
                                >
                                  <div className="flex items-center gap-1.5 px-2 pt-1 text-submeta font-medium text-ink-3">
                                    <Icon
                                      name={bucket.uncategorized ? "inbox" : "target"}
                                      size={11}
                                      stroke={2}
                                      className="shrink-0 opacity-70"
                                    />
                                    <span className="min-w-0 flex-1 truncate">{bucket.label}</span>
                                    <span className="shrink-0 tabular-nums">{bucket.count}</span>
                                  </div>
                                  {bucket.todos.map((todo) => (
                                    <TodoListItem
                                      key={todo.id}
                                      todo={todo}
                                      view={views.get(todo.id)!}
                                      selected={activeTodo?.id === todo.id}
                                      reorganizeSelected={reorganizeProps?.selectedIds.has(todo.id)}
                                      onToggleReorganize={reorganizeProps?.onToggle}
                                      slim={todosV2}
                                      hideDirectionBadge={todosV2}
                                      parentGoalTitle={todo.part_of ? todosById.get(todo.part_of)?.title : undefined}
                                      todosById={todosV2 && todo.wait != null ? todosById : undefined}
                                      onSelect={openFromList}
                                      onToggleDone={toggleDone}
                                      onMenu={openRowMenu}
                                    />
                                  ))}
                                </div>
                              ))
                            ) : (
                              (() => {
                                const selectedBucket = completedDirectionBuckets.find((b) => b.id === activeDoneDirection);
                                if (!selectedBucket) return null;
                                return (
                                  <div
                                    data-testid="todos-done-direction-group"
                                    data-direction-id={selectedBucket.id}
                                    className="flex flex-col gap-1"
                                  >
                                    {selectedBucket.todos.map((todo) => (
                                      <TodoListItem
                                        key={todo.id}
                                        todo={todo}
                                        view={views.get(todo.id)!}
                                        selected={activeTodo?.id === todo.id}
                                        reorganizeSelected={reorganizeProps?.selectedIds.has(todo.id)}
                                        onToggleReorganize={reorganizeProps?.onToggle}
                                        slim={todosV2}
                                        hideDirectionBadge={todosV2}
                                        parentGoalTitle={todo.part_of ? todosById.get(todo.part_of)?.title : undefined}
                                        todosById={todosV2 && todo.wait != null ? todosById : undefined}
                                        onSelect={openFromList}
                                        onToggleDone={toggleDone}
                                        onMenu={openRowMenu}
                                      />
                                    ))}
                                  </div>
                                );
                              })()
                            )}
                          </>
                        ) : (
                          filteredCompleted.map((todo) => (
                            <TodoListItem
                              key={todo.id}
                              todo={todo}
                              view={views.get(todo.id)!}
                              selected={activeTodo?.id === todo.id}
                              reorganizeSelected={reorganizeProps?.selectedIds.has(todo.id)}
                              onToggleReorganize={reorganizeProps?.onToggle}
                              slim={todosV2}
                              parentGoalTitle={todosV2 && todo.part_of ? todosById.get(todo.part_of)?.title : undefined}
                              onSelect={openFromList}
                              onToggleDone={toggleDone}
                              onMenu={openRowMenu}
                            />
                          ))
                        )}
                        <ClosedHistoryMore active={doneOpen} {...closedHistory} />
                      </div>
                    )}
                  </div>
                )}
                {/* Cancelled Fold: its own outcome and reason, never inside Completed (#5732) */}
                {(cancelledCount > 0 || hasOlderClosed) && (
                  <div className="mt-2 border-t border-line/60 pt-2">
                    <button
                      type="button"
                      data-testid="todos-cancelled-fold"
                      aria-expanded={showCancelled}
                      onClick={() => {
                        if (!showCancelled && closedHistory.hasMore) closedHistory.onLoad();
                        setShowCancelled((v) => !v);
                      }}
                      className="flex h-8 w-full items-center justify-between rounded-control px-2.5 text-xs text-ink-3 transition-colors hover:bg-hover hover:text-ink-2"
                    >
                      <span>{t("todos.cancelled_section", { count: cancelledCount })}</span>
                      <Icon name="chevron" size={12} className={cx("transition-transform duration-200", showCancelled ? "rotate-180" : "")} />
                    </button>
                    {showCancelled && (
                      <div className="flex flex-col gap-1 pt-1">
                        {filteredCancelled.map((todo) => (
                          <TodoListItem
                            key={todo.id}
                            todo={todo}
                            view={views.get(todo.id)!}
                            selected={activeTodo?.id === todo.id}
                            reorganizeSelected={reorganizeProps?.selectedIds.has(todo.id)}
                            onToggleReorganize={reorganizeProps?.onToggle}
                            slim={todosV2}
                            parentGoalTitle={todosV2 && todo.part_of ? todosById.get(todo.part_of)?.title : undefined}
                            onSelect={openFromList}
                            onToggleDone={toggleDone}
                            onMenu={openRowMenu}
                          />
                        ))}
                        <ClosedHistoryMore active={showCancelled} {...closedHistory} />
                      </div>
                    )}
                  </div>
                )}
              </div>
              )
            )}
          </TodoListScroller>
          </ReorganizeVisibilityContext.Provider>
          )}
        </div>

        {/* Right Column: Todo Detail */}
        <div
          ref={detailColumn}
          data-testid="todos-detail-column"
          data-narrow-shown={detailOnNarrow || undefined}
          className={cx("min-h-0 flex-1 flex-col overflow-y-auto", (requestMode || boardOn) && !activeTodo ? "md:hidden" : "md:flex", (requestMode || boardOn) && "md:w-[440px] md:flex-none lg:w-[520px]", detailOnNarrow ? "flex" : "hidden")}
        >
          {(requestMode || boardOn) && (
            <div className="hidden shrink-0 justify-end px-2 pt-2 md:flex">
              <IconButton icon="x" label={t("todos.calendar_close")} onClick={closeDetail} />
            </div>
          )}
          {detailOnNarrow && (
            <div className="flex shrink-0 items-center px-2 pt-2 md:hidden" data-testid="todos-back">
              <Button tone="quiet" icon="chevronLeft" onClick={backToList}>
                {t("todos.back_to_list")}
              </Button>
            </div>
          )}
          {/* taking turns, the detail is there only while shown: a to-do opened before and left keeps no hidden page */}
          {activeTodo && (split || detailOnNarrow) ? (
            <TodoDetail
              key={`${owner}/${activeTodo.id}`}
              owner={owner}
              todo={activeTodo}
              activeAgents={activeAgents}
              releasedAgents={releasedAgents}
              lookup={lookup}
              machines={machines}
              interactions={interactions}
              showReleased={showReleased}
              onToggleReleased={() => setShowReleased((v) => !v)}
              open={checksAsOpen(activeTodo, sectionOf.get(activeTodo.id))}
              onToggleDone={(open) => toggleDone(activeTodo, open)}
              onOpenHandover={() => openHandover()}
              onOpenTodo={openFromList}
              onDeleteClick={() => askDelete(activeTodo.id)}
              todosV2={todosV2}
              allTodos={todosV2 ? todos : undefined}
              parentTodo={todosV2 && activeTodo.part_of ? todosById.get(activeTodo.part_of) : undefined}
              childTodos={todosV2 ? todos.filter((td) => td.part_of === activeTodo.id) : undefined}
              effectiveDirection={todosV2 ? resolveDirection?.(activeTodo) : undefined}
              resolveChildDirection={resolveDirection}
              directions={todosV2 ? reorganizeDirections : undefined}
              onReorganizeDirection={todosV2 ? applyDetailReorganize : undefined}
              onUnseen={todosV2 ? markDecidedUnseen : undefined}
              mergeTrainBadge={mergeTrainByTodo?.[activeTodo.id]}
            />
          ) : (
            <div className="flex flex-1 items-center justify-center text-[13px] text-ink-3">
              {t("todos.pick_hint")}
            </div>
          )}
        </div>
      </div>
      )}

      {/* Dialogs */}
      <NewTodoDialog
        key={`${owner}:${repositoryManager}:${newOpen}`}
        manager={repositoryManager}
        owner={owner}
        open={newOpen}
        slot={newSlot}
        onClose={() => {
          setNewOpen(false);
          setNewSlot(undefined);
        }}
        onCreated={(id) => setActiveId(id)}
      />

      {rowMenu && menuTodo && (
        <Menu
          anchor={rowMenu.at.anchor ?? null}
          point={rowMenu.at.point}
          onClose={() => setRowMenu(null)}
          items={todoRowMenu(menuTodo, rowMenu.open, Date.now(), {
            toggleDone: () => void toggleDone(menuTodo, rowMenu.open),
            save: (patch) => void savePlan(menuTodo.id, patch),
            pickDue: () => setDueFor(menuTodo.id),
            handOver: () => openHandover(menuTodo.id),
            remove: () => askDelete(menuTodo.id),
          })}
        />
      )}

      {dueTodo && <TodoDueDialog key={dueTodo.id} todo={dueTodo} onClose={() => setDueFor(undefined)} onSave={(patch) => savePlan(dueTodo.id, patch)} />}

      {reportingConcern && (
        <ReportConcernDialog key={`report:${reportingConcern.id}`} todo={reportingConcern} onClose={() => setReportingConcern(undefined)} />
      )}

      {handoverTodo && (
        <HandoverDialog
          key={handoverTodo.id}
          open={handoverOpen}
          todo={handoverTodo}
          machines={machines}
          onClose={() => setHandoverOpen(false)}
        />
      )}

      {todosV2 && (
        <DecisionQueuePanel
          open={decisionQueueOpen}
          items={decisionQueueItems}
          onClose={() => setDecisionQueueOpen(false)}
          onRefresh={refreshTodos}
        />
      )}

      {deleteTodoShown && (
        <Dialog open={deleteConfirm} onClose={() => setDeleteConfirm(false)} title={t("todos.delete_title")}>
          <div className="py-2 text-[13.5px] text-ink-2">
            {t("todos.delete_confirm", { title: deleteTodoShown.title })}
          </div>
          <div className="mt-4 flex justify-end gap-2">
            <Button tone="secondary" onClick={() => setDeleteConfirm(false)}>
              {t("common.cancel")}
            </Button>
            <Button tone="danger" busy={deleting} onClick={handleDelete}>
              {t("todos.delete")}
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

/** The to-dos a status section's rows list, a merged row's related ones too: the number its title shows (#3712). */
function sectionSize(r: TriageResult, sec: TriageSection): number {
  return r.sections[sec].reduce((n, g) => n + g.members.length, 0);
}

/** The list's scroller: where it was scrolled comes back on a return to the page (Back from another page, audit item
 *  88), as on Overview (lib/scroll-memory.ts). */
function TodoListScroller({ box, children }: { box: RefObject<HTMLDivElement | null>; children: ReactNode }) {
  useScrollMemory(box, "/todos");
  return (
    <div ref={box} data-testid="todos-list-scroller" className="min-h-0 flex-1 overflow-y-auto px-2 pb-4">
      {children}
    </div>
  );
}

// ── Steady values ────────────────────────────────────────────

/** The last value while `steady` says the new one is the same, so memoized children see the same props. */
function useSteady<T>(value: T, steady: (prev: T, next: T) => T): T {
  const last = useRef(value);
  if (last.current !== value) last.current = steady(last.current, value);
  return last.current;
}

/** The same items in the same order. */
function sameItems<T>(a: readonly T[], b: readonly T[]): boolean {
  return a.length === b.length && a.every((x, i) => Object.is(x, b[i]));
}

const sameList = <T,>(prev: T[], next: T[]): T[] => (sameItems(prev, next) ? prev : next);

/** Two objects with the same fields, each the same value or object. */
function sameFields(a: object, b: object): boolean {
  const ka = Object.keys(a), kb = Object.keys(b);
  return ka.length === kb.length && ka.every((k) => Object.is((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]));
}

/** Each group that has the same title, state and todos stays the last one; so does the list when they all do. */
function steadyGroups(prev: TodoProjectGroup[], next: TodoProjectGroup[]): TodoProjectGroup[] {
  const byKey = new Map(prev.map((g) => [g.key, g]));
  const out = next.map((g) => {
    const old = byKey.get(g.key);
    return old && old.name === g.name && old.state === g.state && old.folded === g.folded && sameItems(old.todos, g.todos) ? old : g;
  });
  return sameItems(prev, out) ? prev : out;
}

/** Each list that has the same items stays the last one; so does the map when they all do. */
function steadyLists<T>(prev: ReadonlyMap<string, T[]>, next: ReadonlyMap<string, T[]>): ReadonlyMap<string, T[]> {
  let same = prev.size === next.size;
  const out = new Map<string, T[]>();
  for (const [k, list] of next) {
    const old = prev.get(k);
    const kept = old && sameItems(old, list) ? old : list;
    if (kept !== old) same = false;
    out.set(k, kept);
  }
  return same ? prev : out;
}

/** Each todo's view that has the same fields stays the last one; so does the map when they all do. */
function steadyViews(prev: ReadonlyMap<string, TodoRowView>, next: ReadonlyMap<string, TodoRowView>): ReadonlyMap<string, TodoRowView> {
  let same = prev.size === next.size;
  const out = new Map<string, TodoRowView>();
  for (const [id, v] of next) {
    const old = prev.get(id);
    const kept = old && sameFields(old, v) ? old : v;
    if (kept !== old) same = false;
    out.set(id, kept);
  }
  return same ? prev : out;
}

// ── Project Section ──────────────────────────────────────────

const TREE_TONE: Record<TreeState, string> = { needs_you: "text-attention", working: "text-orange", bg_waiting: "text-status-waiting", waiting: "text-ink-2", done: "text-ink-3" };

interface TodoReorganizeSelection {
  selectedIds: ReadonlySet<string>;
  onToggle: (id: string) => void;
}

/** Section-local parent/child hierarchy row for Direction and Project groupings under todos_v2 (#16939, #17938). */
function TodoHierarchyRow({
  entry,
  views,
  allTodosById,
  reorganize,
  slim,
  hideDirectionBadge,
  mergeTrainByTodo,
  selectedId,
  onSelect,
  onToggleDone,
  onSeen,
  onMenu,
}: {
  entry: SectionHierarchyEntry;
  views: ReadonlyMap<string, TodoRowView>;
  allTodosById?: ReadonlyMap<string, Todo>;
  reorganize?: TodoReorganizeSelection;
  slim?: boolean;
  hideDirectionBadge?: boolean;
  mergeTrainByTodo?: Readonly<Record<string, MergeTrainTodoBadge>>;
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  onSeen?: (id: string) => void;
  onMenu: TodoRowMenuOpener;
}) {
  const hasChildren = entry.children.length > 0;
  // Expanded by default in Direction and Project views so parent/child structure is immediately visible, with collapse toggle.
  const [expanded, setExpanded] = useState(true);
  const selectedChildId = hasChildren && entry.children.some((c) => c.todo.id === selectedId) ? selectedId : undefined;
  const [seenSelectedChildId, setSeenSelectedChildId] = useState(selectedChildId);
  if (selectedChildId !== seenSelectedChildId) {
    setSeenSelectedChildId(selectedChildId);
    if (selectedChildId !== undefined && !expanded) setExpanded(true);
  }
  const toggleExpanded = useCallback(() => setExpanded((v) => !v), []);

  return (
    <div data-testid="todo-hierarchy-row" data-id={entry.todo.id}>
      <TodoListItem
        todo={entry.todo}
        view={views.get(entry.todo.id)!}
        selected={selectedId === entry.todo.id}
        reorganizeSelected={reorganize?.selectedIds.has(entry.todo.id)}
        onToggleReorganize={reorganize?.onToggle}
        slim={slim}
        hideDirectionBadge={hideDirectionBadge}
        todosById={slim && entry.todo.wait != null ? allTodosById : undefined}
        mergeTrainBadge={mergeTrainByTodo?.[entry.todo.id]}
        onSelect={onSelect}
        onToggleDone={onToggleDone}
        onSeen={onSeen}
        onMenu={onMenu}
        goalSummary={
          hasChildren
            ? {
                done: entry.done,
                total: entry.total,
                blockedCount: entry.blockedCount,
                expanded,
                onToggle: toggleExpanded,
              }
            : undefined
        }
        parentGoalTitle={entry.parentGoalTitle}
      />
      {expanded && hasChildren && (
        <div
          data-testid="todo-goal-children"
          data-parent={entry.todo.id}
          className="ml-6 mt-1 flex flex-col gap-1 border-l border-line pl-2"
        >
          {entry.children.map((child) => (
            <TodoListItem
              key={child.todo.id}
              todo={child.todo}
              view={views.get(child.todo.id)!}
              selected={selectedId === child.todo.id}
              reorganizeSelected={reorganize?.selectedIds.has(child.todo.id)}
              onToggleReorganize={reorganize?.onToggle}
              slim={slim}
              hideDirectionBadge={hideDirectionBadge}
              todosById={slim && child.todo.wait != null ? allTodosById : undefined}
              mergeTrainBadge={mergeTrainByTodo?.[child.todo.id]}
              onSelect={onSelect}
              onToggleDone={onToggleDone}
              section={child.section}
              onSeen={onSeen}
              onMenu={onMenu}
            />
          ))}
        </div>
      )}
    </div>
  );
}

/** One project's open todos under a folding title with its Needs you / Working / Done (#2526). */
const TodoProjectSection = memo(function TodoProjectSection({
  group,
  views,
  allTodosById,
  sectionOf,
  reorganize,
  slim,
  foldChildren,
  mergeTrainByTodo,
  selectedId,
  hideUnsortedHeader,
  onSelect,
  onToggleDone,
  onSeen,
  onMenu,
}: {
  group: TodoProjectGroup;
  /** Its todos' row views, in the same order as group.todos. */
  views: readonly TodoRowView[];
  allTodosById?: ReadonlyMap<string, Todo>;
  sectionOf?: ReadonlyMap<string, TriageSection>;
  reorganize?: TodoReorganizeSelection;
  slim?: boolean;
  foldChildren?: boolean;
  mergeTrainByTodo?: Readonly<Record<string, MergeTrainTodoBadge>>;
  selectedId: string | undefined;
  /** Under todos_v2 (#14642), omit the noisy "Unsorted / 未归类" placeholder header for uncategorized rows. */
  hideUnsortedHeader?: boolean;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  /** Got it on a row in Just finished (one closed in the last day stays in its folder until it is seen). */
  onSeen?: (id: string) => void;
  onMenu: TodoRowMenuOpener;
}) {
  const holds = group.todos.some((td) => td.id === selectedId);
  const [folded, toggle] = useFolded(group.key, group.folded, { follow: true, holds });
  const viewById = useMemo(
    () => (foldChildren ? new Map(group.todos.map((td, i) => [td.id, views[i]])) : undefined),
    [foldChildren, group.todos, views],
  );
  const hierarchy = useMemo(
    () => (foldChildren ? nestSectionHierarchy(group.todos, allTodosById, sectionOf) : undefined),
    [foldChildren, group.todos, allTodosById, sectionOf],
  );
  const renderRows = () =>
    slim && hierarchy && viewById
      ? hierarchy.map((entry) => (
          <TodoHierarchyRow
            key={entry.todo.id}
            entry={entry}
            views={viewById}
            allTodosById={allTodosById}
            reorganize={reorganize}
            slim={slim}
            mergeTrainByTodo={mergeTrainByTodo}
            selectedId={selectedId}
            onSelect={onSelect}
            onToggleDone={onToggleDone}
            onSeen={onSeen}
            onMenu={onMenu}
          />
        ))
      : group.todos.map((todo, i) => (
          <TodoListItem
            key={todo.id}
            todo={todo}
            view={views[i]}
            selected={selectedId === todo.id}
            reorganizeSelected={reorganize?.selectedIds.has(todo.id)}
            onToggleReorganize={reorganize?.onToggle}
            slim={slim}
            parentGoalTitle={todo.part_of ? allTodosById?.get(todo.part_of)?.title : undefined}
            todosById={slim && todo.wait != null ? allTodosById : undefined}
            mergeTrainBadge={mergeTrainByTodo?.[todo.id]}
            onSelect={onSelect}
            onToggleDone={onToggleDone}
            onSeen={onSeen}
            onMenu={onMenu}
          />
        ));
  if (hideUnsortedHeader && group.key === "todos-project:\u0000") {
    return (
      <div className="mt-1 first:mt-0 flex flex-col gap-1" data-testid="todo-group" data-group={group.key}>
        {renderRows()}
      </div>
    );
  }
  return (
    <div className="mt-1 first:mt-0" data-testid="todo-group" data-group={group.key}>
      <button type="button" aria-expanded={!folded} onClick={toggle} className="flex h-7 w-full min-w-0 items-center gap-1.5 rounded-control px-2 text-left text-[12px] text-ink-2 hover:bg-hover">
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        <Icon name="folder" size={12} stroke={2} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate font-semibold">{group.name}</span>
        {folded && <OwnerMarkCount count={views.filter((v) => v.owner).length} />}
        <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{group.todos.length}</span>
        {!(group.area && group.state === "done") && (
          <span className={cx("shrink-0 text-[11px] font-semibold whitespace-nowrap", TREE_TONE[group.state])}>{treeStateLabel(group.state)}</span>
        )}
      </button>
      {!folded && (
        <div className="flex flex-col gap-1 pl-2 pt-0.5">
          {renderRows()}
        </div>
      )}
    </div>
  );
});

/** Labeled grouping picker pill for the todos_v2 toolbar (#14830, #16939). */
function TodoGroupingMenu({
  grouping,
  showDirection,
  showRequest,
  onChange,
}: {
  grouping: TodoGrouping;
  showDirection: boolean;
  showRequest: boolean;
  onChange: (g: TodoGrouping) => void;
}) {
  const menu = useMenu();
  const options: { v: TodoGrouping; label: string }[] = [
    { v: "status", label: t("todos.grouping_v2_status") },
    ...(showDirection ? [{ v: "direction" as const, label: t("todos.grouping_v2_direction") }] : []),
    { v: "folder", label: t("todos.grouping_v2_folder") },
    ...(showRequest ? [{ v: "request" as const, label: t("todos.grouping_v2_request") }] : []),
  ];
  const activeLabel = options.find((o) => o.v === grouping)?.label ?? t("todos.grouping_v2_status");
  const items: MenuItem[] = [
    { key: "grouping-heading", label: t("todos.grouping_label"), heading: true, onSelect: () => {} },
    ...options.map((o): MenuItem => ({
      key: `grouping-${o.v}`,
      label: o.label,
      checked: o.v === grouping,
      radio: true,
      onSelect: () => onChange(o.v),
    })),
  ];
  return (
    <>
      <span data-testid="todos-grouping-menu" data-grouping={grouping} className="relative inline-flex shrink-0">
        <button
          type="button"
          data-testid="todos-grouping-trigger"
          aria-label={`${t("todos.grouping_label")}: ${activeLabel}`}
          aria-haspopup="menu"
          aria-expanded={Boolean(menu.anchor)}
          onClick={menu.open}
          className={cx(
            "touch-hit inline-flex h-8 shrink-0 items-center gap-1 rounded-control bg-inset px-2.5 text-submeta font-medium whitespace-nowrap shadow-hairline transition-colors focus-visible:outline-2 focus-visible:outline-accent",
            grouping !== "status" || Boolean(menu.anchor)
              ? "bg-surface text-ink"
              : "text-ink-2 hover:bg-hover hover:text-ink",
          )}
        >
          <Icon
            name={grouping === "direction" ? "target" : grouping === "folder" ? "folder" : "list"}
            size={12}
            stroke={2}
            className="shrink-0 text-ink-3"
          />
          <span data-testid="todos-grouping-label">{activeLabel}</span>
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-60" />
        </button>
      </span>
      {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} align="right" width={180} items={items} />}
    </>
  );
}

// ── Filter and sort (#3740) ──────────────────────────────────

/** Beside the search field: the filter as a chip that clears it, and one button whose menu picks the filter (with how
 *  many each shows) and the order inside the sections. Both start as All and Recent activity, the list as it was. */
function TodoListControls({
  filter,
  sort,
  counts,
  onFilter,
  onSort,
}: {
  filter: TodoListFilter;
  sort: TodoListSort;
  counts: Record<TodoListFilter, number>;
  onFilter: (f: TodoListFilter) => void;
  onSort: (s: TodoListSort) => void;
}) {
  const menu = useMenu();
  const items: MenuItem[] = [
    { key: "show", label: t("todos.filter_show"), heading: true, onSelect: () => {} },
    ...TODO_LIST_FILTERS.map((f): MenuItem => ({ key: `filter-${f}`, label: filterLabel(f), hint: String(counts[f]), checked: f === filter, radio: true, onSelect: () => onFilter(f) })),
    { key: "sort", label: t("todos.sort_by"), heading: true, divider: true, onSelect: () => {} },
    ...TODO_LIST_SORTS.map((s): MenuItem => ({ key: `sort-${s}`, label: sortLabel(s), checked: s === sort, radio: true, onSelect: () => onSort(s) })),
  ];
  return (
    <>
      {filter !== "all" && (
        <button
          type="button"
          data-testid="todos-filter-chip"
          aria-label={t("todos.filter_clear", { filter: filterLabel(filter) })}
          onClick={() => onFilter("all")}
          className="touch-hit flex h-7 shrink-0 items-center gap-1 rounded-full bg-accent-tint px-2.5 text-[12px] font-medium whitespace-nowrap text-accent-ink hover:bg-accent-tint/70"
        >
          {filterLabel(filter)}
          <Icon name="x" size={10} stroke={2.4} />
        </button>
      )}
      {/* a dot while the filter or the order is not the default, as iOS fills its mark: the list is not as it was */}
      <span data-testid="todos-filter" data-filter={filter} data-sort={sort} className="relative inline-flex shrink-0">
        <IconButton icon="filter" label={t("todos.filter_label")} onClick={menu.open} active={Boolean(menu.anchor)} />
        {(filter !== "all" || sort !== "recent") && (
          <span data-testid="todos-filter-changed" aria-hidden className="pointer-events-none absolute top-0.5 right-0.5 size-1.5 rounded-full bg-accent" />
        )}
      </span>
      {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} align="right" width={224} items={items} />}
    </>
  );
}

/** What waits on the user outside the filter, over the sections: Needs you is filtered too, never silently. */
function HiddenWaitingLine({ count, filter, onShowAll }: { count: number; filter: TodoListFilter; onShowAll: () => void }) {
  return (
    <div data-testid="todos-hidden-waiting" className="mx-1 mb-1 flex items-center gap-2 rounded-control bg-attention/10 px-2.5 py-2 text-[12px] text-ink-2">
      <span className="size-1.5 shrink-0 rounded-full bg-attention" aria-hidden />
      <span className="min-w-0 flex-1">{t("todos.filter_hidden_waiting", { count, filter: filterLabel(filter) })}</span>
      <button type="button" onClick={onShowAll} className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline">
        {t("todos.filter_show_all")}
      </button>
    </div>
  );
}

/** One pinned direction's open to-dos (#4352): its name (书记官 for the secretary's group) and the hub's two counts on a
 *  folding title, the to-dos in the tree's order. */
const TodoDirectionSection = memo(function TodoDirectionSection({
  direction,
  title,
  todos,
  views,
  allTodosById,
  sectionOf,
  reorganize,
  slim,
  foldChildren,
  mergeTrainByTodo,
  completedCount = 0,
  onReviewCompleted,
  selectedId,
  onSelect,
  onToggleDone,
  onMenu,
}: {
  direction: Direction;
  title: string;
  todos: readonly Todo[];
  views: ReadonlyMap<string, TodoRowView>;
  allTodosById?: ReadonlyMap<string, Todo>;
  sectionOf?: ReadonlyMap<string, TriageSection>;
  reorganize?: TodoReorganizeSelection;
  slim?: boolean;
  foldChildren?: boolean;
  mergeTrainByTodo?: Readonly<Record<string, MergeTrainTodoBadge>>;
  completedCount?: number;
  onReviewCompleted?: (directionKey: string) => void;
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  onMenu: TodoRowMenuOpener;
}) {
  const holds = todos.some((td) => td.id === selectedId);
  const [folded, toggle] = useFolded(`todos-direction:${direction.id}`, false, { holds });
  const hierarchy = useMemo(
    () => (foldChildren ? nestSectionHierarchy(todos, allTodosById, sectionOf) : undefined),
    [foldChildren, todos, allTodosById, sectionOf],
  );
  const directionKey = direction.secretary ? DONE_UNCATEGORIZED_ID : direction.id;
  // Under todos_v2 (#17938), expanded direction headers omit the lone running arc next to "✓ Done N"
  // because every child row below already displays its own live StatusMark; folded headers keep it when
  // an Agent under the direction is actually running. Legacy mode preserves direction.running verbatim.
  const headerDirection = slim
    ? {
        ...direction,
        running: folded && todos.some((td) => views.get(td.id)?.dotState === "working"),
      }
    : direction;
  return (
    <div className="mt-1 first:mt-0" data-testid="todo-group" data-group={`direction:${direction.secretary ? "secretary" : direction.id}`}>
      <div className="flex min-w-0 items-center gap-1 overflow-hidden">
        <button type="button" aria-expanded={!folded} onClick={toggle} className="flex h-7 min-w-0 flex-1 items-center gap-1.5 overflow-hidden rounded-control px-2 text-left text-[12px] text-ink-2 hover:bg-hover [@media(pointer:coarse)]:min-h-11">
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
          <Icon name={direction.secretary ? (slim ? "inbox" : "person") : "target"} size={12} stroke={2} className="shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1 truncate font-semibold">{title}</span>
          {folded && <OwnerMarkCount count={ownerCount(todos, views)} />}
          <DirectionCountsText direction={headerDirection} className="px-0.5" />
        </button>
        {slim && completedCount > 0 && onReviewCompleted && (
          <button
            type="button"
            data-testid="todo-direction-review-done"
            data-direction-id={directionKey}
            aria-label={t("todos.direction_review_done_label", { count: completedCount, direction: title })}
            title={t("todos.direction_review_done_label", { count: completedCount, direction: title })}
            onClick={(e) => {
              e.stopPropagation();
              onReviewCompleted(directionKey);
            }}
            className="touch-hit inline-flex h-6 shrink-0 items-center gap-1 rounded-chip px-1.5 text-submeta whitespace-nowrap text-ink-3 tabular-nums transition-colors hover:bg-hover hover:text-ink-2 focus-visible:outline-2 focus-visible:outline-accent"
          >
            <Icon name="check" size={10} stroke={2.2} className="shrink-0 opacity-70" />
            <span>{t("todos.direction_done_count", { count: completedCount })}</span>
          </button>
        )}
      </div>
      {!folded && (
        <div className="flex flex-col gap-1 pl-2 pt-0.5">
          {slim && hierarchy
            ? hierarchy.map((entry) => (
                <TodoHierarchyRow
                  key={entry.todo.id}
                  entry={entry}
                  views={views}
                  allTodosById={allTodosById}
                  reorganize={reorganize}
                  slim={slim}
                  hideDirectionBadge={slim}
                  mergeTrainByTodo={mergeTrainByTodo}
                  selectedId={selectedId}
                  onSelect={onSelect}
                  onToggleDone={onToggleDone}
                  onMenu={onMenu}
                />
              ))
            : todos.map((todo) => (
                <TodoListItem
                  key={todo.id}
                  todo={todo}
                  view={views.get(todo.id)!}
                  selected={selectedId === todo.id}
                  reorganizeSelected={reorganize?.selectedIds.has(todo.id)}
                  onToggleReorganize={reorganize?.onToggle}
                  slim={slim}
                  hideDirectionBadge={slim}
                  parentGoalTitle={todo.part_of ? allTodosById?.get(todo.part_of)?.title : undefined}
                  todosById={slim && todo.wait != null ? allTodosById : undefined}
                  mergeTrainBadge={mergeTrainByTodo?.[todo.id]}
                  onSelect={onSelect}
                  onToggleDone={onToggleDone}
                  onMenu={onMenu}
                />
              ))}
        </div>
      )}
    </div>
  );
});

// ── Status sections (#3156) ──────────────────────────────────

const SECTION_TONE: Record<TriageSection, string> = {
  waiting: "text-attention",
  can_wait: "text-ink-2",
  working: "text-orange",
  parked: "text-ink-2",
  just_done: "text-green",
  other: "text-ink-2",
  todo: "text-ink-2",
  shelved: "text-ink-3",
  done: "text-ink-3",
  cancelled: "text-ink-3",
};

/** A section's title: agent states share the state names; only a section made entirely of unaccepted deliveries uses
 *  the awaiting-review wording. */
function sectionTitle(section: TriageSection, rules: readonly TriageRule[]): string {
  if (section === "waiting") return stateName("needs_you");
  if (section === "working") return stateName("working");
  if (section === "just_done" && rules.length > 0 && rules.every((rule) => rule === "finished_unseen")) {
    return t("todos.section_awaiting_review");
  }
  if (section === "just_done") return stateName("just_finished");
  return t(`todos.section_${section}`);
}

/** Below an open Done or Cancelled section: cursor-loads older closed rows when it comes into view, with the same
 *  request behind its button where IntersectionObserver is unavailable or a failed page waits to be retried. */
function ClosedHistoryMore({ active, hasMore, loading, error, onLoad }: ClosedHistory & { active: boolean }) {
  const end = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!active || !hasMore || loading || error) return;
    const el = end.current;
    if (!el || typeof IntersectionObserver === "undefined") return;
    let asked = false;
    const io = new IntersectionObserver((entries) => {
      if (asked || !entries.some((entry) => entry.isIntersecting)) return;
      asked = true;
      onLoad();
    }, { rootMargin: "160px" });
    io.observe(el);
    return () => io.disconnect();
  }, [active, hasMore, loading, error, onLoad]);
  if (!active || (!hasMore && !loading && !error)) return null;
  return (
    <div ref={end} className="flex flex-col items-center gap-1 px-2.5 py-2" data-testid="todos-closed-history">
      <button
        type="button"
        data-testid="todos-closed-history-more"
        disabled={loading}
        onClick={onLoad}
        className="touch-hit rounded-control px-3 py-1 text-label font-medium text-accent-ink transition-colors hover:bg-hover disabled:text-ink-3"
      >
        {loading ? t("todos.closed_loading") : error ? t("todos.closed_retry") : t("todos.closed_more")}
      </button>
      {error && <span className="max-w-full text-center text-submeta text-ink-3">{error}</span>}
    </div>
  );
}

/** One status section (docs/assistant/todo-status-sop.md): a folding title with its count, a row per group of related
 *  to-dos. Every section starts open but Shelved and Done (#5707). */
const TodoTriageSection = memo(function TodoTriageSection({
  section,
  folded: foldedFirst,
  groups,
  count,
  views,
  goalFold,
  todosById,
  reorganize,
  slim,
  mergeTrainByTodo,
  selectedId,
  onSelect,
  onToggleDone,
  onRestore,
  onSeen,
  onMenu,
  closedHistory,
}: {
  section: TriageSection;
  /** Whether it starts folded: Shelved and Done (plus Parked under todos_v2). */
  folded: boolean;
  groups: TriageGroup[];
  /** To-dos its rows list (a merged row holds several): the number its title shows (#3712, the same on iOS). */
  count: number;
  views: ReadonlyMap<string, TodoRowView>;
  /** Goal folding summary when todos_v2 is active (#14661); undefined otherwise. */
  goalFold?: GoalFoldResult;
  /** Loaded to-dos indexed by ID for resolving parent goal titles on unnested children under todos_v2 (#16939). */
  todosById?: ReadonlyMap<string, Todo>;
  reorganize?: TodoReorganizeSelection;
  /** Under todos_v2 (#14830), compact row and folded-section presentation. */
  slim?: boolean;
  mergeTrainByTodo?: Readonly<Record<string, MergeTrainTodoBadge>>;
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  onRestore: (id: string) => void;
  /** Got it on a Just finished row; unset while the hub keeps no seen marks. */
  onSeen?: (id: string) => void;
  onMenu: TodoRowMenuOpener;
  /** Older Done/Cancelled rows beyond the default closed window (#15683). */
  closedHistory?: ClosedHistory;
}) {
  const holds = groups.some(
    (g) =>
      g.members.some((m) => m.id === selectedId) ||
      Boolean(goalFold?.goals.get(g.lead.id)?.children.some((c) => c.todo.id === selectedId)),
  );
  const [folded, toggle] = useFolded(`todos-section:${section}`, foldedFirst, { follow: true, holds });
  const title = sectionTitle(section, groups.flatMap((g) => [...g.triage.values()].map((t) => t.rule)));
  return (
    <section className="mt-1 first:mt-0" data-testid="todo-section" data-section={section} data-count={groups.length} data-todos={count}>
      <button type="button" aria-expanded={!folded} onClick={() => {
        if (folded && closedHistory?.hasMore) closedHistory.onLoad();
        toggle();
      }} className="flex h-8 w-full min-w-0 items-center gap-1.5 rounded-control px-2 text-left text-label hover:bg-hover">
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 text-ink-3 opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        <span className={cx("shrink-0 truncate font-semibold whitespace-nowrap", !(slim && folded && section === "parked") && "min-w-0 flex-1", SECTION_TONE[section])}>{title}</span>
        {slim && folded && section === "parked" && (
          <span data-testid="todo-section-folded-hint" className="min-w-0 flex-1 truncate text-submeta text-ink-3">
            · {t("todos.parked_folded_hint")}
          </span>
        )}
        {folded && <OwnerMarkCount count={ownerCount(groups.flatMap((g) => g.members), views)} />}
        <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{count}</span>
      </button>
      {!folded && section === "shelved" && <p className="px-2.5 pb-1 text-[11.5px] text-ink-3">{t("todos.shelved_hint")}</p>}
      {!folded && (
        <div className="flex flex-col gap-1 pt-0.5">
          {groups.map((g) => (
            <TodoTriageRow
              key={g.key}
              group={g}
              views={views}
              goalInfo={goalFold?.goals.get(g.lead.id)}
              surfacedParent={goalFold?.surfacedParent}
              todosById={todosById}
              reorganize={reorganize}
              slim={slim}
              mergeTrainByTodo={mergeTrainByTodo}
              selectedId={selectedId}
              onSelect={onSelect}
              onToggleDone={onToggleDone}
              onRestore={onRestore}
              onSeen={onSeen}
              onMenu={onMenu}
            />
          ))}
          {closedHistory && <ClosedHistoryMore active={!folded} {...closedHistory} />}
        </div>
      )}
    </section>
  );
});

/** Keeps the pinned display order of still-present members while appending newly joined members at the end. */
function mergePinnedOrder(pinned: readonly string[], members: readonly Todo[]): string[] {
  const present = new Set(members.map((m) => m.id));
  const out = pinned.filter((id) => present.has(id));
  const seen = new Set(out);
  for (const m of members) if (!seen.has(m.id)) out.push(m.id);
  return out;
}

/** A section's row: the group's lead, and its related to-dos under "N related" (same PR, issue, title or plan). */
function TodoTriageRow({
  group,
  views,
  goalInfo,
  surfacedParent,
  todosById,
  reorganize,
  slim,
  mergeTrainByTodo,
  selectedId,
  onSelect,
  onToggleDone,
  onRestore,
  onSeen,
  onMenu,
}: {
  group: TriageGroup;
  views: ReadonlyMap<string, TodoRowView>;
  goalInfo?: GoalSummary;
  surfacedParent?: ReadonlyMap<string, Todo>;
  todosById?: ReadonlyMap<string, Todo>;
  reorganize?: TodoReorganizeSelection;
  slim?: boolean;
  mergeTrainByTodo?: Readonly<Record<string, MergeTrainTodoBadge>>;
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  onRestore: (id: string) => void;
  onSeen?: (id: string) => void;
  onMenu: TodoRowMenuOpener;
}) {
  // When selection lands on a non-lead member (deep link, click, or a live tick demoting the selected lead),
  // latch the row open so it stays expanded even if that member later becomes lead again (#12417).
  const selectedOtherId = group.members.slice(1).some((m) => m.id === selectedId) ? selectedId : undefined;
  const [opened, setOpened] = useState(selectedOtherId !== undefined);
  const [seenSelectedOtherId, setSeenSelectedOtherId] = useState(selectedOtherId);
  // While expanded, pin member positions so live activity ticks that swap `group.lead` never jump rows under the cursor.
  const [pinnedOrder, setPinnedOrder] = useState<string[] | null>(() => (selectedOtherId !== undefined ? group.members.map((m) => m.id) : null));

  if (selectedOtherId !== seenSelectedOtherId) {
    setSeenSelectedOtherId(selectedOtherId);
    if (selectedOtherId !== undefined && !opened) {
      setOpened(true);
      setPinnedOrder(group.members.map((m) => m.id));
    }
  }

  // Under todos_v2 (#14661), expand folded goal children on demand or when deep-linked directly to a folded child.
  const selectedFoldedChildId = goalInfo?.children.some((c) => c.todo.id === selectedId && !c.blocker) ? selectedId : undefined;
  const [goalExpanded, setGoalExpanded] = useState(selectedFoldedChildId !== undefined);
  const [seenFoldedChildId, setSeenFoldedChildId] = useState(selectedFoldedChildId);
  if (selectedFoldedChildId !== seenFoldedChildId) {
    setSeenFoldedChildId(selectedFoldedChildId);
    if (selectedFoldedChildId !== undefined && !goalExpanded) setGoalExpanded(true);
  }
  const toggleGoalExpanded = useCallback(() => setGoalExpanded((v) => !v), []);

  if (opened && pinnedOrder) {
    const nextOrder = mergePinnedOrder(pinnedOrder, group.members);
    if (nextOrder.length !== pinnedOrder.length || nextOrder.some((id, i) => id !== pinnedOrder[i])) {
      setPinnedOrder(nextOrder);
    }
  }

  const displayMembers = useMemo(() => {
    if (!opened || !pinnedOrder) return group.members;
    const byId = new Map(group.members.map((m) => [m.id, m]));
    const ordered: Todo[] = [];
    for (const id of pinnedOrder) {
      const m = byId.get(id);
      if (m) {
        ordered.push(m);
        byId.delete(id);
      }
    }
    for (const m of byId.values()) ordered.push(m);
    return ordered;
  }, [opened, pinnedOrder, group.members]);

  const lead = displayMembers[0] ?? group.lead;
  const others = displayMembers.slice(1);
  const toggleOpen = () => {
    if (opened) {
      setOpened(false);
      setPinnedOrder(null);
    } else {
      setOpened(true);
      setPinnedOrder(group.members.map((m) => m.id));
    }
  };
  const item = (td: Todo, isLead = false) => (
    <TodoListItem
      key={td.id}
      todo={td}
      view={views.get(td.id)!}
      selected={selectedId === td.id}
      reorganizeSelected={reorganize?.selectedIds.has(td.id)}
      onToggleReorganize={reorganize?.onToggle}
      slim={slim}
      todosById={slim && td.wait != null ? todosById : undefined}
      mergeTrainBadge={mergeTrainByTodo?.[td.id]}
      onSelect={onSelect}
      onToggleDone={onToggleDone}
      section={group.triage.get(td.id)?.section}
      onRestore={onRestore}
      onSeen={onSeen}
      onMenu={onMenu}
      goalSummary={
        isLead && goalInfo
          ? {
              done: goalInfo.done,
              total: goalInfo.total,
              blockedCount: goalInfo.blockedCount,
              expanded: goalExpanded,
              onToggle: toggleGoalExpanded,
            }
          : undefined
      }
      parentGoalTitle={
        surfacedParent?.get(td.id)?.title ??
        (slim && td.part_of ? todosById?.get(td.part_of)?.title : undefined)
      }
    />
  );
  return (
    <div data-testid="todo-triage-row" data-id={lead.id} data-group-key={group.key} data-members={displayMembers.length}>
      {item(lead, true)}
      {goalExpanded && goalInfo && goalInfo.children.length > 0 && (
        <div
          data-testid="todo-goal-children"
          data-parent={lead.id}
          className="ml-6 mt-1 flex flex-col gap-1 border-l border-line pl-2"
        >
          {goalInfo.children.map((child) => (
            <TodoListItem
              key={child.todo.id}
              todo={child.todo}
              view={views.get(child.todo.id)!}
              selected={selectedId === child.todo.id}
              reorganizeSelected={reorganize?.selectedIds.has(child.todo.id)}
              onToggleReorganize={reorganize?.onToggle}
              slim={slim}
              todosById={slim && child.todo.wait != null ? todosById : undefined}
              mergeTrainBadge={mergeTrainByTodo?.[child.todo.id]}
              onSelect={onSelect}
              onToggleDone={onToggleDone}
              section={child.section}
              onRestore={onRestore}
              onSeen={onSeen}
              onMenu={onMenu}
            />
          ))}
        </div>
      )}
      {others.length > 0 && (
        <button
          type="button"
          aria-expanded={opened}
          onClick={toggleOpen}
          className="ml-9 flex h-7 items-center gap-1 rounded-control px-1.5 text-[11.5px] whitespace-nowrap text-ink-3 hover:bg-hover hover:text-ink-2"
        >
          <Icon name="chevron" size={10} stroke={2.4} className="transition-transform" style={{ transform: opened ? "none" : "rotate(-90deg)" }} />
          {t("todos.related", { count: others.length })}
          {!opened && <OwnerMarkCount count={ownerCount(others, views)} />}
        </button>
      )}
      {opened && others.length > 0 && <div className="ml-6 flex flex-col gap-1 border-l border-line pl-2">{others.map((m) => item(m))}</div>}
    </div>
  );
}

// ── The badge's mark (#11838) ────────────────────────────────

/** On a row the sidebar's To-dos badge counts (lib/todo-owner-needs.ts): the badge's look (its accent tint,
 *  SidebarParts BADGE_CLASS.accent) and its words, why it counts in the tooltip and said after them. */
function OwnerMark({ need }: { need: OwnerNeed }) {
  const why = t(`todos.owner_why_${need}`);
  return (
    <span data-testid="todo-owner-mark" data-need={need} title={t("todos.owner_mark_title", { why })} className="inline-flex shrink-0">
      <Tag tone="accent">
        {t("todos.owner_mark")}
        <span className="sr-only">{t("todos.owner_mark_said", { why })}</span>
      </Tag>
    </span>
  );
}

/** On a folded heading (a status section, a folder, a pinned direction, "N related"): how many to-dos under it the badge
 *  counts, so none of them is out of sight (DESIGN_SYSTEM 3.4 "Sessions that need the user"). Nothing at none. */
function OwnerMarkCount({ count }: { count: number }) {
  if (!count) return null;
  return (
    <span data-testid="todo-owner-count" data-count={count} className="inline-flex shrink-0">
      <Tag tone="accent">{t("todos.owner_mark_count", { count })}</Tag>
    </span>
  );
}

/** How many of these the badge counts (their views' `owner`). */
function ownerCount(todos: readonly Todo[], views: ReadonlyMap<string, TodoRowView>): number {
  return todos.reduce((n, td) => n + (views.get(td.id)?.owner ? 1 : 0), 0);
}

// ── Todo List Item ───────────────────────────────────────────

/** What a todo's row draws from its agents and their cards: plain values, so a push that leaves them alone leaves an
 *  equal view and the row skips its render. */
export type TodoRowView = {
  status: TodoStatus;
  dotState: AgentState;
  /** How long since it changed ("3 minutes ago"): here so a push that moves the text still redraws it, as before. */
  age: string;
  /** Its agents not released. */
  activeCount: number;
  /** Whether the most urgent agent's line shows (open, with a session, something to say). */
  urgentLine: boolean;
  /** That line's red lead ("<agent> Needs you<kicker>"), empty unless the agent waits on you. */
  urgentLead: string;
  /** What the agent asks (its card) or does / last said. */
  urgentText: string;
  /** The line says nobody is working on it (#3618): its lead is in the caution color, the reason after it. */
  orphaned: boolean;
  /** One of its agents runs with ultracode on (#12): the Agent list row's mark after its title. */
  ultracode: boolean;
  /** Its status section (sectionOf), in every grouping: its checkbox reads open or done by it (checksAsOpen). */
  section?: TriageSection;
  /** What it waits on while its status is Waiting (#11090, lib/todo-wait-reason.ts waitOf): its label says so. */
  wait?: WaitReason;
  supervisorLabel?: string;
  /** Why the sidebar's To-dos badge counts it (#11838, lib/todo-owner-needs.ts), unset when it does not: its mark. */
  owner?: OwnerNeed;
  /** Under todos_v2 (#14642), the resolved project name when assigned; undefined when unset or todos_v2 is off. */
  projectLabel?: string;
  /** Under todos_v2 (#14830), the resolved direction name when assigned; undefined when unset or todos_v2 is off. */
  directionLabel?: string;
};

const DOT_STATE: Record<TodoStatus, AgentState> = {
  needs_you: "needs_you",
  interrupted: "needs_you",
  usage_limit: "idle",
  unread: "unread",
  working: "working",
  waiting: "working",
  follow_up: "idle",
  done: "offline",
  not_started: "offline",
  failed: "offline",
};

/** The view of one todo's row (TodoRowView). Every open row says its latest progress: what its agent asks or does,
 *  else what its agent last reported to its supervisor (#3156). */
export function todoRowView(todo: Todo, lookup: SessionLookup, interactions: Interaction[]): TodoRowView {
  const view = agentRowView(todo, lookup, interactions);
  if (todo.wait != null) return view;
  // Nobody is working on it (#3618): the hub's finding goes ahead of what a stopped agent last said (Interrupted,
  // Follow up; docs/assistant/todos.md). A card that waits for the user still says its question.
  const orphan = view.status !== "needs_you" ? orphanedLine(todo) : undefined;
  if (orphan) {
    view.urgentLine = true;
    view.orphaned = true;
    view.urgentLead = orphan.lead;
    view.urgentText = orphan.reason ? ` · ${orphan.reason}` : "";
    return view;
  }
  if (!view.urgentLine && !todo.done_at) {
    const said = parkedLine(todo) ?? progressLine(todo.supervision);
    if (said) {
      view.urgentLine = true;
      view.urgentText = said;
    }
  }
  return view;
}

/** How long since it or any of its agents last moved (#3156): the time the status sections go by. */
function lastMoved(todo: Todo, lookup: SessionLookup): string {
  const last = lastActivity(todo, lookup);
  return last ? ago(new Date(last).toISOString()) : ago(todo.updated_at || todo.created_at);
}

function agentRowView(todo: Todo, lookup: SessionLookup, interactions: Interaction[]): TodoRowView {
  const status = todoStatus(todo, lookup);
  const urgent = mostUrgentAgent(todo, lookup);
  const activeCount = (todo.agents ?? []).filter((a) => !a.released_at).length;
  const view: TodoRowView = { status, dotState: DOT_STATE[shownStatus(todo, status)], age: lastMoved(todo, lookup), activeCount, urgentLine: false, urgentLead: "", urgentText: "", orphaned: false, ultracode: todoUltracode(todo, lookup), wait: waitOf(todo, status, lookup), supervisorLabel: todoSupervisorLabel(todo, status, lookup) };
  if (todo.wait != null || todo.done_at || !urgent?.session) return view;
  const limit = status === "usage_limit" ? usageLimitLabel(urgent.session) : undefined;
  if (limit) {
    view.urgentLine = true;
    view.urgentText = limit;
    return view;
  }
  const urgentNow = taskNow(urgent.session, foldedState(urgent.session) === "working");
  // its own card, else one of its child sessions' (#5120: their cards wait on its page)
  const urgentIx = urgent.status === "needs_you" ? ownerCard(interactions, urgent.agent, lookup) : undefined;
  // what it waits on, from its card (#4111): the question, the command; not its last words
  const urgentAsk = urgent.status === "needs_you" ? askText(urgentIx) : "";
  if (!urgentAsk && !urgentNow) return view;
  const kicker = urgentIx?.kind === "plan" ? t("todos.kicker_plan") : urgentIx?.kind === "permission" ? t("todos.kicker_permission") : urgentIx?.kind === "screen" ? t("todos.kicker_screen") : urgentIx?.kind === "elicitation" ? t("todos.kicker_elicitation") : t("todos.kicker_question");
  view.urgentLine = true;
  if (urgent.status === "needs_you") view.urgentLead = t("todos.urgent_line", { name: urgent.agent.title || urgent.session.title || "Agent", kicker });
  view.urgentText = urgentAsk || (urgentNow ? (urgentNow.kind === "words" ? plainMarkdown(urgentNow.text) : urgentNow.text) : "");
  return view;
}

function ownerCard(interactions: Interaction[], agent: TodoAgent, lookup: SessionLookup): Interaction | undefined {
  const { machine_id: mid, session_id: sid } = agent;
  const pending = lookup.pendingCards ? lookup.pendingCards(mid, sid) ?? [] : interactions;
  return sessionCard(pending.filter((ix) => !ix.delegating && !ix.mirror_of), mid, sid);
}

/** Every todo's row view by id, with its status section and why the sidebar's badge counts it (`needs`, its own set). */
function todoRowViews(
  todos: Todo[],
  lookup: SessionLookup,
  interactions: Interaction[],
  sections: ReadonlyMap<string, TriageSection>,
  needs: ReadonlyMap<string, OwnerNeed>,
  projectLabel?: (slug: string) => string,
  resolveDirection?: (todo: Todo) => EffectiveDirection | undefined,
): ReadonlyMap<string, TodoRowView> {
  return new Map(
    todos.map((td) => [
      td.id,
      {
        ...todoRowView(td, lookup, interactions),
        section: sections.get(td.id),
        owner: needs.get(td.id),
        projectLabel: projectLabel && td.project ? projectLabel(td.project) : undefined,
        directionLabel: resolveDirection ? resolveDirection(td)?.label : undefined,
      },
    ]),
  );
}

const TONE_CLASS = TODO_TONE_CLASS;

/** Opens a row's menu (#4429) where `at` says, for the to-do the row's checkbox shows open (`open`) or done. */
type TodoRowMenuOpener = (todo: Todo, at: MenuAt, open: boolean) => void;

interface TodoGoalRowControl {
  done: number;
  total: number;
  blockedCount: number;
  expanded: boolean;
  onToggle: () => void;
}

const SLIM_CUE_TONE: Record<SlimCueTone, string> = {
  attention: "text-attention font-medium",
  red: "text-red",
  green: "text-green",
  orange: "text-orange",
  muted: "text-ink-3",
};

function mergeTrainStateLabel(state: string): string {
  switch (state) {
    case "queued":
      return t("todos.mergetrain_state_queued");
    case "rebasing":
      return t("todos.mergetrain_state_rebasing");
    case "ci_running":
      return t("todos.mergetrain_state_ci_running");
    case "ready":
      return t("todos.mergetrain_state_ready");
    case "merging":
      return t("todos.mergetrain_state_merging");
    case "escalated":
      return t("todos.mergetrain_state_escalated");
    default:
      return state;
  }
}

function mergeTrainBadgeText(badge: MergeTrainTodoBadge): string {
  const stateText = mergeTrainStateLabel(badge.state);
  return badge.position > 0
    ? t("todos.mergetrain_position_badge", { position: badge.position, state: stateText })
    : stateText;
}

function slimMergeTrainBadgeText(badge: MergeTrainTodoBadge): string {
  if (badge.state === "queued" && badge.position > 0) {
    return t("todos.slim_mergetrain_queued", { position: badge.position });
  }
  return mergeTrainBadgeText(badge);
}

function safeHttpUrl(raw?: string): string | undefined {
  return safeWebURL(raw);
}

const TodoListItem = memo(function TodoListItem({
  todo,
  view,
  selected,
  reorganizeSelected,
  onToggleReorganize,
  slim,
  hideDirectionBadge,
  todosById,
  onSelect,
  onToggleDone,
  section,
  onRestore,
  onSeen,
  onMenu,
  goalSummary,
  parentGoalTitle,
  mergeTrainBadge,
}: {
  todo: Todo;
  view: TodoRowView;
  selected: boolean;
  /** Under todos_v2 (#14758), whether this row is selected in batch reorganize mode. */
  reorganizeSelected?: boolean;
  onToggleReorganize?: (id: string) => void;
  /** Under todos_v2 (#14830), compact 1-line personal / max 2-line task row layout. */
  slim?: boolean;
  /** Under todos_v2 (#17938), omit redundant inline direction tag when rows sit inside a direction group. */
  hideDirectionBadge?: boolean;
  /** Optional lookup map passed for waiting slim rows so raw `todo_...` wait targets resolve cleanly. */
  todosById?: ReadonlyMap<string, Todo>;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  /** Its status section (#3156): a PR to merge shows its link, a shelved one its Restore, a Just finished one Got it. */
  section?: TriageSection;
  onRestore?: (id: string) => void;
  /** Got it (#5707), on a row in Just finished (`section`, else the view's). */
  onSeen?: (id: string) => void;
  onMenu?: TodoRowMenuOpener;
  /** Under todos_v2 (#14661), compact child-task progress & inline expand toggle on a top-level parent goal row. */
  goalSummary?: TodoGoalRowControl;
  /** Under todos_v2 (#14661), parent goal title shown on a surfaced `needs_you` child blocker. */
  parentGoalTitle?: string;
  /** Under todos_v2 (#16433), non-coercive same-repo merge-train queue position badge. */
  mergeTrainBadge?: MergeTrainTodoBadge;
}) {
  const { status, dotState, activeCount } = view;
  const effectiveSection = section ?? view.section;
  const parkedRow = effectiveSection === "parked";
  // the PR it waits on: its current one, or an earlier one still open (#7935)
  const legacyMerges = todo.wait == null && parkedRow ? openMergePRs(todo, Date.now()) : [];
  const openPR = todo.wait?.kind === "pr_merge" || legacyMerges.length > 0;
  // its state word and tone; a waiting one's say what it waits on, attention only on the owner (#11090)
  const repoGateLook = slim ? repoGatePRLook : undefined;
  const look = todoLook(todo, status, view.wait, undefined, undefined, repoGateLook);
  const hasRestStatus = todo.wait != null || Boolean(todoRestLook(todo, status, undefined, repoGateLook));
  // its steps as the hub counts them (#11858, lib/todo-steps.ts): in place of Not started, or after its Agent count
  const steps = goalSummary ? undefined : stepsWords(todo, status);
  const held = heldDelivery(todo);
  const responsible = responsibleOf(todo);
  const thread = useStore((s) => todoThreadsOn(s.features)) ? boundThread(todo) : undefined;
  // a failed start still waiting on the owner reads open, not done, with why it did not start (#3713), in every grouping
  const open = checksAsOpen(todo, effectiveSection);
  const completeLabel = todo.kind === "concern" ? t("todos.report_concern_action") : t("todos.complete_todo");
  const failedOpen = open && Boolean(todo.start_error);
  // Its menu (#4429): a right click opens it at the pointer, the menu key and Shift+F10 on the row
  const menu = onMenu ? contextMenuProps((at) => onMenu(todo, at, open)) : undefined;
  const reorganizeRegistry = useContext(ReorganizeVisibilityContext);
  useLayoutEffect(() => {
    if (!onToggleReorganize || !reorganizeRegistry) return;
    return reorganizeRegistry.register(todo.id);
  }, [onToggleReorganize, reorganizeRegistry, todo.id]);

  if (slim) {
    const due = todoDueText(todo);
    const overdue = isOverdue(todo);
    const highPriority = todo.priority === "high" && !todo.done_at;
    const remind = Boolean(todo.remind_at && !todo.reminded_at && !todo.done_at);
    const childSummary = todo.child_summary && todo.child_summary.total > 0 ? todo.child_summary : undefined;
    const personalSlim = isPersonalSlimRow(todo, {
      hasGoalSummary: Boolean(goalSummary) || Boolean(childSummary) || Boolean(mergeTrainBadge),
      hasParentGoal: Boolean(parentGoalTitle),
      effectiveSection,
    });

    if (personalSlim) {
      return (
        <div
          data-todo={todo.id}
          data-todo-row-layout="personal-slim"
          aria-current={selected || undefined}
          onClick={(e) => {
            if ((e.target as HTMLElement).closest("button, a, input, select, textarea, summary")) return;
            onSelect(todo.id);
          }}
          onContextMenu={menu && ((e) => {
            if (!(e.target as HTMLElement).closest("a[href]")) menu.onContextMenu(e);
          })}
          onKeyDown={(e) => {
            menu?.onKeyDown(e);
          }}
          className={cx(
            "group/item relative flex cursor-pointer items-center gap-2 rounded-control px-2.5 py-2 transition-colors",
            selected ? "bg-accent-tint/70" : "hover:bg-hover",
          )}
        >
          {onToggleReorganize && (
            <input
              type="checkbox"
              data-testid="todo-reorganize-checkbox"
              data-todo-id={todo.id}
              aria-label={t("todos.reorganize_select_item", { title: todo.title })}
              checked={Boolean(reorganizeSelected)}
              onClick={(e) => e.stopPropagation()}
              onKeyDown={(e) => e.stopPropagation()}
              onChange={() => onToggleReorganize(todo.id)}
              className="relative z-10 size-4 shrink-0 cursor-pointer accent-accent"
            />
          )}
          <button
            type="button"
            aria-label={open ? completeLabel : t("todos.reopen_todo")}
            onClick={(e) => {
              e.stopPropagation();
              onToggleDone(todo, open);
            }}
            className={cx(
              "touch-hit relative z-10 flex size-4 shrink-0 items-center justify-center rounded-full border transition-colors",
              !open
                ? "border-accent bg-accent text-white"
                : "border-line-strong text-transparent hover:border-accent hover:text-accent",
            )}
          >
            <Icon name="check" size={10} stroke={2.6} />
          </button>
          <button
            type="button"
            data-testid="todo-row-open"
            aria-label={todo.title}
            onClick={() => onSelect(todo.id)}
            className="absolute inset-0 cursor-pointer rounded-[inherit] focus-visible:outline-2 focus-visible:outline-accent"
          />
          <span className="flex min-w-0 flex-1 items-center gap-1.5">
            <span
              className={cx(
                "truncate text-meta font-medium text-ink",
                !open && "font-normal text-ink-3",
                strikesThrough(todo, open) && "line-through",
              )}
              data-testid="todo-title"
            >
              {todo.title}
            </span>
            {!hideDirectionBadge && view.directionLabel && (
              <span
                data-testid="todo-direction-tag"
                className="max-w-28 shrink-0 truncate rounded-chip bg-field px-1.5 py-0.5 text-submeta text-ink-2"
              >
                {view.directionLabel}
              </span>
            )}
            {view.projectLabel && (
              <span
                data-testid="todo-project-tag"
                className="shrink-0 truncate rounded-chip bg-field px-1.5 py-0.5 text-submeta text-ink-3"
              >
                {view.projectLabel}
              </span>
            )}
          </span>
          <span className="flex shrink-0 items-center gap-1.5 text-submeta text-ink-3 tabular-nums">
            {highPriority && (
              <span
                data-testid="todo-priority-subtle"
                role="img"
                aria-label={t("todos.priority_high")}
                title={t("todos.priority_high")}
                className="inline-flex shrink-0 items-center text-ink-3"
              >
                <Icon name="flag" size={10} stroke={2} />
              </span>
            )}
            {due && (
              <span data-testid="todo-due-compact" className={cx("whitespace-nowrap", overdue && "text-red")}>
                {due}
              </span>
            )}
            {remind && todo.remind_at && (
              <span
                role="img"
                aria-label={t("todos.reminder_at", { when: formatDate(todo.remind_at, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) })}
                className="inline-flex shrink-0"
              >
                <Icon name="bell" size={11} stroke={2} />
              </span>
            )}
            {effectiveSection === "just_done" && seenCanFile(todo) && onSeen && (
              <span className="relative z-10">
                <SeenButton onClick={() => onSeen(todo.id)} />
              </span>
            )}
          </span>
        </div>
      );
    }

    const pr = prLink(todo.pr_status?.url ?? todo.pr_url);
    const hasLine1PR = Boolean(!goalSummary && !childSummary && pr);
    const activeAsk = openAsk(todo);
    const repoCount = repoCountOf(todo);
    const rowRepoGate = shouldShowRowRepoGate(todo.repo_gate, {
      effectiveSection,
      hasOpenAsk: Boolean(activeAsk),
      askOutcome: activeAsk?.outcome,
      done: Boolean(todo.done_at),
      hasLine1PR,
      hasActiveDeliveryPR: Boolean(pr || todo.repo_gate?.blocking_pr?.trim()),
      hasExecutionFailure: failedOpen,
    })
      ? todo.repo_gate
      : undefined;
    const rowRepoGateText = rowRepoGate ? formatRowRepoGate(rowRepoGate, { hasLine1PR }) : undefined;
    const rowRepoGateTitle = rowRepoGate ? formatRowRepoGateTitle(rowRepoGate) : undefined;
    const rowRepoGateToneClass: Record<RepoGateTone, string> = {
      green: "text-green font-medium",
      red: "text-red font-medium",
      orange: "text-orange font-medium",
      muted: "text-ink-3",
    };
    const cue = slimSecondaryCue(todo, view, {
      effectiveSection,
      failedOpen,
      held,
      due,
      overdue,
      line1PrText: hasLine1PR ? pr?.text : undefined,
      todosById,
    });
    const showSeenAction = effectiveSection === "just_done" && seenCanFile(todo) && Boolean(onSeen);
    const showRestoreAction = section === "shelved" && Boolean(onRestore);
    const line2Budget = selectSlimLine2Badges(
      {
        goalBlocked: Boolean(goalSummary && goalSummary.blockedCount > 0),
        childNeedsYou: Boolean(!goalSummary && childSummary && childSummary.needs_you > 0),
        repoGate: Boolean(rowRepoGate && rowRepoGateText),
        mergeTrain: Boolean(mergeTrainBadge),
        parentGoal: Boolean(parentGoalTitle),
        childWorking: Boolean(childSummary && childSummary.working > 0),
        direction: !hideDirectionBadge && Boolean(view.directionLabel),
        project: Boolean(view.projectLabel),
        repoCount: Boolean(!rowRepoGate && repoCount > 0),
      },
      { hasCue: Boolean(cue), hasAction: showSeenAction || showRestoreAction },
    );
    const badgeMaxClass = cue ? "max-w-[42%]" : "max-w-[55%]";
    const renderLine2Badge = (kind: SlimLine2BadgeKind): ReactNode => {
      switch (kind) {
        case "parent_goal":
          return parentGoalTitle ? (
            <span key="parent_goal" data-testid="todo-parent-goal" className={cx("inline-flex min-w-0 shrink items-center gap-1 truncate", badgeMaxClass)}>
              <Icon name="target" size={11} stroke={2} className="shrink-0" />
              <span className="truncate">{t("todos.goal_parent_hint", { title: parentGoalTitle })}</span>
            </span>
          ) : null;
        case "goal_blocked":
          return goalSummary && goalSummary.blockedCount > 0 ? (
            <span key="goal_blocked" data-testid="todo-goal-blocked" className={cx("shrink-0 truncate whitespace-nowrap font-medium text-attention", badgeMaxClass)}>
              {t("todos.goal_blocked", { count: goalSummary.blockedCount })}
            </span>
          ) : null;
        case "child_needs_you":
          return !goalSummary && childSummary && childSummary.needs_you > 0 ? (
            <span key="child_needs_you" data-testid="todo-child-summary-needs-you" className={cx("shrink-0 truncate whitespace-nowrap font-medium text-attention", badgeMaxClass)}>
              {t("todos.goal_blocked", { count: childSummary.needs_you })}
            </span>
          ) : null;
        case "child_working":
          return childSummary && childSummary.working > 0 ? (
            <span key="child_working" data-testid="todo-child-summary-working" className={cx("shrink-0 truncate whitespace-nowrap text-ink-2 tabular-nums", badgeMaxClass)}>
              {t("todos.child_summary_working", { count: childSummary.working })}
            </span>
          ) : null;
        case "mergetrain":
          return mergeTrainBadge ? (
            <span
              key="mergetrain"
              data-testid="todo-mergetrain-badge"
              data-mergetrain-state={mergeTrainBadge.state}
              data-mergetrain-position={mergeTrainBadge.position}
              title={mergeTrainBadge.repo_key}
              className={cx(
                "shrink-0 truncate rounded-chip px-1.5 text-submeta font-medium whitespace-nowrap tabular-nums",
                badgeMaxClass,
                mergeTrainBadge.state === "escalated"
                  ? "bg-orange-tint text-attention"
                  : "bg-field text-ink-2",
              )}
            >
              {slimMergeTrainBadgeText(mergeTrainBadge)}
            </span>
          ) : null;
        case "direction":
          return !hideDirectionBadge && view.directionLabel ? (
            <span key="direction" data-testid="todo-direction-tag" className={cx("max-w-28 shrink-0 truncate text-ink-2", cue && "max-w-[42%]")}>
              {view.directionLabel}
            </span>
          ) : null;
        case "project":
          return view.projectLabel ? (
            <span key="project" data-testid="todo-project-tag" className={cx("max-w-24 shrink-0 truncate rounded-chip bg-field px-1.5 text-ink-3", cue && "max-w-[42%]")}>
              {view.projectLabel}
            </span>
          ) : null;
        case "repo_gate":
          return rowRepoGate && rowRepoGateText ? (
            <span
              key="repo_gate"
              data-testid="todo-row-repo-gate"
              data-gate-state={rowRepoGate.state}
              title={rowRepoGateTitle}
              className={cx("shrink-0 truncate whitespace-nowrap tabular-nums", cue ? "max-w-[42%]" : "max-w-44", rowRepoGateToneClass[repoGateTone(rowRepoGate.state)])}
            >
              {rowRepoGateText}
            </span>
          ) : null;
        case "repo_count":
          return repoCount > 0 ? (
            <span key="repo_count" data-testid="todo-repo-count" className="shrink-0 whitespace-nowrap tabular-nums">
              {t("todos.slim_repo_count", { count: repoCount })}
            </span>
          ) : null;
      }
    };
    const hasLine2 = Boolean(
      line2Budget.shown.length > 0 ||
      cue ||
      showSeenAction ||
      showRestoreAction,
    );

    return (
      <div
        data-todo={todo.id}
        data-todo-row-layout="task-slim"
        aria-current={selected || undefined}
        onClick={(e) => {
          if ((e.target as HTMLElement).closest("button, a, input, select, textarea, summary")) return;
          onSelect(todo.id);
        }}
        onContextMenu={menu && ((e) => {
          if (!(e.target as HTMLElement).closest("a[href]")) menu.onContextMenu(e);
        })}
        onKeyDown={(e) => {
          menu?.onKeyDown(e);
        }}
        className={cx(
          "group/item relative flex cursor-pointer gap-2 rounded-control px-2.5 py-2 transition-colors",
          selected ? "bg-accent-tint/70" : "hover:bg-hover",
        )}
      >
        {onToggleReorganize && (
          <input
            type="checkbox"
            data-testid="todo-reorganize-checkbox"
            data-todo-id={todo.id}
            aria-label={t("todos.reorganize_select_item", { title: todo.title })}
            checked={Boolean(reorganizeSelected)}
            onClick={(e) => e.stopPropagation()}
            onKeyDown={(e) => e.stopPropagation()}
            onChange={() => onToggleReorganize(todo.id)}
            className="relative z-10 mt-0.5 size-4 shrink-0 cursor-pointer accent-accent"
          />
        )}
        <button
          type="button"
          aria-label={open ? completeLabel : t("todos.reopen_todo")}
          onClick={(e) => {
            e.stopPropagation();
            onToggleDone(todo, open);
          }}
          className={cx(
            "touch-hit relative z-10 mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border transition-colors",
            !open
              ? "border-accent bg-accent text-white"
              : "border-line-strong text-transparent hover:border-accent hover:text-accent",
          )}
        >
          <Icon name="check" size={10} stroke={2.6} />
        </button>
        <button
          type="button"
          data-testid="todo-row-open"
          aria-label={todo.title}
          onClick={() => onSelect(todo.id)}
          className="absolute inset-0 cursor-pointer rounded-[inherit] focus-visible:outline-2 focus-visible:outline-accent"
        />

        <div className="min-w-0 flex-1">
          {/* Line 1: status dot + truncated title + compact right anchor */}
          <div className="flex min-w-0 items-center gap-1.5" data-testid="todo-slim-line1">
            {open && <StatusMark state={dotState} size="sm" />}
            <span
              className={cx(
                "min-w-0 flex-1 truncate text-meta font-semibold text-ink",
                !view.ultracode && !open && "font-normal text-ink-3",
                !view.ultracode && strikesThrough(todo, open) && "line-through",
              )}
              data-testid="todo-title"
            >
              {todo.title}
            </span>
            {view.ultracode && <UltraMark size={11} />}
            <span className="flex shrink-0 items-center gap-1.5 text-submeta text-ink-3 tabular-nums">
              {highPriority && (
                <span
                  data-testid="todo-priority-subtle"
                  role="img"
                  aria-label={t("todos.priority_high")}
                  title={t("todos.priority_high")}
                  className="inline-flex shrink-0 items-center text-ink-3"
                >
                  <Icon name="flag" size={10} stroke={2} />
                </span>
              )}
              {goalSummary ? (
                <button
                  type="button"
                  data-testid="todo-goal-toggle"
                  aria-expanded={goalSummary.expanded}
                  aria-label={t(goalSummary.expanded ? "todos.goal_collapse" : "todos.goal_expand", {
                    title: todo.title,
                    done: goalSummary.done,
                    total: goalSummary.total,
                  })}
                  onClick={(e) => {
                    e.stopPropagation();
                    goalSummary.onToggle();
                  }}
                  className="touch-hit relative z-10 inline-flex h-5 shrink-0 items-center gap-1 rounded-full bg-field px-1.5 text-submeta font-medium whitespace-nowrap text-ink-2 tabular-nums transition-colors hover:bg-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-accent"
                >
                  <Icon
                    name="chevron"
                    size={10}
                    stroke={2.4}
                    className="shrink-0 opacity-70 transition-transform"
                    style={{ transform: goalSummary.expanded ? "none" : "rotate(-90deg)" }}
                  />
                  <span data-testid="todo-goal-progress">{t("todos.goal_progress", { done: goalSummary.done, total: goalSummary.total })}</span>
                </button>
              ) : childSummary ? (
                <span
                  data-testid="todo-child-summary-badge"
                  className="inline-flex h-5 shrink-0 items-center rounded-full bg-field px-1.5 text-submeta font-medium whitespace-nowrap text-ink-2 tabular-nums"
                >
                  {t("todos.goal_progress", { done: childSummary.done, total: childSummary.total })}
                </span>
              ) : pr ? (
                <span data-testid="todo-slim-pr" className="whitespace-nowrap font-medium text-ink-2">
                  {pr.text}
                </span>
              ) : (
                <span className="whitespace-nowrap" data-testid="todo-age">
                  {view.age}
                </span>
              )}
            </span>
          </div>

          {/* Line 2: budgeted metadata badges + guaranteed readable wait/decision cue (#17696) */}
          {hasLine2 && (
            <div
              className="mt-1 flex min-w-0 items-center gap-1.5 overflow-hidden text-submeta text-ink-3"
              data-testid="todo-slim-line2"
              data-badge-count={line2Budget.shown.length}
              data-omitted-badges={line2Budget.omittedCount || undefined}
            >
              {line2Budget.shown.map(renderLine2Badge)}
              {cue && (
                <span
                  data-testid={cue.testId}
                  data-ask={cue.askOutcome}
                  title={cue.text}
                  className={cx("min-w-0 flex-1 truncate", SLIM_CUE_TONE[cue.tone])}
                >
                  {cue.text}
                </span>
              )}
              {showSeenAction && onSeen && (
                <span className="relative z-10 ml-auto shrink-0">
                  <SeenButton onClick={() => onSeen(todo.id)} />
                </span>
              )}
              {showRestoreAction && onRestore && (
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation();
                    onRestore(todo.id);
                  }}
                  className="touch-hit relative z-10 ml-auto shrink-0 whitespace-nowrap font-medium text-accent-ink hover:underline"
                >
                  {t("todos.restore")}
                </button>
              )}
            </div>
          )}
        </div>
      </div>
    );
  }

  return (
    <div
      data-todo={todo.id}
      aria-current={selected || undefined}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest("button, a, input, select, textarea, summary")) return;
        onSelect(todo.id);
      }}
      // a right click on its PR link keeps the browser's menu (Copy link, open in a new tab)
      onContextMenu={menu && ((e) => {
        if (!(e.target as HTMLElement).closest("a[href]")) menu.onContextMenu(e);
      })}
      // Shift+F10 / ContextMenu key opens the row's menu
      onKeyDown={(e) => {
        menu?.onKeyDown(e);
      }}
      className={cx(
        "group/item relative flex cursor-pointer gap-2.5 rounded-control p-2.5 transition-colors",
        selected ? "bg-accent-tint/70" : "hover:bg-hover",
      )}
    >
      {/* Batch reorganize selection checkbox under todos_v2 (#14758) */}
      {onToggleReorganize && (
        <input
          type="checkbox"
          data-testid="todo-reorganize-checkbox"
          data-todo-id={todo.id}
          aria-label={t("todos.reorganize_select_item", { title: todo.title })}
          checked={Boolean(reorganizeSelected)}
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
          onChange={() => onToggleReorganize(todo.id)}
          className="relative z-10 mt-0.5 size-4 shrink-0 cursor-pointer accent-accent"
        />
      )}
      {/* Checkbox button: touch-hit makes the 16px circle a 44px target on touch, its own (no row underneath) */}
      <button
        type="button"
        aria-label={open ? completeLabel : t("todos.reopen_todo")}
        onClick={(e) => {
          e.stopPropagation();
          onToggleDone(todo, open);
        }}
        className={cx(
          "touch-hit relative z-10 mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border transition-colors",
          !open
            ? "border-accent bg-accent text-white"
            : "border-line-strong hover:border-accent hover:text-accent text-transparent",
        )}
      >
        <Icon name="check" size={10} stroke={2.6} />
      </button>
      <button
        type="button"
        data-testid="todo-row-open"
        aria-label={todo.title}
        onClick={() => onSelect(todo.id)}
        className="absolute inset-0 cursor-pointer rounded-[inherit] focus-visible:outline-2 focus-visible:outline-accent"
      />

      {/* Body */}
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline justify-between gap-2">
          {view.ultracode || todo.kind === "concern" || view.owner || view.projectLabel ? (
            <span className="flex min-w-0 items-center gap-1">
              <span className={cx("truncate text-[13.5px] font-semibold text-ink", !view.ultracode && !open && "text-ink-3 font-normal", !view.ultracode && strikesThrough(todo, open) && "line-through")} data-testid="todo-title">{todo.title}</span>
              {view.ultracode && <UltraMark size={11} />}
              {todo.kind === "concern" && <Tag><span data-testid="todo-concern">{t("todos.concern")}</span></Tag>}
              {view.projectLabel && <Tag><span data-testid="todo-project-tag">{view.projectLabel}</span></Tag>}
              {view.owner && <OwnerMark need={view.owner} />}
            </span>
          ) : (
            <span className={cx("truncate text-[13.5px] font-semibold text-ink", !open && "text-ink-3 font-normal", strikesThrough(todo, open) && "line-through")} data-testid="todo-title">
              {todo.title}
            </span>
          )}
          <span className="flex shrink-0 items-center gap-1.5 text-[11px] text-ink-3" data-testid="todo-age">
            {thread && (
              <span data-testid="todo-thread-mark" role="img" aria-label={t("todos.thread_mark")} title={threadLine(thread)} className="inline-flex">
                <Icon name="chat" size={12} stroke={2} />
              </span>
            )}
            {view.age}
          </span>
        </div>

        {/* Surfaced blocker parent goal context under todos_v2 (#14661) */}
        {parentGoalTitle && (
          <div className="mt-0.5 flex min-w-0 items-center gap-1 text-xs text-ink-3" data-testid="todo-parent-goal">
            <Icon name="target" size={11} stroke={2} className="shrink-0" />
            <span className="truncate">{t("todos.goal_parent_hint", { title: parentGoalTitle })}</span>
          </div>
        )}

        {/* Parent goal progress & accessible inline fold toggle under todos_v2 (#14661) */}
        {goalSummary && (
          <div className="mt-1 flex flex-wrap items-center gap-1.5" data-testid="todo-goal-summary">
            <button
              type="button"
              data-testid="todo-goal-toggle"
              aria-expanded={goalSummary.expanded}
              aria-label={t(goalSummary.expanded ? "todos.goal_collapse" : "todos.goal_expand", {
                title: todo.title,
                done: goalSummary.done,
                total: goalSummary.total,
              })}
              onClick={(e) => {
                e.stopPropagation();
                goalSummary.onToggle();
              }}
              className="touch-hit relative z-10 inline-flex h-5 shrink-0 items-center gap-1 rounded-full bg-field px-2 text-xs font-medium whitespace-nowrap text-ink-2 tabular-nums transition-colors hover:bg-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-accent"
            >
              <Icon
                name="chevron"
                size={10}
                stroke={2.4}
                className="shrink-0 opacity-70 transition-transform"
                style={{ transform: goalSummary.expanded ? "none" : "rotate(-90deg)" }}
              />
              <span data-testid="todo-goal-progress">{t("todos.goal_progress", { done: goalSummary.done, total: goalSummary.total })}</span>
            </button>
            {goalSummary.blockedCount > 0 && (
              <span data-testid="todo-goal-blocked" className="inline-flex shrink-0">
                <Tag tone="accent">{t("todos.goal_blocked", { count: goalSummary.blockedCount })}</Tag>
              </span>
            )}
          </div>
        )}

        {/* Status Line (parked rows collapse to title + concise TaskWaitInfo summary line) */}
        {!parkedRow && (
          <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[12px]" data-testid="todo-status-line">
            {open && <StatusMark state={dotState} size="sm" />}
            <span className={cx("shrink-0 whitespace-nowrap font-medium", TONE_CLASS[look.tone])} data-testid="todo-status">{steps?.label ?? view.supervisorLabel ?? look.label}</span>
            {!todo.done_at && activeCount > 0 && (
              <>
                <span className="text-ink-3">·</span>
                <span className="text-ink-3">{t("todos.agent_count", { count: activeCount })}</span>
              </>
            )}
            {steps?.after && (
              <>
                <span className="text-ink-3">·</span>
                <span className="shrink-0 whitespace-nowrap text-ink-3" data-testid="todo-steps">{steps.after}</span>
              </>
            )}
            {responsible && (
              <>
                <span className="text-ink-3">·</span>
                <span
                  data-testid="todo-responsible"
                  data-kind={responsible.kind}
                  title={t("todos.responsible_label", { name: responsible.name })}
                  className="inline-flex min-w-0 items-center gap-1 truncate text-ink-2"
                >
                  <Icon name={responsible.kind === "person" ? "person" : "terminal"} size={11} stroke={2} className="shrink-0 text-ink-3" />
                  <span className="truncate">{responsible.name}</span>
                </span>
              </>
            )}
            {/* its steps were handed out: what is next once they are all closed, else nothing about handing it over */}
            {!todo.done_at && activeCount === 0 && (steps?.tail !== undefined ? (
              steps.tail && <span className="truncate text-ink-3" data-testid="todo-steps-next">· {steps.tail}</span>
            ) : !responsible && !hasRestStatus ? (
              <span className="truncate text-ink-3">· {t("todos.not_handed")}</span>
            ) : null)}
            {(section ?? view.section) === "just_done" && seenCanFile(todo) && onSeen ? (
              <span className="relative z-10 ml-auto flex shrink-0 items-center gap-1.5">
                {!todo.done_at && <ReviewBadge todo={todo} />}
                <SeenButton onClick={() => onSeen(todo.id)} />
              </span>
            ) : (
              !todo.done_at && <ReviewBadge todo={todo} className="ml-auto" />
            )}
          </div>
        )}

        {/* What it waits on from the user, with the one tap that answers it (#2056) */}
        <div className="relative z-10">
          <TodoAskLine todo={todo} className="mt-1" />
        </div>

        {/* Due date, a reminder still to push, high priority (#1683) */}
        <TodoPlanLine todo={todo} />
        {(todo.wait != null || parkedRow) && <div className="relative z-10 mt-1" data-testid={openPR ? "todo-pr-line" : undefined}><TaskWaitInfo todo={todo} merges={legacyMerges} /></div>}

        {/* Why its agent did not start, while it waits on the owner (DESIGN_SYSTEM 5.6: what failed, in red) */}
        {failedOpen && <div className="mt-1 truncate text-[11.5px] text-red" data-testid="todo-start-failed">{t("todos.start_failed", { error: todo.start_error ?? "" })}</div>}

        {/* Why it was cancelled, or why the legacy hub auto-closed it */}
        {isCancelled(todo) && cancellationReason(todo) && (
          <div className="mt-1 truncate text-xs text-ink-3" data-testid="todo-cancelled-reason">{cancellationReason(todo)}</div>
        )}
        {isAutoDone(todo) && todo.done_reason && (
          <div className="mt-1 truncate text-[11.5px] text-ink-3">{todo.done_reason}</div>
        )}

        {/* Where its finished work is held short of live */}
        {held && (
          <div className="mt-1 truncate text-[11.5px] text-ink-3">
            <span className="font-medium text-orange">{t(`todos.delivery_${held.state}`)}</span>
            {held.gate && <> · {t("todos.delivery_gate", { gate: held.gate })}</>}
            {held.unblock && ["user", "agent", "other", "supervisor"].includes(held.unblock) && <> · {t(`todos.unblock_${held.unblock}`)}</>}
          </div>
        )}

        {/* Most Urgent Agent Line */}
        {view.urgentLine && !parkedRow && todo.wait == null && (
          <div className="mt-1 truncate text-[11.5px] text-ink-3" data-orphaned={view.orphaned || undefined}>
            {view.urgentLead && <span className={cx("font-medium", view.orphaned ? "text-orange" : "text-attention")}>{view.urgentLead}</span>}
            {view.urgentText}
          </div>
        )}


        {/* Shelved: one tap brings it back (#3156); nothing is deleted */}
        {section === "shelved" && onRestore && (
          <div className="relative z-10 mt-1.5" onClick={(e) => e.stopPropagation()}>
            <Button tone="secondary" icon="undo" onClick={() => onRestore(todo.id)} className="h-7 text-[12px] whitespace-nowrap">
              {t("todos.restore")}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
});

/** Got it on an ordinary, unsupervised Delivered · awaiting review row (#5707): files it under Done at once. A small
 * capsule at the end of the status line, under the time (#6578), so the row is as tall as any other: its 20 px hang
 * over the 18 px line (the negative margin keeps them out of the line's height), and `touch-hit` gives a finger 44 px.
 * Its press stays its own and never opens the row. Supervised work and unaccepted deliveries get no such receipt. */
function SeenButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      data-testid="todo-seen"
      onClick={(e) => {
        e.stopPropagation();
        onClick();
      }}
      className="touch-hit -my-1 inline-flex h-5 shrink-0 items-center gap-1 rounded-full bg-surface px-2 text-[11.5px] font-medium whitespace-nowrap text-ink-2 shadow-btn transition-colors hover:bg-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-accent"
    >
      <Icon name="check" size={11} stroke={2.2} />
      {t("todos.seen")}
    </button>
  );
}

const PLAN_FIELD = "rounded-control border border-line bg-canvas px-2 py-1 text-[12.5px] text-ink outline-none focus:border-accent";

/** "YYYY-MM-DD", "HH:MM" and "YYYY-MM-DDTHH:MM" of a moment in local time, as date / time inputs take them. */
export function localParts(ms: number): { day: string; time: string } {
  const d = new Date(ms);
  if (!Number.isFinite(d.getTime())) return { day: "", time: "" };
  const y = d.getFullYear();
  if (y < 0 || y > 9999) return { day: "", time: "" };
  const p = (n: number) => String(n).padStart(2, "0");
  return { day: `${String(y).padStart(4, "0")}-${p(d.getMonth() + 1)}-${p(d.getDate())}`, time: `${p(d.getHours())}:${p(d.getMinutes())}` };
}

const LOCAL_DATETIME_RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?$/;

/** Validates a 4-digit calendar YYYY-MM-DD string, rejecting rollover dates (e.g. 2026-02-30) and out-of-range years. */
export function validDay(day: string): boolean {
  const ms = parseDayKey(day);
  if (ms === undefined) return false;
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!m) return false;
  const d = new Date(ms);
  return d.getFullYear() === Number(m[1]) && d.getMonth() + 1 === Number(m[2]) && d.getDate() === Number(m[3]);
}

/**
 * A date or time field of the detail's plan (audit item 2): it saves when the editing is done, not on each typed
 * digit. A day typed segment by segment is a valid date after its first digit, so saving then stored a day the user
 * was still typing and, with the field disabled while it saved, took the focus away before the next digit. Typed
 * changes save when the field is left or on Enter; a change made without typing (a pick in the browser's picker) saves
 * at once. What was typed shows until the save is through.
 */
export function safeISO(raw: string): string | undefined {
  const s = raw.trim();
  const m = LOCAL_DATETIME_RE.exec(s);
  if (!m) return undefined;
  const y = Number(m[1]), mo = Number(m[2]), da = Number(m[3]), hh = Number(m[4]), mi = Number(m[5]), ss = Number(m[6] ?? "0");
  if (hh > 23 || mi > 59 || ss > 59) return undefined;
  const d = new Date(s);
  if (!Number.isFinite(d.getTime()) || d.getFullYear() !== y || d.getMonth() + 1 !== mo || d.getDate() !== da) return undefined;
  return d.toISOString();
}

function PlanField({ value, onCommit, ...field }: { value: string; onCommit: (value: string) => Promise<void> } & Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">) {
  const [draft, setDraft] = useState<string>();
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  // the change comes from the keyboard: it waits to be committed
  const typed = useRef(false);
  // the value on its way to the hub: Enter, then the field left before the save is through, sends it once
  const saving = useRef<string | undefined>(undefined);
  // a part left half-typed (badInput: the browser says "" for it) goes back to what is saved, it does not clear it
  const commit = async (el: HTMLInputElement, next = draft) => {
    typed.current = false;
    if (next !== undefined && next === saving.current) return;
    if (next === undefined || next === value || el.validity.badInput) return setDraft(undefined);
    setDraft(next);
    saving.current = next;
    try {
      await onCommit(next);
    } finally {
      if (saving.current === next) saving.current = undefined;
      if (mountedRef.current) setDraft((d) => (d === next ? undefined : d));
    }
  };
  return (
    <input
      {...field}
      value={draft ?? value}
      onPointerDown={() => (typed.current = false)}
      onKeyDown={(e) => {
        if (e.key === "Enter") void commit(e.currentTarget);
        else if (e.key !== "Tab") typed.current = true;
      }}
      onChange={(e) => (typed.current ? setDraft(e.target.value) : void commit(e.target, e.target.value))}
      onBlur={(e) => void commit(e.currentTarget)}
      className={PLAN_FIELD}
    />
  );
}

/** The detail's due date (a day, with a time when one is set), reminder, priority and project. A pick from a menu is
 *  saved at once, a date or time once it is entered (PlanField); no field is disabled while it saves, so the focus stays. */
function TodoPlanEditor({ todo, todosV2 }: { todo: Todo; todosV2?: boolean }) {
  const [busy, setBusy] = useState(false);
  const projects = projectChoices(useTodoProjects(), todo.project);
  const save = async (patch: TodoPatch) => {
    setBusy(true);
    try {
      await updateTodo(todo.id, patch);
      await refreshTodos();
    } catch (e) {
      toast(t("todos.save_plan_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  const at = todo.due_at ? Date.parse(todo.due_at) : NaN;
  const due = !Number.isFinite(at) ? { day: todoDueDay(todo) === undefined ? "" : (todo.due_date ?? ""), time: "" } : localParts(at);
  const remindAt = todo.remind_at ? Date.parse(todo.remind_at) : NaN;
  const remindParts = Number.isFinite(remindAt) ? localParts(remindAt) : undefined;
  const remind = remindParts?.day ? `${remindParts.day}T${remindParts.time}` : "";
  const moment = (day: string, time: string) => safeISO(`${day}T${time}`);
  return (
    <div
      className={cx(
        "flex flex-wrap items-center text-ink-2",
        todosV2 ? "mt-3 gap-x-4 gap-y-2 text-meta" : "mt-4 gap-x-5 gap-y-2.5 text-[12.5px]",
      )}
      data-testid="todo-plan"
      aria-busy={busy}
    >
      <span className="inline-flex items-center gap-1.5">
        <Icon name="calendar" size={13} stroke={2} />
        <span className="whitespace-nowrap">{t("todos.due_label")}</span>
        <PlanField
          type="date"
          aria-label={t("todos.due_label")}
          value={due.day}
          onCommit={async (day) => {
            if (!day) return save({ due_at: null });
            if (!validDay(day)) return;
            if (!due.time) return save({ due_date: day });
            const iso = moment(day, due.time);
            if (!iso) return;
            return save({ due_at: iso });
          }}
          data-testid="todo-plan-due"
        />
        {due.day && (
          <PlanField
            type="time"
            aria-label={t("todos.due_time")}
            value={due.time}
            onCommit={async (time) => {
              if (!time) return save({ due_date: due.day });
              const iso = moment(due.day, time);
              if (!iso) return;
              return save({ due_at: iso });
            }}
            data-testid="todo-plan-due-time"
          />
        )}
      </span>
      <span className="inline-flex items-center gap-1.5">
        <Icon name="bell" size={13} stroke={2} />
        <span className="whitespace-nowrap">{t("todos.remind_label")}</span>
        <PlanField
          type="datetime-local"
          aria-label={t("todos.remind_label")}
          value={remind}
          onCommit={async (v) => {
            if (!v) return save({ remind_at: null });
            const iso = safeISO(v);
            if (!iso) return;
            return save({ remind_at: iso });
          }}
          data-testid="todo-plan-remind"
        />
      </span>
      <span className={cx("inline-flex items-center gap-1.5", todosV2 && "text-ink-3")}>
        <Icon name="flag" size={13} stroke={2} />
        <span className="whitespace-nowrap">{t("todos.priority_label")}</span>
        <select
          aria-label={t("todos.priority_label")}
          value={todo.priority ?? ""}
          onChange={(e) => void save({ priority: e.target.value })}
          className={cx(PLAN_FIELD, todosV2 && "text-ink-3")}
          data-testid="todo-plan-priority"
        >
          <option value="high">{t("todos.priority_option_high")}</option>
          <option value="">{t("todos.priority_normal")}</option>
          <option value="low">{t("todos.priority_low")}</option>
        </select>
      </span>
      <span className={cx("inline-flex min-w-0 items-center gap-1.5", todosV2 && "max-w-full")}>
        <Icon name="folder" size={13} stroke={2} className="shrink-0" />
        <span className="whitespace-nowrap">{t("todos.project_label")}</span>
        <select
          aria-label={t("todos.project_label")}
          value={todo.project ?? ""}
          onChange={(e) => void save({ project: e.target.value })}
          className={cx(PLAN_FIELD, todosV2 ? "min-w-0 max-w-md truncate" : "max-w-56 truncate")}
          data-testid="todo-plan-project"
        >
          <option value="">{t("todos.project_none")}</option>
          {projects.map((p, index) => (
            <option key={p.slug || `project-${index}`} value={p.slug ?? ""}>
              {p.name}
            </option>
          ))}
        </select>
      </span>
    </div>
  );
}

/** A to-do whose decision is held for the briefing (#4930, #5803, docs/assistant/todos.md "Decisions that can wait"):
 *  nothing was pushed for it, and it comes with the next briefing. The to-do's page writes out what that means under
 *  its ask; a row's ask line carries the quiet mark itself (views/TodoAsk AskLine), as the decision's card does
 *  (views/assistant/BriefingMark). Nothing for any other to-do, and for a hub that does not say. */
export function HeldForBriefing({ todo }: { todo: Pick<Todo, "ask"> }) {
  const ask = todo.ask;
  if (!ask?.verdict || !ask.asked_at || ask.for_briefing !== true) return null;
  return (
    <p data-for-briefing="caption" className="mt-2 flex min-w-0 items-start gap-1.5 px-1 text-xs text-ink-2 [overflow-wrap:anywhere]">
      <Icon name="clock" size={13} className="mt-0.5 shrink-0" />
      <span>
        <span className="font-medium">{t("todos.ask_held")}</span>
        {t("assistant-decisionrow.separator")}
        {t("todos.ask_held_hint")}
      </span>
    </p>
  );
}

/** A to-do's due date, a reminder still to push and high priority under its status (#1683); nothing without them. */
function TodoPlanLine({ todo }: { todo: Todo }) {
  const due = todoDueText(todo);
  const remind = Boolean(todo.remind_at && !todo.reminded_at && !todo.done_at);
  const high = todo.priority === "high" && !todo.done_at;
  if (!due && !remind && !high) return null;
  const overdue = isOverdue(todo);
  return (
    <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-3" data-testid="todo-plan-line">
      {high && (
        <span role="img" aria-label={t("todos.priority_high")} title={t("todos.priority_high")} className="inline-flex text-orange">
          <Icon name="flag" size={12} stroke={2} />
        </span>
      )}
      {due && (
        <span className={cx("inline-flex min-w-0 items-center gap-1", overdue && "text-red")}>
          <Icon name="calendar" size={12} stroke={2} />
          <span className="truncate">{t(overdue ? "todos.was_due" : "todos.due", { when: due })}</span>
        </span>
      )}
      {remind && todo.remind_at && (
        <span role="img" aria-label={t("todos.reminder_at", { when: formatDate(todo.remind_at, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) })} className="inline-flex">
          <Icon name="bell" size={12} stroke={2} />
        </span>
      )}
    </div>
  );
}

/** What a waiting agent's card asks, as one line of plain text; empty for a plan (its kicker says it) or no card. */
function askText(ix: Interaction | undefined): string {
  if (!ix || ix.kind === "plan") return "";
  return plainMarkdown(interactionAsk(ix)).replace(/\s+/g, " ").trim();
}

// ── Todo Detail ──────────────────────────────────────────────

// A task's brief (#3822) comes with the first to-do that has one, not with the page.
const TodoRequestGraph = lazy(() => import("./TodoRequestGraph"));
const TaskBriefPanel = lazy(() => import("./assistant/TaskBrief").then((m) => ({ default: m.TaskBriefPanel })));
const WorkspaceNaming = lazyPage(() => import("../ui/WorkspaceNaming").then((m) => m.WorkspaceNaming));
const TaskBriefHistory = lazy(() => import("./assistant/TaskBrief").then((m) => ({ default: m.TaskBriefHistory })));
// the question a failed start's tab stopped on, answered on its page (#10365): only for such a failure
const ShellQuestion = lazy(() => import("./ShellQuestionCard"));
const TodoImageViewer = lazyPage(() => import("./chat/ImageViewer").then((m) => m.ImageViewer));

const TODO_IMAGE_LIMIT = 50;
const TODO_IMAGE_PARALLEL = 3;

/** A picture picked for a new to-do and still being made ready to go with it (NewTodoDialog). */
type PreparingImage = { id: string; name: string; file: File };

type TodoImageEntry = {
  id: string;
  path: string;
  state: "queued" | "reading" | "ready" | "failed";
  url?: string;
};
type TodoImageBook = {
  scope: string;
  owner: string;
  manager: string;
  token: string;
  ac: AbortController;
  entries: Map<string, TodoImageEntry>;
  queue: TodoImageEntry[];
  running: number;
};

/** Only the owning manager's own media route is safe to turn back into apiBlob's path form. */
function storedTodoImagePath(todoId: string, image: TodoImage): string | undefined {
  const expected = `/api/v1/todos/${encodeURIComponent(todoId)}/images/${encodeURIComponent(image.id)}`;
  return image.url === expected ? image.url.slice("/api/v1".length) : undefined;
}

/** Read each listed private image once, at most three at a time. A refresh reconciles metadata without replacing
 *  unchanged object URLs; a credential, manager or to-do change retires the whole book and every late answer. */
function useStoredTodoImages(todo: Todo, owner: string) {
  const manager = todo.manager ?? defaultManager();
  const scope = `${owner}/${manager}/${todo.id}`;
  const images = useMemo(() => (todo.images ?? []).slice(0, TODO_IMAGE_LIMIT).filter((image) => storedTodoImagePath(todo.id, image)), [todo.id, todo.images]);
  const book = useRef<TodoImageBook | null>(null);
  const [, setRevision] = useState(0);
  const redraw = useCallback(() => setRevision((n) => n + 1), []);
  const drop = useCallback(() => {
    const b = book.current;
    if (!b) return;
    b.ac.abort();
    b.entries.forEach((entry) => { if (entry.url) URL.revokeObjectURL(entry.url); });
    book.current = null;
  }, []);
  const pump = useCallback((b: TodoImageBook) => {
    while (b.running < TODO_IMAGE_PARALLEL && b.queue.length) {
      const entry = b.queue.shift()!;
      if (b.entries.get(entry.id) !== entry || entry.state !== "queued") continue;
      entry.state = "reading";
      b.running++;
      apiBlob(entry.path, {
        signal: b.ac.signal,
        token: b.token,
        tokenOwner: b.owner,
        isAuthContextCurrent: () => book.current === b && credentialOwner() === b.owner,
        headers: { [MANAGER_HEADER]: b.manager },
      }).then((blob) => {
        if (book.current !== b || b.ac.signal.aborted || b.entries.get(entry.id) !== entry || entry.state !== "reading" || credentialOwner() !== b.owner) return;
        entry.url = URL.createObjectURL(blob);
        entry.state = "ready";
        redraw();
      }).catch(() => {
        if (book.current !== b || b.ac.signal.aborted || b.entries.get(entry.id) !== entry || entry.state !== "reading" || credentialOwner() !== b.owner) return;
        entry.state = "failed";
        redraw();
      }).finally(() => {
        b.running--;
        if (book.current === b) pump(b);
      });
    }
  }, [redraw]);

  useEffect(() => {
    let b = book.current;
    let changed = false;
    if (!b || b.scope !== scope) {
      drop();
      b = { scope, owner, manager, token: getToken(), ac: new AbortController(), entries: new Map(), queue: [], running: 0 };
      book.current = b;
      changed = true;
    }
    const wanted = new Map(images.map((image) => [image.id, storedTodoImagePath(todo.id, image)!] as const));
    b.entries.forEach((entry, id) => {
      if (wanted.get(id) === entry.path) return;
      if (entry.url) URL.revokeObjectURL(entry.url);
      b.entries.delete(id);
      changed = true;
    });
    b.queue = b.queue.filter((entry) => b.entries.get(entry.id) === entry);
    for (const [id, path] of wanted) {
      if (b.entries.has(id)) continue;
      const entry: TodoImageEntry = { id, path, state: "queued" };
      b.entries.set(id, entry);
      b.queue.push(entry);
      changed = true;
    }
    pump(b);
    if (changed) redraw();
  }, [scope, owner, manager, todo.id, images, drop, pump, redraw]);
  useEffect(() => () => drop(), [drop]);

  const retry = useCallback((id: string) => {
    const b = book.current;
    const entry = b?.scope === scope ? b.entries.get(id) : undefined;
    if (!b || !entry || entry.state !== "failed") return;
    entry.state = "queued";
    b.queue.push(entry);
    redraw();
    pump(b);
  }, [scope, pump, redraw]);
  return { images, entries: book.current?.scope === scope ? book.current.entries : undefined, retry };
}

function TodoImages({ todo, owner }: { todo: Todo; owner: string }) {
  const { images, entries, retry } = useStoredTodoImages(todo, owner);
  const [open, setOpen] = useState<string>();
  const shown = images.map((image, index) => {
    const entry = entries?.get(image.id);
    return { image, entry, name: image.name?.trim() || t("todos.image_label", { n: index + 1 }) };
  });
  const ready = shown.flatMap(({ image, entry, name }) => entry?.url ? [{ id: image.id, name, url: entry.url, mime: image.mime }] : []);
  const failed = shown.filter(({ entry }) => entry?.state === "failed");
  const openAt = ready.findIndex((image) => image.id === open);
  if (!images.length) return null;
  return (
    <section className="mt-4" aria-label={t("todos.images_section", { count: images.length })}>
      <div className="mb-2 text-submeta font-medium text-ink-3">{t("todos.images_section", { count: images.length })}</div>
      <AttachmentTray attachments={ready} onOpen={setOpen} openLabel={(name) => t("todos.image_open", { name })} />
      {ready.length === 0 && failed.length === 0 && <p className="text-submeta text-ink-3">{t("todos.images_loading")}</p>}
      {failed.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-2">
          {failed.map(({ image, name }) => (
            <Button key={image.id} tone="quiet" icon="retry" className="touch-hit whitespace-nowrap" onClick={() => retry(image.id)}>
              {t("todos.image_retry", { name })}
            </Button>
          ))}
        </div>
      )}
      {openAt >= 0 && (
        <Suspense fallback={null}>
          <TodoImageViewer
            images={ready.map((image) => ({ id: image.id, src: image.url, mime: image.mime, label: image.name, name: image.name }))}
            start={openAt}
            onClose={() => setOpen(undefined)}
          />
        </Suspense>
      )}
    </section>
  );
}

function activeBlockerKindLabel(kind: string): string {
  switch (kind) {
    case "external_pr":
      return t("todos.active_blocker_kind_external_pr");
    case "todo_done":
      return t("todos.active_blocker_kind_todo_done");
    case "todo_stage":
      return t("todos.active_blocker_kind_todo_stage");
    case "merge_train":
      return t("todos.active_blocker_kind_merge_train");
    case "write_conflict":
      return t("todos.active_blocker_kind_write_conflict");
    case "owner_decision":
      return t("todos.active_blocker_kind_owner_decision");
    default:
      return kind.trim() || t("todos.active_blocker_kind_other");
  }
}

export function activeBlockerLink(raw: string): { url: string; label: string } | null {
  const safe = safeWebURL(raw, true);
  if (!safe) return null;
  const u = new URL(safe);
  const ghPr = /^\/([^/]+)\/([^/]+)\/pull\/(\d+)\/?$/.exec(u.pathname);
  if (ghPr) return { url: u.href, label: `${ghPr[1]}/${ghPr[2]}#${ghPr[3]}` };
  const ghIssue = /^\/([^/]+)\/([^/]+)\/issues\/(\d+)\/?$/.exec(u.pathname);
  if (ghIssue) return { url: u.href, label: `${ghIssue[1]}/${ghIssue[2]}#${ghIssue[3]}` };
  const glMr = /^\/(.+?)\/-\/merge_requests\/(\d+)\/?$/.exec(u.pathname);
  if (glMr) return { url: u.href, label: `${glMr[1]}!${glMr[2]}` };
  const path = u.pathname.replace(/\/$/, "");
  return { url: u.href, label: path ? `${u.host}${path}` : u.host };
}

export function validateExternalPRUrl(raw: string): string | null {
  return safeWebURL(raw, true) ?? null;
}

/** Compact inline form to add an external PR blocker edge under todos_v2 (#16199). */
function TodoAddPRBlockerForm({
  value,
  error,
  busy,
  onChange,
  onSubmit,
  onCancel,
}: {
  value: string;
  error: string | null;
  busy: boolean;
  onChange: (next: string) => void;
  onSubmit: () => void;
  onCancel: () => void;
}) {
  return (
    <form
      noValidate
      data-testid="todo-add-pr-blocker-form"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
      className="mt-2 rounded-control border border-line bg-inset/70 p-2.5 text-meta"
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <input
          type="url"
          value={value}
          disabled={busy}
          autoFocus
          data-testid="todo-add-pr-blocker-input"
          aria-label={t("todos.active_blocker_pr_input_aria")}
          placeholder={t("todos.active_blocker_pr_placeholder")}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape" && !busy && !e.nativeEvent.isComposing && e.nativeEvent.keyCode !== 229) {
              e.preventDefault();
              onCancel();
            }
          }}
          className="h-7 min-w-0 flex-1 rounded-control border border-line bg-surface px-2 text-meta text-ink outline-none placeholder:text-ink-3 focus:border-accent disabled:opacity-50"
        />
        <Button
          type="submit"
          tone="primary"
          size="sm"
          busy={busy}
          disabled={busy}
          testId="todo-add-pr-blocker-submit"
          className="h-7 shrink-0 px-2.5 text-label whitespace-nowrap"
        >
          {t("todos.active_blocker_pr_submit")}
        </Button>
        <Button
          type="button"
          tone="quiet"
          size="sm"
          disabled={busy}
          testId="todo-add-pr-blocker-cancel"
          onClick={onCancel}
          className="h-7 shrink-0 px-2 text-label whitespace-nowrap"
        >
          {t("todos.active_blocker_pr_cancel")}
        </Button>
      </div>
      {error && (
        <p data-testid="todo-add-pr-blocker-error" role="alert" className="mt-1.5 text-submeta text-red">
          {error}
        </p>
      )}
    </form>
  );
}

const RAW_TODO_ID_RE = /^todo_[a-zA-Z0-9_-]+$/;

function parseInternalTodoBlockerId(
  blocker: TodoActiveBlocker,
  allTodosById?: ReadonlyMap<string, Todo>,
): string | null {
  const ref = blocker.target_ref.trim();
  if (!ref) return null;
  if (blocker.kind === "todo_stage" && ref.includes(":")) {
    const head = ref.split(":")[0]!.trim();
    if (head && (allTodosById?.has(head) || RAW_TODO_ID_RE.test(head))) return head;
  }
  if (
    blocker.kind === "todo_done" ||
    blocker.kind === "todo_stage" ||
    allTodosById?.has(ref) ||
    RAW_TODO_ID_RE.test(ref)
  ) {
    return ref;
  }
  return null;
}

function resolveInternalTodoBlocker(
  blocker: TodoActiveBlocker,
  allTodosById?: ReadonlyMap<string, Todo>,
  fallbackTodoTitle?: string,
): { todoId: string; title: string; navigable: boolean } | null {
  const todoId = parseInternalTodoBlockerId(blocker, allTodosById);
  if (!todoId) return null;
  const localTodo = allTodosById?.get(todoId);
  const title =
    blocker.target_title?.trim() ||
    localTodo?.title?.trim() ||
    fallbackTodoTitle?.trim() ||
    t("todos.active_blocker_linked_todo");
  const navigable = Boolean(localTodo) || blocker.kind === "todo_done" || blocker.kind === "todo_stage";
  return { todoId, title, navigable };
}

function matchesWaitingOnTarget(waitingOn: string, candidate: string): boolean {
  const w = waitingOn.trim();
  const c = candidate.trim();
  if (!w || !c) return false;
  return w === c || w.startsWith(`${c} (`) || (w.startsWith(c) && w.charCodeAt(c.length) === 0xff08);
}

function waitDuplicatesActiveBlockers(
  wait: NonNullable<Todo["wait"]>,
  blockers: readonly TodoActiveBlocker[],
  allTodosById?: ReadonlyMap<string, Todo>,
): boolean {
  if (
    wait.contact ||
    wait.reason?.trim() ||
    wait.wake_condition?.trim() ||
    wait.wake_at ||
    wait.until ||
    wait.step?.trim() ||
    (wait.pr_blockers && wait.pr_blockers.length > 0)
  ) {
    return false;
  }
  if (wait.kind === "todo_handoff" && wait.handoff_todo_id) {
    return blockers.some((b) => parseInternalTodoBlockerId(b, allTodosById) === wait.handoff_todo_id);
  }
  const waitUrl = wait.url ? normalizedPR(wait.url) ?? safeWebURL(wait.url, true) : undefined;
  const waitingOn = wait.waiting_on?.trim() ?? "";
  if (wait.kind === "pr_merge") {
    return blockers.some((b) => {
      const bUrl = normalizedPR(b.target_ref) ?? safeWebURL(b.target_ref, true);
      if (waitUrl && bUrl && waitUrl === bUrl) return true;
      const link = activeBlockerLink(b.target_ref);
      return Boolean(link && waitingOn && matchesWaitingOnTarget(waitingOn, link.label));
    });
  }
  if (wait.kind === "external_event") {
    return blockers.some((b) => {
      const bUrl = normalizedPR(b.target_ref) ?? safeWebURL(b.target_ref, true);
      if (waitUrl && bUrl && waitUrl === bUrl) return true;
      if (waitingOn && matchesWaitingOnTarget(waitingOn, b.target_ref)) return true;
      const link = activeBlockerLink(b.target_ref);
      if (link && waitingOn && matchesWaitingOnTarget(waitingOn, link.label)) return true;
      const todoId = parseInternalTodoBlockerId(b, allTodosById);
      if (!todoId) return false;
      const knownTitle = b.target_title?.trim() || allTodosById?.get(todoId)?.title?.trim();
      if (knownTitle && matchesWaitingOnTarget(waitingOn, knownTitle)) return true;
      return Boolean(!knownTitle && !waitUrl && waitingOn);
    });
  }
  return false;
}

/** Compact strip of active persistent blocker edges projected by the server under todos_v2 (#16135, #16138, #16199). */
function TodoActiveBlockersStrip({
  blockers,
  allTodosById,
  onOpenTodo,
  fallbackTodoTitle,
  canAddPR = false,
  addingPROpen = false,
  onToggleAddPR,
  cancellingId = null,
  onCancelBlocker,
  embedded = false,
}: {
  blockers: readonly TodoActiveBlocker[];
  allTodosById?: ReadonlyMap<string, Todo>;
  onOpenTodo?: (id: string) => void;
  fallbackTodoTitle?: string;
  canAddPR?: boolean;
  addingPROpen?: boolean;
  onToggleAddPR?: () => void;
  cancellingId?: string | null;
  onCancelBlocker?: (blockerId: string) => void;
  embedded?: boolean;
}) {
  return (
    <section
      data-testid="todo-active-blockers"
      aria-label={t("todos.active_blockers_region")}
      className={
        embedded
          ? "text-meta text-ink-2"
          : "mt-3 rounded-control border border-orange/30 bg-orange-tint/40 px-3 py-2 text-meta text-ink-2"
      }
    >
      <div className="mb-1.5 flex items-center justify-between gap-2 text-label font-semibold text-orange">
        <span className="inline-flex min-w-0 items-center gap-1.5">
          <Icon name="alertCircle" size={12} stroke={2} className="shrink-0" />
          <span className="truncate">{t("todos.active_blockers_title", { count: blockers.length })}</span>
        </span>
        {canAddPR && onToggleAddPR && (
          <Button
            tone="quiet"
            size="sm"
            icon="plus"
            expanded={addingPROpen}
            testId="todo-add-pr-blocker-btn"
            onClick={onToggleAddPR}
            className="h-6 shrink-0 border border-line bg-surface px-2 text-label whitespace-nowrap text-ink-2 hover:bg-hover"
          >
            {t("todos.active_blocker_add_pr")}
          </Button>
        )}
      </div>
      <ul className="flex flex-col gap-1">
        {blockers.map((blocker) => {
          const link = activeBlockerLink(blocker.target_ref);
          const internalTodo = link ? null : resolveInternalTodoBlocker(blocker, allTodosById, fallbackTodoTitle);
          const displayTarget = link?.label ?? internalTodo?.title ?? blocker.target_ref;
          const attempts = typeof blocker.attempts === "number" && blocker.attempts > 0 ? blocker.attempts : 0;
          const cancelling = cancellingId === blocker.id;
          return (
            <li
              key={blocker.id}
              data-testid="todo-active-blocker-item"
              data-blocker-kind={blocker.kind}
              className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs"
            >
              <span
                data-testid="todo-active-blocker-kind"
                className="shrink-0 rounded-chip bg-surface px-1.5 py-0.5 text-submeta font-medium text-ink-2 shadow-hairline"
              >
                {activeBlockerKindLabel(blocker.kind)}
              </span>
              {link ? (
                <a
                  href={link.url}
                  target="_blank"
                  rel="noreferrer noopener"
                  data-testid="todo-active-blocker-link"
                  className="inline-flex min-w-0 items-center gap-1 truncate font-medium text-accent-ink hover:underline"
                >
                  <span className="truncate">{link.label}</span>
                  <Icon name="external" size={11} stroke={2} className="shrink-0" />
                </a>
              ) : internalTodo && internalTodo.navigable && onOpenTodo ? (
                <button
                  type="button"
                  data-testid="todo-active-blocker-todo-link"
                  data-blocker-todo-id={internalTodo.todoId}
                  onClick={() => onOpenTodo(internalTodo.todoId)}
                  className="touch-hit inline-flex min-w-0 items-center gap-1 truncate font-medium text-accent-ink hover:underline focus-visible:outline-2 focus-visible:outline-accent"
                >
                  <span className="truncate">{internalTodo.title}</span>
                  <Icon name="chevronRight" size={11} stroke={2.2} className="shrink-0" />
                </button>
              ) : (
                <span data-testid="todo-active-blocker-target" className="min-w-0 truncate text-ink">
                  {displayTarget}
                </span>
              )}
              {attempts > 0 && (
                <span
                  data-testid="todo-active-blocker-attempts"
                  className="shrink-0 rounded-chip bg-surface px-1.5 py-0.5 text-submeta text-ink-3 tabular-nums shadow-hairline"
                >
                  {t("todos.active_blocker_attempts", { count: attempts })}
                </span>
              )}
              {onCancelBlocker && (
                <button
                  type="button"
                  data-testid={`todo-cancel-blocker-${blocker.id}`}
                  disabled={cancelling}
                  aria-label={t("todos.active_blocker_cancel_aria", { target: displayTarget })}
                  title={t("todos.active_blocker_cancel")}
                  onClick={() => onCancelBlocker(blocker.id)}
                  className="touch-hit ml-auto inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-chip text-ink-3 transition-colors hover:bg-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-accent disabled:opacity-50"
                >
                  {cancelling ? <Spinner size={11} /> : <Icon name="x" size={12} stroke={2} />}
                </button>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

/**
 * Unified progressive focus banner under todos_v2 (#17696): merges active wait state, active blockers,
 * target repo gate, and merge-train status into one grounded card instead of stacking up to four banners.
 */
function TodoDetailFocusBanner({
  todo,
  allTodosById,
  onOpenTodo,
  mergeTrainBadge,
  canAddPR,
  addingPROpen,
  onToggleAddPR,
  cancellingBlockerId,
  onCancelBlocker,
}: {
  todo: Todo;
  allTodosById?: ReadonlyMap<string, Todo>;
  onOpenTodo?: (id: string) => void;
  mergeTrainBadge?: MergeTrainTodoBadge;
  canAddPR: boolean;
  addingPROpen: boolean;
  onToggleAddPR: () => void;
  cancellingBlockerId: string | null;
  onCancelBlocker: (blockerId: string) => void;
}) {
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    setExpanded(false);
  }, [todo.id]);

  const blockers = todo.active_blockers && todo.active_blockers.length > 0 ? todo.active_blockers : undefined;
  const repoGate = todo.repo_gate;
  const hasWait = todo.wait != null;
  const waitMergedIntoBlockers = Boolean(
    hasWait && todo.wait && blockers && waitDuplicatesActiveBlockers(todo.wait, blockers, allTodosById),
  );
  const internalBlockers = blockers?.filter((b) => parseInternalTodoBlockerId(b, allTodosById) !== null) ?? [];
  const fallbackTodoTitle =
    internalBlockers.length === 1 &&
    todo.wait?.kind === "external_event" &&
    !todo.wait.url &&
    todo.wait.waiting_on?.trim() &&
    !RAW_TODO_ID_RE.test(todo.wait.waiting_on.trim()) &&
    !/\s[\uFF08(]\+\d+/.test(todo.wait.waiting_on)
      ? todo.wait.waiting_on.trim()
      : undefined;

  type FocusEntry = {
    key: "repo_gate" | "mergetrain" | "blockers" | "wait";
    priority: number;
    chip: string;
    render: () => ReactNode;
  };

  const entries: FocusEntry[] = [];

  if (repoGate) {
    const tone = repoGateTone(repoGate.state);
    const isBlocked = tone === "red";
    const toneText: Record<RepoGateTone, string> = {
      green: "text-green",
      red: "text-red",
      orange: "text-orange",
      muted: "text-ink-3",
    };
    const headline =
      repoGate.state === "reference_only"
        ? t("todos.repo_gate_detail_reference_only", { count: repoGate.reference_count })
        : repoGate.state === "ready"
          ? t("todos.repo_gate_detail_ready", { ready: repoGate.ready_count, total: repoGate.target_count })
          : repoGate.blocking_repo?.trim()
            ? t("todos.repo_gate_detail_blocked", {
                ready: repoGate.ready_count,
                total: repoGate.target_count,
                repo: repoGate.blocking_repo.trim(),
              })
            : t("todos.repo_gate_detail_pending", { ready: repoGate.ready_count, total: repoGate.target_count });
    const reasonText = repoGateReasonLabel(repoGate);
    const blockingPR = safeWebURL(repoGate.blocking_pr);
    entries.push({
      key: "repo_gate",
      priority: isBlocked ? 0 : 5,
      chip: formatRowRepoGate(repoGate),
      render: () => (
        <div
          data-testid="todo-detail-repo-gate"
          data-gate-state={repoGate.state}
          className="flex flex-wrap items-center justify-between gap-2 text-submeta"
        >
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
            <span className={cx("inline-flex items-center gap-1.5 font-medium", toneText[tone])}>
              <Icon name={tone === "green" ? "check" : "alertCircle"} size={13} className="shrink-0" />
              <span className="tabular-nums">{headline}</span>
            </span>
            {repoGate.state !== "reference_only" && repoGate.state !== "ready" && reasonText && (
              <span data-testid="todo-detail-repo-gate-reason" className="text-ink-2">
                · {reasonText}
              </span>
            )}
            {repoGate.state !== "reference_only" && repoGate.reference_count > 0 && (
              <span data-testid="todo-detail-repo-gate-refs" className="text-ink-3 tabular-nums">
                · {t("todos.repo_gate_detail_refs_note", { count: repoGate.reference_count })}
              </span>
            )}
          </div>
          {blockingPR && (
            <a
              href={blockingPR}
              target="_blank"
              rel="noreferrer noopener"
              data-testid="todo-detail-repo-gate-pr"
              className="touch-hit inline-flex shrink-0 items-center gap-1 font-medium whitespace-nowrap text-accent-ink hover:underline"
            >
              <span>{repoGatePRLabel(blockingPR)}</span>
              <Icon name="external" size={11} />
            </a>
          )}
        </div>
      ),
    });
  }

  if (mergeTrainBadge) {
    const aheadUrl = safeHttpUrl(mergeTrainBadge.ahead_pr_url);
    const badgeText = mergeTrainBadgeText(mergeTrainBadge);
    entries.push({
      key: "mergetrain",
      priority: mergeTrainBadge.state === "escalated" ? 1 : 4,
      chip: `${t("todos.mergetrain_detail_title")} · ${badgeText}`,
      render: () => (
        <div
          data-testid="todo-detail-mergetrain"
          data-mergetrain-state={mergeTrainBadge.state}
          data-mergetrain-position={mergeTrainBadge.position}
          className="flex flex-wrap items-center justify-between gap-2 text-submeta text-ink-2"
        >
          <span className="inline-flex min-w-0 flex-wrap items-center gap-1.5">
            <span className="font-medium text-ink">{t("todos.mergetrain_detail_title")}</span>
            <span
              className={cx(
                "rounded-chip px-1.5 py-0.5 text-xs font-medium whitespace-nowrap tabular-nums",
                mergeTrainBadge.state === "escalated" ? "bg-orange-tint text-attention" : "bg-surface text-ink shadow-hairline",
              )}
            >
              {badgeText}
            </span>
            {mergeTrainBadge.repo_key && (
              <span className="truncate text-ink-3">{mergeTrainBadge.repo_key}</span>
            )}
          </span>
          {aheadUrl && (
            <a
              href={aheadUrl}
              target="_blank"
              rel="noreferrer noopener"
              data-testid="todo-detail-mergetrain-ahead"
              className="inline-flex shrink-0 items-center gap-1 font-medium whitespace-nowrap text-accent-ink hover:underline"
            >
              <span>{t("todos.mergetrain_ahead_pr")}</span>
              <Icon name="external" size={11} stroke={2} className="shrink-0" />
            </a>
          )}
        </div>
      ),
    });
  }

  if (blockers) {
    entries.push({
      key: "blockers",
      priority: 2,
      chip: t("todos.active_blockers_title", { count: blockers.length }),
      render: () => (
        <TodoActiveBlockersStrip
          embedded
          blockers={blockers}
          allTodosById={allTodosById}
          onOpenTodo={onOpenTodo}
          fallbackTodoTitle={fallbackTodoTitle}
          canAddPR={canAddPR}
          addingPROpen={addingPROpen}
          onToggleAddPR={onToggleAddPR}
          cancellingId={cancellingBlockerId}
          onCancelBlocker={onCancelBlocker}
        />
      ),
    });
  }

  if (hasWait && todo.wait && !waitMergedIntoBlockers) {
    entries.push({
      key: "wait",
      priority: 3,
      chip: waitSummary(todo.wait),
      render: () => (
        <div className="select-text">
          <TaskWaitInfo todo={todo} expanded />
        </div>
      ),
    });
  }

  if (entries.length === 0) return null;

  entries.sort((a, b) => a.priority - b.priority);
  const [primary, ...secondary] = entries;
  const blockersHidden = secondary.some((e) => e.key === "blockers") && !expanded;

  return (
    <section
      data-testid="todo-detail-focus-banner"
      data-focus-count={entries.length}
      aria-label={t("todos.detail_focus_region")}
      className="mt-3 rounded-control border border-line bg-inset px-3 py-2.5 text-meta text-ink-2"
    >
      <div>{primary.render()}</div>
      {secondary.length > 0 && (
        <>
          <div className="mt-2 flex flex-wrap items-center gap-1.5 border-t border-line/70 pt-2">
            {secondary.map((item) => (
              <span
                key={item.key}
                data-testid={`todo-detail-focus-chip-${item.key}`}
                title={item.chip}
                className="min-w-0 max-w-md truncate rounded-chip bg-surface px-1.5 py-0.5 text-submeta text-ink-2 shadow-hairline"
              >
                {item.chip}
              </span>
            ))}
            <div className="ml-auto flex shrink-0 items-center gap-1.5">
              {canAddPR && blockersHidden && (
                <Button
                  tone="quiet"
                  size="sm"
                  icon="plus"
                  expanded={addingPROpen}
                  testId="todo-add-pr-blocker-btn"
                  onClick={onToggleAddPR}
                  className="h-6 shrink-0 border border-line bg-surface px-2 text-label whitespace-nowrap text-ink-2 hover:bg-hover"
                >
                  {t("todos.active_blocker_add_pr")}
                </Button>
              )}
              <button
                type="button"
                data-testid="todo-detail-focus-more"
                aria-expanded={expanded}
                onClick={() => setExpanded((v) => !v)}
                className="touch-hit inline-flex h-6 shrink-0 items-center gap-1 rounded-chip px-1.5 text-submeta font-medium whitespace-nowrap text-accent-ink hover:bg-hover focus-visible:outline-2 focus-visible:outline-accent"
              >
                <span>
                  {expanded
                    ? t("todos.detail_focus_less")
                    : t("todos.detail_focus_more", { count: secondary.length })}
                </span>
                <Icon
                  name="chevron"
                  size={10}
                  stroke={2.2}
                  className={cx("shrink-0 transition-transform", expanded ? "" : "-rotate-90")}
                />
              </button>
            </div>
          </div>
          {expanded && (
            <div data-testid="todo-detail-focus-extra" className="mt-2 flex flex-col gap-2 border-t border-line/70 pt-2">
              {secondary.map((item) => (
                <div key={item.key}>{item.render()}</div>
              ))}
            </div>
          )}
        </>
      )}
    </section>
  );
}

/** Uniform compact disclosure row for secondary audit/traceback sections under todos_v2 (#17696). */
function DetailAuditDisclosure({
  testId,
  toggleTestId,
  title,
  summary,
  count,
  expanded,
  onToggle,
  right,
  children,
}: {
  testId: string;
  toggleTestId: string;
  title: string;
  summary?: string;
  count?: number;
  expanded: boolean;
  onToggle: () => void;
  right?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section
      data-testid={testId}
      aria-label={title}
      className="rounded-control border border-line bg-canvas"
    >
      <div className="flex items-center justify-between gap-2 px-3 py-2">
        <button
          type="button"
          data-testid={toggleTestId}
          aria-expanded={expanded}
          onClick={onToggle}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left text-label font-medium text-ink-2 transition-colors hover:text-ink focus-visible:outline-2 focus-visible:outline-accent"
        >
          <Icon
            name="chevron"
            size={11}
            stroke={2.2}
            className={cx("shrink-0 text-ink-3 transition-transform", expanded ? "" : "-rotate-90")}
          />
          <span className="shrink-0 whitespace-nowrap">{title}</span>
          {count !== undefined && (
            <span className="shrink-0 rounded-chip bg-field px-1.5 py-0.5 text-submeta text-ink-3 tabular-nums">
              {count}
            </span>
          )}
          {summary && (
            <span className="min-w-0 truncate text-submeta font-normal text-ink-3">
              · {summary}
            </span>
          )}
        </button>
        {right && <div className="flex shrink-0 items-center gap-1">{right}</div>}
      </div>
      {expanded && <div className="border-t border-line px-3 py-2.5">{children}</div>}
    </section>
  );
}

function TodoDetail({
  todo,
  owner,
  activeAgents,
  releasedAgents,
  lookup,
  machines,
  interactions,
  showReleased,
  onToggleReleased,
  open,
  onToggleDone,
  onOpenHandover,
  onOpenTodo,
  onDeleteClick,
  todosV2,
  allTodos = [],
  parentTodo,
  childTodos = [],
  effectiveDirection,
  resolveChildDirection,
  directions = [],
  onReorganizeDirection,
  onUnseen,
  mergeTrainBadge,
}: {
  todo: Todo;
  owner: string;
  activeAgents: TodoAgent[];
  releasedAgents: TodoAgent[];
  lookup: ReturnType<typeof makeSessionLookup>;
  machines: Machine[];
  interactions: Interaction[];
  showReleased: boolean;
  onToggleReleased: () => void;
  /** Whether it reads open, as its row's checkbox does (checksAsOpen): a failed start waiting on the owner is (#3713). */
  open: boolean;
  /** Closes it when `open`, reopens it otherwise: the row's checkbox. */
  onToggleDone: (open: boolean) => void;
  onOpenHandover: () => void;
  onOpenTodo: (id: string) => void;
  onDeleteClick: () => void;
  todosV2?: boolean;
  allTodos?: readonly Todo[];
  parentTodo?: Todo;
  childTodos?: readonly Todo[];
  effectiveDirection?: EffectiveDirection;
  resolveChildDirection?: (todo: Todo) => EffectiveDirection | undefined;
  directions?: readonly ReorganizeDirectionOption[];
  onReorganizeDirection?: (req: TodoReorganizeRequest) => Promise<void>;
  onUnseen?: (id: string) => void;
  mergeTrainBadge?: MergeTrainTodoBadge;
}) {
  const title = useTodoField({
    remote: todo.title, owner, allowEmpty: false,
    write: async (value) => (await updateTodo(todo.id, { title: value })).title,
    onError: (error) => toast(t("todos.save_title_failed", { error: errorText(error) }), "error"),
  });
  const notes = useTodoField({
    remote: todo.notes ?? "", owner, allowEmpty: true,
    write: async (value) => (await updateTodo(todo.id, { notes: value })).notes ?? "",
    onError: (error) => toast(t("todos.save_notes_failed", { error: errorText(error) }), "error"),
  });
  const menu = useMenu();
  const allSessions = useAgentSessions();
  const spacesSupported = useStore((s) => s.spacesSupported);
  const [repositoryCount, setRepositoryCount] = useState(0);
  // Under todos_v2 (#17696), participated history starts collapsed as a secondary audit disclosure row.
  const [releasedOpenV2, setReleasedOpenV2] = useState(false);
  const releasedVisible = todosV2 === true ? releasedOpenV2 : showReleased;
  // Single-item direction and parent-goal pickers strictly gated behind todos_v2 (#15653, #16100).
  const [directionPickerOpen, setDirectionPickerOpen] = useState(false);
  const [parentPickerOpen, setParentPickerOpen] = useState(false);
  const [itemTypeBusy, setItemTypeBusy] = useState(false);
  const [unseeing, setUnseeing] = useState(false);
  // Manual external PR blocker add & active blocker cancel strictly gated behind todos_v2 (#16199).
  const [prBlockerOpen, setPrBlockerOpen] = useState(false);
  const [prBlockerUrl, setPrBlockerUrl] = useState("");
  const [prBlockerError, setPrBlockerError] = useState<string | null>(null);
  const [prBlockerBusy, setPrBlockerBusy] = useState(false);
  const [cancellingBlockerId, setCancellingBlockerId] = useState<string | null>(null);

  useEffect(() => {
    setPrBlockerOpen(false);
    setPrBlockerUrl("");
    setPrBlockerError(null);
    setPrBlockerBusy(false);
    setCancellingBlockerId(null);
  }, [todo.id, todosV2]);

  const hasActiveBlockers = Boolean(todo.active_blockers && todo.active_blockers.length > 0);
  const canAddPRBlocker = todosV2 === true && open;

  const toggleAddPRBlocker = () => {
    setPrBlockerOpen((prev) => !prev);
    setPrBlockerError(null);
  };

  const submitExternalPRBlocker = async () => {
    if (prBlockerBusy) return;
    const validUrl = validateExternalPRUrl(prBlockerUrl);
    if (!validUrl) {
      setPrBlockerError(t("todos.active_blocker_pr_url_invalid"));
      return;
    }
    setPrBlockerBusy(true);
    setPrBlockerError(null);
    try {
      await addTodoExternalPRBlocker(todo.id, validUrl);
      await refreshTodos();
      setPrBlockerOpen(false);
      setPrBlockerUrl("");
      toast(t("todos.active_blocker_added"), "ok");
    } catch (e) {
      setPrBlockerError(t("todos.active_blocker_add_failed", { error: errorText(e) }));
    } finally {
      setPrBlockerBusy(false);
    }
  };

  const handleCancelBlocker = async (blockerId: string) => {
    if (cancellingBlockerId) return;
    setCancellingBlockerId(blockerId);
    try {
      await cancelTodoBlocker(todo.id, blockerId);
      await refreshTodos();
      toast(t("todos.active_blocker_cancelled"), "ok");
    } catch (e) {
      toast(t("todos.active_blocker_cancel_failed", { error: errorText(e) }), "error");
    } finally {
      setCancellingBlockerId(null);
    }
  };
  const effectiveDirectionId = effectiveDirection?.id ?? (todo.direction_id?.trim() || undefined);
  const resolvedDirectionLabel = effectiveDirection?.label ?? effectiveDirectionId;
  const directionInherited = Boolean(effectiveDirection?.inherited);
  const canChangeDirection =
    todosV2 === true &&
    Boolean(onReorganizeDirection) &&
    (directions.length > 0 || Boolean(effectiveDirectionId));
  const showDirectionPicker = canChangeDirection && directionPickerOpen;
  const hasEligibleParentTarget = useMemo(
    () =>
      todosV2 === true &&
      eligibleParentTargets(allTodos, [todo.id]).some((td) => !td.done_at && !td.part_of?.trim()),
    [todosV2, allTodos, todo.id],
  );
  const canChangeParent =
    todosV2 === true &&
    Boolean(onReorganizeDirection) &&
    (hasEligibleParentTarget || Boolean(todo.part_of));
  const showParentPicker = canChangeParent && parentPickerOpen;
  const currentItemType: "personal" | "task" = isAgentTask(todo) ? "task" : "personal";
  const canUnsee =
    todosV2 === true &&
    (Boolean(todo.seen_at) || (Boolean(todo.done_at) && (todo.audience === "autonomous" || isAutoDone(todo))));

  const switchItemType = async (nextType: "personal" | "task") => {
    if (itemTypeBusy || nextType === currentItemType) return;
    setItemTypeBusy(true);
    try {
      const updated = await updateTodo(todo.id, { item_type: nextType });
      if (updated) putTodo(updated);
      await refreshTodos();
      toast(
        t("todos.item_type_updated", {
          type: nextType === "task" ? t("todos.item_type_task") : t("todos.item_type_personal"),
        }),
        "ok",
      );
    } catch (e) {
      toast(t("todos.item_type_update_failed", { error: errorText(e) }), "error");
    } finally {
      setItemTypeBusy(false);
    }
  };

  const handleUnsee = async () => {
    if (unseeing) return;
    setUnseeing(true);
    try {
      const updated = await unseeTodo(todo.id);
      onUnseen?.(todo.id);
      if (updated) {
        putTodo({ ...updated, seen_at: updated.seen_at ?? null });
      } else {
        putTodo({ ...todo, seen_at: null });
      }
      await refreshTodos();
      toast(t("todos.detail_marked_unread"), "ok");
    } catch (e) {
      toast(t("todos.action_failed", { error: errorText(e) }), "error");
    } finally {
      setUnseeing(false);
    }
  };

  const template = useMemo(
    () => (todosV2 === true ? classifyTodoDetailTemplate(todo, childTodos.length, { repositoryCount }) : undefined),
    [todosV2, todo, childTodos.length, repositoryCount],
  );
  const isWorkGoalV2 = todosV2 === true && template === "work_goal";
  const { openChildren, doneChildren } = useMemo(() => {
    const openList: Todo[] = [];
    const doneList: Todo[] = [];
    for (const child of childTodos) {
      if (child.done_at) doneList.push(child);
      else openList.push(child);
    }
    return { openChildren: openList, doneChildren: doneList };
  }, [childTodos]);
  const doneChildCount = doneChildren.length;
  const defaultDoneSubtasksOpen = openChildren.length === 0 && doneChildren.length <= 3;
  const [doneSubtasksOverride, setDoneSubtasksOverride] = useState<{ todoId: string; open: boolean } | null>(null);
  const doneSubtasksOpen =
    doneSubtasksOverride?.todoId === todo.id ? doneSubtasksOverride.open : defaultDoneSubtasksOpen;
  const workGoalActions = useMemo(
    () => (isWorkGoalV2 ? resolveWorkGoalActions(todo, lookup) : undefined),
    [isWorkGoalV2, todo, lookup],
  );

  const hasLiveAgent = (mid: string, spaceName: string) => {
    return allSessions.some(
      (s) =>
        s.machine_id === mid &&
        (s.space_name || s.space_id) === spaceName &&
        activityOf(s) !== "offline" &&
        machines.some((m) => m.id === mid && m.online),
    );
  };

  const removeSpace = async (mid: string, space: string) => {
    try {
      await unlinkSpaceFromTodo(todo.id, mid, space);
      await refreshTodos();
      await refresh();
      toast(t("todos.space_removed"), "ok");
    } catch (e) {
      toast(t("todos.space_remove_failed", { error: errorText(e) }), "error");
    }
  };

  // "Remove" for an agent that did not start, "Unlink" for the others (iOS: the row's swipe; Android: its ×).
  const unlinkAgent = async (agent: TodoAgent) => {
    const failed = Boolean(agent.start_error);
    try {
      await unlinkAgentFromTodo(todo.id, agent.machine_id, agent.session_id);
      await refreshTodos();
      await refresh();
      toast(failed ? t("todos.failed_agent_removed") : t("todos.agent_unlinked"), "ok");
    } catch (e) {
      toast(failed ? t("todos.remove_failed", { error: errorText(e) }) : t("todos.unlink_failed", { error: errorText(e) }), "error");
    }
  };

  // A task whose agent never started, waiting on the owner (#3713, audit item 13): open, as its row reads, with why it
  // did not start; Retry for any a conversation handed out (POST …/retry: the hub finds the plan that binds it, one a
  // plan took over through todo_id has no plan id of its own, #8770, and says when there is none), and Hand to agent.
  // Its launcher could not start there: "Use … instead" on the computer's other launcher comes first (#10365). Its
  // tab's shell stopped on a question: the answers are offered here too, and the task then starts in that tab.
  const failedStart = open && Boolean(todo.start_error);
  const canRetry = failedStart && Boolean(todo.origin);
  const features = useStore((s) => s.features);
  const failedTask = useFeed((s) => (canRetry ? planTaskOf(todo, s.plans) : undefined));
  const instead = retryLauncher(failedTask, features);
  // the main chat's task card's Retry (lib/feed retryTask): one on its way at a time from either, and the to-do the hub
  // reopens shows at once; its agent starts in the background and the list says how the start goes
  const retrying = useFeed((s) => Boolean(s.busy[retryKey(todo.id)]));
  const retry = (launcher?: string) =>
    (launcher ? retryOnLauncher(todo, launcher) : retryTask(todo)).catch((e) => toast(t("todos.action_failed", { error: errorText(e) }), "error"));
  const status = todoStatus(todo, lookup);
  // its state word and tone; a waiting one's say what it waits on, attention only on the owner (#11090)
  const look = todoLook(todo, status, waitOf(todo, status, lookup), undefined, undefined, todosV2 ? repoGatePRLook : undefined);
  const statusLabel = todoSupervisorLabel(todo, status, lookup) ?? look.label;
  // its steps as the hub counts them (#11858), as its row says them
  const steps = stepsWords(todo, status);
  const responsible = responsibleOf(todo);
  // The to-do's own PR, parsed once here (the list row and the repository section's fallback share it).
  const legacyPR = prLink(todo.pr_url);
  const toneClass = {
    red: "bg-red-tint text-red",
    green: "bg-green-tint text-green",
    amber: status === "needs_you" ? "bg-field text-attention" : "bg-orange-tint text-orange",
    idle: "bg-hover text-ink-2",
    off: "bg-hover text-ink-3",
    attention: "bg-field text-attention",
  }[look.tone];

  const shown = shownStatus(todo, status);
  const dotState =
    shown === "needs_you" || shown === "interrupted"
      ? "needs_you"
      : shown === "usage_limit"
        ? "idle"
      : shown === "unread"
        ? "unread"
        : shown === "working" || shown === "waiting"
          ? "working"
          : shown === "follow_up"
            ? "idle"
            : "offline";

  const allTodosById = useMemo(
    () => new Map(allTodos.map((td) => [td.id, td] as const)),
    [allTodos],
  );
  const hasLinkedAgents = activeAgents.length > 0 || releasedAgents.length > 0;
  const completeLabel = todo.kind === "concern" ? t("todos.report_concern_action") : t("todos.complete");
  const hasChildSummary = Boolean(todo.child_summary && todo.child_summary.total > 0);
  const showOverviewPrimaryAgent =
    isWorkGoalV2 && open && workGoalActions?.primaryAction === "open_agent" && Boolean(workGoalActions.activeAgent);
  const showOwnershipStrip =
    todosV2 === true &&
    (Boolean(effectiveDirectionId) ||
      Boolean(todo.part_of) ||
      childTodos.length > 0 ||
      hasChildSummary ||
      canChangeParent ||
      canChangeDirection ||
      showOverviewPrimaryAgent);
  const overviewItems: ReactNode[] = [];
  if (showOwnershipStrip) {
    const dirLabel = effectiveDirectionId ? (resolvedDirectionLabel || effectiveDirectionId) : undefined;
    const parentNode = todo.part_of ? (
      parentTodo ? (
        <button
          key="parent"
          type="button"
          data-testid="todo-detail-parent-link"
          title={t("todos.detail_parent_label", { title: parentTodo.title })}
          onClick={() => onOpenTodo(parentTodo.id)}
          className="touch-hit inline-flex min-w-0 items-center gap-1 font-medium text-accent-ink hover:underline focus-visible:outline-2 focus-visible:outline-accent"
        >
          <span className="truncate">
            {dirLabel ? parentTodo.title : t("todos.detail_parent_label", { title: parentTodo.title })}
          </span>
          <Icon name="chevronRight" size={11} stroke={2.2} className="shrink-0" />
        </button>
      ) : (
        <span key="parent" data-testid="todo-detail-parent-link" className="truncate text-ink-3">
          {t("todos.detail_parent_label", {
            title: RAW_TODO_ID_RE.test(todo.part_of) ? t("todos.active_blocker_linked_todo") : todo.part_of,
          })}
        </span>
      )
    ) : null;
    if (dirLabel && parentNode) {
      overviewItems.push(
        <nav
          key="hierarchy-breadcrumb"
          aria-label={t("todos.detail_ownership_region")}
          className="inline-flex min-w-0 flex-wrap items-center gap-1.5"
        >
          <span
            data-testid="todo-detail-direction"
            data-direction-inherited={directionInherited ? "true" : "false"}
            title={directionInherited ? t("todos.detail_direction_inherited_hint", { name: dirLabel }) : dirLabel}
            className="inline-flex min-w-0 items-center gap-1 text-ink-2"
          >
            <Icon name="folder" size={12} stroke={2} className="shrink-0 text-ink-3" />
            <span className="truncate">{dirLabel}</span>
          </span>
          <Icon name="chevronRight" size={11} stroke={2} className="shrink-0 text-ink-3" />
          {parentNode}
        </nav>,
      );
    } else if (dirLabel) {
      overviewItems.push(
        <span
          key="direction"
          data-testid="todo-detail-direction"
          data-direction-inherited={directionInherited ? "true" : "false"}
          title={directionInherited ? t("todos.detail_direction_inherited_hint", { name: dirLabel }) : dirLabel}
          className="inline-flex min-w-0 items-center gap-1 text-ink-2"
        >
          <Icon name="folder" size={12} stroke={2} className="shrink-0 text-ink-3" />
          <span className="truncate">{dirLabel}</span>
        </span>,
      );
    } else if (parentNode) {
      overviewItems.push(parentNode);
    }
    if (todo.child_summary && todo.child_summary.total > 0) {
      overviewItems.push(
        <span key="child-summary" data-testid="todo-detail-child-summary" className="inline-flex shrink-0 items-center gap-1.5 tabular-nums">
          <span>{t("todos.detail_subtasks_section", { done: todo.child_summary.done, total: todo.child_summary.total })}</span>
          {todo.child_summary.working > 0 && (
            <span className="text-ink-3">{t("todos.child_summary_working", { count: todo.child_summary.working })}</span>
          )}
          {todo.child_summary.needs_you > 0 && (
            <span className="font-medium text-attention">{t("todos.goal_blocked", { count: todo.child_summary.needs_you })}</span>
          )}
        </span>,
      );
    } else if (childTodos.length > 0) {
      overviewItems.push(
        <span key="subtasks" data-testid="todo-detail-summary-subtasks" className="shrink-0 whitespace-nowrap tabular-nums">
          {t("todos.detail_subtasks_section", { done: doneChildCount, total: childTodos.length })}
        </span>,
      );
    }
  }
  const v2MetaItems: ReactNode[] = [];
  if (todosV2 === true) {
    v2MetaItems.push(
      <span key="status" className={cx("inline-flex h-5.5 items-center gap-1.5 rounded-md px-2 text-label font-semibold", toneClass)}>
        <StatusMark state={dotState} size="sm" />
        <span data-testid="todo-detail-status">{steps?.label ?? statusLabel}</span>
      </span>,
    );
    if (activeAgents.length > 0) {
      v2MetaItems.push(
        <span key="agents">{t("todos.agent_count", { count: activeAgents.length })}</span>,
      );
    }
    if (steps?.tail) {
      v2MetaItems.push(<span key="steps-tail" data-testid="todo-detail-steps-next">{steps.tail}</span>);
    }
    if (responsible) {
      v2MetaItems.push(
        <span
          key="responsible"
          data-testid="todo-detail-responsible"
          data-kind={responsible.kind}
          title={t("todos.responsible_label", { name: responsible.name })}
          className="inline-flex min-w-0 items-center gap-1 text-ink-2"
        >
          <Icon name={responsible.kind === "person" ? "person" : "terminal"} size={12} stroke={2} className="shrink-0 text-ink-3" />
          <span className="truncate">{responsible.name}</span>
        </span>,
      );
    }
    if (steps?.after) {
      v2MetaItems.push(<span key="steps-after" data-testid="todo-detail-steps">{steps.after}</span>);
    }
    v2MetaItems.push(<span key="created">{t("todos.created_at", { time: clock(todo.created_at) })}</span>);
  }
  const notesBlock = (
    <div className={todosV2 === true ? "mt-3" : "mt-4"} data-testid={todosV2 === true ? "todo-detail-notes" : undefined}>
      <div className="relative rounded-control bg-inset p-3.5 shadow-hairline">
        <textarea
          rows={3}
          value={notes.value}
          aria-busy={notes.saving}
          onChange={(e) => notes.edit(e.target.value)}
          onBlur={notes.save}
          placeholder={t("todos.notes_placeholder")}
          className="w-full resize-none bg-transparent text-[13.5px] leading-[1.65] text-ink outline-none placeholder:text-ink-3"
        />
      </div>
    </div>
  );

  return (
    // a page's reading width (DESIGN_SYSTEM 2.5 "Widths", as the inbox's detail): a wide window centers it (audit item 75)
    <div
      className={cx(
        "mx-auto flex min-h-full w-full flex-col",
        todosV2 === true ? "max-w-[880px] px-5 py-5 sm:px-7 sm:py-6" : "max-w-[760px] p-6 sm:p-8",
      )}
      data-testid="todo-detail"
    >
      {/* Single consolidated top overview strip under todos_v2 (#17696) */}
      {showOwnershipStrip && (
        <>
          <div
            data-testid="todo-detail-ownership"
            aria-label={t("todos.detail_ownership_region")}
            className="mb-3 flex flex-wrap items-center gap-x-2 gap-y-1.5 rounded-control border border-line bg-inset px-3 py-2 text-label text-ink-2"
          >
            {overviewItems.flatMap((node, index) =>
              index === 0 ? [node] : [<span key={`sep-${index}`} className="text-ink-3" aria-hidden>·</span>, node],
            )}
            {(canChangeParent || canChangeDirection || showOverviewPrimaryAgent) && (
              <div className={cx("flex shrink-0 flex-wrap items-center gap-1.5", overviewItems.length > 0 && "ml-auto")}>
                {canChangeParent && (
                  <Button
                    tone="quiet"
                    size="sm"
                    icon="layers"
                    expanded={showParentPicker}
                    testId="todo-detail-change-parent"
                    onClick={() => {
                      setParentPickerOpen((v) => !v);
                      setDirectionPickerOpen(false);
                    }}
                    className="h-6 shrink-0 border border-line bg-surface px-2 text-label whitespace-nowrap text-ink-2 hover:bg-hover"
                  >
                    {t("todos.detail_change_parent")}
                  </Button>
                )}
                {canChangeDirection && (
                  <Button
                    tone="quiet"
                    size="sm"
                    icon="folder"
                    expanded={showDirectionPicker}
                    testId="todo-detail-change-direction"
                    onClick={() => {
                      setDirectionPickerOpen((v) => !v);
                      setParentPickerOpen(false);
                    }}
                    className="h-6 shrink-0 border border-line bg-surface px-2 text-label whitespace-nowrap text-ink-2 hover:bg-hover"
                  >
                    {t("todos.detail_change_direction")}
                  </Button>
                )}
                {showOverviewPrimaryAgent && workGoalActions?.activeAgent && (
                  <Button
                    tone={instead || canRetry ? "secondary" : "accent"}
                    icon="terminal"
                    testId="todo-summary-open-active-agent"
                    onClick={() => openFromPage({ name: "session", mid: workGoalActions.activeAgent!.machineId, sid: workGoalActions.activeAgent!.sessionId })}
                    className="h-6 shrink-0 px-2.5 text-label whitespace-nowrap"
                  >
                    {t("todos.detail_open_active_agent")}
                  </Button>
                )}
              </div>
            )}
          </div>
          {showParentPicker && onReorganizeDirection && (
            <TodoParentPicker
              key={`${todo.id}:${todo.part_of ?? ""}`}
              todoId={todo.id}
              currentParentId={todo.part_of}
              currentParentTitle={parentTodo?.title}
              todos={allTodos}
              onApply={onReorganizeDirection}
              onClose={() => setParentPickerOpen(false)}
            />
          )}
          {showDirectionPicker && onReorganizeDirection && (
            <TodoDirectionPicker
              key={`${todo.id}:${effectiveDirectionId ?? ""}:${directionInherited ? "1" : "0"}`}
              todoId={todo.id}
              currentDirectionId={effectiveDirectionId}
              currentDirectionLabel={resolvedDirectionLabel}
              currentDirectionInherited={directionInherited}
              childCount={childTodos.length}
              directions={directions}
              onApply={onReorganizeDirection}
              onClose={() => setDirectionPickerOpen(false)}
            />
          )}
        </>
      )}

      {/* Title Row with ⋯ menu */}
      <div className="flex items-center justify-between gap-3">
        <input
          type="text"
          value={title.value}
          aria-busy={title.saving}
          onChange={(e) => title.edit(e.target.value)}
          onBlur={title.save}
          onKeyDown={(e) => {
            if (composing(e.nativeEvent)) return;
            if (e.key === "Enter") {
              e.currentTarget.blur();
            }
          }}
          placeholder={t("todos.title_placeholder")}
          className="min-w-0 flex-1 bg-transparent text-[22px] font-bold text-ink outline-none transition-colors focus:border-b focus:border-accent"
        />
        <IconButton icon="more" label={t("todos.more_options")} onClick={menu.open} active={Boolean(menu.anchor)} />
        {menu.anchor && (
          <Menu
            anchor={menu.anchor}
            onClose={menu.close}
            align="right"
            items={[
              {
                key: "delete",
                label: t("todos.delete_menu"),
                icon: "x" as const,
                danger: true,
                onSelect: onDeleteClick,
              },
            ]}
          />
        )}
      </div>

      {/* Meta Line: under todos_v2 work goals, status and agent count live solely in the top overview strip (#17696) */}
      {todosV2 === true ? (
        <div className="mt-2.5 flex flex-wrap items-center gap-2 text-meta text-ink-2" data-testid="todo-detail-meta">
          {v2MetaItems.flatMap((node, index) =>
            index === 0 ? [node] : [<span key={`meta-sep-${index}`} className="text-ink-3" aria-hidden>·</span>, node],
          )}
          <span className="text-ink-3" aria-hidden>·</span>
          <div
            role="radiogroup"
            aria-label={t("todos.detail_item_type_label")}
            data-testid="todo-detail-item-type"
            className="inline-flex h-6 shrink-0 items-center gap-0.5 rounded-control bg-inset p-0.5 shadow-hairline"
          >
            {(["task", "personal"] as const).map((nextType) => {
              const active = currentItemType === nextType;
              return (
                <button
                  key={nextType}
                  type="button"
                  role="radio"
                  aria-checked={active}
                  data-item-type={nextType}
                  disabled={itemTypeBusy}
                  onClick={() => void switchItemType(nextType)}
                  className={cx(
                    "touch-hit h-5 rounded-chip px-2 text-submeta whitespace-nowrap transition-colors focus-visible:outline-2 focus-visible:outline-accent disabled:opacity-50",
                    active ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-3 hover:text-ink-2",
                  )}
                >
                  {nextType === "task" ? t("todos.item_type_task") : t("todos.item_type_personal")}
                </button>
              );
            })}
          </div>
          {canUnsee && (
            <Button
              tone="quiet"
              size="sm"
              icon="undo"
              busy={unseeing}
              disabled={unseeing}
              testId="todo-unsee-btn"
              onClick={() => void handleUnsee()}
              className="h-6 shrink-0 border border-line bg-surface px-2 text-label whitespace-nowrap text-ink-2 hover:bg-hover"
            >
              {t("todos.detail_mark_unread")}
            </Button>
          )}
          {canAddPRBlocker && !hasActiveBlockers && (
            <Button
              tone="quiet"
              size="sm"
              icon="plus"
              expanded={prBlockerOpen}
              testId="todo-add-pr-blocker-btn"
              onClick={toggleAddPRBlocker}
              className="h-6 shrink-0 border border-line bg-surface px-2 text-label whitespace-nowrap text-ink-2 hover:bg-hover"
            >
              {t("todos.active_blocker_add_pr")}
            </Button>
          )}
        </div>
      ) : (
        <div className="mt-2.5 flex flex-wrap items-center gap-2.5 text-[12.5px] text-ink-2">
          <span className={cx("inline-flex h-5.5 items-center gap-1.5 rounded-md px-2 text-[12px] font-semibold", toneClass)}>
            <StatusMark state={dotState} size="sm" />
            <span data-testid="todo-detail-status">{steps?.label ?? statusLabel}</span>
          </span>
          {steps?.tail && (
            <>
              <span className="text-ink-3">·</span>
              <span data-testid="todo-detail-steps-next">{steps.tail}</span>
            </>
          )}
          <span className="text-ink-3">·</span>
          <span>{t("todos.agent_count", { count: activeAgents.length })}</span>
          {responsible && (
            <>
              <span className="text-ink-3">·</span>
              <span
                data-testid="todo-detail-responsible"
                data-kind={responsible.kind}
                title={t("todos.responsible_label", { name: responsible.name })}
                className="inline-flex min-w-0 items-center gap-1 text-ink-2"
              >
                <Icon name={responsible.kind === "person" ? "person" : "terminal"} size={12} stroke={2} className="shrink-0 text-ink-3" />
                <span className="truncate">{responsible.name}</span>
              </span>
            </>
          )}
          {steps?.after && (
            <>
              <span className="text-ink-3">·</span>
              <span data-testid="todo-detail-steps">{steps.after}</span>
            </>
          )}
          <span className="text-ink-3">·</span>
          <span>{t("todos.created_at", { time: clock(todo.created_at) })}</span>
        </div>
      )}
      {isCancelled(todo) && cancellationReason(todo) && (
        <p className="mt-2 text-xs leading-[1.6] text-ink-3" data-testid="todo-detail-cancelled-reason">
          {t("todos.cancelled_reason", { reason: cancellationReason(todo) })}
        </p>
      )}
      {failedStart && (
        <p className="mt-2 text-meta leading-[1.6] text-red" data-testid="todo-detail-start-failed">
          {t("todos.start_failed", { error: todo.start_error ?? "" })}
        </p>
      )}
      {failedTask?.error_code === "shell_waiting" && <Suspense fallback={null}><ShellQuestion todo={todo} task={failedTask} className="mt-3" /></Suspense>}
      {isAutoDone(todo) && (
        <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-3">
          {todo.done_reason ? t("todos.auto_done_reason", { reason: todo.done_reason }) : t("todos.auto_done")}
        </p>
      )}

      {/* What it waits on from the user and how to answer it (#2056) */}
      {reviewState(todo) === "user_check" ? <VerificationReport todo={todo} lookup={lookup} /> : <TodoAskPanel todo={todo} />}
      <HeldForBriefing todo={todo} />

      {/* Single progressive focus banner unifying wait, active blockers, repo gate, and merge train under todos_v2 (#17696, #17938) */}
      {todosV2 === true && (
        <TodoDetailFocusBanner
          todo={todo}
          allTodosById={allTodosById}
          onOpenTodo={onOpenTodo}
          mergeTrainBadge={mergeTrainBadge}
          canAddPR={open}
          addingPROpen={prBlockerOpen}
          onToggleAddPR={toggleAddPRBlocker}
          cancellingBlockerId={cancellingBlockerId}
          onCancelBlocker={(blockerId) => void handleCancelBlocker(blockerId)}
        />
      )}
      {canAddPRBlocker && prBlockerOpen && (
        <TodoAddPRBlockerForm
          value={prBlockerUrl}
          error={prBlockerError}
          busy={prBlockerBusy}
          onChange={(next) => {
            setPrBlockerUrl(next);
            if (prBlockerError) setPrBlockerError(null);
          }}
          onSubmit={() => void submitExternalPRBlocker()}
          onCancel={() => {
            setPrBlockerOpen(false);
            setPrBlockerUrl("");
            setPrBlockerError(null);
          }}
        />
      )}

      {/* Elevate Notes / Description directly beneath header + focus banner under todos_v2 (#17938) */}
      {todosV2 === true && notesBlock}

      {/* Due date, reminder, priority (#1683) */}
      <TodoPlanEditor todo={todo} todosV2={todosV2} />
      {/* The brief its work is held to (#3822): who asked and in what words above the criteria; the scope changes
          and who worked on it under them. */}
      {todo.brief && (
        <Suspense fallback={null}>
          <TaskBriefPanel todo={todo} />
        </Suspense>
      )}
      {todosV2 !== true && todo.wait != null && <div className="mt-2 select-text"><TaskWaitInfo todo={todo} expanded /></div>}
      {reviewState(todo) !== "user_check" && <ReviewChecklist todo={todo} lookup={lookup} compact={todosV2 === true} />}
      {todo.brief && (
        <Suspense fallback={null}>
          <TaskBriefHistory todo={todo} onOpenTodo={onOpenTodo} />
        </Suspense>
      )}
      <LatestReport todo={todo} />

      {/* Code task stages from the hub. Its PR link is not drawn again here: the repository links below are the one
          place a task's issues and PRs are shown (legacy ones included). */}
      <TodoStageBar stage={todo.stage} stageKind={todo.stage_kind} done={todo.stage_done} className="mt-4" />

      {todo.origin && (todosV2 === true ? (hasLinkedAgents && <TaskCompletionCheck todo={todo} compact />) : <TaskCompletionCheck todo={todo} />)}

      {/* Notes / Description (legacy placement preserved when todos_v2 is off) */}
      {todosV2 !== true && notesBlock}

      <TodoImages todo={todo} owner={owner} />

      <ExternalSourcesEntry manager={todo.manager ?? defaultManager()} kind="todo" id={todo.id} title={todo.title}>
        <TodoRepositoryBindings key={`${owner}:${todo.manager ?? defaultManager()}:${todo.id}`} manager={todo.manager ?? defaultManager()} owner={owner} todoId={todo.id}
          prStatus={todo.pr_status} otherPRs={todo.other_prs} legacyPRs={legacyPR ? [legacyPR] : []} version={todo.updated_at} todosV2={todosV2}
          repoGate={todosV2 ? todo.repo_gate : undefined}
          hideRepoGateBanner={todosV2 === true}
          onRepositoriesCount={todosV2 === true ? setRepositoryCount : undefined} />
      </ExternalSourcesEntry>
      {todosV2 === true && childTodos.length > 0 && (() => {
        const renderSubtaskItem = (child: Todo) => {
          const childDone = Boolean(child.done_at);
          const childStatus = todoStatus(child, lookup);
          const childShown = shownStatus(child, childStatus);
          const childLook = todoLook(child, childStatus, waitOf(child, childStatus, lookup), undefined, undefined, repoGatePRLook);
          const childDot =
            childShown === "needs_you" || childShown === "interrupted"
              ? "needs_you"
              : childShown === "unread"
                ? "unread"
                : childShown === "working" || childShown === "waiting"
                  ? "working"
                  : "offline";
          const childDirection = resolveChildDirection?.(child);
          const showDirectionMismatch = Boolean(
            childDirection && childDirection.id !== effectiveDirectionId,
          );
          return (
            <button
              key={child.id}
              type="button"
              data-testid="todo-detail-subtask-item"
              data-subtask-id={child.id}
              onClick={() => onOpenTodo(child.id)}
              className="flex items-center gap-2.5 rounded-control border border-line bg-canvas px-3 py-2 text-left transition-colors hover:bg-hover focus-visible:outline-2 focus-visible:outline-accent"
            >
              {childDone ? (
                <span className="flex size-3.5 shrink-0 items-center justify-center rounded-full bg-accent text-white">
                  <Icon name="check" size={9} stroke={2.6} />
                </span>
              ) : (
                <StatusMark state={childDot} size="sm" />
              )}
              <span className={cx("min-w-0 flex-1 truncate text-meta", childDone ? "text-ink-3 line-through" : "font-medium text-ink")}>
                {child.title}
              </span>
              {showDirectionMismatch && childDirection && (
                <span
                  data-testid="todo-detail-subtask-direction"
                  title={t("todos.detail_subtask_direction_mismatch", { direction: childDirection.label })}
                  className="inline-flex max-w-28 shrink-0 items-center gap-1 truncate rounded-chip bg-field px-1.5 py-0.5 text-submeta whitespace-nowrap text-ink-2"
                >
                  <Icon name="folder" size={10} stroke={2} className="shrink-0 text-ink-3" />
                  <span className="truncate">{childDirection.label}</span>
                </span>
              )}
              <span
                data-testid="todo-detail-subtask-status"
                className={cx("shrink-0 text-submeta whitespace-nowrap", TONE_CLASS[childLook.tone])}
              >
                {todoSupervisorLabel(child, childStatus, lookup) ?? childLook.label}
              </span>
              <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
            </button>
          );
        };
        return (
          <section className="mt-6" data-testid="todo-detail-subtasks">
            <div className="mb-2 text-submeta font-medium tracking-[0.04em] text-ink-3">
              {t("todos.detail_subtasks_section", {
                done: doneChildCount,
                total: childTodos.length,
              })}
            </div>
            <div className="flex flex-col gap-1.5">
              {openChildren.map(renderSubtaskItem)}
              {doneChildren.length > 0 && (
                <div className={cx(openChildren.length > 0 && "pt-1")}>
                  <button
                    type="button"
                    data-testid="todo-detail-subtasks-done-toggle"
                    aria-expanded={doneSubtasksOpen}
                    onClick={() => setDoneSubtasksOverride({ todoId: todo.id, open: !doneSubtasksOpen })}
                    className="touch-hit flex h-7 w-full items-center gap-1.5 rounded-control px-2 text-left text-submeta font-medium text-ink-3 transition-colors hover:bg-hover hover:text-ink-2 focus-visible:outline-2 focus-visible:outline-accent"
                  >
                    <Icon
                      name="chevron"
                      size={10}
                      stroke={2.4}
                      className="shrink-0 opacity-60 transition-transform"
                      style={{ transform: doneSubtasksOpen ? "none" : "rotate(-90deg)" }}
                    />
                    <span className="truncate">
                      {t("todos.detail_subtasks_done_toggle", { count: doneChildren.length })}
                    </span>
                  </button>
                  {doneSubtasksOpen && (
                    <div data-testid="todo-detail-subtasks-done-list" className="mt-1.5 flex flex-col gap-1.5">
                      {doneChildren.map(renderSubtaskItem)}
                    </div>
                  )}
                </div>
              )}
            </div>
          </section>
        );
      })()}
      <PageBoundary page="workspace-naming">
        <Suspense fallback={<Loading />}>
          <WorkspaceNaming manager={todo.manager ?? defaultManager()} owner={owner} todoId={todo.id} />
        </Suspense>
      </PageBoundary>

      {todosV2 !== true && (
        <>
          <ResponsibleSection todo={todo} machines={machines} />

          <ThreadSection todo={todo} />

          <DecisionsSection todo={todo} lookup={lookup} />
        </>
      )}

      {/* Spaces Section */}
      {spacesSupported !== false && todo.spaces && todo.spaces.length > 0 && (
        <div className="mt-6">
          <div className="mb-2 text-[11.5px] font-medium tracking-[0.04em] text-ink-3 uppercase">
            {t("todos.spaces_section", { count: todo.spaces.length })}
          </div>
          <div className="flex flex-col gap-2">
            {todo.spaces.map((sp) => {
              const mName = machines.find((m) => m.id === sp.machine_id)?.name || sp.machine_id;
              const live = hasLiveAgent(sp.machine_id, sp.space);
              return (
                <div
                  key={`${sp.machine_id}/${sp.space}`}
                  className="flex items-center justify-between gap-3 rounded-control border border-line bg-canvas p-3"
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-[13px] font-semibold text-ink">
                      {sp.space} · {mName}
                    </span>
                    {!live && (
                      <span className="shrink-0 rounded bg-hover px-1.5 py-0.5 text-[10.5px] text-ink-3">
                        {t("todos.space_no_agent")}
                      </span>
                    )}
                  </div>
                  <Button
                    tone="quiet"
                    className="shrink-0 text-ink-3 hover:bg-red-tint/50 hover:text-red whitespace-nowrap px-2 py-1 text-[12px]"
                    onClick={() => void removeSpace(sp.machine_id, sp.space)}
                  >
                    {t("todos.space_remove")}
                  </Button>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* Current Agents */}
      <div className="mt-6 flex-1">
        <div className="mb-2 text-[11.5px] font-medium tracking-[0.04em] text-ink-3">
          {t("todos.agents_section", { count: activeAgents.length })}
        </div>

        {activeAgents.length === 0 ? (
          <div className="rounded-control border border-dashed border-line p-6 text-center text-[13px] text-ink-3">
            {open ? (responsible ? t("todos.no_agents_responsible", { name: responsible.name }) : t("todos.no_agents")) : t("todos.no_agents_done")}
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {activeAgents.map((agent) => {
              const session = lookup(agent.machine_id, agent.session_id);
              const st = todoAgentState(agent, session, lookup);
              const now = taskNow(session, st === "working");
              const isExpired = !session && !agent.start_error;
              const displayTitle = session?.title || agent.title || t("lib-format.untitled_session");

              // computer · space › tab (the computer's name is its display name in machines)
              const mName = machines.find((m) => m.id === agent.machine_id)?.name || agent.machine_id;
              const spaceTab = session ? [session.space_name, tabLabel(session)].filter(Boolean).join(" › ") : "";
              const displaySub = spaceTab
                ? `${mName} · ${spaceTab}`
                : `${mName} · ${shortPath(session?.cwd || agent.cwd)}`;

              const ix = st === "needs_you" ? ownerCard(interactions, agent, lookup) : undefined;
              const kicker = ix?.kind === "plan" ? t("todos.kicker_plan") : ix?.kind === "permission" ? t("todos.kicker_permission") : ix?.kind === "screen" ? t("todos.kicker_screen") : ix?.kind === "elicitation" ? t("todos.kicker_elicitation") : t("todos.kicker_question");
              const sourceLabel = formatAgentSource(agent, todo.spaces, lookup);

              const openAgent = () => {
                // a drill-down: Back and the sidebar stay with this to-do (#12055)
                if (session) {
                  openFromPage({ name: "session", mid: agent.machine_id, sid: agent.session_id });
                }
              };
              const cardBody = (
                <>
                  <div className="mt-1">
                    <StatusMark state={agent.start_error ? "offline" : isExpired ? "offline" : st} />
                  </div>
                  <div className="min-w-0 flex-1">
                    {/* The source tag leads the title line (as on iOS, #3150) and is never squeezed; a long one
                        ("Spawned by …") stops at 45% of the line, so the title keeps the rest and truncates (#3577). */}
                    <div className="flex min-w-0 items-center gap-2">
                      {sourceLabel && (
                        <span
                          data-testid="todo-agent-source"
                          title={sourceLabel}
                          className="max-w-[45%] shrink-0 truncate rounded bg-hover px-1.5 py-0.5 text-[10.5px] text-ink-3"
                        >
                          {sourceLabel}
                        </span>
                      )}
                      <span data-testid="todo-agent-title" title={displayTitle} className="min-w-0 flex-1 truncate text-[13.5px] font-semibold text-ink">
                        {displayTitle}
                      </span>
                      {isExpired && (
                        <span className="shrink-0 whitespace-nowrap rounded bg-hover px-1.5 py-0.5 text-[10.5px] text-ink-3">{t("todos.ended")}</span>
                      )}
                    </div>
                    <div className="mt-0.5 truncate text-[12px] text-ink-3">{displaySub}</div>

                    {/* Error / Last Text */}
                    {agent.start_error ? (
                      <div className="mt-1.5 flex items-center justify-between gap-2 rounded bg-red-tint/50 px-2 py-1 text-[12px] text-red">
                        <span className="truncate">{t("todos.start_failed", { error: agent.start_error })}</span>
                        <button
                          type="button"
                          onClick={(e) => {
                            e.stopPropagation();
                            void unlinkAgent(agent);
                          }}
                          className="shrink-0 font-medium hover:underline"
                        >
                          {t("todos.remove")}
                        </button>
                      </div>
                    ) : now || (st === "needs_you" && askText(ix)) ? (
                      <div className="mt-1 line-clamp-2 text-[12.5px] text-ink-2">
                        {st === "needs_you" && (
                          <span className="font-medium text-attention">{t("todos.kicker_line", { kicker })}</span>
                        )}
                        {(st === "needs_you" && askText(ix)) || (now && (now.kind === "words" ? plainMarkdown(now.text) : now.text))}
                      </div>
                    ) : null}
                  </div>
                </>
              );

              return (
                <div
                  key={`${agent.machine_id}/${agent.session_id}`}
                  data-testid="todo-agent-card"
                  className={cx(
                    "flex items-start gap-3 rounded-control border border-line bg-canvas p-3 transition-colors",
                    session ? "hover:bg-hover" : "",
                  )}
                >
                  {session ? (
                    <button
                      type="button"
                      data-testid="todo-agent-open"
                      onClick={openAgent}
                      className="flex min-w-0 flex-1 cursor-pointer items-start gap-3 rounded-chip text-left focus-visible:outline-2 focus-visible:outline-accent"
                    >
                      {cardBody}
                    </button>
                  ) : (
                    <div className="flex min-w-0 flex-1 items-start gap-3">
                      {cardBody}
                    </div>
                  )}
                  <div className="flex shrink-0 items-center gap-1">
                    <span data-testid="todo-agent-time" className="whitespace-nowrap text-[11px] text-ink-3">
                      {ago(session?.updated_at || agent.linked_at)}
                    </span>
                    {!agent.start_error && (
                      <IconButton
                        icon="x"
                        label={t("todos.unlink_agent", { name: displayTitle })}
                        size={24}
                        iconSize={12}
                        onClick={(e) => {
                          e.stopPropagation();
                          void unlinkAgent(agent);
                        }}
                      />
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        )}

        {/* Released Agents (Participated) */}
        {releasedAgents.length > 0 && (
          <div className="mt-4">
            <button
              type="button"
              aria-expanded={releasedVisible}
              onClick={todosV2 === true ? () => setReleasedOpenV2((v) => !v) : onToggleReleased}
              className="flex items-center gap-1 text-[12px] text-ink-3 hover:text-ink-2 focus-visible:outline-2 focus-visible:outline-accent"
            >
              <Icon
                name="chevron"
                size={11}
                className={cx("transition-transform duration-200", releasedVisible ? "rotate-180" : "")}
              />
              <span>{t("todos.released_section", { count: releasedAgents.length })}</span>
            </button>
            {releasedVisible && (
              <div className="mt-2 flex flex-col gap-1.5">
                {releasedAgents.map((agent) => {
                  const mName = machines.find((m) => m.id === agent.machine_id)?.name || agent.machine_id;
                  const displaySub = `${mName} · ${shortPath(agent.cwd)}`;
                  return (
                    <div
                      key={`${agent.machine_id}/${agent.session_id}`}
                      className="flex items-center justify-between rounded-control border border-line/60 bg-hover/40 px-3 py-2 text-[12px] text-ink-3"
                    >
                      <div className="min-w-0 flex-1">
                        <span className="truncate font-medium text-ink-2">{agent.title || agent.session_id}</span>
                        <div className="text-[11px] text-ink-3">{displaySub}</div>
                      </div>
                      <span className="shrink-0 text-[11px]">{t("todos.left_at", { time: ago(agent.released_at ?? undefined) })}</span>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        )}
      </div>

      {/* Secondary audit & traceback sections: collapsed by default with count/summary under todos_v2 (#17696) */}
      {todosV2 === true && (
        <div
          data-testid="todo-detail-audit-group"
          aria-label={t("todos.detail_audit_group")}
          className="mt-6 flex flex-col gap-2"
        >
          <DecisionsSection todo={todo} lookup={lookup} defaultCollapsed />
          <ResponsibleSection todo={todo} machines={machines} defaultCollapsed />
          <ThreadSection todo={todo} defaultCollapsed />
          <TodoCausalTraceSection todoId={todo.id} enabled compact />
        </div>
      )}

      {/* Bottom Action Bar. A done to-do can't be handed over (the hub refuses, #1856): Reopen next to it comes first. A
          failed start is open: Retry (or Hand to agent, which reopens it first) and Complete, which closes it for good as
          its row's checkbox does. */}
      <div className="mt-8 flex flex-wrap items-center gap-2.5 border-t border-line pt-4">
        {instead && (
          <Button
            tone="accent"
            icon="retry"
            busy={retrying}
            disabled={retrying}
            onClick={() => void retry(instead)}
            testId="todo-retry-launcher"
            className="h-8.5 px-3.5 text-meta whitespace-nowrap"
          >
            {t("assistant-task-actions.use_launcher_instead", { launcher: instead })}
          </Button>
        )}
        {canRetry && (
          <Button
            tone={instead ? "secondary" : "accent"}
            icon="retry"
            busy={retrying}
            disabled={retrying}
            onClick={() => void retry()}
            className="h-8.5 px-3.5 text-meta whitespace-nowrap"
          >
            {t("todos.retry")}
          </Button>
        )}
        {isWorkGoalV2 && open && workGoalActions?.primaryAction === "open_agent" && workGoalActions.activeAgent ? (
          <>
            <Button
              tone={instead || canRetry ? "secondary" : "accent"}
              icon="terminal"
              testId="todo-open-active-agent"
              onClick={() => openFromPage({ name: "session", mid: workGoalActions.activeAgent!.machineId, sid: workGoalActions.activeAgent!.sessionId })}
              className="h-8.5 px-3.5 text-meta whitespace-nowrap"
            >
              {t("todos.detail_open_active_agent")}
            </Button>
            {workGoalActions.prUrl && (
              <a
                href={workGoalActions.prUrl}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="todo-open-pr"
                className="inline-flex h-8.5 items-center gap-1.5 rounded-control border border-line bg-surface px-3.5 text-meta font-medium whitespace-nowrap text-ink transition-colors hover:bg-hover focus-visible:outline-2 focus-visible:outline-accent"
              >
                <Icon name="external" size={13} stroke={2} />
                <span>{t("todos.detail_open_pr")}</span>
              </a>
            )}
            <Button
              tone="secondary"
              icon="plus"
              testId="todo-add-agent"
              onClick={onOpenHandover}
              className="h-8.5 px-3.5 text-meta whitespace-nowrap"
            >
              {t("todos.detail_add_agent")}
            </Button>
          </>
        ) : (
          <>
            {open && (
              <Button tone={canRetry ? "secondary" : "accent"} icon="hand" onClick={onOpenHandover} className="h-8.5 px-3.5 text-[13px] whitespace-nowrap">
                {t("todos.hand_over")}
              </Button>
            )}
            {isWorkGoalV2 && workGoalActions?.prUrl && (
              <a
                href={workGoalActions.prUrl}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="todo-open-pr"
                className="inline-flex h-8.5 items-center gap-1.5 rounded-control border border-line bg-surface px-3.5 text-meta font-medium whitespace-nowrap text-ink transition-colors hover:bg-hover focus-visible:outline-2 focus-visible:outline-accent"
              >
                <Icon name="external" size={13} stroke={2} />
                <span>{t("todos.detail_open_pr")}</span>
              </a>
            )}
          </>
        )}
        <Button tone="secondary" icon="check" onClick={() => onToggleDone(open)} className="h-8.5 px-3 text-[13px] whitespace-nowrap">
          {open ? completeLabel : t("todos.reopen")}
        </Button>
      </div>
    </div>
  );
}

// ── Responsible party (#12712) ───────────────────────────────

/**
 * The external responsible party (long-running session or person) when one is recorded on the to-do (#12712).
 * Renders nothing when unset so to-dos without a responsible party stay unchanged.
 */
function ResponsibleSection({
  todo,
  machines,
  defaultCollapsed = false,
}: {
  todo: Todo;
  machines: Machine[];
  defaultCollapsed?: boolean;
}) {
  const responsible = responsibleOf(todo);
  const [busy, setBusy] = useState(false);
  const [expanded, setExpanded] = useState(!defaultCollapsed);

  useEffect(() => {
    setBusy(false);
    setExpanded(!defaultCollapsed);
  }, [todo.id, defaultCollapsed]);

  if (!responsible) return null;

  const machineName = responsible.machine_id
    ? machines.find((m) => m.id === responsible.machine_id)?.name || responsible.machine_id
    : undefined;
  const hint = [machineName, responsible.session_id].filter(Boolean).join(" · ");

  const clear = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await updateTodo(todo.id, { responsible: null });
      await refreshTodos();
      toast(t("todos.responsible_cleared"), "ok");
    } catch (e) {
      toast(t("todos.responsible_clear_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  const body = (
    <div className="flex items-center justify-between gap-3">
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2 text-sm font-semibold text-ink">
          <span className="shrink-0 text-ink-3">
            <Icon name={responsible.kind === "person" ? "person" : "terminal"} size={14} />
          </span>
          <span data-testid="todo-responsible-name" className="truncate">
            {responsible.name}
          </span>
          <span
            data-testid="todo-responsible-kind"
            className="shrink-0 whitespace-nowrap rounded bg-hover px-1.5 py-0.5 text-xs font-normal text-ink-3"
          >
            {t(`todos.responsible_kind_${responsible.kind}`)}
          </span>
        </div>
        {hint && (
          <div data-testid="todo-responsible-hint" className="mt-0.5 truncate pl-5.5 text-xs text-ink-3">
            {hint}
          </div>
        )}
      </div>
      <Button
        tone="quiet"
        busy={busy}
        disabled={busy}
        title={t("todos.responsible_clear_label", { name: responsible.name })}
        className="shrink-0 whitespace-nowrap px-2 py-1 text-xs text-ink-3 hover:bg-red-tint/50 hover:text-red"
        onClick={() => void clear()}
      >
        {t("todos.responsible_clear")}
      </Button>
    </div>
  );

  if (defaultCollapsed) {
    return (
      <DetailAuditDisclosure
        testId="todo-responsible-section"
        toggleTestId="todo-responsible-toggle"
        title={t("todos.responsible_section")}
        count={1}
        summary={responsible.name}
        expanded={expanded}
        onToggle={() => setExpanded((v) => !v)}
      >
        {body}
      </DetailAuditDisclosure>
    );
  }

  return (
    <div className="mt-6" data-testid="todo-responsible-section">
      <div className="mb-2 text-xs font-medium tracking-[0.04em] text-ink-3 uppercase">
        {t("todos.responsible_section")}
      </div>
      <div className="rounded-control border border-line bg-canvas p-3">
        {body}
      </div>
    </div>
  );
}

// ── Chat thread (#5279) ──────────────────────────────────────

/**
 * The chat thread the todo is bound to, with Unbind. Binding is not done here: it needs the bot's recent topics, which the
 * hub has no route for, so an unbound todo says how a binding comes about. A hub from before threads answers the unbind
 * with a route-level 404: the section then goes away without a word, as the decisions do. It shows only when the hub says it
 * has to-do threads (feature `todo_threads`): an older hub never sends a thread, so an "unbound" note there would be false.
 */
function ThreadSection({ todo, defaultCollapsed = false }: { todo: Todo; defaultCollapsed?: boolean }) {
  const thread = boundThread(todo);
  const threadsOn = useStore((s) => todoThreadsOn(s.features));
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [unsupported, setUnsupported] = useState(false);
  const [expanded, setExpanded] = useState(!defaultCollapsed);

  useEffect(() => {
    setConfirm(false);
    setBusy(false);
    setExpanded(!defaultCollapsed);
  }, [todo.id, defaultCollapsed]);

  const unbind = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await unbindTodoThread(todo.id);
      setConfirm(false);
      await refreshTodos();
      toast(t("todos.thread_unbound"), "ok");
    } catch (e) {
      if (isLegacySpacesRouteError(e)) {
        setConfirm(false);
        setUnsupported(true);
      } else {
        toast(t("todos.thread_unbind_failed", { error: errorText(e) }), "error");
      }
    } finally {
      setBusy(false);
    }
  };

  if (!threadsOn || unsupported) return null;

  const dialog = thread && (
    <Dialog open={confirm} onClose={() => !busy && setConfirm(false)} title={t("todos.thread_unbind_title")}>
      <div className="py-2 text-[13.5px] text-ink-2">{t("todos.thread_unbind_confirm", { name: threadName(thread) })}</div>
      <div className="mt-4 flex justify-end gap-2">
        <Button tone="secondary" disabled={busy} onClick={() => setConfirm(false)}>
          {t("common.cancel")}
        </Button>
        <Button tone="danger" busy={busy} onClick={() => void unbind()}>
          {t("todos.thread_unbind")}
        </Button>
      </div>
    </Dialog>
  );

  if (defaultCollapsed) {
    return (
      <>
        <DetailAuditDisclosure
          testId="todo-thread"
          toggleTestId="todo-thread-toggle"
          title={t("todos.thread_section")}
          count={thread ? 1 : 0}
          summary={thread ? threadName(thread) : undefined}
          expanded={expanded}
          onToggle={() => setExpanded((v) => !v)}
        >
          {thread ? (
            <div className="flex items-center justify-between gap-3">
              <div className="flex min-w-0 items-center gap-2 text-meta font-semibold text-ink">
                <span className="shrink-0 text-ink-3">
                  <Icon name="chat" size={14} />
                </span>
                <span data-testid="todo-thread-line" title={threadLine(thread)} className="truncate">
                  {threadLine(thread)}
                </span>
              </div>
              <Button
                tone="quiet"
                title={t("todos.thread_unbind_label", { name: threadName(thread) })}
                className="shrink-0 whitespace-nowrap px-2 py-1 text-label text-ink-3 hover:bg-red-tint/50 hover:text-red"
                onClick={() => setConfirm(true)}
              >
                {t("todos.thread_unbind")}
              </Button>
            </div>
          ) : (
            <p className="text-submeta leading-relaxed text-ink-3">{t("todos.thread_empty")}</p>
          )}
        </DetailAuditDisclosure>
        {dialog}
      </>
    );
  }

  return (
    <div className="mt-6" data-testid="todo-thread">
      <div className="mb-2 text-[11.5px] font-medium tracking-[0.04em] text-ink-3 uppercase">{t("todos.thread_section")}</div>
      {thread ? (
        <div className="flex items-center justify-between gap-3 rounded-control border border-line bg-canvas p-3">
          <div className="flex min-w-0 items-center gap-2 text-[13px] font-semibold text-ink">
            <span className="shrink-0 text-ink-3">
              <Icon name="chat" size={14} />
            </span>
            <span data-testid="todo-thread-line" title={threadLine(thread)} className="truncate">
              {threadLine(thread)}
            </span>
          </div>
          <Button
            tone="quiet"
            title={t("todos.thread_unbind_label", { name: threadName(thread) })}
            className="shrink-0 whitespace-nowrap px-2 py-1 text-[12px] text-ink-3 hover:bg-red-tint/50 hover:text-red"
            onClick={() => setConfirm(true)}
          >
            {t("todos.thread_unbind")}
          </Button>
        </div>
      ) : (
        <div className="rounded-control border border-dashed border-line p-4 text-[12.5px] leading-[1.6] text-ink-3">
          {t("todos.thread_empty")}
        </div>
      )}
      {dialog}
    </div>
  );
}

// ── Decisions (#1618) ────────────────────────────────────────

/**
 * Decisions: the calls made on this todo, in effect first-to-last; "Revise" writes a new one that replaces it, "full history" shows
 * the replaced ones struck through. Refetched whenever the todo changes (the hub bumps it when a decision is written,
 * also from an answered card). Hidden on a hub without decisions (404 on the route).
 * Only the first five show at first, with Show all under them; the header's fold-all button folds the list away and
 * opens it whole (#6447, `shownDecisions`). Nothing is stored: each to-do opens with the first five, and one just
 * recorded or revised shows the whole list so it is in view.
 */
function DecisionsSection({
  todo,
  lookup,
  defaultCollapsed = false,
}: {
  todo: Todo;
  lookup: ReturnType<typeof makeSessionLookup>;
  defaultCollapsed?: boolean;
}) {
  const [all, setAll] = useState<TodoDecision[] | null>(null);
  const [failed, setFailed] = useState("");
  const [unsupported, setUnsupported] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const [view, setView] = useState<DecisionsView>("first");
  const [expanded, setExpanded] = useState(!defaultCollapsed);
  const [form, setForm] = useState<{ supersedes?: TodoDecision } | null>(null);
  const [linking, setLinking] = useState<TodoDecision | null>(null);
  const menu = useMenu();
  // Only the newest load lands: a slow earlier one must not put back an older list.
  const loads = useRef(0);

  const load = useCallback(async () => {
    const n = ++loads.current;
    try {
      const list = await fetchTodoDecisions(todo.id);
      if (n !== loads.current) return;
      setAll(list);
      setFailed("");
      setUnsupported(false);
    } catch (e) {
      if (n !== loads.current) return;
      if (isLegacySpacesRouteError(e)) setUnsupported(true); // a hub from before decisions: no route
      else setFailed(errorText(e));
    }
  }, [todo.id]);

  useEffect(() => {
    void load();
  }, [load, todo.updated_at]);

  useEffect(() => {
    setAll(null);
    setFailed("");
    setShowAll(false);
    setView("first");
    setExpanded(!defaultCollapsed);
    setForm(null);
  }, [todo.id, defaultCollapsed]);

  if (unsupported) return null;
  const effective = (all ?? []).filter((d) => !d.superseded_by);
  const replaced = (all ?? []).length - effective.length;
  const listed = showAll ? (all ?? []) : effective;
  const shown = shownDecisions(listed, view);
  const folded = view === "folded" && listed.length > 0;
  const byId = new Map((all ?? []).map((d) => [d.id, d]));

  const markSupersedes = async (d: TodoDecision, old: string) => {
    try {
      await setDecisionSupersedes(todo.id, d.id, old);
      await load();
      toast(old ? t("todos.marked_superseded") : t("todos.unmarked_superseded"), "ok");
    } catch (e) {
      toast(t("todos.action_failed", { error: errorText(e) }), "error");
    }
  };

  const listContent = (
    <>
      {failed && all === null ? (
        <div className="flex items-center justify-between gap-3 rounded-control border border-dashed border-line p-4 text-[12.5px] text-ink-3">
          <span className="min-w-0 truncate">{t("todos.decisions_failed", { error: failed })}</span>
          <Button tone="quiet" className="shrink-0 whitespace-nowrap px-2 py-1 text-[12px]" onClick={() => void load()}>
            {t("todos.retry")}
          </Button>
        </div>
      ) : all === null || (!defaultCollapsed && folded) ? null : shown.length === 0 ? (
        <div className="rounded-control border border-dashed border-line p-4 text-[12.5px] leading-[1.6] text-ink-3">
          {t("todos.decisions_empty")}
        </div>
      ) : (
        <ol className="flex flex-col gap-2">
          {shown.map((d) => {
            const off = Boolean(d.superseded_by);
            const replaces = d.supersedes_id ? byId.get(d.supersedes_id) : undefined;
            const candidates = supersedeCandidates(d, all ?? []);
            return (
              <li
                key={d.id}
                className={cx("rounded-control border border-line p-3", off ? "bg-hover/40" : "bg-canvas")}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <div className={cx("text-[13.5px] font-semibold", off ? "text-ink-3 line-through" : "text-ink")}>{d.question}</div>
                    <div className={cx("mt-1 text-[13px] whitespace-pre-wrap", off ? "text-ink-3 line-through" : "text-ink")}>
                      <span className="text-ink-3">{t("todos.chose")}</span>
                      {d.choice}
                    </div>
                    {d.reason && (
                      <div className={cx("mt-0.5 text-[12.5px] whitespace-pre-wrap text-ink-2", off && "line-through")}>
                        <span className="text-ink-3">{t("todos.reason")}</span>
                        {d.reason}
                      </div>
                    )}
                    <div className="mt-1.5 flex flex-wrap items-center gap-x-1.5 text-[11.5px] text-ink-3">
                      <span>{clock(d.phase_completed_at ?? d.created_at)}</span>
                      <span>·</span>
                      <span>{decisionSourceLabel(d, todo, lookup)}</span>
                      {off && (
                        <>
                          <span>·</span>
                          <span>{t("todos.superseded")}</span>
                        </>
                      )}
                      {!off && replaces && (
                        <>
                          <span>·</span>
                          <span className="truncate">{t("todos.supersedes", { choice: replaces.choice })}</span>
                        </>
                      )}
                    </div>
                  </div>
                  {!off && (
                    <div className="flex shrink-0 items-center gap-0.5">
                      <Button tone="quiet" className="whitespace-nowrap px-2 py-1 text-[12px]" onClick={() => setForm({ supersedes: d })}>
                        {t("todos.supersede")}
                      </Button>
                      {(candidates.length > 0 || d.supersedes_id) && (
                        <IconButton
                          icon="more"
                          label={t("todos.more")}
                          size={26}
                          active={linking?.id === d.id && Boolean(menu.anchor)}
                          onClick={(e) => {
                            setLinking(d);
                            menu.open(e);
                          }}
                        />
                      )}
                    </div>
                  )}
                </div>
              </li>
            );
          })}
        </ol>
      )}
      {(!folded || defaultCollapsed) && listed.length > DECISIONS_FIRST && (
        <Button tone="quiet" testId="todo-decisions-more" expanded={view === "all"} className="mt-1.5 whitespace-nowrap px-2 py-1 text-xs" onClick={() => setView(view === "first" ? "all" : "first")}>
          {view === "first" ? t("todos.decisions_show_all", { count: listed.length }) : t("todos.decisions_show_first", { count: DECISIONS_FIRST })}
        </Button>
      )}
    </>
  );

  const overlays = (
    <>
      {menu.anchor && linking && (
        <Menu
          anchor={menu.anchor}
          onClose={menu.close}
          align="right"
          width={260}
          items={[
            ...supersedeCandidates(linking, all ?? []).map((c) => ({
              key: c.id,
              label: t("todos.supersedes_pair", { question: c.question, choice: c.choice }),
              onSelect: () => void markSupersedes(linking, c.id),
            })),
            ...(linking.supersedes_id
              ? [{ key: "undo", label: t("todos.undo_supersede"), divider: true, onSelect: () => void markSupersedes(linking, "") }]
              : []),
          ]}
        />
      )}

      <DecisionDialog
        open={form !== null}
        todoId={todo.id}
        supersedes={form?.supersedes}
        onClose={() => setForm(null)}
        onSaved={async () => {
          // the one just recorded or revised is last: show them all so it is in view
          setExpanded(true);
          setView("all");
          await load();
        }}
      />
    </>
  );

  if (defaultCollapsed) {
    return (
      <>
        <DetailAuditDisclosure
          testId="todo-decisions"
          toggleTestId="todo-decisions-toggle"
          title={t("todos.decisions_section", { count: effective.length })}
          expanded={expanded}
          onToggle={() => setExpanded((v) => !v)}
          right={
            <>
              {replaced > 0 && expanded && (
                <Button tone="quiet" size="sm" className="h-6 px-2 text-submeta whitespace-nowrap" onClick={() => setShowAll((v) => !v)}>
                  {showAll ? t("todos.show_effective") : t("todos.show_history", { count: replaced })}
                </Button>
              )}
              <Button tone="quiet" size="sm" icon="plus" className="h-6 px-2 text-submeta whitespace-nowrap" onClick={() => setForm({})}>
                {t("todos.add_decision")}
              </Button>
            </>
          }
        >
          {listContent}
        </DetailAuditDisclosure>
        {overlays}
      </>
    );
  }

  return (
    <div className="mt-6" data-testid="todo-decisions">
      <div className="relative mb-2 flex items-center justify-between gap-3">
        <span className="text-[11.5px] font-medium tracking-[0.04em] text-ink-3 uppercase">{t("todos.decisions_section", { count: effective.length })}</span>
        <div className="flex shrink-0 items-center gap-1">
          {replaced > 0 && !folded && (
            <Button tone="quiet" className="whitespace-nowrap px-2 py-1 text-[12px]" onClick={() => setShowAll((v) => !v)}>
              {showAll ? t("todos.show_effective") : t("todos.show_history", { count: replaced })}
            </Button>
          )}
          <Button tone="quiet" icon="plus" className="whitespace-nowrap px-2 py-1 text-[12px]" onClick={() => setForm({})}>
            {t("todos.add_decision")}
          </Button>
          {/* the Agents title row's fold-all button and meaning: Collapse all while any is shown, Expand all once folded */}
          {all !== null && listed.length > 0 && (
            <FoldAllButton
              label={folded ? t("todos.decisions_expand") : t("todos.decisions_fold")}
              expand={folded}
              onClick={() => setView(folded ? "all" : "folded")}
              className={SESSION_LIST_STYLE.foldButton}
              testId="todo-decisions-fold"
            />
          )}
        </div>
      </div>

      {listContent}
      {overlays}
    </div>
  );
}

/** ⌘↩ / Ctrl+↩ anywhere in a dialog's form submits it (audit item 70), as New Agent, Compose and the inbox's hand-off do
 *  (lib/shortcuts.ts keeps Mod+Enter for dialogs); a multi-line field keeps Enter for its new lines. The form's own
 *  submit decides, so one busy or incomplete stays as it is. */
function submitOnModEnter(e: React.KeyboardEvent<HTMLFormElement>) {
  if (e.defaultPrevented || e.key !== "Enter" || !(e.metaKey || e.ctrlKey) || composing(e.nativeEvent)) return;
  e.preventDefault();
  e.currentTarget.requestSubmit();
}

function ReportConcernDialog({ todo, onClose }: { todo: Todo; onClose: () => void }) {
  const [report, setReport] = useState("");
  const [busy, setBusy] = useState(false);
  const fieldId = useId();
  const hintId = useId();
  const canSubmit = report.trim().length > 0 && !busy;
  const close = () => { if (!busy) onClose(); };

  const submit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    try {
      await reportConcern(todo.id, report);
      await refreshTodos();
      await refresh();
      onClose();
      toast(t("todos.report_concern_success"), "ok");
    } catch (err) {
      toast(t("todos.action_failed", { error: errorText(err) }), "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open onClose={close} title={t("todos.report_concern_title")}>
      <form onSubmit={submit} onKeyDown={submitOnModEnter} className="flex flex-col gap-3.5">
        <p id={hintId} className="wrap-break-word text-meta text-ink-3">{t("todos.report_concern_hint", { title: todo.title })}</p>
        <div>
          <label htmlFor={fieldId} className="mb-1 block text-label font-medium text-ink-2">{t("todos.report_concern_label")}</label>
          <textarea
            id={fieldId}
            aria-describedby={hintId}
            rows={3}
            required
            autoFocus
            readOnly={busy}
            value={report}
            onChange={(e) => setReport(e.target.value)}
            placeholder={t("todos.report_concern_placeholder")}
            className="w-full resize-y rounded-control border border-line bg-canvas px-3 py-1.5 text-meta text-ink outline-none focus:border-accent"
          />
        </div>
        <div className="mt-2 flex flex-wrap justify-end gap-2">
          <Button tone="secondary" disabled={busy} onClick={close}>{t("common.cancel")}</Button>
          <Button tone="accent" type="submit" disabled={!canSubmit} busy={busy}>{t("todos.report_concern_submit")}</Button>
        </div>
      </form>
    </Dialog>
  );
}

function DecisionDialog({
  open,
  todoId,
  supersedes,
  onClose,
  onSaved,
}: {
  open: boolean;
  todoId: string;
  supersedes?: TodoDecision;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [question, setQuestion] = useState("");
  const [choice, setChoice] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setQuestion(supersedes?.question ?? "");
      setChoice("");
      setReason("");
    }
  }, [open, supersedes]);

  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!question.trim() || !choice.trim() || busy) return;
    setBusy(true);
    try {
      await addTodoDecision(todoId, { question, choice, reason, supersedes_id: supersedes?.id });
      await onSaved();
      toast(supersedes ? t("todos.superseded_toast") : t("todos.recorded"), "ok");
      onClose();
    } catch (err) {
      toast(t("todos.save_failed", { error: errorText(err) }), "error");
    } finally {
      setBusy(false);
    }
  };

  const field = "w-full rounded-control border border-line bg-canvas px-3 py-1.5 text-[13.5px] text-ink outline-none focus:border-accent";
  return (
    <Dialog open={open} onClose={onClose} title={supersedes ? t("todos.supersede") : t("todos.decision_title")}>
      <form onSubmit={submit} onKeyDown={submitOnModEnter} className="flex flex-col gap-3.5">
        {supersedes && (
          <p className="text-[12.5px] leading-[1.6] text-ink-3">
            {t("todos.supersede_hint", { choice: supersedes.choice })}
          </p>
        )}
        <div>
          <label htmlFor="decision-question" className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.question")}</label>
          <input id="decision-question" type="text" required autoFocus={!supersedes} value={question}
            onChange={(e) => setQuestion(e.target.value)} placeholder={t("todos.question_placeholder")} className={field} />
        </div>
        <div>
          <label htmlFor="decision-choice" className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.choice")}</label>
          <input id="decision-choice" type="text" required autoFocus={Boolean(supersedes)} value={choice}
            onChange={(e) => setChoice(e.target.value)} placeholder={t("todos.choice_placeholder")} className={field} />
        </div>
        <div>
          <label htmlFor="decision-reason" className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.reason_optional")}</label>
          <textarea id="decision-reason" rows={2} value={reason} onChange={(e) => setReason(e.target.value)}
            placeholder={t("todos.reason_placeholder")} className={cx(field, "resize-y text-[13px]")} />
        </div>
        <div className="mt-2 flex justify-end gap-2">
          <Button tone="secondary" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="accent" type="submit" disabled={!question.trim() || !choice.trim() || busy} busy={busy}>
            {supersedes ? t("todos.supersede") : t("todos.record")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ── New Todo Dialog ──────────────────────────────────────────

function NewTodoDialog({
  open,
  manager,
  owner,
  slot,
  onClose,
  onCreated,
}: {
  open: boolean;
  manager: string;
  owner: string;
  /** Made on the calendar (#2029): due there, a moment or a whole day; the calendar stays in front once it is saved. */
  slot?: CalendarSlot;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const repositoryContext = useRepositoryRequestContext(manager, owner);
  const [repositoryBindings, setRepositoryBindings] = useState<CreateRepositoryBindings>();
  const [repositoryCatalog, setRepositoryCatalog] = useState<RepositoryCatalog>();
  const creating = useRef(false);
  const [title, setTitle] = useState("");
  const [notes, setNotes] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const imagesSupported = useStore((s) => s.features.includes("todo_images"));
  const keptAttachments = useRef<LocalAttachment[]>([]);
  // pictures picked and still being made ready (lib/todo-image.ts, #13797), in the order picked; the tray shows them by
  // name until they are
  const [preparing, setPreparing] = useState<PreparingImage[]>([]);
  const queued = useRef<PreparingImage[]>([]);
  const pumping = useRef(false);
  const operationId = useRef(uuid());
  const operationKey = useRef("");
  const imageInput = useRef<HTMLInputElement>(null);
  const [dueDay, setDueDay] = useState("");
  const [dueTime, setDueTime] = useState("");
  const [allDay, setAllDay] = useState(true);
  const repositoryIdentities = useMemo(() => new Map((repositoryCatalog?.repositories ?? []).map((entry) => [entry.id, entry] as const)), [repositoryCatalog]);
  const cleanBindings = repositoryBindings ? cleanRepositoryDraft(repositoryBindings.repositories) : [];
  const duplicateLinks = repositoryBindings ? repositoryDuplicateLink(cleanBindings, repositoryIdentities) : false;
  const bindingsValid = !repositoryBindings || validRepositoryDraft(cleanBindings, repositoryIdentities);

  useEffect(() => () => {
    queued.current = [];
    releaseAttachments(keptAttachments.current);
  }, []);
  useEffect(() => {
    if (!open) return;
    operationId.current = uuid();
    operationKey.current = "";
    setFailure("");
    queued.current = [];
    setPreparing([]);
    releaseAttachments(keptAttachments.current);
    keptAttachments.current = [];
    setAttachments([]);
  }, [open]);

  useEffect(() => {
    if (open) {
      setTitle("");
      setNotes("");
      if (slot) {
        setDueDay(dayKey(slot.at ?? slot.day));
        setDueTime(slot.at !== undefined ? localParts(slot.at).time : "09:00");
        setAllDay(slot.at === undefined);
      }
    }
  }, [open, slot]);

  const discardImages = () => {
    queued.current = [];
    setPreparing([]);
    releaseAttachments(keptAttachments.current);
    keptAttachments.current = [];
    setAttachments([]);
  };
  const cancel = () => {
    if (creating.current) return;
    discardImages();
    onClose();
  };
  const addImages = (files: FileList | File[]) => {
    if (creating.current) return;
    const picked: PreparingImage[] = [];
    let problem = "";
    for (const file of Array.from(files)) {
      if (keptAttachments.current.length + queued.current.length + picked.length >= TODO_IMAGE_LIMIT) {
        problem = t("todos.images_limit", { max: TODO_IMAGE_LIMIT });
        break;
      }
      if (file.type !== "image/png" && file.type !== "image/jpeg") {
        problem = t("todos.image_type", { name: file.name });
        continue;
      }
      picked.push({ id: uuid(), name: file.name || "image.png", file });
    }
    setFailure(problem);
    if (!picked.length) return;
    queued.current = [...queued.current, ...picked];
    setPreparing(queued.current);
    void prepareQueued();
  };
  /** Makes the queued pictures ready one at a time (a large one is decoded whole, so never several at once). One removed
   *  meanwhile, or picked before the dialog closed, is dropped when it is done; one that cannot be made ready says so
   *  under the others, which stay, as does the draft. */
  const prepareQueued = async () => {
    if (pumping.current) return;
    pumping.current = true;
    try {
      for (let next = queued.current[0]; next; next = queued.current[0]) {
        let done: { file: File } | { error: string };
        try {
          const { prepareTodoImage } = await import("../lib/todo-image");
          done = await prepareTodoImage(next.file);
        } catch (error) {
          done = { error: errorText(error) };
        }
        if (queued.current[0] !== next) continue;
        queued.current = queued.current.slice(1);
        setPreparing(queued.current);
        if ("error" in done) {
          const error = done.error;
          setFailure((shown) => (shown ? `${shown}\n${error}` : error));
          continue;
        }
        keptAttachments.current = [...keptAttachments.current, pickAttachment(done.file)];
        setAttachments(keptAttachments.current);
      }
    } finally {
      pumping.current = false;
    }
  };
  const removeImage = (id: string) => {
    if (creating.current) return;
    queued.current = queued.current.filter((image) => image.id !== id);
    setPreparing(queued.current);
    releaseAttachments(keptAttachments.current.filter((image) => image.id === id));
    const next = keptAttachments.current.filter((image) => image.id !== id);
    keptAttachments.current = next;
    setAttachments(next);
    setFailure("");
  };

  /** What the slot's row now says: due on the day, or at the day's time. */
  const due = (): { due_at?: string; due_date?: string } | undefined => {
    if (!slot || !parseDayKey(dueDay)) return undefined;
    if (allDay || !/^\d{2}:\d{2}$/.test(dueTime)) return { due_date: dueDay };
    const [h, m] = dueTime.split(":").map(Number);
    const at = new Date(parseDayKey(dueDay)!);
    at.setHours(h, m, 0, 0);
    return { due_at: at.toISOString() };
  };

  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!title.trim() || creating.current || queued.current.length || !bindingsValid || !repositoryContext.isCurrent()) return;
    if (keptAttachments.current.length > 0 && !imagesSupported) {
      setFailure(t("lib-todos.images_unsupported"));
      return;
    }
    const nextOperationKey = JSON.stringify({
      title: title.trim(), notes: notes.trim(), due: due(), repositoryBindings,
      images: keptAttachments.current.map((image) => image.id),
    });
    if (nextOperationKey !== operationKey.current) {
      operationKey.current = nextOperationKey;
      operationId.current = uuid();
    }
    creating.current = true;
    setBusy(true);
    setFailure("");
    try {
      const creation = keptAttachments.current.length ? {
        operationId: operationId.current,
        images: await Promise.all(keptAttachments.current.map(async (image) => ({
          id: image.id,
          name: image.name,
          ...await imagePayload(image.file),
        }))),
      } : undefined;
      const todo = repositoryBindings
        ? await createTodoWithRepositories(repositoryContext, title, notes, due(), repositoryBindings, repositoryIdentities, creation)
        : await createTodo(title, notes, due(), creation);
      if (!repositoryContext.isCurrent()) return;
      await refreshTodos();
      await refresh();
      toast(t("todos.created"), "ok");
      discardImages();
      onClose();
      if (slot) return; // the calendar stays, the new one on it
      onCreated(todo.id);
      navigate(`/todos?id=${encodeURIComponent(todo.id)}`);
    } catch (err) {
      if (repositoryContext.isCurrent()) {
        const message = t("todos.create_failed", { error: errorText(err) });
        setFailure(message);
        toast(message, "error");
      }
    } finally {
      creating.current = false;
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={cancel} title={t("todos.new_todo")}>
      <form onSubmit={submit} onKeyDown={submitOnModEnter} className="flex flex-col gap-3.5">
        <div>
          <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.title_label")}</label>
          <input
            type="text"
            required
            autoFocus
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={t("todos.new_title_placeholder")}
            className="w-full rounded-control border border-line bg-canvas px-3 py-1.5 text-[13.5px] text-ink outline-none focus:border-accent"
          />
        </div>

        {slot && (
          <div data-testid="todo-new-due">
            <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.due_label")}</label>
            <div className="flex flex-wrap items-center gap-2">
              <input
                type="date"
                data-testid="todo-new-due-day"
                value={dueDay}
                onChange={(e) => setDueDay(e.target.value)}
                className="rounded-control border border-line bg-canvas px-2.5 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
              />
              {!allDay && (
                <input
                  type="time"
                  data-testid="todo-new-due-time"
                  value={dueTime}
                  onChange={(e) => setDueTime(e.target.value)}
                  className="rounded-control border border-line bg-canvas px-2.5 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
                />
              )}
              <label className="flex cursor-pointer items-center gap-1.5 text-[12.5px] text-ink-2">
                <input type="checkbox" data-testid="todo-new-all-day" checked={allDay} onChange={(e) => setAllDay(e.target.checked)} className="accent-accent" />
                {t("todos.calendar_all_day")}
              </label>
            </div>
          </div>
        )}

        <div>
          <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.notes_optional")}</label>
          <textarea
            rows={3}
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder={t("todos.new_notes_placeholder")}
            className="w-full resize-y rounded-control border border-line bg-canvas px-3 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
          />
        </div>

        {imagesSupported && (
        <div>
          <input
            ref={imageInput}
            type="file"
            accept="image/jpeg,image/png"
            multiple
            hidden
            disabled={busy}
            aria-label={t("todos.add_images")}
            onChange={(e) => {
              if (e.currentTarget.files) addImages(e.currentTarget.files);
              e.currentTarget.value = "";
            }}
          />
          <Button tone="quiet" icon="plus" className="touch-hit whitespace-nowrap" disabled={busy || attachments.length + preparing.length >= TODO_IMAGE_LIMIT} onClick={() => imageInput.current?.click()}>
            {t("todos.add_images")}
          </Button>
          <p className="mt-1 text-submeta text-ink-3">{t("todos.images_hint", { max: TODO_IMAGE_LIMIT })}</p>
          {attachments.length + preparing.length > 0 && (
            <div className="mt-2">
              <AttachmentTray attachments={[...attachments, ...preparing]} onRemove={busy ? undefined : removeImage} />
              {preparing.length > 0 && (
                <p role="status" data-testid="todo-images-preparing" className="mt-1.5 flex items-center gap-1.5 text-submeta text-ink-3">
                  <Spinner />
                  {t("todos.images_preparing", { count: preparing.length })}
                </p>
              )}
            </div>
          )}
        </div>
        )}

        <NewTodoRepositoryBindings context={repositoryContext} value={repositoryBindings} onChange={setRepositoryBindings} pending={busy} onCatalog={setRepositoryCatalog} />
        {!bindingsValid && <p role="alert" className="text-submeta text-red">{t(duplicateLinks ? "task-repositories.duplicate_link" : "task-repositories.draft_invalid")}</p>}
        {failure && <p role="alert" className="whitespace-pre-line text-meta text-red">{failure}</p>}

        <div className="mt-2 flex justify-end gap-2">
          <Button tone="secondary" disabled={busy} onClick={cancel}>
            {t("common.cancel")}
          </Button>
          <Button tone="accent" type="submit" disabled={!title.trim() || busy || preparing.length > 0 || !bindingsValid} busy={busy}>
            {busy && attachments.length > 0 ? t("todos.images_uploading") : t("todos.create")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ── Due Date Dialog ──────────────────────────────────────────

/** A row menu's Due date › Pick a date… (#4429): any day, starting from the one it is due on (else today). A to-do due
 *  at a time keeps the time (`duePatch`). It stays open, with the toast, when the save fails. */
function TodoDueDialog({ todo, onClose, onSave }: { todo: Todo; onClose: () => void; onSave: (patch: TodoPatch) => Promise<boolean> }) {
  const [day, setDay] = useState(() => dayKey(dueOn(todo) ?? Date.now()));
  const [busy, setBusy] = useState(false);
  const picked = parseDayKey(day);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (picked === undefined || busy) return;
    setBusy(true);
    if (await onSave(duePatch(todo, picked))) onClose();
    else setBusy(false);
  };
  return (
    <Dialog open onClose={onClose} title={t("todos-menu.due")}>
      <form onSubmit={submit} className="flex flex-col gap-3.5">
        <div>
          <label htmlFor="todo-due-pick" className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.due_label")}</label>
          <input
            id="todo-due-pick"
            type="date"
            required
            autoFocus
            data-testid="todo-due-pick"
            value={day}
            onChange={(e) => setDay(e.target.value)}
            className="rounded-control border border-line bg-canvas px-2.5 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
          />
        </div>
        <div className="mt-2 flex justify-end gap-2">
          <Button tone="secondary" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="accent" type="submit" disabled={picked === undefined || busy} busy={busy}>
            {t("common.save")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ── Handover to Agent Dialog ─────────────────────────────────

export function HandoverDialog({
  open,
  todo,
  machines,
  onClose,
}: {
  open: boolean;
  todo: Todo;
  machines: Machine[];
  onClose: () => void;
}) {
  const [tab, setTab] = useState<"new" | "existing">("new");
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);
  const mountedRef = useRef(true);
  const sessionGenRef = useRef(0);
  const inFlightGenRef = useRef(-1);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      sessionGenRef.current += 1;
      inFlightGenRef.current = -1;
    };
  }, []);
  useEffect(() => {
    sessionGenRef.current += 1;
    inFlightGenRef.current = -1;
    setBusy(false);
  }, [open, todo.id]);
  const phone = useMedia(SHEET_QUERY);
  const pickLabel = useId();

  // For "new":
  const onlineMachines = useMemo(() => machines.filter((m) => m.online), [machines]);
  const [mid, setMid] = useState(() => onlineMachines[0]?.id ?? "");
  const machine = onlineMachines.find((m) => m.id === mid);
  const [cwd, setCwd] = useState("");
  const [launchers, setLaunchers] = useState<Launcher[]>([]);
  const [launchersOf, setLaunchersOf] = useState("");
  // What Model settings › Agents runs on this computer, as the hub says (null: no launcher default); undefined until the list
  // is in, and from an older hub, which has no default to follow.
  const [hubDefault, setHubDefault] = useState<LauncherDefault | null | undefined>(undefined);
  // "": the default (the hub resolves it at the start, as New Agent's "Default: …")
  const [launcher, setLauncher] = useState("");

  // For "existing":
  const { sessions: allSessions } = useFullAgentSessions(open);
  const eligibleSessions = useMemo(() => {
    const online = new Set(onlineMachines.map((m) => m.id));
    const linked = new Set((todo.agents ?? []).filter((a) => !a.released_at).map((a) => `${a.machine_id}/${a.session_id}`));
    return allSessions.filter((s) => !s.assistant && online.has(s.machine_id) && sessionCan(s, "send")).sort((a, b) =>
      Number(linked.has(`${b.machine_id}/${b.session_id}`)) - Number(linked.has(`${a.machine_id}/${a.session_id}`))
      || byTime(b.updated_at, a.updated_at)
      || a.machine_id.localeCompare(b.machine_id) || a.session_id.localeCompare(b.session_id));
  }, [allSessions, onlineMachines, todo.agents]);
  const [selectedKey, setSelectedKey] = useState("");

  // The form starts over when the dialog opens. While it is open the lists change with every push of a session or a
  // machine: what is typed stays, and the picked agent / computer changes only once it is gone from its list.
  const wasOpen = useRef(false);
  useEffect(() => {
    const opening = open && !wasOpen.current;
    wasOpen.current = open;
    if (!open) return;
    if (opening) setPrompt("");
    if (!mid || !onlineMachines.some((m) => m.id === mid)) {
      setMid(onlineMachines[0]?.id ?? "");
    }
    const first = eligibleSessions[0];
    setSelectedKey((key) => opening || !eligibleSessions.some((s) => `${s.machine_id}/${s.session_id}` === key)
      ? first ? `${first.machine_id}/${first.session_id}` : ""
      : key);
  }, [open, onlineMachines, eligibleSessions]);

  useEffect(() => {
    if (!open) return;
    // another computer's list is not this one's; an empty computer selection clears it
    setLaunchers([]);
    setLaunchersOf("");
    setHubDefault(undefined);
    setLauncher("");
    if (!mid) return;
    const controller = new AbortController();
    const owner = credentialOwner();
    machineLaunchers(mid, { signal: controller.signal })
      .then((r) => {
        if (controller.signal.aborted || owner !== credentialOwner()) return;
        setLaunchersOf(JSON.stringify([mid, owner]));
        const ls = r.launchers ?? [];
        setLaunchers(ls);
        setHubDefault(r.default);
        // the default the user configured, as New Agent starts from it (#4384); an older hub has none: the first this computer
        // can start now, not one its terminal can't run (#1643)
        setLauncher(r.default !== undefined ? "" : initialLauncher(null, ls));
      })
      .catch((e) => { if (!controller.signal.aborted) toast(errorText(e), "error"); });
    return () => controller.abort();
  }, [open, mid]);

  const previewCard = useMemo(() => {
    return formatTodoContext(todo.title, todo.notes);
  }, [todo]);
  // the default is held to what it runs here
  const pickedBlock = launcherBlock(launchers.find((l) => l.name === (launcher || hubDefault?.provider)));
  const defaultChoice = hubDefault ? defaultLabel(hubDefault, "", launchers) : t("newagent.follow_model_settings");

  // A failed start (#3713) is closed on the hub, which refuses a hand-over to a done to-do (#1856): handing it over reopens
  // it first, only then, so a dialog closed without a choice leaves it as it was.
  const reopenFailed = async () => {
    if (todo.done_at && todo.start_error) await updateTodo(todo.id, { done: false });
  };

  const submitNew = async () => {
    const gen = sessionGenRef.current;
    if (!machine || inFlightGenRef.current === gen) return;
    inFlightGenRef.current = gen;
    setBusy(true);
    let succeeded = false;
    try {
      await reopenFailed();
      if (!mountedRef.current || sessionGenRef.current !== gen) return;
      const fullText = [previewCard, prompt.trim()].filter(Boolean).join("\n\n");
      // It only queues the start and opens the new agent's page, which says how it went: no success toast before the hub
      // has answered (#1856).
      const inline = launchersOf === JSON.stringify([mid, credentialOwner()])
        ? inlineTextStart(launchers.find((l) => l.name === (launcher || hubDefault?.provider)), currentHosts(machine).default, fullText, 0)
        : undefined;
      launchAgent({
        machineId: mid,
        cwd: cwd.trim(),
        launcher,
        text: fullText,
        attachments: [],
        todoId: todo.id,
        ...inline,
      });
      succeeded = true;
      onClose();
    } catch (e) {
      if (mountedRef.current && sessionGenRef.current === gen) toast(t("todos.launch_failed", { error: errorText(e) }), "error");
    } finally {
      if (!succeeded) {
        if (inFlightGenRef.current === gen) inFlightGenRef.current = -1;
        if (mountedRef.current && sessionGenRef.current === gen) setBusy(false);
      }
    }
  };

  const submitExisting = async () => {
    const gen = sessionGenRef.current;
    const s = eligibleSessions.find((x) => `${x.machine_id}/${x.session_id}` === selectedKey);
    if (!s || inFlightGenRef.current === gen) return;
    inFlightGenRef.current = gen;
    setBusy(true);
    let succeeded = false;
    try {
      await reopenFailed();
      if (!mountedRef.current || sessionGenRef.current !== gen) return;
      const fullText = [previewCard, prompt.trim()].filter(Boolean).join("\n\n");
      const clientMsgId = uuid();
      await api(`/sessions/${enc(s.machine_id)}/${enc(s.session_id)}/messages`, {
        method: "POST",
        json: {
          text: fullText,
          client_msg_id: clientMsgId,
          todo_id: todo.id,
        },
      });
      await refreshTodos();
      await refresh();
      if (!mountedRef.current || sessionGenRef.current !== gen) return;
      succeeded = true;
      toast(t("todos.sent"), "ok");
      onClose();
      openFromPage({ name: "session", mid: s.machine_id, sid: s.session_id });
    } catch (e) {
      if (mountedRef.current && sessionGenRef.current === gen) toast(t("todos.send_failed", { error: errorText(e) }), "error");
    } finally {
      if (!succeeded) {
        if (inFlightGenRef.current === gen) inFlightGenRef.current = -1;
        if (mountedRef.current && sessionGenRef.current === gen) setBusy(false);
      }
    }
  };

  const actions = (
    <>
      <Button tone="secondary" onClick={onClose}>
        {t("common.cancel")}
      </Button>
      {tab === "new" ? (
        <Button tone="accent" onClick={submitNew} disabled={!machine || busy} busy={busy}>
          {t("todos.launch_and_hand")}
        </Button>
      ) : (
        <Button
          tone="accent"
          onClick={submitExisting}
          disabled={!selectedKey || busy || eligibleSessions.length === 0}
          busy={busy}
        >
          {t("todos.send_and_hand")}
        </Button>
      )}
    </>
  );

  // The arrows move the pick through the Existing agent rows, and the focus with it, round from the last to the first.
  const pickByArrow = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const step = e.key === "ArrowDown" || e.key === "ArrowRight" ? 1 : e.key === "ArrowUp" || e.key === "ArrowLeft" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const n = eligibleSessions.length;
    const at = eligibleSessions.findIndex((s) => `${s.machine_id}/${s.session_id}` === selectedKey);
    const i = at < 0 ? (step > 0 ? 0 : n - 1) : (at + step + n) % n;
    setSelectedKey(`${eligibleSessions[i].machine_id}/${eligibleSessions[i].session_id}`);
    e.currentTarget.querySelector<HTMLElement>(`[data-choice="${i}"]`)?.focus();
  };

  // On a phone the buttons are the sheet's footer, above the keyboard as in the other sheets; wide, they end the form.
  return (
    <Dialog open={open} onClose={onClose} title={t("todos.hand_over")} width={520} footer={phone ? actions : undefined}>
      <div className="flex flex-col gap-4">
        {/* Tab Switcher: a finger's height on a touch screen, not touch-hit, whose reach would cover the title's ✕ */}
        <div className="flex rounded-control bg-field p-0.5">
          <button
            type="button"
            onClick={() => setTab("new")}
            className={cx(
              "flex-1 rounded-[6px] py-1 text-[13px] font-medium transition-colors pointer-coarse:min-h-11",
              tab === "new" ? "bg-surface text-ink shadow-hairline" : "text-ink-3 hover:text-ink-2",
            )}
          >
            {t("todos.tab_new")}
          </button>
          <button
            type="button"
            onClick={() => setTab("existing")}
            className={cx(
              "flex-1 rounded-[6px] py-1 text-[13px] font-medium transition-colors pointer-coarse:min-h-11",
              tab === "existing" ? "bg-surface text-ink shadow-hairline" : "text-ink-3 hover:text-ink-2",
            )}
          >
            {t("todos.tab_existing")}
          </button>
        </div>

        {/* Tab 1: New Agent */}
        {tab === "new" && (
          <div className="flex flex-col gap-3">
            <div>
              <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.computer")}</label>
              <select
                value={mid}
                onChange={(e) => setMid(e.target.value)}
                className="w-full rounded-control border border-line bg-canvas px-2.5 py-1.5 text-[13px] text-ink outline-none"
              >
                {onlineMachines.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </select>
            </div>

            <div>
              <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.cwd_optional")}</label>
              <input
                type="text"
                placeholder={t("todos.cwd_placeholder")}
                value={cwd}
                onChange={(e) => setCwd(e.target.value)}
                className="w-full rounded-control border border-line bg-canvas px-2.5 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
              />
            </div>

            {(launchers.length > 0 || hubDefault !== undefined) && (
              <div>
                <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.launcher")}</label>
                <select
                  data-testid="todo-handoff-launcher"
                  value={launcher}
                  onChange={(e) => setLauncher(e.target.value)}
                  className="w-full rounded-control border border-line bg-canvas px-2.5 py-1.5 text-[13px] text-ink outline-none"
                >
                  {hubDefault !== undefined && <option value="">{defaultChoice}</option>}
                  {usableFirst(launchers).map((l) => {
                    const block = launcherBlock(l);
                    // still a choice (a start that works clears it), with why, after the ones that can start (#1643)
                    return (
                      <option key={l.name} value={l.name}>
                        {block ? `${providerLabel(l)} · ${block.reason}` : providerLabel(l)}
                      </option>
                    );
                  })}
                </select>
                {pickedBlock && (
                  <p data-testid="todo-handoff-blocked" className="mt-1.5 flex min-w-0 items-start gap-1.5 text-[11.5px] leading-snug text-ink-3">
                    <Icon name="warning" size={12} className="mt-px shrink-0 text-orange" />
                    <span className="min-w-0">{pickedBlock.reason}</span>
                  </p>
                )}
                {/* add, hide or restore launchers (#4384): Settings › Model providers, as New Agent's entry */}
                <button
                  type="button"
                  data-testid="todo-handoff-manage-launchers"
                  onClick={() => {
                    onClose();
                    navigate(settingsHref("models", null, false, null, "providers"));
                  }}
                  className="mt-1.5 inline-flex items-center gap-1 text-[11.5px] font-medium whitespace-nowrap text-accent-ink hover:underline"
                >
                  <Icon name="gear" size={11} />
                  {t("newagent.manage_launchers")}
                </button>
              </div>
            )}
          </div>
        )}

        {/* Tab 2: Existing Agent */}
        {tab === "existing" && (
          <div>
            <label id={pickLabel} className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.pick_agent")}</label>
            {eligibleSessions.length === 0 ? (
              <div className="py-6 text-center text-[12.5px] text-ink-3">{t("todos.no_live_agents")}</div>
            ) : (
              // one radio group: Tab reaches the picked agent, the arrows move the pick
              <div role="radiogroup" aria-labelledby={pickLabel} onKeyDown={pickByArrow} className="max-h-44 overflow-y-auto rounded-control border border-line p-1">
                {eligibleSessions.map((s, i) => {
                  const key = `${s.machine_id}/${s.session_id}`;
                  const isSel = key === selectedKey;
                  const st = activityOf(s);
                  return (
                    <button
                      type="button"
                      role="radio"
                      aria-checked={isSel}
                      tabIndex={isSel || (!selectedKey && i === 0) ? 0 : -1}
                      data-choice={i}
                      key={key}
                      onClick={() => setSelectedKey(key)}
                      className={cx(
                        "flex w-full cursor-pointer items-center justify-between gap-2 rounded px-2.5 py-1.5 text-left text-[12.5px] transition-colors pointer-coarse:min-h-11",
                        isSel ? "bg-accent text-white" : "hover:bg-hover text-ink",
                      )}
                      // the page's text rendering, which a browser resets on a button: the rows look as they did as divs
                      // (inline, so the first screen's stylesheet gets no rule for one dialog)
                      style={{ textRendering: "inherit" }}
                    >
                      <span className="block min-w-0">
                        <span className="flex min-w-0 items-center gap-2">
                          <StatusMark state={st} size="sm" />
                          <span className="truncate font-medium">{s.title || s.cwd || t("todos.unnamed")}</span>
                          <span className={cx("text-[11px]", isSel ? "text-white/80" : "text-ink-3")}>
                            {s.space_name || s.machine_id}
                          </span>
                        </span>
                        {st === "offline" && (
                          <span className={cx("block text-[11px]", isSel ? "text-white/80" : "text-ink-3")}>
                            {t("todos.closed_resume")}
                          </span>
                        )}
                      </span>
                      {isSel && <Icon name="check" size={13} stroke={2.5} />}
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        )}

        {/* Prompt Input */}
        <div>
          <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.prompt_label")}</label>
          <textarea
            rows={2}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            // ⌘↩ / Ctrl+↩ hands it over, as the dialog's button (submitOnModEnter)
            onKeyDown={(e) => {
              if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey) || composing(e.nativeEvent)) return;
              e.preventDefault();
              if (busy) return;
              if (tab === "new") void (machine && submitNew());
              else void submitExisting();
            }}
            placeholder={t("todos.prompt_placeholder")}
            className="w-full resize-none rounded-control border border-line bg-canvas px-3 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
          />
        </div>

        {/* Context Preview Card */}
        <div>
          <label className="mb-1 block text-[11px] font-medium text-ink-3">{t("todos.preview")}</label>
          <div className="flex flex-col gap-1 rounded-control bg-field p-2.5 shadow-hairline">
            <div className="rounded border-l-2 border-accent bg-surface px-2.5 py-1.5 text-left shadow-hairline">
              <div className="text-[11.5px] font-medium text-ink-2">{t("todos.preview_todo", { title: todo.title })}</div>
              {todo.notes && <div className="mt-0.5 line-clamp-2 text-[12px] text-ink-3">{todo.notes}</div>}
            </div>
            {prompt.trim() && (
              <div className="mt-1 px-1 text-[13px] whitespace-pre-wrap text-ink">{prompt.trim()}</div>
            )}
          </div>
        </div>

        {!phone && <div className="mt-2 flex justify-end gap-2">{actions}</div>}
      </div>
    </Dialog>
  );
}
