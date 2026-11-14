// Beautiful UI Todos View: left list (sorted by status & created_at), right detail, handover dialog.
import { lazy, memo, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type InputHTMLAttributes, type ReactNode, type RefObject } from "react";
import { api, credentialOwner, enc, errorText, subscribeCredentialOwner } from "../lib/api";
import { ago, clock, sessionCan, shortPath, stateOf, tabLabel, uuid } from "../lib/format";
import { foldedState } from "../lib/child-sessions";
import { sessionCard } from "../lib/folded-rows";
import { byTime } from "../lib/times";
import { launchAgent } from "../lib/new-agent";
import { currentHosts } from "../lib/engines";
import { initialLauncher, launcherBlock, usableFirst } from "../lib/launcher-health";
import { defaultLabel, inlineTextStart, machineLaunchers, type LauncherDefault } from "../lib/launcher-models";
import { providerLabel } from "../lib/provider-names";
import { navigate } from "../lib/router";
import { settingsHref } from "./settings/nav";
import { interactionAsk } from "../lib/inbox";
import { usageLimitLabel } from "../lib/usage-limit";
import { plainMarkdown } from "../lib/plain-markdown";
import { composing } from "../lib/send-history";
import { useScrollMemory } from "../lib/scroll-memory";
import { useAgentSessions, useFullAgentSessions } from "../lib/sessions";
import { useScoped } from "../lib/current-assistant";
import { dropTodo, refresh, refreshTodos, useStore, withTaskRows } from "../lib/store";
import {
  addTodoDecision,
  createTodo,
  decisionSourceLabel,
  DECISIONS_FIRST,
  type DecisionsView,
  deleteTodo,
  fetchTodoDecisions,
  setDecisionSupersedes,
  supersedeCandidates,
  shownDecisions,
  formatAgentSource,
  formatTodoContext,
  isLegacySpacesRouteError,
  makeSessionLookup,
  mostUrgentAgent,
  orphanedLine,
  sortTodos,
  TODO_STATUS_INFO,
  heldDelivery,
  TODO_TONE_CLASS,
  isAutoDone,
  isOverdue,
  todoDueDay,
  todoDueText,
  todoStatusLabel,
  shownStatus,
  todoStatus,
  unlinkAgentFromTodo,
  unlinkSpaceFromTodo,
  updateTodo,
  type Todo,
  type TodoAgent,
  type TodoPatch,
  type TodoDecision,
  type TodoStatus,
  type SessionLookup,
  taskNow,
} from "../lib/todos";
import type { AgentState, Interaction, Launcher, Machine } from "../lib/types";
import { Button, cx, Dialog, IconButton, Loading, Menu, toast, useMenu } from "../ui/atoms";
import { contextMenuProps } from "../ui/context-menu";
import type { MenuAt } from "../ui/menu-model";
import { SearchField } from "../ui/SearchField";
import { StatusMark } from "../ui/StatusMark";
import { Icon } from "../ui/icons";
import { FoldAllButton } from "../ui/FoldAllButton";
import { SESSION_LIST_STYLE } from "../ui/list-metrics";
import { formatDate, t } from "../i18n";
import { groupTodosByProject, treeStateLabel, type TodoProjectGroup, type TreeState } from "../lib/todo-tree";
import { directionTitle, groupsByDirection, useDirectionTree, type Direction } from "../lib/direction-tree";
import { DirectionCountsText } from "./DirectionMarks";
import { FOLDED_SECTIONS, TRIAGE_SECTIONS, checksAsOpen, countsAsOpen, lastActivity, supersededFailedStarts, triage, triageTodos, type TriageGroup, type TriageResult, type TriageSection } from "../lib/todo-triage";
import {
  TODO_LIST_FILTERS,
  TODO_LIST_SORTS,
  hiddenWaiting,
  isTodoListFilter,
  isTodoListSort,
  listFilterCounts,
  matchesListFilter,
  sortTriageGroups,
  type TodoListFilter,
  type TodoListSort,
} from "../lib/todo-list-filter";
import type { MenuItem } from "../ui/menu-model";
import { useRestoredTodos } from "../lib/todo-restore";
import { autoReviewLine, checklist, latestReview, parkedLine, progressLine, type CheckMark, type Supervision } from "../lib/supervision";
import { resultExtras } from "../lib/brief";
import { prLink } from "./TodoStageBar";
import { projectChoices, projectNames, useTodoProjects } from "../lib/todo-projects";
import { useFolded } from "../lib/use-folded";
import { stateName } from "../lib/state-labels";
import { answeredLookup } from "./assistant/answered-lookup";
import { Empty, Segmented } from "./assistant/parts";
import { OfflineState, useShownOffline } from "./ManagerParts";
import { TodoCalendar, type CalendarSlot } from "./TodoCalendar";
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
import { retryKey, retryTask, useFeed } from "../lib/feed";
import { dueOn, duePatch, todoRowMenu } from "./todo-row-menu";

/** What this browser was last on (#2029): the list or the calendar, the calendar's mode, and whether it shows done ones. */
const VIEW_KEY = "persona.todos.view";
const MODE_KEY = "persona.todos.calendar.mode";
const DONE_KEY = "persona.todos.calendar.showDone";
/** The list by status sections (default, #3156), by folder (#2526) or by pinned direction (#4352). */
const GROUPING_KEY = "persona.todos.grouping";
/** The list's filter by due date and its order inside each section (#3740), kept per browser like the view. */
const FILTER_KEY = "persona.todos.filter";
const SORT_KEY = "persona.todos.sort";

/** A filter's and a sort's name, as the menu and the chip say them. */
const filterLabel = (f: TodoListFilter) => t(`todos.filter_${f}`);
const sortLabel = (s: TodoListSort) => t(`todos.sort_${s}`);

type TodoGrouping = "status" | "folder" | "direction";

/** The grouping saved in this browser; status for none or an unknown one. */
function todoGrouping(saved: string | null): TodoGrouping {
  return saved === "folder" || saved === "direction" ? saved : "status";
}

function pref(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function setPref(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // private mode, storage full: the choice lasts the page
  }
}

/** The calendar's address: its mode, the day it shows, the to-do open beside it. */
function calendarRoute(mode: CalendarMode, day: number, id?: string): Route {
  return { name: "todos", view: "calendar", mode, date: dayKey(day), ...(id ? { id } : {}) };
}

export function TodosView({
  selectedId,
  view,
  mode: routeMode,
  date,
  onToggleSidebar,
}: {
  selectedId?: string;
  /** The calendar (#2029) rather than the list; its mode and day, from the address. */
  view?: "calendar";
  mode?: CalendarMode;
  date?: string;
  onToggleSidebar?: () => void;
}) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  // with several assistants, the current one's to-dos (docs/assistant/managers.md "Clients"); a link to another assistant's
  // to-do still opens it, without switching
  const everyTodo = useStore((s) => s.todos);
  const ownTodos = useScoped(everyTodo);
  const todos = useMemo(() => {
    const linked = selectedId && !ownTodos.some((x) => x.id === selectedId) ? everyTodo.filter((x) => x.id === selectedId) : [];
    return linked.length ? [...ownTodos, ...linked] : ownTodos;
  }, [everyTodo, ownTodos, selectedId]);
  const todosLoaded = useStore((s) => s.todosLoaded);
  const todosError = useStore((s) => s.todosError);
  const todosSupported = useStore((s) => s.todosSupported);
  // the current assistant offline (several assistants): the hub's merged list leaves it out, so none here means it
  // couldn't answer; the page says so instead of the empty one
  const offline = useShownOffline();
  const machines = useStore((s) => s.machines);
  const interactions = useStore((s) => s.interactions);
  const allSessions = useAgentSessions();
  const answered = useStore((s) => s.answered);
  // a card answered here stops its agent's "Needs you" at once, not at the hub's next push (#4186)
  const taskRows = useStore((s) => s.taskRows);
  const lookup = useMemo(() => answeredLookup(withTaskRows(makeSessionLookup(allSessions), taskRows), interactions, answered), [allSessions, taskRows, interactions, answered]);

  const [activeId, setActiveId] = useState<string | undefined>(selectedId);
  const [search, setSearch] = useState("");
  const [newOpen, setNewOpen] = useState(false);
  const [handoverOpen, setHandoverOpen] = useState(false);
  const [deleteConfirm, setDeleteConfirm] = useState(false);
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
  const [showReleased, setShowReleased] = useState(false);
  // The calendar (#2029): the address says which mode and day; a click on its empty axis opens New To-Do due there.
  const calendarOn = view === "calendar";
  const storedMode = pref(MODE_KEY);
  const calendarMode: CalendarMode = routeMode ?? (isCalendarMode(storedMode) ? storedMode : "week");
  const anchor = parseDayKey(date) ?? startOfDay(Date.now());
  const [calendarDone, setCalendarDone] = useState(() => pref(DONE_KEY) !== "0");
  const [newSlot, setNewSlot] = useState<CalendarSlot | undefined>();
  // /todos with no view: the one this browser was last on
  useEffect(() => {
    if (!view && !selectedId && pref(VIEW_KEY) === "calendar") navigate(calendarRoute(calendarMode, startOfDay(Date.now())), true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view, selectedId]);
  // on the calendar a new to-do is due on the day shown
  const openNew = () => {
    setNewSlot(calendarOn ? { day: anchor } : undefined);
    setNewOpen(true);
  };
  const showView = (next: "list" | "calendar") => {
    setPref(VIEW_KEY, next);
    navigate(next === "calendar" ? calendarRoute(calendarMode, startOfDay(Date.now()), selectedId) : { name: "todos", ...(selectedId ? { id: selectedId } : {}) });
  };

  // Sync selectedId from URL
  useEffect(() => {
    if (selectedId) setActiveId(selectedId);
  }, [selectedId]);
  // A link to a to-do the list doesn't have (deleted, or an old link): said once, and the address loses it, so another
  // to-do is not shown under it (web usability audit item 42). Not while the list can't tell: the current assistant
  // offline (the merged list leaves its to-dos out) or a hub without to-dos (an empty list); the link is kept for later.
  const linkGone = todosLoaded && !todosError && todosSupported && !offline && Boolean(selectedId) && !todos.some((td) => td.id === selectedId);
  useEffect(() => {
    if (!linkGone) return;
    toast(t("not-found.todo_gone"));
    navigate(calendarOn ? calendarRoute(calendarMode, anchor) : { name: "todos" }, true);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per link that is gone
  }, [linkGone]);

  // a minute's tick moves to-dos across the 24 h and 48 h lines (and today's end) while nothing else changes
  const [tick, setTick] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setTick(Date.now()), 60_000);
    return () => window.clearInterval(id);
  }, []);
  const [restored, restoreTodo] = useRestoredTodos();
  // whether a failed start was superseded by finished work (#3713) is decided on every to-do: a search or a filter that
  // leaves the finished one out must not put it back in Needs you
  const superseded = useMemo(() => supersededFailedStarts(todos), [todos]);
  // Each to-do's own status section (docs/assistant/todo-status-sop.md), whatever the list is grouped by: its checkbox
  // and its page read a failed start waiting on the owner as open (audit item 13).
  const sectionOf = useMemo(
    () => new Map(todos.map((td) => [td.id, triage(td, lookup, Date.now(), restored.get(td.id) ?? 0, superseded.has(td.id)).section])),
    [todos, lookup, restored, superseded, tick],
  );
  // A sessions push builds a new lookup: what it leaves in the same order stays the same list (and the same groups,
  // the same row views below), so the rows and sections it did not touch skip their render.
  const views = useSteady(useMemo(() => todoRowViews(todos, lookup, interactions, sectionOf), [todos, lookup, interactions, sectionOf]), steadyViews);

  // The filter by due date and the order inside each section (#3740): this browser's, in both groupings
  const [listFilter, setListFilter] = useState<TodoListFilter>(() => {
    const v = pref(FILTER_KEY);
    return isTodoListFilter(v) ? v : "all";
  });
  const [listSort, setListSort] = useState<TodoListSort>(() => {
    const v = pref(SORT_KEY);
    return isTodoListSort(v) ? v : "recent";
  });
  const chooseFilter = (f: TodoListFilter) => {
    setPref(FILTER_KEY, f);
    setListFilter(f);
  };
  const chooseSort = (s: TodoListSort) => {
    setPref(SORT_KEY, s);
    setListSort(s);
  };
  const inFilter = (td: Todo) => matchesListFilter(td, listFilter, tick);
  // Filter by search query
  const matchesSearch = (t: Todo) => {
    const q = search.trim().toLowerCase();
    if (!q) return true;
    return t.title.toLowerCase().includes(q) || (t.notes ?? "").toLowerCase().includes(q);
  };
  // a search and the filter narrow the to-dos first; the sections (or the folders) place what is left
  const filterTodo = (t: Todo) => matchesSearch(t) && inFilter(t);

  const triaged = useMemo(
    () => triageTodos(todos.filter(filterTodo), lookup, Date.now(), restored, { superseded }),
    [todos, lookup, restored, search, listFilter, superseded, tick],
  );
  // the header's counts: what the filter shows, whatever is searched
  const triageAll = useMemo(
    () => (search.trim() ? triageTodos(todos.filter(inFilter), lookup, Date.now(), restored, { superseded }) : triaged),
    [todos, lookup, restored, search, listFilter, superseded, triaged, tick],
  );
  // Where the status sections place each to-do the search and the filter leave: its row's section (a merged row takes
  // its most urgent member's), and whether it counts as open (countsAsOpen, the sidebar badge's rule, its own reading).
  // The folders and 常驻 split by the same placement, so their Done fold is the Done section to the to-do and every
  // grouping says one number for it (audit item 12).
  const placed = useMemo(() => {
    const row = new Map<string, TriageSection>();
    const open = new Set<string>();
    for (const sec of TRIAGE_SECTIONS)
      for (const g of triaged.sections[sec])
        for (const m of g.members) {
          row.set(m.id, sec);
          if (countsAsOpen(m, g.triage.get(m.id)!)) open.add(m.id);
        }
    return { row, open };
  }, [triaged]);
  const filteredUncompleted = useSteady(
    useMemo(() => sortTodos(todos.filter((td) => placed.row.has(td.id) && placed.row.get(td.id) !== "done"), lookup), [todos, placed, lookup]),
    sameList,
  );
  const filteredCompleted = useSteady(
    useMemo(() => sortTodos(todos.filter((td) => placed.row.get(td.id) === "done"), lookup), [todos, placed, lookup]),
    sameList,
  );
  // open todos under the knowledge-base project they are in (#2581), else the one their agents work in (#2526)
  const kbProjects = useTodoProjects();
  const projectName = useMemo(() => projectNames(kbProjects), [kbProjects]);
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
  const directionTree = useDirectionTree();
  const byDirection = groupsByDirection(directionTree);
  const directionPending = chosenGrouping === "direction" && directionTree === undefined;
  const grouping: TodoGrouping = chosenGrouping === "direction" && !byDirection && !directionPending ? "status" : chosenGrouping;
  const byStatus = grouping === "status";
  const leaderTitles = useStore((s) => s.assistant?.conversations);
  // The tree lists only open to-dos (server/internal/assistant/direction_tree.go, the same countsAsOpen rule), so a
  // direction holds the open ones it lists: one ticked here leaves at once, not when the tree's push comes.
  const directionSections = useMemo(() => {
    if (grouping !== "direction" || !byDirection) return [];
    const open = new Map(filteredUncompleted.filter((td) => placed.open.has(td.id)).map((td) => [td.id, td]));
    return directionTree.directions
      .map((d) => ({ d, todos: (d.tasks ?? []).map((task) => open.get(task.id)).filter((td): td is Todo => !!td) }))
      .filter((g) => g.todos.length > 0)
      .map(({ d, todos }) => ({ d, todos, title: directionTitle(d, leaderTitles?.find((c) => c.session_id === d.id)?.title) }));
  }, [grouping, byDirection, directionTree, filteredUncompleted, placed, leaderTitles]);
  // open to-dos no 常驻 section lists: the hub nests a worker's to-do under its leader's Agent instead of listing it
  // (server/internal/assistant/direction_tree.go), so the page says how many and where to see them rather than hide them
  const directionUnlisted = useMemo(() => {
    if (grouping !== "direction" || !byDirection) return 0;
    const listed = new Set(directionSections.flatMap((g) => g.todos.map((td) => td.id)));
    return filteredUncompleted.filter((td) => placed.open.has(td.id) && !listed.has(td.id)).length;
  }, [grouping, byDirection, directionSections, filteredUncompleted, placed]);
  // What 常驻 shows below the directions besides Done: the to-dos outside Done that do not count as open (closed in the
  // last day, accepted, deployed, inactive), which the tree never lists, in their status sections, as Android does
  // (`DirectionTree.todoRows`), so a to-do ticked here moves to Just finished rather than out of the list.
  const directionRest = useMemo(() => {
    if (grouping !== "direction" || !byDirection) return [];
    return TRIAGE_SECTIONS.filter((sec) => sec !== "done")
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
    () => (listFilter === "all" ? 0 : hiddenWaiting(todos.filter(matchesSearch), inFilter, lookup, Date.now(), restored, superseded)),
    [todos, lookup, restored, search, listFilter, superseded, tick],
  );
  const filterCounts = useMemo(() => listFilterCounts(todos.filter(matchesSearch), tick), [todos, search, tick]);
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
  const opened = seenOn && selectedId !== undefined ? todos.find((td) => td.id === selectedId) : undefined;
  const openedJustDone = opened !== undefined && triage(opened, lookup, Date.now(), restored.get(opened.id) ?? 0, superseded.has(opened.id)).section === "just_done";
  useEffect(() => {
    if (openedJustDone && !decided.current.has(selectedId!)) seenOnLeave.current = selectedId;
  }, [openedJustDone, selectedId]);
  useEffect(() => {
    const id = selectedId;
    return () => {
      if (id === undefined || seenOnLeave.current !== id) return;
      seenOnLeave.current = undefined;
      void setTodoSeen(id, true).catch(() => {}); // it stays in Just finished, as if never opened
    };
  }, [selectedId]);

  // Active selected todo
  const activeTodo = useMemo(() => {
    if (activeId) {
      const found = todos.find((t) => t.id === activeId);
      if (found) return found;
    }
    if (byStatus) {
      // the first row of an open section; none rather than one that would unfold a folded section (Shelved, Done)
      for (const sec of TRIAGE_SECTIONS) if (!FOLDED_SECTIONS.has(sec) && triaged.sections[sec].length) return triaged.sections[sec][0].lead;
      return undefined;
    }
    // by 常驻: the first row shown (a direction's, else an unfolded status section's below them), else the first completed one
    if (grouping === "direction") return directionSections[0]?.todos[0] ?? directionRest.find((s) => !FOLDED_SECTIONS.has(s.sec))?.groups[0].lead ?? filteredCompleted[0];
    return filteredUncompleted[0] ?? filteredCompleted[0];
  }, [activeId, todos, filteredUncompleted, filteredCompleted, byStatus, triaged, grouping, directionSections, directionRest]);

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
      navigate(`/todos?id=${encodeURIComponent(id)}`);
    },
    [selectedId],
  );
  const backToList = () => {
    if (selectedId && pushedId.current === selectedId) {
      pushedId.current = undefined;
      history.back();
    } else navigate("/todos", true);
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
    if (id) [...list.querySelectorAll<HTMLElement>("[data-todo]")].find((el) => el.dataset.todo === id)?.focus();
  }, [detailOnNarrow, selectedId]);
  // Esc on a phone's width goes back to the list, as Back does; a dialog or a menu over the page and a field being
  // edited keep it.
  useEffect(() => {
    if (!detailOnNarrow) return;
    const onKey = (e: KeyboardEvent) => {
      const list = listColumn.current;
      if (e.key !== "Escape" || e.defaultPrevented || e.isComposing || !list || list.getClientRects().length) return;
      if (document.querySelector("[role=menu],[role=dialog]") || (e.target as Element | null)?.closest?.("input,textarea,select,[contenteditable=true]")) return;
      e.preventDefault();
      back.current();
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [detailOnNarrow]);

  // `open`: the row's checkbox shows it open (`checksAsOpen`): a failed start waiting on the owner is ticked to close it
  const toggleDone = useCallback(async (todo: Todo, open = !todo.done_at) => {
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
      navigate(calendarOn ? calendarRoute(calendarMode, anchor) : { name: "todos" }, true);
    }
    toast(t("todos.deleted"), "ok");
    // the rest of the page catches up in the background; a failed refresh leaves the list as dropped, the next push fixes it
    void refreshTodos().then(() => refresh()).catch(() => {});
  };

  return (
    <div className="flex h-full flex-col bg-surface">
      {/* Top Header */}
      <header className="flex h-13 shrink-0 items-center justify-between border-b border-line bg-surface px-4 sm:px-6">
        <div className="flex items-center gap-3">
          {onToggleSidebar && <IconButton icon="sidebar" label={t("agents-agentsview.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />}
          <div className="flex items-baseline gap-2">
            <h1 className="text-[16px] font-semibold whitespace-nowrap text-ink">{t("todos.title")}</h1>
            {/* the counts once the list has its to-dos (audit item 83): not while it loads, nor after an empty failed read */}
            {(todosLoaded || todos.length > 0) && !(todos.length === 0 && todosError) && (
              // a phone's header has the List | Calendar switch to fit (#2029): the counts stay on wider screens
              <span className="text-[12px] whitespace-nowrap text-ink-3 max-sm:hidden" data-testid="todos-summary">
                {/* one count in every grouping (audit item 12): open as the sidebar's badge counts it, the rest as the
                    sections' titles do (a merged row's members under its section) */}
                {t(listFilter === "all" ? "todos.summary_triage" : "todos.summary_filtered", { filter: filterLabel(listFilter), open: triageAll.open, parked: sectionSize(triageAll, "parked"), shelved: sectionSize(triageAll, "shelved"), done: sectionSize(triageAll, "done") })}
              </span>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2.5">
          <Segmented
            label={t("todos.view_label")}
            value={calendarOn ? "calendar" : "list"}
            options={[
              { v: "list", label: t("todos.view_list") },
              { v: "calendar", label: t("todos.view_calendar") },
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
              selectedId={selectedId}
              onMode={(m, day) => {
                setPref(MODE_KEY, m);
                navigate(calendarRoute(m, day ?? anchor, selectedId));
              }}
              onAnchor={(day) => navigate(calendarRoute(calendarMode, day, selectedId), true)}
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
                onDeleteClick={() => askDelete(selectedTodo.id)}
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
          className={cx("flex w-full shrink-0 flex-col border-line md:w-[380px] md:border-r lg:w-[400px]", detailOnNarrow && "max-md:hidden")}
        >
          {/* Search Bar */}
          <div className="flex items-center gap-2 p-3">
            <div className="min-w-0 flex-1">
              <SearchField tone="inset" value={search} onChange={setSearch} placeholder={t("todos.search_placeholder")} />
            </div>
            <TodoListControls filter={listFilter} sort={listSort} counts={filterCounts} onFilter={chooseFilter} onSort={chooseSort} />
            <Segmented
              label={t("todos.grouping_label")}
              value={grouping}
              options={[
                { v: "status", label: t("todos.grouping_status") },
                { v: "folder", label: t("todos.grouping_folder") },
                ...(byDirection || directionPending ? [{ v: "direction" as const, label: t("todos.grouping_direction") }] : []),
              ]}
              onChange={(next) => {
                setPref(GROUPING_KEY, next);
                setGrouping(next);
              }}
              compact
            />
          </div>

          {/* List Content */}
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
            ) : filteredUncompleted.length === 0 && filteredCompleted.length === 0 ? (
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
              <div className="flex flex-col gap-1" data-testid="todos-sections">
                {hiddenWaitingCount > 0 && <HiddenWaitingLine count={hiddenWaitingCount} filter={listFilter} onShowAll={() => chooseFilter("all")} />}
                {TRIAGE_SECTIONS.map((sec) =>
                  triaged.sections[sec].length === 0 ? null : (
                    <TodoTriageSection
                      key={sec}
                      section={sec}
                      // unfinished to-dos start open, above Just finished; Shelved and Done start folded (#5707)
                      folded={FOLDED_SECTIONS.has(sec)}
                      groups={sortTriageGroups(triaged.sections[sec], listSort)}
                      count={sectionSize(triaged, sec)}
                      views={views}
                      selectedId={activeTodo?.id}
                      onSelect={openFromList}
                      onToggleDone={toggleDone}
                      onRestore={onRestore}
                      onSeen={seenOn ? onSeen : undefined}
                      onMenu={openRowMenu}
                    />
                  ),
                )}
              </div>
              ) : (
              <div className="flex flex-col gap-1">
                {grouping === "direction" ? (
                  <>
                    {directionSections.map((g) => (
                      <TodoDirectionSection
                        key={g.d.id}
                        direction={g.d}
                        title={g.title}
                        todos={g.todos}
                        views={views}
                        selectedId={activeTodo?.id}
                        onSelect={openFromList}
                        onToggleDone={toggleDone}
                        onMenu={openRowMenu}
                      />
                    ))}
                    {directionUnlisted > 0 && (
                      <p data-testid="todos-direction-unlisted" className="px-2 pt-1 text-[12px] text-ink-3">
                        {t("todos.direction_unlisted", { count: directionUnlisted })}
                      </p>
                    )}
                    {directionRest.map(({ sec, groups }) => (
                      <TodoTriageSection
                        key={sec}
                        section={sec}
                        folded={FOLDED_SECTIONS.has(sec)}
                        groups={groups}
                        count={groups.reduce((n, g) => n + g.members.length, 0)}
                        views={views}
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
                    selectedId={activeTodo?.id}
                    onSelect={openFromList}
                    onToggleDone={toggleDone}
                    onSeen={seenOn ? onSeen : undefined}
                    onMenu={openRowMenu}
                  />
                ))}

                {/* Completed Fold */}
                {filteredCompleted.length > 0 && (
                  <div className="mt-2 border-t border-line/60 pt-2">
                    <button
                      type="button"
                      onClick={() => setShowDone((v) => !v)}
                      className="flex h-8 w-full items-center justify-between rounded-control px-2.5 text-[12px] text-ink-3 transition-colors hover:bg-hover hover:text-ink-2"
                    >
                      <span>{t("todos.done_section", { count: filteredCompleted.length })}</span>
                      <Icon
                        name="chevron"
                        size={12}
                        className={cx("transition-transform duration-200", showDone ? "rotate-180" : "")}
                      />
                    </button>
                    {showDone && (
                      <div className="flex flex-col gap-1 pt-1">
                        {filteredCompleted.map((todo) => (
                          <TodoListItem
                            key={todo.id}
                            todo={todo}
                            view={views.get(todo.id)!}
                            selected={activeTodo?.id === todo.id}
                            onSelect={openFromList}
                            onToggleDone={toggleDone}
                            onMenu={openRowMenu}
                          />
                        ))}
                      </div>
                    )}
                  </div>
                )}
              </div>
              )
            )}
          </TodoListScroller>
        </div>

        {/* Right Column: Todo Detail */}
        <div
          ref={detailColumn}
          data-testid="todos-detail-column"
          data-narrow-shown={detailOnNarrow || undefined}
          className={cx("min-h-0 flex-1 flex-col overflow-y-auto md:flex", detailOnNarrow ? "flex" : "hidden")}
        >
          {detailOnNarrow && (
            <div className="flex shrink-0 items-center px-2 pt-2 md:hidden" data-testid="todos-back">
              <Button tone="quiet" icon="chevronLeft" onClick={backToList}>
                {t("todos.back_to_list")}
              </Button>
            </div>
          )}
          {activeTodo ? (
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
              onDeleteClick={() => askDelete(activeTodo.id)}
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

      {handoverTodo && (
        <HandoverDialog
          key={handoverTodo.id}
          open={handoverOpen}
          todo={handoverTodo}
          machines={machines}
          onClose={() => setHandoverOpen(false)}
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

/** One project's open todos under a folding title with its Needs you / Working / Done (#2526). */
const TodoProjectSection = memo(function TodoProjectSection({
  group,
  views,
  selectedId,
  onSelect,
  onToggleDone,
  onSeen,
  onMenu,
}: {
  group: TodoProjectGroup;
  /** Its todos' row views, in the same order as group.todos. */
  views: readonly TodoRowView[];
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  /** Got it on a row in Just finished (one closed in the last day stays in its folder until it is seen). */
  onSeen?: (id: string) => void;
  onMenu: TodoRowMenuOpener;
}) {
  const holds = group.todos.some((td) => td.id === selectedId);
  const [folded, toggle] = useFolded(group.key, group.folded, { follow: true, holds });
  return (
    <div className="mt-1 first:mt-0" data-testid="todo-group" data-group={group.key}>
      <button type="button" aria-expanded={!folded} onClick={toggle} className="flex h-7 w-full min-w-0 items-center gap-1.5 rounded-control px-2 text-left text-[12px] text-ink-2 hover:bg-hover">
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        <Icon name="folder" size={12} stroke={2} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate font-semibold">{group.name}</span>
        <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{group.todos.length}</span>
        {!(group.area && group.state === "done") && (
          <span className={cx("shrink-0 text-[11px] font-semibold whitespace-nowrap", TREE_TONE[group.state])}>{treeStateLabel(group.state)}</span>
        )}
      </button>
      {!folded && (
        <div className="flex flex-col gap-1 pl-2 pt-0.5">
          {group.todos.map((todo, i) => (
            <TodoListItem
              key={todo.id}
              todo={todo}
              view={views[i]}
              selected={selectedId === todo.id}
              onSelect={onSelect}
              onToggleDone={onToggleDone}
              onSeen={onSeen}
              onMenu={onMenu}
            />
          ))}
        </div>
      )}
    </div>
  );
});

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
  selectedId,
  onSelect,
  onToggleDone,
  onMenu,
}: {
  direction: Direction;
  title: string;
  todos: readonly Todo[];
  views: ReadonlyMap<string, TodoRowView>;
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  onMenu: TodoRowMenuOpener;
}) {
  const holds = todos.some((td) => td.id === selectedId);
  const [folded, toggle] = useFolded(`todos-direction:${direction.id}`, false, { holds });
  return (
    <div className="mt-1 first:mt-0" data-testid="todo-group" data-group={`direction:${direction.secretary ? "secretary" : direction.id}`}>
      <button type="button" aria-expanded={!folded} onClick={toggle} className="flex h-7 w-full min-w-0 items-center gap-1.5 rounded-control px-2 text-left text-[12px] text-ink-2 hover:bg-hover [@media(pointer:coarse)]:min-h-11">
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        <Icon name={direction.secretary ? "person" : "target"} size={12} stroke={2} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate font-semibold">{title}</span>
        <DirectionCountsText direction={direction} />
      </button>
      {!folded && (
        <div className="flex flex-col gap-1 pl-2 pt-0.5">
          {todos.map((todo) => (
            <TodoListItem key={todo.id} todo={todo} view={views.get(todo.id)!} selected={selectedId === todo.id} onSelect={onSelect} onToggleDone={onToggleDone} onMenu={onMenu} />
          ))}
        </div>
      )}
    </div>
  );
});

// ── Status sections (#3156) ──────────────────────────────────

const SECTION_TONE: Record<TriageSection, string> = {
  waiting: "text-attention",
  working: "text-orange",
  parked: "text-ink-2",
  just_done: "text-green",
  pr: "text-accent-ink",
  other: "text-ink-2",
  shelved: "text-ink-3",
  done: "text-ink-3",
};

/** A section's title: the two that are agent states take the state names (lib/state-labels.ts). */
function sectionTitle(section: TriageSection): string {
  if (section === "waiting") return stateName("needs_you");
  if (section === "working") return stateName("working");
  return t(`todos.section_${section}`);
}

/** One status section (docs/assistant/todo-status-sop.md): a folding title with its count, a row per group of related
 *  to-dos. Every section starts open but Shelved and Done (#5707). */
const TodoTriageSection = memo(function TodoTriageSection({
  section,
  folded: foldedFirst,
  groups,
  count,
  views,
  selectedId,
  onSelect,
  onToggleDone,
  onRestore,
  onSeen,
  onMenu,
}: {
  section: TriageSection;
  /** Whether it starts folded: Shelved and Done. */
  folded: boolean;
  groups: TriageGroup[];
  /** To-dos its rows list (a merged row holds several): the number its title shows (#3712, the same on iOS). */
  count: number;
  views: ReadonlyMap<string, TodoRowView>;
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  onRestore: (id: string) => void;
  /** Got it on a Just finished row; unset while the hub keeps no seen marks. */
  onSeen?: (id: string) => void;
  onMenu: TodoRowMenuOpener;
}) {
  const holds = groups.some((g) => g.members.some((m) => m.id === selectedId));
  const [folded, toggle] = useFolded(`todos-section:${section}`, foldedFirst, { follow: true, holds });
  return (
    <section className="mt-1 first:mt-0" data-testid="todo-section" data-section={section} data-count={groups.length} data-todos={count}>
      <button type="button" aria-expanded={!folded} onClick={toggle} className="flex h-8 w-full min-w-0 items-center gap-1.5 rounded-control px-2 text-left text-[12px] hover:bg-hover">
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 text-ink-3 opacity-60 transition-transform" style={{ transform: folded ? "rotate(-90deg)" : "none" }} />
        <span className={cx("min-w-0 flex-1 truncate font-semibold whitespace-nowrap", SECTION_TONE[section])}>{sectionTitle(section)}</span>
        <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{count}</span>
      </button>
      {!folded && section === "shelved" && <p className="px-2.5 pb-1 text-[11.5px] text-ink-3">{t("todos.shelved_hint")}</p>}
      {!folded && (
        <div className="flex flex-col gap-1 pt-0.5">
          {groups.map((g) => (
            <TodoTriageRow key={g.key} group={g} views={views} selectedId={selectedId} onSelect={onSelect} onToggleDone={onToggleDone} onRestore={onRestore} onSeen={onSeen} onMenu={onMenu} />
          ))}
        </div>
      )}
    </section>
  );
});

/** A section's row: the group's lead, and its related to-dos under "N related" (same PR, issue, title or plan). */
function TodoTriageRow({
  group,
  views,
  selectedId,
  onSelect,
  onToggleDone,
  onRestore,
  onSeen,
  onMenu,
}: {
  group: TriageGroup;
  views: ReadonlyMap<string, TodoRowView>;
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  onRestore: (id: string) => void;
  onSeen?: (id: string) => void;
  onMenu: TodoRowMenuOpener;
}) {
  const others = group.members.slice(1);
  const [opened, setOpen] = useState(false);
  // a selected member always shows, however it was selected (a click, the address, a push)
  const open = opened || others.some((m) => m.id === selectedId);
  const item = (td: Todo) => (
    <TodoListItem
      key={td.id}
      todo={td}
      view={views.get(td.id)!}
      selected={selectedId === td.id}
      onSelect={onSelect}
      onToggleDone={onToggleDone}
      section={group.triage.get(td.id)?.section}
      onRestore={onRestore}
      onSeen={onSeen}
      onMenu={onMenu}
    />
  );
  return (
    <div data-testid="todo-triage-row" data-id={group.lead.id} data-members={group.members.length}>
      {item(group.lead)}
      {others.length > 0 && (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="ml-9 flex h-7 items-center gap-1 rounded-control px-1.5 text-[11.5px] whitespace-nowrap text-ink-3 hover:bg-hover hover:text-ink-2"
        >
          <Icon name="chevron" size={10} stroke={2.4} className="transition-transform" style={{ transform: open ? "none" : "rotate(-90deg)" }} />
          {t("todos.related", { count: others.length })}
        </button>
      )}
      {open && <div className="ml-6 flex flex-col gap-1 border-l border-line pl-2">{others.map(item)}</div>}
    </div>
  );
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
  const view: TodoRowView = { status, dotState: DOT_STATE[shownStatus(todo, status)], age: lastMoved(todo, lookup), activeCount, urgentLine: false, urgentLead: "", urgentText: "", orphaned: false, ultracode: todoUltracode(todo, lookup) };
  if (todo.done_at || !urgent?.session) return view;
  const limit = status === "usage_limit" ? usageLimitLabel(urgent.session) : undefined;
  if (limit) {
    view.urgentLine = true;
    view.urgentText = limit;
    return view;
  }
  const urgentNow = taskNow(urgent.session, foldedState(urgent.session) === "working");
  // its own card, else one of its child sessions' (#5120: their cards wait on its page)
  const urgentIx = sessionCard(interactions, urgent.agent.machine_id, urgent.agent.session_id);
  // what it waits on, from its card (#4111): the question, the command; not its last words
  const urgentAsk = urgent.status === "needs_you" ? askText(urgentIx) : "";
  if (!urgentAsk && !urgentNow) return view;
  const kicker = urgentIx?.kind === "plan" ? t("todos.kicker_plan") : urgentIx?.kind === "permission" ? t("todos.kicker_permission") : urgentIx?.kind === "screen" ? t("todos.kicker_screen") : urgentIx?.kind === "elicitation" ? t("todos.kicker_elicitation") : t("todos.kicker_question");
  view.urgentLine = true;
  if (urgent.status === "needs_you") view.urgentLead = t("todos.urgent_line", { name: urgent.agent.title || urgent.session.title || "Agent", kicker });
  view.urgentText = urgentAsk || (urgentNow ? (urgentNow.kind === "words" ? plainMarkdown(urgentNow.text) : urgentNow.text) : "");
  return view;
}

/** Every todo's row view by id, with its status section. */
function todoRowViews(todos: Todo[], lookup: SessionLookup, interactions: Interaction[], sections: ReadonlyMap<string, TriageSection>): ReadonlyMap<string, TodoRowView> {
  return new Map(todos.map((td) => [td.id, { ...todoRowView(td, lookup, interactions), section: sections.get(td.id) }]));
}

const TONE_CLASS = TODO_TONE_CLASS;

/** Opens a row's menu (#4429) where `at` says, for the to-do the row's checkbox shows open (`open`) or done. */
type TodoRowMenuOpener = (todo: Todo, at: MenuAt, open: boolean) => void;

const TodoListItem = memo(function TodoListItem({
  todo,
  view,
  selected,
  onSelect,
  onToggleDone,
  section,
  onRestore,
  onSeen,
  onMenu,
}: {
  todo: Todo;
  view: TodoRowView;
  selected: boolean;
  onSelect: (id: string) => void;
  onToggleDone: (todo: Todo, open?: boolean) => void;
  /** Its status section (#3156): a PR to merge shows its link, a shelved one its Restore, a Just finished one Got it. */
  section?: TriageSection;
  onRestore?: (id: string) => void;
  /** Got it (#5707), on a row in Just finished (`section`, else the view's). */
  onSeen?: (id: string) => void;
  onMenu?: TodoRowMenuOpener;
}) {
  const { status, dotState, activeCount } = view;
  const pr = section === "pr" ? prLink(todo.pr_url) : null;
  const toneClass = TONE_CLASS[TODO_STATUS_INFO[shownStatus(todo, status)].tone];
  const held = heldDelivery(todo);
  const thread = useStore((s) => todoThreadsOn(s.features)) ? boundThread(todo) : undefined;
  // a failed start still waiting on the owner reads open, not done, with why it did not start (#3713), in every grouping
  const open = checksAsOpen(todo, section ?? view.section);
  const failedOpen = open && Boolean(todo.start_error);
  // Its menu (#4429): a right click opens it at the pointer, the menu key and Shift+F10 on the row
  const menu = onMenu ? contextMenuProps((at) => onMenu(todo, at, open)) : undefined;

  return (
    <div
      role="button"
      tabIndex={0}
      data-todo={todo.id}
      aria-current={selected || undefined}
      onClick={() => onSelect(todo.id)}
      // a right click on its PR link keeps the browser's menu (Copy link, open in a new tab)
      onContextMenu={menu && ((e) => {
        if (!(e.target as HTMLElement).closest("a[href]")) menu.onContextMenu(e);
      })}
      // Enter / Space on the row itself (not on its own buttons) opens it, as a click does
      onKeyDown={(e) => {
        menu?.onKeyDown(e);
        if (e.defaultPrevented) return;
        if (e.target !== e.currentTarget || (e.key !== "Enter" && e.key !== " ")) return;
        e.preventDefault();
        onSelect(todo.id);
      }}
      className={cx(
        "group/item flex cursor-pointer gap-2.5 rounded-control p-2.5 transition-colors focus-visible:outline-2 focus-visible:outline-accent",
        selected ? "bg-accent-tint/70" : "hover:bg-hover",
      )}
    >
      {/* Checkbox button: touch-hit makes the 16px circle a 44px target on touch, its own (no row underneath) */}
      <button
        type="button"
        aria-label={open ? t("todos.complete_todo") : t("todos.reopen_todo")}
        onClick={(e) => {
          e.stopPropagation();
          onToggleDone(todo, open);
        }}
        className={cx(
          "touch-hit mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border transition-colors",
          !open
            ? "border-accent bg-accent text-white"
            : "border-line-strong hover:border-accent hover:text-accent text-transparent",
        )}
      >
        <Icon name="check" size={10} stroke={2.6} />
      </button>

      {/* Body */}
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline justify-between gap-2">
          {view.ultracode ? (
            <span className="flex min-w-0 items-center gap-1">
              <span className="truncate text-[13.5px] font-semibold text-ink" data-testid="todo-title">{todo.title}</span>
              <UltraMark size={11} />
            </span>
          ) : (
            <span className={cx("truncate text-[13.5px] font-semibold text-ink", !open && "line-through text-ink-3 font-normal")} data-testid="todo-title">
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

        {/* Status Line */}
        <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[12px]" data-testid="todo-status-line">
          {open && <StatusMark state={dotState} size="sm" />}
          <span className={cx("shrink-0 whitespace-nowrap font-medium", toneClass)}>{todoStatusLabel(todo, status)}</span>
          {!todo.done_at && activeCount > 0 && (
            <>
              <span className="text-ink-3">·</span>
              <span className="text-ink-3">{t("todos.agent_count", { count: activeCount })}</span>
            </>
          )}
          {!todo.done_at && activeCount === 0 && (
            <span className="truncate text-ink-3">· {t("todos.not_handed")}</span>
          )}
          {(section ?? view.section) === "just_done" && onSeen ? (
            <span className="ml-auto flex shrink-0 items-center gap-1.5">
              {!todo.done_at && <ReviewBadge todo={todo} />}
              <SeenButton onClick={() => onSeen(todo.id)} />
            </span>
          ) : (
            !todo.done_at && <ReviewBadge todo={todo} className="ml-auto" />
          )}
        </div>

        {/* What it waits on from the user, with the one tap that answers it (#2056) */}
        <TodoAskLine todo={todo} className="mt-1" />

        {/* Due date, a reminder still to push, high priority (#1683) */}
        <TodoPlanLine todo={todo} />

        {/* Why its agent did not start, while it waits on the owner (DESIGN_SYSTEM 5.6: what failed, in red) */}
        {failedOpen && <div className="mt-1 truncate text-[11.5px] text-red" data-testid="todo-start-failed">{t("todos.start_failed", { error: todo.start_error ?? "" })}</div>}

        {/* Why the hub closed it */}
        {isAutoDone(todo) && todo.done_reason && (
          <div className="mt-1 truncate text-[11.5px] text-ink-3">{todo.done_reason}</div>
        )}

        {/* Where its finished work is held short of live */}
        {held && (
          <div className="mt-1 truncate text-[11.5px] text-ink-3">
            <span className="font-medium text-orange">{t(`todos.delivery_${held.state}`)}</span>
            {held.gate && <> · {t("todos.delivery_gate", { gate: held.gate })}</>}
            {held.unblock && ["user", "agent", "other"].includes(held.unblock) && <> · {t(`todos.unblock_${held.unblock}`)}</>}
          </div>
        )}

        {/* Most Urgent Agent Line */}
        {view.urgentLine && (
          <div className="mt-1 truncate text-[11.5px] text-ink-3" data-orphaned={view.orphaned || undefined}>
            {view.urgentLead && <span className={cx("font-medium", view.orphaned ? "text-orange" : "text-attention")}>{view.urgentLead}</span>}
            {view.urgentText}
          </div>
        )}

        {/* A PR to merge: its link and how long it has not moved (#3156) */}
        {pr && (
          <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-3" data-testid="todo-pr-line">
            <a
              href={pr.url}
              target="_blank"
              rel="noreferrer noopener"
              aria-label={pr.label}
              onClick={(e) => e.stopPropagation()}
              className="inline-flex shrink-0 items-center gap-1 font-medium whitespace-nowrap text-accent-ink hover:underline"
            >
              <Icon name="pullRequest" size={11} />
              {pr.text}
            </a>
            <span className="truncate">· {t("todos.pr_idle", { age: view.age })}</span>
          </div>
        )}

        {/* Shelved: one tap brings it back (#3156); nothing is deleted */}
        {section === "shelved" && onRestore && (
          <div className="mt-1.5" onClick={(e) => e.stopPropagation()}>
            <Button tone="secondary" icon="undo" onClick={() => onRestore(todo.id)} className="h-7 text-[12px] whitespace-nowrap">
              {t("todos.restore")}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
});

/** Got it on a Just finished row (#5707): files it under Done at once. A small capsule at the end of the status line,
 *  under the time (#6578), so the row is as tall as any other: its 20 px hang over the 18 px line (the negative margin
 *  keeps them out of the line's height), and `touch-hit` gives a finger 44 px. Its press stays its own and never opens
 *  the row. */
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
function localParts(ms: number): { day: string; time: string } {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return { day: `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`, time: `${p(d.getHours())}:${p(d.getMinutes())}` };
}

/**
 * A date or time field of the detail's plan (audit item 2): it saves when the editing is done, not on each typed
 * digit. A day typed segment by segment is a valid date after its first digit, so saving then stored a day the user
 * was still typing and, with the field disabled while it saved, took the focus away before the next digit. Typed
 * changes save when the field is left or on Enter; a change made without typing (a pick in the browser's picker) saves
 * at once. What was typed shows until the save is through.
 */
function PlanField({ value, onCommit, ...field }: { value: string; onCommit: (value: string) => Promise<void> } & Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">) {
  const [draft, setDraft] = useState<string>();
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
    }
    setDraft((d) => (d === next ? undefined : d));
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
function TodoPlanEditor({ todo }: { todo: Todo }) {
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
  const due = Number.isNaN(at) ? { day: todoDueDay(todo) === undefined ? "" : (todo.due_date ?? ""), time: "" } : localParts(at);
  const remindAt = todo.remind_at ? Date.parse(todo.remind_at) : NaN;
  const remind = Number.isNaN(remindAt) ? "" : `${localParts(remindAt).day}T${localParts(remindAt).time}`;
  const moment = (day: string, time: string) => new Date(`${day}T${time}`).toISOString();
  return (
    <div className="mt-4 flex flex-wrap items-center gap-x-5 gap-y-2.5 text-[12.5px] text-ink-2" data-testid="todo-plan" aria-busy={busy}>
      <span className="inline-flex items-center gap-1.5">
        <Icon name="calendar" size={13} stroke={2} />
        <span className="whitespace-nowrap">{t("todos.due_label")}</span>
        <PlanField
          type="date"
          aria-label={t("todos.due_label")}
          value={due.day}
          onCommit={(day) => save(!day ? { due_at: null } : due.time ? { due_at: moment(day, due.time) } : { due_date: day })}
          data-testid="todo-plan-due"
        />
        {due.day && (
          <PlanField
            type="time"
            aria-label={t("todos.due_time")}
            value={due.time}
            onCommit={(time) => save(time ? { due_at: moment(due.day, time) } : { due_date: due.day })}
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
          onCommit={(v) => save({ remind_at: v ? new Date(v).toISOString() : null })}
          data-testid="todo-plan-remind"
        />
      </span>
      <span className="inline-flex items-center gap-1.5">
        <Icon name="flag" size={13} stroke={2} />
        <span className="whitespace-nowrap">{t("todos.priority_label")}</span>
        <select
          aria-label={t("todos.priority_label")}
          value={todo.priority ?? ""}
          onChange={(e) => void save({ priority: e.target.value })}
          className={PLAN_FIELD}
          data-testid="todo-plan-priority"
        >
          <option value="high">{t("todos.priority_option_high")}</option>
          <option value="">{t("todos.priority_normal")}</option>
          <option value="low">{t("todos.priority_low")}</option>
        </select>
      </span>
      <span className="inline-flex min-w-0 items-center gap-1.5">
        <Icon name="folder" size={13} stroke={2} />
        <span className="whitespace-nowrap">{t("todos.project_label")}</span>
        <select
          aria-label={t("todos.project_label")}
          value={todo.project ?? ""}
          onChange={(e) => void save({ project: e.target.value })}
          className={cx(PLAN_FIELD, "max-w-56 truncate")}
          data-testid="todo-plan-project"
        >
          <option value="">{t("todos.project_none")}</option>
          {projects.map((p) => (
            <option key={p.slug} value={p.slug}>
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
const TaskBriefPanel = lazy(() => import("./assistant/TaskBrief").then((m) => ({ default: m.TaskBriefPanel })));
const TaskBriefHistory = lazy(() => import("./assistant/TaskBrief").then((m) => ({ default: m.TaskBriefHistory })));
const BriefResultExtras = lazy(() => import("./assistant/TaskBrief").then((m) => ({ default: m.BriefResultExtras })));

// The existing review UI lives with this lazy page, not the first screen's card badges.
const MARK: Record<CheckMark, { icon: "check" | "x" | "clock" | "minus"; className: string; label: () => string }> = {
  met: { icon: "check", className: "text-green", label: () => t("assistant-tasks.review_met") },
  missing: { icon: "x", className: "text-red", label: () => t("assistant-tasks.review_missing") },
  open: { icon: "clock", className: "text-ink-3", label: () => t("assistant-tasks.review_open") },
  // added after the acceptance, which still stands (#3156): nothing waits on it
  unchecked: { icon: "minus", className: "text-ink-3", label: () => t("assistant-tasks.review_unchecked") },
};

/** The task's criteria, each as its latest report or review found it, and what the supervisor said last. */
export function ReviewChecklist({ todo }: { todo: Todo }) {
  const sup = todo.supervision;
  if (!sup) return null;
  const list = checklist(sup);
  const review = latestReview(sup);
  const parked = parkedLine(todo);
  // What this list cannot say of finished work (#3822): the criteria a scope change dropped, and what was done beyond the brief.
  const extras = todo.done_at ? resultExtras(todo) : undefined;
  return (
    <section data-testid="review-checklist" className="mt-4 rounded-control bg-inset p-3.5 shadow-hairline">
      <div className="flex items-center gap-2">
        <span className="text-[12.5px] font-semibold text-ink-2">{t("assistant-tasks.review_title")}</span>
        <ReviewBadge todo={todo} />
        <span className="flex-1" />
        {sup.expect_minutes ? <span className="text-[12px] text-ink-3">{t("assistant-tasks.review_expect", { minutes: sup.expect_minutes })}</span> : null}
      </div>
      {list.length === 0 ? (
        <p className="mt-2 text-[12.5px] text-ink-3">{t("assistant-tasks.review_none")}</p>
      ) : (
        <ul className="mt-2 flex flex-col gap-1.5">
          {list.map((c) => {
            const m = MARK[c.mark];
            return (
              <li key={c.criterion} className="flex items-start gap-2 text-[13px]">
                <span role="img" aria-label={m.label()} title={m.label()} className={cx("mt-0.5 shrink-0", m.className)}>
                  <Icon name={m.icon} size={13} stroke={2.4} />
                </span>
                <span className="flex min-w-0 flex-col">
                  <span className="text-ink">{c.criterion}</span>
                  {c.evidence && <span className="text-[12px] text-ink-3 select-text">{c.evidence}</span>}
                  {(c.at || c.by) && (
                    <span className="flex flex-wrap gap-x-1.5 text-[11px] text-ink-3">
                      {c.by && <span>{c.by}</span>}
                      {c.at && <time dateTime={c.at} title={c.at}>{new Date(c.at).toLocaleString()}</time>}
                    </span>
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {extras && (
        <Suspense fallback={null}>
          <BriefResultExtras dropped={extras.dropped} alsoDone={extras.alsoDone} />
        </Suspense>
      )}
      {parked && <p data-testid="review-parked" className="mt-2 text-[12.5px] leading-[1.6] text-ink-2 select-text">{parked}</p>}
      {review?.note && <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-2 select-text">{t("assistant-tasks.review_note", { note: review.note })}</p>}
      <AutoReviewResult sup={sup} />
    </section>
  );
}

/** The independent reviewer's result (#2173) under the criteria: a line, and its report on a click. Nothing when no
 *  review ran, so a task without one looks as it did. */
function AutoReviewResult({ sup }: { sup: Supervision }) {
  const [open, setOpen] = useState(false);
  const line = autoReviewLine(sup);
  if (!line) return null;
  const findings = sup.auto_review?.findings;
  return (
    <div data-testid="auto-review" className="mt-2 flex flex-col gap-1 text-[12.5px] text-ink-2">
      <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5">
        <Icon name="target" size={12} className="shrink-0" />
        <span className="shrink-0 whitespace-nowrap font-semibold">{t("assistant-tasks.auto_review_title")}</span>
        <span className="min-w-0">· {line}</span>
        {findings && (
          <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="touch-hit ml-auto shrink-0 whitespace-nowrap text-ink-3 hover:text-ink-2">
            {t(open ? "assistant-tasks.auto_review_hide" : "assistant-tasks.auto_review_show")}
          </button>
        )}
      </div>
      {open && findings && (
        <div data-testid="auto-review-findings" className="leading-[1.55] whitespace-pre-wrap text-ink select-text [overflow-wrap:anywhere]">
          {findings}
        </div>
      )}
    </div>
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
  onDeleteClick,
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
  onDeleteClick: () => void;
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

  const hasLiveAgent = (mid: string, spaceName: string) => {
    return allSessions.some(
      (s) =>
        s.machine_id === mid &&
        (s.space_name || s.space_id) === spaceName &&
        stateOf(s) !== "offline" &&
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
  // did not start; Retry when the hub can start it again from its plan (POST …/retry), else Hand to agent.
  const failedStart = open && Boolean(todo.start_error);
  const canRetry = failedStart && Boolean(todo.plan_id && todo.origin);
  // the main chat's task card's Retry (lib/feed retryTask): one on its way at a time from either, and the to-do the hub
  // reopens shows at once; its agent starts in the background and the list says how the start goes
  const retrying = useFeed((s) => Boolean(s.busy[retryKey(todo.id)]));
  const retry = () => retryTask(todo).catch((e) => toast(t("todos.action_failed", { error: errorText(e) }), "error"));
  const status = todoStatus(todo, lookup);
  const statusInfo = TODO_STATUS_INFO[shownStatus(todo, status)];
  const toneClass = {
    red: "bg-red-tint text-red",
    green: "bg-green-tint text-green",
    amber: status === "needs_you" ? "bg-field text-attention" : "bg-orange-tint text-orange",
    idle: "bg-hover text-ink-2",
    off: "bg-hover text-ink-3",
  }[statusInfo.tone];

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

  return (
    // a page's reading width (DESIGN_SYSTEM 2.5 "Widths", as the inbox's detail): a wide window centers it (audit item 75)
    <div className="mx-auto flex min-h-full w-full max-w-[760px] flex-col p-6 sm:p-8" data-testid="todo-detail">
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

      {/* Meta Line */}
      <div className="mt-2.5 flex flex-wrap items-center gap-2.5 text-[12.5px] text-ink-2">
        <span className={cx("inline-flex h-5.5 items-center gap-1.5 rounded-md px-2 text-[12px] font-semibold", toneClass)}>
          <StatusMark state={dotState} size="sm" />
          {todoStatusLabel(todo, status)}
        </span>
        <span className="text-ink-3">·</span>
        <span>{t("todos.agent_count", { count: activeAgents.length })}</span>
        <span className="text-ink-3">·</span>
        <span>{t("todos.created_at", { time: clock(todo.created_at) })}</span>
      </div>
      {failedStart && (
        <p className="mt-2 text-meta leading-[1.6] text-red" data-testid="todo-detail-start-failed">
          {t("todos.start_failed", { error: todo.start_error ?? "" })}
        </p>
      )}
      {isAutoDone(todo) && (
        <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-3">
          {todo.done_reason ? t("todos.auto_done_reason", { reason: todo.done_reason }) : t("todos.auto_done")}
        </p>
      )}

      {/* What it waits on from the user and how to answer it (#2056) */}
      <TodoAskPanel todo={todo} />
      <HeldForBriefing todo={todo} />

      {/* Due date, reminder, priority (#1683) */}
      <TodoPlanEditor todo={todo} />
      {/* The brief its work is held to (#3822): who asked and in what words above the criteria; the scope changes
          and who worked on it under them. */}
      {todo.brief && (
        <Suspense fallback={null}>
          <TaskBriefPanel todo={todo} />
        </Suspense>
      )}
      <ReviewChecklist todo={todo} />
      {todo.brief && (
        <Suspense fallback={null}>
          <TaskBriefHistory todo={todo} />
        </Suspense>
      )}
      <LatestReport todo={todo} />

      {/* Code task stages from the hub; general tasks show only their PR/MR link, when present. */}
      <TodoStageBar stage={todo.stage} stageKind={todo.stage_kind} prURL={todo.pr_url} className="mt-4" />

      {todo.origin && <TaskCompletionCheck todo={todo} />}

      {/* Notes / Description */}
      <div className="mt-4">
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

      <ThreadSection todo={todo} />

      <DecisionsSection todo={todo} lookup={lookup} />

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
            {open ? t("todos.no_agents") : t("todos.no_agents_done")}
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {activeAgents.map((agent) => {
              const session = lookup(agent.machine_id, agent.session_id);
              const st = session ? foldedState(session) : "offline";
              const now = taskNow(session, st === "working");
              const isExpired = !session && !agent.start_error;
              const displayTitle = session?.title || agent.title || t("lib-format.untitled_session");

              // computer · space › tab (the computer's name is its display name in machines)
              const mName = machines.find((m) => m.id === agent.machine_id)?.name || agent.machine_id;
              const spaceTab = session ? [session.space_name, tabLabel(session)].filter(Boolean).join(" › ") : "";
              const displaySub = spaceTab
                ? `${mName} · ${spaceTab}`
                : `${mName} · ${shortPath(session?.cwd || agent.cwd)}`;

              const ix = sessionCard(interactions, agent.machine_id, agent.session_id);
              const kicker = ix?.kind === "plan" ? t("todos.kicker_plan") : ix?.kind === "permission" ? t("todos.kicker_permission") : ix?.kind === "screen" ? t("todos.kicker_screen") : ix?.kind === "elicitation" ? t("todos.kicker_elicitation") : t("todos.kicker_question");
              const sourceLabel = formatAgentSource(agent, todo.spaces, lookup);

              return (
                <div
                  key={`${agent.machine_id}/${agent.session_id}`}
                  onClick={() => {
                    if (session) {
                      navigate({ name: "session", mid: agent.machine_id, sid: agent.session_id });
                    }
                  }}
                  className={cx(
                    "flex items-start gap-3 rounded-control border border-line bg-canvas p-3 transition-colors",
                    session ? "cursor-pointer hover:bg-hover" : "",
                  )}
                >
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
              onClick={onToggleReleased}
              className="flex items-center gap-1 text-[12px] text-ink-3 hover:text-ink-2"
            >
              <Icon
                name="chevron"
                size={11}
                className={cx("transition-transform duration-200", showReleased ? "rotate-180" : "")}
              />
              <span>{t("todos.released_section", { count: releasedAgents.length })}</span>
            </button>
            {showReleased && (
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

      {/* Bottom Action Bar. A done to-do can't be handed over (the hub refuses, #1856): Reopen next to it comes first. A
          failed start is open: Retry (or Hand to agent, which reopens it first) and Complete, which closes it for good as
          its row's checkbox does. */}
      <div className="mt-8 flex flex-wrap items-center gap-2.5 border-t border-line pt-4">
        {canRetry && (
          <Button
            tone="accent"
            icon="retry"
            busy={retrying}
            disabled={retrying}
            onClick={() => void retry()}
            className="h-8.5 px-3.5 text-meta whitespace-nowrap"
          >
            {t("todos.retry")}
          </Button>
        )}
        {open && (
          <Button tone={canRetry ? "secondary" : "accent"} icon="hand" onClick={onOpenHandover} className="h-8.5 px-3.5 text-[13px] whitespace-nowrap">
            {t("todos.hand_over")}
          </Button>
        )}
        <Button tone="secondary" icon="check" onClick={() => onToggleDone(open)} className="h-8.5 px-3 text-[13px] whitespace-nowrap">
          {open ? t("todos.complete") : t("todos.reopen")}
        </Button>
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
function ThreadSection({ todo }: { todo: Todo }) {
  const thread = boundThread(todo);
  const threadsOn = useStore((s) => todoThreadsOn(s.features));
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [unsupported, setUnsupported] = useState(false);

  useEffect(() => {
    setConfirm(false);
    setBusy(false);
  }, [todo.id]);

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
      {thread && (
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
      )}
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
function DecisionsSection({ todo, lookup }: { todo: Todo; lookup: ReturnType<typeof makeSessionLookup> }) {
  const [all, setAll] = useState<TodoDecision[] | null>(null);
  const [failed, setFailed] = useState("");
  const [unsupported, setUnsupported] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const [view, setView] = useState<DecisionsView>("first");
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
    setForm(null);
  }, [todo.id]);

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

      {failed && all === null ? (
        <div className="flex items-center justify-between gap-3 rounded-control border border-dashed border-line p-4 text-[12.5px] text-ink-3">
          <span className="min-w-0 truncate">{t("todos.decisions_failed", { error: failed })}</span>
          <Button tone="quiet" className="shrink-0 whitespace-nowrap px-2 py-1 text-[12px]" onClick={() => void load()}>
            {t("todos.retry")}
          </Button>
        </div>
      ) : all === null || folded ? null : shown.length === 0 ? (
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
                      <span>{clock(d.created_at)}</span>
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
      {!folded && listed.length > DECISIONS_FIRST && (
        <Button tone="quiet" testId="todo-decisions-more" expanded={view === "all"} className="mt-1.5 whitespace-nowrap px-2 py-1 text-xs" onClick={() => setView(view === "first" ? "all" : "first")}>
          {view === "first" ? t("todos.decisions_show_all", { count: listed.length }) : t("todos.decisions_show_first", { count: DECISIONS_FIRST })}
        </Button>
      )}

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
          setView("all");
          await load();
        }}
      />
    </div>
  );
}

/** ⌘↩ / Ctrl+↩ anywhere in a dialog's form submits it (audit item 70), as New Agent, Compose and the inbox's hand-off do
 *  (lib/shortcuts.ts keeps Mod+Enter for dialogs); a multi-line field keeps Enter for its new lines. The form's own
 *  submit decides, so one busy or incomplete stays as it is. */
function submitOnModEnter(e: React.KeyboardEvent<HTMLFormElement>) {
  if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey) || composing(e.nativeEvent)) return;
  e.preventDefault();
  e.currentTarget.requestSubmit();
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
  slot,
  onClose,
  onCreated,
}: {
  open: boolean;
  /** Made on the calendar (#2029): due there, a moment or a whole day; the calendar stays in front once it is saved. */
  slot?: CalendarSlot;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const [title, setTitle] = useState("");
  const [notes, setNotes] = useState("");
  const [busy, setBusy] = useState(false);
  const [dueDay, setDueDay] = useState("");
  const [dueTime, setDueTime] = useState("");
  const [allDay, setAllDay] = useState(true);

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
    if (!title.trim() || busy) return;
    setBusy(true);
    try {
      const todo = await createTodo(title, notes, due());
      await refreshTodos();
      await refresh();
      toast(t("todos.created"), "ok");
      onClose();
      if (slot) return; // the calendar stays, the new one on it
      onCreated(todo.id);
      navigate(`/todos?id=${encodeURIComponent(todo.id)}`);
    } catch (err) {
      toast(t("todos.create_failed", { error: errorText(err) }), "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title={t("todos.new_todo")}>
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

        <div className="mt-2 flex justify-end gap-2">
          <Button tone="secondary" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="accent" type="submit" disabled={!title.trim() || busy} busy={busy}>
            {t("todos.create")}
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

  // For "new":
  const onlineMachines = useMemo(() => machines.filter((m) => m.online), [machines]);
  const [mid, setMid] = useState(() => onlineMachines[0]?.id ?? "");
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
    if (onlineMachines[0] && (!mid || !onlineMachines.some((m) => m.id === mid))) {
      setMid(onlineMachines[0].id);
    }
    const first = eligibleSessions[0];
    setSelectedKey((key) => opening || !eligibleSessions.some((s) => `${s.machine_id}/${s.session_id}` === key)
      ? first ? `${first.machine_id}/${first.session_id}` : ""
      : key);
  }, [open, onlineMachines, eligibleSessions]);

  useEffect(() => {
    if (!open || !mid) return;
    const controller = new AbortController();
    const owner = credentialOwner();
    // another computer's list is not this one's
    setLaunchers([]);
    setLaunchersOf("");
    setHubDefault(undefined);
    setLauncher("");
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
    if (!mid) return;
    setBusy(true);
    try {
      await reopenFailed();
      const fullText = [previewCard, prompt.trim()].filter(Boolean).join("\n\n");
      // It only queues the start and opens the new agent's page, which says how it went: no success toast before the hub
      // has answered (#1856).
      const machine = onlineMachines.find((m) => m.id === mid);
      const inline = machine && launchersOf === JSON.stringify([mid, credentialOwner()])
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
      onClose();
    } catch (e) {
      toast(t("todos.launch_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  const submitExisting = async () => {
    const s = eligibleSessions.find((x) => `${x.machine_id}/${x.session_id}` === selectedKey);
    if (!s) return;
    setBusy(true);
    try {
      await reopenFailed();
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
      toast(t("todos.sent"), "ok");
      onClose();
      navigate({ name: "session", mid: s.machine_id, sid: s.session_id });
    } catch (e) {
      toast(t("todos.send_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title={t("todos.hand_over")} width={520}>
      <div className="flex flex-col gap-4">
        {/* Tab Switcher */}
        <div className="flex rounded-control bg-field p-0.5">
          <button
            type="button"
            onClick={() => setTab("new")}
            className={cx(
              "flex-1 rounded-[6px] py-1 text-[13px] font-medium transition-colors",
              tab === "new" ? "bg-surface text-ink shadow-hairline" : "text-ink-3 hover:text-ink-2",
            )}
          >
            {t("todos.tab_new")}
          </button>
          <button
            type="button"
            onClick={() => setTab("existing")}
            className={cx(
              "flex-1 rounded-[6px] py-1 text-[13px] font-medium transition-colors",
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
            <label className="mb-1 block text-[12px] font-medium text-ink-2">{t("todos.pick_agent")}</label>
            {eligibleSessions.length === 0 ? (
              <div className="py-6 text-center text-[12.5px] text-ink-3">{t("todos.no_live_agents")}</div>
            ) : (
              <div className="max-h-44 overflow-y-auto rounded-control border border-line p-1">
                {eligibleSessions.map((s) => {
                  const key = `${s.machine_id}/${s.session_id}`;
                  const isSel = key === selectedKey;
                  const st = stateOf(s);
                  return (
                    <div
                      key={key}
                      onClick={() => setSelectedKey(key)}
                      className={cx(
                        "flex cursor-pointer items-center justify-between gap-2 rounded px-2.5 py-1.5 text-[12.5px] transition-colors",
                        isSel ? "bg-accent text-white" : "hover:bg-hover text-ink",
                      )}
                    >
                      <div className="min-w-0">
                        <div className="flex min-w-0 items-center gap-2">
                          <StatusMark state={st} size="sm" />
                          <span className="truncate font-medium">{s.title || s.cwd || t("todos.unnamed")}</span>
                          <span className={cx("text-[11px]", isSel ? "text-white/80" : "text-ink-3")}>
                            {s.space_name || s.machine_id}
                          </span>
                        </div>
                        {st === "offline" && (
                          <div className={cx("text-[11px]", isSel ? "text-white/80" : "text-ink-3")}>
                            {t("todos.closed_resume")}
                          </div>
                        )}
                      </div>
                      {isSel && <Icon name="check" size={13} stroke={2.5} />}
                    </div>
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
              if (tab === "new") void (mid && submitNew());
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

        {/* Footer Buttons */}
        <div className="mt-2 flex justify-end gap-2">
          <Button tone="secondary" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {tab === "new" ? (
            <Button tone="accent" onClick={submitNew} disabled={!mid || busy} busy={busy}>
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
        </div>
      </div>
    </Dialog>
  );
}
