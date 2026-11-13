// A session's terminal in the side panel, as the iOS TerminalFeed (docs/integrations/terminal.md, docs/protocol/protocol.md "Terminal stream"): the
// hub's live stream of the pane written into xterm and continued after a reconnect, or — with a hub, node or herdr
// that cannot stream — the colored screen read once a second.
//
// Driving it (the default where the hub can, `terminal_control`): the pane takes the panel's columns × rows, so the
// program lays itself out for the panel; keys, the wheel and clicks go to it. Watching: xterm is the pane's own size
// and the panel scrolls over it; nothing goes in.
import type { Terminal as XTerm } from "@xterm/xterm";
import type { FitAddon as XFit } from "@xterm/addon-fit";
import type { SearchAddon as XSearch } from "@xterm/addon-search";
import { api, errorText, isAbort, sessionPath } from "../../../lib/api";
import { openLink } from "../../../lib/links";
import { getSession, getState, isOpen, onHubEvent, send } from "../../../lib/store";
import { passesToApp } from "../../../lib/shortcuts";
import {
  ANSWER_LIMIT_MS,
  FrameOrder,
  HISTORY_PAGE,
  NO_MODIFIERS,
  WheelLines,
  backoff,
  cellAt,
  decodeBase64,
  encodeKey,
  historyAnchor,
  isStale,
  keyDecision,
  keyNameForBytes,
  lineCount,
  modifierHeld,
  modifierReleased,
  modifierTapped,
  modifiersActive,
  modifiersPressed,
  parseTerminalPush,
  pasteBytes,
  typedWithModifiers,
  usableGrid,
  withModifiers,
  type ModifierKey,
  type Modifiers,
  type TerminalFrame,
  type TerminalPalette,
} from "../../../lib/terminal";
import { gridRow, parseAnsiRows, plainRow, type CellRow } from "../../../lib/terminal-cells";
import {
  CellThrottle,
  clampedPoint,
  contextAction,
  mouseButton,
  mouseModifiers,
  mouseRoute,
  parseMouseMode,
  type MouseAction,
  type MouseButton,
  type MouseMode,
} from "../../../lib/terminal-mouse";
import {
  DEFAULT_CURSOR,
  lineEditBytes,
  parseCursorStyle,
  parseFlag,
  webLink,
  type CursorStyle,
} from "../../../lib/terminal-details";
import { FIND_LIMIT, findIndex, findInLines, marksFor, stepFind, type FindMarks, type FindPos, type RowMatch } from "../../../lib/terminal-search";
import { subscribeAppearance } from "../../../lib/terminal-appearance";
import { toast } from "../../../ui/atoms";
import { copyNotice, copyWith, osc52Text, transferFiles } from "../../../lib/terminal-clipboard";
import { cjkFontStack, terminalFontReady } from "./cjk";
import { browserClipboard, clipboardFiles } from "./clipboard";
import { findDecorations, terminalColors } from "./colors";
import { FileTransfer } from "./transfer";
import { t } from "../../../i18n";

/** Driving: not asked (off), asked (starting), granted (active), another program holds it and the user is to say
 * whether to take it (busy), or watching although it wanted to drive (declined, `why`). */
export type Control = "off" | "starting" | "active" | "busy" | "declined";
export type Why = "" | "taken" | "lost" | "unsupported" | "chosen";

export interface FeedView {
  /** xterm is loaded and open. */
  ready: boolean;
  mode: "stream" | "poll";
  control: Control;
  why: Why;
  note: string;
  /** The user wants to drive it ("Take control") rather than watch ("View only"); remembered across terminals. */
  wants: boolean;
  /** xterm is the pane's size (watching): the panel scrolls over it instead of fitting it. */
  pinned: boolean;
  cols: number;
  rows: number;
  /** The first screen arrived (or the first read failed). */
  loaded: boolean;
  offline: boolean;
  /** What is shown may be old: the status bar is up and the screen dimmed. */
  stale: boolean;
  failure: string | null;
  /** A paste of several lines waiting for "Paste". */
  pasting: string | null;
  /** Files being sent to the computer to be pasted by path ("Uploading …"), null when none are. */
  uploading: string | null;
  /** Files are being dragged over the panel. */
  dropping: boolean;
  /** Text is selected in xterm (the key bar's copy). */
  selection: boolean;
  /** The key bar's ctrl / alt / shift: they apply to the next key, from the bar or typed. */
  mods: Modifiers;
  /** Who gets the mouse while driving: the program (via herdr, Shift selects here) or this page (Shift sends it on). */
  mouse: MouseMode;
  /** Pages of herdr's history above the screen, oldest first (watching only, iOS TerminalFeed.older). */
  older: CellRow[];
  /** Reading history: the screen stays as it was until "Back to latest". */
  paused: boolean;
  loadingOlder: boolean;
  hasOlder: boolean;
  /** The page reached the start of what herdr keeps (about 1000 lines). */
  atLimit: boolean;
  /** "Wrap": long rows fold at the panel's width while watching (remembered). */
  wrap: boolean;
  fontSize: number;
  /** The terminal's font stack (with Chinese scaled to two cells once that font loads). */
  fontFamily: string;
  /** The screen as rows the page draws ("Wrap" while watching); null while xterm draws it. */
  screen: ScreenRows | null;
  /** xterm's colors, for the rows the page draws (the terminal's Appearance: the page's light / dark, or its own). */
  palette: TerminalPalette | null;
  /** "Cursor style" / "Blinking cursor" / "WebGL rendering": the options in "Terminal settings" (remembered). */
  cursorStyle: CursorStyle;
  cursorBlink: boolean;
  webgl: boolean;
  /** The search: the current match (0-based, -1 when none is current) among `count`; null while not searching. */
  find: { index: number; count: number } | null;
  /** The page's own rows (history, and the screen when the page draws it) that hold a match. */
  marks: FindMarks | null;
}

export interface ScreenRows {
  rows: CellRow[];
  /** Where the cursor is, when the program shows it. */
  cursor: { row: number; col: number } | null;
}

const TICK_MS = 250;
const RESIZE_MS = 250;
const POLL_MS = 1000;
const WHEEL_MS = 50;
const LINK_DOWN = t("session-terminal-feed.hub_unreachable");
const WATCH_KEY = "persona.terminal.watch";
const WRAP_KEY = "persona.terminal.wrap";
const FONT_KEY = "persona.terminal.fontSize";
const CURSOR_KEY = "persona.terminal.cursorStyle";
const BLINK_KEY = "persona.terminal.cursorBlink";
const WEBGL_KEY = "persona.terminal.webgl";
const MOUSE_KEY = "persona.terminal.mouse";
export const FONT_SIZES = { min: 9, max: 20, default: 13 };
/** Frames held while history is read; past this the screen starts over from a whole frame instead. */
const HELD_MAX = 4 << 20;

function savedFontSize(): number {
  const n = Number(localStorage.getItem(FONT_KEY));
  return Number.isFinite(n) && n >= FONT_SIZES.min && n <= FONT_SIZES.max ? n : FONT_SIZES.default;
}

export class TerminalFeed {
  private view: FeedView;
  private listeners = new Set<() => void>();
  private term: XTerm | null = null;
  private fit: XFit | null = null;
  private cleanup: (() => void)[] = [];
  private disposed = false;
  private order = new FrameOrder();
  private subscribed = false;
  private everSubscribed = false;
  /** This session cannot stream (old node / herdr): stay on polling. */
  private unsupported = false;
  private takeover = false;
  private failures = 0;
  private lastOK: number | null = null;
  private nextTry = 0;
  /** When the wait for the first screen started. */
  private since: number | null = Date.now();
  /** When the subscription still waiting for its answer went out (its first frame, and the hub's word on taking the
   * terminal over when it asked to); null: answered, or nothing asked. Past ANSWER_LIMIT_MS it is given up (#6327). */
  private asked: number | null = null;
  /** The screen the last subscription builds on is there: its first frame came, or it continues the frames the page has. */
  private framed = false;
  /** The computer was offline at the last tick: once it is back, subscribe again at once. */
  private computerWasOffline = false;
  /** The grid that fits the panel: what driving asks for. */
  private grid: { cols: number; rows: number } | null = null;
  private gridTimer = 0;
  private polling: AbortController | null = null;
  private lastPoll: string | null = null;
  private wheel = new WheelLines();
  private wheelPending = 0;
  private wheelAt: [number, number] | null = null;
  private wheelTimer = 0;
  private down: { x: number; y: number } | null = null;
  /** The pointer that last went down ("mouse", "pen", "touch"), and the button herdr holds pressed for a drag (the mouse section). */
  private pointerKind = "mouse";
  private mouseHeld: { button: MouseButton; cell: [number, number]; throttle: CellThrottle } | null = null;
  /** The Shift press is being handed to xterm without Shift (selectHere). */
  private replaying = false;
  /** The press went to herdr: the click event that follows it is not also a click in the terminal. */
  private mouseSent = false;
  private focusedOnce = false;
  /** The last screen read while polling (its rows, when the page draws them). */
  private pollText = "";
  /** The plain lines of `older` (what the next page is located by). */
  private olderPlain: string[] = [];
  /** Frames that arrived while history is read, applied on "Back to latest". */
  private held: { frame: TerminalFrame; bytes: Uint8Array }[] = [];
  private heldBytes = 0;
  /** Too much arrived meanwhile: start over from a whole frame. */
  private heldOverflow = false;
  private screenFrame = 0;
  /** Files pasted or dropped (transfer.ts): sent to the computer, their paths typed in. */
  private transfer: FileTransfer;
  /** A link was opened by this click (the click is not also a click in the terminal). */
  private linkHit = false;
  private webglAddon: { dispose(): void } | null = null;
  private search: XSearch | null = null;
  /** The search: what is looked for, the matches the page's rows hold, what xterm's screen holds, and which is current. */
  private found: { query: string; cs: boolean; pos: FindPos | null; rows: RowMatch[]; base: number; drawn: boolean; busy: boolean; screen: { index: number; count: number } } = {
    query: "",
    cs: false,
    pos: null,
    rows: [],
    base: 0,
    drawn: false,
    busy: false,
    screen: { index: -1, count: 0 },
  };
  private recountFrame = 0;
  /** The pane's working directory when the page knows it: a dropped Files entry is named relative to it. */
  cwd = "";

  constructor(
    readonly mid: string,
    readonly sid: string,
  ) {
    this.transfer = new FileTransfer(
      {
        mid,
        get cwd() {
          return feed.cwd;
        },
        get driving() {
          return feed.driving;
        },
        get disposed() {
          return feed.disposed;
        },
        setUploading: (uploading) => this.patch({ uploading }),
        setDropping: (dropping) => this.patch({ dropping }),
        paste: (text) => {
          this.term?.paste(text); // bracketed when the program asked for it; the keys go through type()
          this.term?.focus();
        },
      },
      { notDriving: t("session-terminal-feed.take_control_to_paste"), changed: t("session-terminal-feed.switched_during_upload"), released: t("session-terminal-feed.released_during_upload") },
    );
    const feed = this;
    this.view = {
      ready: false,
      mode: "poll",
      control: "off",
      why: "",
      note: "",
      wants: localStorage.getItem(WATCH_KEY) !== "1",
      pinned: false,
      cols: 0,
      rows: 0,
      loaded: false,
      offline: false,
      stale: false,
      failure: null,
      pasting: null,
      uploading: null,
      dropping: false,
      selection: false,
      mods: NO_MODIFIERS,
      mouse: parseMouseMode(localStorage.getItem(MOUSE_KEY)),
      older: [],
      paused: false,
      loadingOlder: false,
      hasOlder: true,
      atLimit: false,
      wrap: localStorage.getItem(WRAP_KEY) === "1",
      fontSize: savedFontSize(),
      fontFamily: "",
      screen: null,
      palette: null,
      cursorStyle: parseCursorStyle(localStorage.getItem(CURSOR_KEY)),
      cursorBlink: parseFlag(localStorage.getItem(BLINK_KEY)),
      webgl: parseFlag(localStorage.getItem(WEBGL_KEY)),
      find: null,
      marks: null,
    };
  }

  // ── React ─────────────────────────────────────────────

  subscribe = (l: () => void) => {
    this.listeners.add(l);
    return () => {
      this.listeners.delete(l);
    };
  };

  snapshot = () => this.view;

  /** The link's health right now, for the status bar's countdown. */
  link() {
    return { lastOK: this.lastOK, failure: this.view.failure, nextTry: this.nextTry };
  }

  private patch(p: Partial<FeedView>) {
    // The key bar's ctrl / alt / shift go with the keys: its caps are disabled once control leaves, and a disabled cap
    // gets no pointerup, so a modifier held, tapped or locked then would chord the first key typed when it comes back.
    if (p.control !== undefined && p.control !== "active") p = { ...p, mods: NO_MODIFIERS };
    const v = this.view as unknown as Record<string, unknown>;
    if (!Object.entries(p).some(([k, x]) => v[k] !== x)) return;
    this.view = { ...this.view, ...p };
    this.listeners.forEach((l) => l());
  }

  // ── lifecycle ─────────────────────────────────────────

  async mount(host: HTMLElement, viewport: HTMLElement) {
    let mod: typeof import("./xterm");
    try {
      mod = await import("./xterm");
    } catch {
      if (!this.disposed) this.patch({ loaded: true, failure: t("session-terminal-feed.load_failed"), stale: true });
      return;
    }
    if (this.disposed) return;
    const { Terminal, FitAddon, ClipboardAddon, WebLinksAddon, SearchAddon, matchHerdrWidths } = mod;
    const family = getComputedStyle(host).fontFamily;
    await terminalFontReady(this.view.fontSize, family); // xterm measures a cell when it opens: not before the font is there
    if (this.disposed) return;
    this.patch({ palette: this.theme() });
    const term = new Terminal({
      fontFamily: family,
      fontSize: this.view.fontSize,
      lineHeight: 1.15,
      scrollback: 0, // herdr redraws the grid in place: nothing ever scrolls into xterm's history
      cursorBlink: this.view.cursorBlink,
      cursorStyle: this.view.cursorStyle,
      cursorInactiveStyle: "outline",
      disableStdin: true,
      allowProposedApi: true, // the search addon marks its matches with decorations
      macOptionIsMeta: true,
      theme: this.view.palette ?? undefined,
      linkHandler: {
        activate: (_e, uri) => {
          this.openWebLink(uri); // OSC 8 links from the program
        },
      },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new WebLinksAddon((_e, uri) => this.openWebLink(uri))); // plain URLs in the output
    const search = new SearchAddon({ highlightLimit: FIND_LIMIT });
    term.loadAddon(search);
    this.search = search;
    // OSC 52 from the program (herdr's streams do not carry it, docs/integrations/terminal.md): a write goes to the clipboard, a read
    // is refused — a program must not read what the user copied, so it is told the clipboard is empty.
    term.loadAddon(new ClipboardAddon(undefined, { readText: () => "", writeText: (_selection: string, text: string) => this.programCopied(text) }));
    matchHerdrWidths(term);
    term.open(host);
    this.term = term;
    this.fit = fit;

    const data = term.onData((d) => this.type(d));
    const resized = term.onResize(() => this.sizeElement());
    const parsed = term.onWriteParsed(() => this.screenChanged());
    const selected = term.onSelectionChange(() => this.patch({ selection: term.hasSelection() }));
    const results = search.onDidChangeResults((r) => this.screenResults(r.resultIndex, r.resultCount));
    term.attachCustomKeyEventHandler(this.key);
    term.attachCustomWheelEventHandler(this.wheelEvent);
    host.addEventListener("paste", this.paste, true);
    viewport.addEventListener("dragenter", this.transfer.dragEnter, true);
    viewport.addEventListener("dragover", this.transfer.dragOver, true);
    viewport.addEventListener("dragleave", this.transfer.dragLeave, true);
    viewport.addEventListener("drop", this.transfer.drop, true);
    host.addEventListener("pointerdown", this.pointerDown, true);
    host.addEventListener("mousedown", this.mouseDownEvent, true);
    host.addEventListener("contextmenu", this.mouseContextMenu, true);
    host.addEventListener("click", this.click, true);
    // the page's scheme, or the terminal's Appearance, changed
    const colors = new MutationObserver(this.recolor);
    colors.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
    const appearance = subscribeAppearance(this.recolor);
    const sizes = new ResizeObserver(() => this.measure());
    sizes.observe(viewport);
    const offs = [onHubEvent("terminal", this.onTerminal), onHubEvent("terminal_control", this.onControl), onHubEvent("hello", this.onHello)];
    const tick = window.setInterval(this.tick, TICK_MS);
    this.cleanup.push(
      () => data.dispose(),
      () => resized.dispose(),
      () => parsed.dispose(),
      () => selected.dispose(),
      () => results.dispose(),
      () => cancelAnimationFrame(this.screenFrame),
      () => cancelAnimationFrame(this.recountFrame),
      () => host.removeEventListener("paste", this.paste, true),
      () => viewport.removeEventListener("dragenter", this.transfer.dragEnter, true),
      () => viewport.removeEventListener("dragover", this.transfer.dragOver, true),
      () => viewport.removeEventListener("dragleave", this.transfer.dragLeave, true),
      () => viewport.removeEventListener("drop", this.transfer.drop, true),
      () => host.removeEventListener("pointerdown", this.pointerDown, true),
      () => host.removeEventListener("mousedown", this.mouseDownEvent, true),
      () => host.removeEventListener("contextmenu", this.mouseContextMenu, true),
      () => this.mouseEnd(),
      () => host.removeEventListener("click", this.click, true),
      () => colors.disconnect(),
      appearance,
      () => sizes.disconnect(),
      ...offs,
      () => clearInterval(tick),
    );

    this.measure(true); // the first grid sizes the empty terminal and is what driving asks for
    this.patch({ ready: true, fontFamily: family });
    if (this.view.webgl) void this.enableWebgl();
    this.tick();
    void cjkFontStack(family).then((stack) => {
      if (!stack || this.disposed || !this.term) return;
      this.term.options.fontFamily = stack; // the cell is the same width: nothing resizes
      this.patch({ fontFamily: stack });
    });
  }

  /** Leaves the terminal: the hub lets go of it (the pane goes back to the computer's size). */
  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    if (this.everSubscribed && this.view.mode === "stream") send({ type: "terminal_unsubscribe", ...this.ids() });
    this.polling?.abort();
    clearTimeout(this.gridTimer);
    clearTimeout(this.wheelTimer);
    this.cleanup.forEach((f) => f());
    this.cleanup = [];
    this.term?.dispose();
    this.term = null;
    this.listeners.clear();
  }

  private ids() {
    return { machine_id: this.mid, session_id: this.sid };
  }

  private has(feature: string) {
    return getState().features.includes(feature);
  }

  /** The socket is up and the hub said hello. */
  private live() {
    return isOpen() && getState().conn === "online";
  }

  private theme(): TerminalPalette {
    return terminalColors();
  }

  private recolor = () => {
    const palette = this.theme();
    this.patch({ palette });
    if (this.term) this.term.options.theme = palette;
  };

  // ── the loop (four times a second, as iOS) ─────────────

  private tick = () => {
    if (!this.term || this.disposed) return;
    const now = Date.now();
    const v = this.view;
    if (v.mode === "poll" && !this.unsupported && this.has("terminal_stream") && this.live()) {
      if (v.wants && this.has("terminal_control") && v.control === "off" && !this.grid) return; // the first layout tells the size
      this.polling?.abort();
      this.polling = null;
      this.patch({ mode: "stream" });
      this.subscribeNow(false);
    } else if (v.mode === "stream") {
      if (!this.live()) {
        this.subscribed = false; // the hub's hello subscribes again
        this.linkLost();
        if (!v.failure) this.patch({ failure: LINK_DOWN });
      } else {
        if (v.failure === LINK_DOWN) this.patch({ failure: null }); // back: an idle pane sends nothing, that is not a failure
        const offline = this.machine()?.online === false;
        if (offline !== this.computerWasOffline) {
          this.computerWasOffline = offline;
          if (!offline) {
            // its node is back: subscribe (and take the terminal over) again now, not at the backoff's turn
            this.failures = 0;
            this.nextTry = 0;
          }
        }
        if (this.asked !== null && now - this.asked > ANSWER_LIMIT_MS) this.unanswered();
        else if (!this.subscribed && now >= this.nextTry) this.subscribeNow(true);
        else if (this.subscribed && this.view.loaded && !this.view.failure) this.lastOK = now;
      }
    } else if (now >= this.nextTry && !this.polling && !v.paused) {
      void this.poll();
    }
    this.patch({ stale: isStale(this.lastOK, this.view.failure, now, this.since) });
  };

  private willDrive() {
    const v = this.view;
    return v.wants && this.has("terminal_control") && usableGrid(this.grid) && v.control !== "declined" && v.control !== "busy";
  }

  private subscribeNow(continuing: boolean) {
    this.subscribed = this.everSubscribed = true;
    const msg: Record<string, unknown> = { type: "terminal_subscribe", ...this.ids() };
    const fresh = !(continuing && this.order.epoch);
    if (!fresh) {
      msg.epoch = this.order.epoch;
      msg.after_seq = this.order.seq;
    } else {
      this.order.restart(Date.now());
    }
    if (this.willDrive() && this.grid) {
      msg.control = { cols: this.grid.cols, rows: this.grid.rows };
      if (this.view.control === "off") this.patch({ control: "starting" });
    }
    // Answered for sure: a stream from its whole frame, and taking it over (terminal_control). One continuing from the
    // frame it has may hear nothing of an idle pane, which is no failure.
    if (this.asked === null && (fresh || this.view.control === "starting")) this.asked = Date.now();
    this.framed = !fresh;
    if (this.takeover) msg.takeover = true;
    this.takeover = false;
    send(msg);
    this.inputMode();
  }

  /** The subscription's answer is in: a frame, and the hub's word on taking the terminal over when it asked to. */
  private answeredIfSo() {
    if (this.framed && this.view.control !== "starting") this.asked = null;
  }

  /** Nothing answered the subscription within ANSWER_LIMIT_MS: it goes, at the hub too, and the terminal says why; the
   * next one goes at the backoff's turn, or at once when the computer comes back. */
  private unanswered() {
    this.drop(); // still taking it over (the label says so): the next try asks again
    this.patch({ loaded: true }); // the reason, not a spinner
    this.failed(this.notAnswering());
    this.inputMode();
  }

  /** Unsubscribes at the hub (the next subscription has it ask the node again) and starts over from a whole frame. */
  private drop() {
    send({ type: "terminal_unsubscribe", ...this.ids() });
    this.subscribed = false;
    this.asked = null;
    this.order.reset();
  }

  private machine() {
    return getState().machines.find((m) => m.id === this.mid);
  }

  /** Why the terminal hasn't come: its computer is offline, or isn't answering. */
  private notAnswering() {
    const m = this.machine();
    if (!m) return t("lib-terminal.not_answered_yet");
    return m.online ? t("lib-terminal.not_answering", { name: m.name }) : t("lib-terminal.offline", { name: m.name });
  }

  private onHello = () => {
    if (this.view.mode !== "stream" || this.disposed) return;
    this.subscribed = false;
    this.linkLost();
    if (this.live()) this.subscribeNow(true);
  };

  /** The link went (or the hub started over): it no longer knows who drives. Keep the wish, so subscribing asks for
   * control again, but let no key in until the hub's terminal_control says active. */
  private linkLost() {
    if (this.view.control !== "active") return;
    this.patch({ control: "starting" });
    this.inputMode();
  }

  // ── frames ────────────────────────────────────────────

  private onTerminal = (msg: Record<string, unknown>) => {
    if (msg.machine_id !== this.mid || msg.session_id !== this.sid || !this.term) return;
    const p = parseTerminalPush(msg);
    if (!p) return;
    if (p.kind === "ended") return this.ended(p.error, p.unsupported);
    if (this.view.mode !== "stream") return;
    const bytes = decodeBase64(p.frame.data);
    if (!bytes) return; // the next diff will not follow: a gap
    const now = Date.now();
    switch (this.order.accept(p.frame, now)) {
      case "apply":
        if (this.view.paused) this.hold(p.frame, bytes);
        else this.draw(p.frame, bytes);
        this.healthy(now);
        this.patch({ offline: false, loaded: true });
        this.framed = true;
        this.answeredIfSo();
        break;
      case "gap":
        this.subscribeNow(false);
        break;
    }
  };

  private draw(f: TerminalFrame, bytes: Uint8Array) {
    const t = this.term!;
    if (f.full) t.reset(); // a whole screen: nothing of the last one stays (nor a resize's leftovers)
    if (f.cols && f.rows) this.resize(f.cols, f.rows);
    t.write(bytes);
  }

  /** Reading history: the frame waits for "Back to latest" (a whole one makes the ones before it moot). */
  private hold(frame: TerminalFrame, bytes: Uint8Array) {
    if (frame.full) {
      this.held = [];
      this.heldBytes = 0;
      this.heldOverflow = false;
    }
    if (this.heldOverflow) return;
    this.held.push({ frame, bytes });
    this.heldBytes += bytes.length;
    if (this.heldBytes > HELD_MAX) {
      this.held = [];
      this.heldBytes = 0;
      this.heldOverflow = true;
    }
  }

  private ended(error: string, unsupported: boolean) {
    this.subscribed = false;
    this.asked = null;
    this.order.reset();
    const c = this.view.control;
    const taking = c === "active" || c === "starting";
    if (unsupported) {
      this.unsupported = true;
      this.nextTry = 0;
      this.patch({ mode: "poll", control: taking ? "off" : c }); // a screen read can't be driven: it only watches
    } else {
      const offline = error.includes("not running");
      // Nothing runs it: nothing to take over. Otherwise it still takes the terminal over, and the label says so until the
      // next try is answered ("View only" between tries read as a mirror that had given up, #6327).
      this.patch({ offline, loaded: true, control: taking ? (offline ? "off" : "starting") : c });
      // An offline computer in the page's words, whatever the hub's error says (it may name the machine by id).
      this.failed(offline ? t("session-terminal-feed.not_in_herdr") : this.machine()?.online === false ? this.notAnswering() : error);
    }
    this.inputMode();
  }

  private onControl = (msg: Record<string, unknown>) => {
    if (msg.machine_id !== this.mid || msg.session_id !== this.sid || !this.term) return;
    const why = typeof msg.error === "string" ? msg.error : "";
    switch (msg.state) {
      case "active":
        this.leaveHistory();
        this.patch({ control: "active", why: "", note: "" });
        if (!this.focusedOnce) {
          this.focusedOnce = true;
          this.term.focus();
        }
        break;
      case "busy":
        this.term.blur(); // the question's buttons take the keys, not the terminal (Esc would reach the program)
        this.patch({ control: "busy" });
        break;
      case "unsupported":
        this.patch({ control: "declined", why: "unsupported", note: why || t("session-terminal-feed.watch_only_computer") });
        toast(this.view.note);
        break;
      case "taken":
        this.patch({ control: "declined", why: "taken", note: why || t("session-terminal-feed.taken") });
        break;
      default: // lost: the node let go on its own
        this.patch({ control: "declined", why: "lost", note: why || t("session-terminal-feed.lost") });
    }
    this.answeredIfSo();
    this.inputMode();
  };

  // ── sizes ─────────────────────────────────────────────

  /** The panel changed size (dragged, the window): the first grid at once, later ones once they settle. */
  private measure(now = false) {
    clearTimeout(this.gridTimer);
    const run = () => {
      const g = this.fit?.proposeDimensions();
      if (g && Number.isFinite(g.cols) && Number.isFinite(g.rows)) this.setGrid({ cols: g.cols, rows: g.rows });
    };
    if (now || !this.grid) run();
    else this.gridTimer = window.setTimeout(run, RESIZE_MS);
  }

  private setGrid(g: { cols: number; rows: number }) {
    if (!usableGrid(g) || (this.grid && this.grid.cols === g.cols && this.grid.rows === g.rows)) return;
    this.grid = g;
    const c = this.view.control;
    if (this.subscribed && (c === "active" || c === "starting")) send({ type: "terminal_input", ...this.ids(), control: g });
    if (this.view.mode === "poll" || !this.view.loaded) {
      this.resize(g.cols, g.rows);
      this.lastPoll = null; // read again at the new size
      if (this.view.mode === "poll") this.nextTry = 0;
    }
  }

  private resize(cols: number, rows: number) {
    const t = this.term;
    if (!t || cols < 1 || rows < 1) return;
    if (t.cols !== cols || t.rows !== rows) t.resize(cols, rows); // onResize sizes the element
    this.patch({ cols, rows });
  }

  /** Watching, xterm keeps the pane's size and the panel scrolls over it; otherwise it fills the panel. */
  private sizeElement() {
    const v = this.view;
    const pinned = v.mode === "stream" && v.control !== "active" && v.control !== "starting";
    const el = this.term?.element;
    const screen = el?.querySelector<HTMLElement>(".xterm-screen");
    if (el) {
      el.style.width = pinned && screen ? screen.style.width : "";
      el.style.height = pinned && screen ? screen.style.height : "";
    }
    this.patch({ pinned });
  }

  // ── input ─────────────────────────────────────────────

  private inputMode() {
    const t = this.term;
    if (!t) return;
    t.options.disableStdin = this.view.control !== "active";
    this.sizeElement();
    this.screenChanged();
  }

  private send(text: string) {
    if (text && this.view.control === "active") send({ type: "terminal_input", ...this.ids(), text });
  }

  /** What xterm makes of the keys typed on it: the terminal's bytes; with the key bar's ctrl / alt / shift down, that key. */
  private type(data: string) {
    const m = this.view.mods;
    if (!modifiersActive(m)) return this.send(data);
    const name = keyNameForBytes(data);
    if (name) {
      this.sendKey(withModifiers(m, name));
    } else if ([...data].length === 1) {
      const t = typedWithModifiers(m, data);
      if (t.key) this.sendKey(t.key);
      else if (t.text) this.send(t.text);
    } else {
      this.send(data);
    }
    this.patch({ mods: modifiersPressed(m) });
  }

  /** A key by herdr's name ("ctrl+c", "up"), as the program asked for its arrows. */
  private sendKey(name: string) {
    const b = encodeKey(name, this.term?.modes.applicationCursorKeysMode ?? false);
    if (b) this.send(b);
  }

  // ── the key bar ───────────────────────────────────────

  /** Driving it: keys go in. */
  get driving() {
    return this.view.control === "active";
  }

  /** A key of the bar, with the modifiers that are down (then the armed-once ones let go). */
  pressKey(name: string) {
    if (!this.driving) return;
    const m = this.view.mods;
    this.sendKey(withModifiers(m, name));
    this.patch({ mods: modifiersPressed(m) });
  }

  /** A key that goes out as it is, whatever is down (⌃C). */
  pressBare(name: string) {
    if (this.driving) this.sendKey(name);
  }

  tapModifier(k: ModifierKey, quick: boolean) {
    this.patch({ mods: modifierTapped(this.view.mods, k, quick) });
  }

  holdModifier(k: ModifierKey) {
    this.patch({ mods: modifierHeld(this.view.mods, k) });
  }

  releaseModifier(k: ModifierKey) {
    this.patch({ mods: modifierReleased(this.view.mods, k) });
  }

  /** A saved command: typed into the terminal, and run (with return) when `run`. */
  typeText(text: string, run: boolean) {
    if (!this.driving || !text) return;
    this.send(pasteBytes(text) + (run ? "\r" : ""));
    this.term?.focus();
  }

  /** The key bar's paste: what the clipboard has, as ⌘V does (several lines ask first). */
  async pasteClipboard() {
    if (!this.driving) return;
    const files = await clipboardFiles();
    if (files.length) return void this.transfer.uploadFiles(files);
    let text = "";
    try {
      text = (await navigator.clipboard?.readText?.()) ?? "";
    } catch {
      return void toast(t("session-terminal-feed.clipboard_denied"));
    }
    if (!navigator.clipboard?.readText) return void toast(t("session-terminal-feed.clipboard_insecure"));
    if (!text) return void toast(t("session-terminal-feed.clipboard_empty"));
    if (lineCount(text) > 1) {
      this.term?.blur();
      this.patch({ pasting: text });
    } else {
      this.send(pasteBytes(text));
    }
  }

  focus() {
    this.term?.focus();
  }

  /**
   * Keys the page takes before xterm (keyDecision): ⌃C / ⌘C with text selected copies it (nothing selected: ^C goes to the
   * program as ever), shift+enter is a new line in Claude Code's box (ESC CR, as iOS), an IME composition is left alone.
   */
  private key = (e: KeyboardEvent) => {
    // one of the app's shortcuts (Ctrl+K opens the palette, not ^K; lib/shortcuts.ts): the page takes it, xterm sends nothing
    if (passesToApp(e)) return false;
    if (this.lineEdit(e)) return false;
    const d = keyDecision(e, this.term?.hasSelection() ?? false);
    if (d === "xterm") return true;
    if (e.type === "keydown") {
      if (d === "copy") this.copySelection();
      else if (d === "newline") this.send("\x1b\r");
    }
    e.preventDefault();
    e.stopPropagation();
    return false;
  };

  /** Mac line editing while driving: ⌘⌫ ^U, ⌘← ^A, ⌘→ ^E (lib/terminal-details.ts). True when the key was taken. */
  private lineEdit(e: KeyboardEvent): boolean {
    if (!this.driving) return false;
    const bytes = lineEditBytes(e);
    if (!bytes) return false;
    e.preventDefault();
    e.stopPropagation();
    this.send(bytes);
    return true;
  }

  /** Copies what is selected in xterm (the key bar's copy, ⌃C / ⌘C); the selection goes once it is on the clipboard. */
  copySelection() {
    const text = this.term?.getSelection() ?? "";
    if (!text) return void toast(t("session-terminal-feed.select_first"));
    void copyWith(text, browserClipboard()).then((outcome) => {
      const n = copyNotice(outcome, window.isSecureContext);
      toast(n.text, n.tone);
      if (outcome !== "failed") this.term?.clearSelection();
    });
  }

  hasSelection() {
    return this.term?.hasSelection() ?? false;
  }

  /** What is selected in xterm ("" when nothing). */
  selectedText() {
    return this.term?.getSelection() ?? "";
  }

  selectAll() {
    this.term?.selectAll();
  }

  clearSelection() {
    this.term?.clearSelection();
  }

  /** Copies text the page selected itself (the rows it draws), with the same notices as the xterm selection. */
  copyText(text: string) {
    if (!text) return void toast(t("session-terminal-feed.select_first"));
    void copyWith(text, browserClipboard()).then((outcome) => {
      const n = copyNotice(outcome, window.isSecureContext);
      toast(n.text, n.tone);
    });
  }

  /** A program asked for the clipboard (OSC 52): written at once where the browser allows it, else the user taps "Copy" (it wants a gesture). */
  private programCopied(text: string) {
    const copied = osc52Text(text);
    if (!copied) return;
    void copyWith(copied, browserClipboard()).then((outcome) => {
      if (outcome !== "failed") return toast(t("session-terminal-feed.program_copied"), "ok");
      toast(t("session-terminal-feed.program_wants_copy"), "info", {
        label: t("session-terminal-feed.copy"),
        run: () =>
          void copyWith(copied, browserClipboard()).then((o) => {
            const n = copyNotice(o, window.isSecureContext);
            toast(n.text, n.tone);
          }),
      });
    });
  }

  /** ⌘V: one line goes in as typed; several ask first, then go as one bracketed paste. */
  private paste = (e: ClipboardEvent) => {
    e.preventDefault();
    e.stopPropagation();
    const files = transferFiles(e.clipboardData);
    if (files.length) return void this.transfer.uploadFiles(files);
    if (this.view.control !== "active") return;
    const text = e.clipboardData?.getData("text/plain") ?? "";
    if (!text) return;
    if (lineCount(text) > 1) {
      this.term?.blur();
      this.patch({ pasting: text });
    } else {
      this.send(pasteBytes(text));
    }
  };

  // ── files: pasted or dropped, sent to the computer, their paths typed in (transfer.ts) ──

  confirmPaste(ok: boolean) {
    const text = this.view.pasting;
    this.patch({ pasting: null });
    if (ok && text) this.send(pasteBytes(text));
    this.term?.focus();
  }

  private box() {
    const t = this.term;
    const s = t?.element?.querySelector<HTMLElement>(".xterm-screen");
    if (!t || !s) return null;
    const r = s.getBoundingClientRect();
    return { left: r.left, top: r.top, width: r.width, height: r.height, cols: t.cols, rows: t.rows };
  }

  /**
   * Driving, the wheel is the terminal's (`terminal_scroll`): herdr gives it to a program that takes the mouse
   * (Claude Code, Codex scroll their conversation) or scrolls its own history — in lines, over the cell under the
   * pointer, one message per burst. Watching, the panel scrolls (xterm has no history; it would send arrow keys).
   */
  private wheelEvent = (e: WheelEvent) => {
    if (this.view.control !== "active" || !this.has("terminal_scroll")) return false;
    e.preventDefault();
    const b = this.box();
    if (!b) return false;
    const n = this.wheel.add(e.deltaY, e.deltaMode, b.height / b.rows, b.rows);
    this.wheelAt ??= cellAt(e.clientX - b.left, e.clientY - b.top, b.width, b.height, b.cols, b.rows);
    if (n) {
      this.wheelPending += n;
      if (!this.wheelTimer) this.wheelTimer = window.setTimeout(this.flushWheel, WHEEL_MS);
    }
    return false;
  };

  private flushWheel = () => {
    this.wheelTimer = 0;
    const n = this.wheelPending, at = this.wheelAt;
    this.wheelPending = 0;
    this.wheelAt = null;
    if (n && this.view.control === "active") send({ type: "terminal_input", ...this.ids(), scroll: n, ...(at ? { at } : {}) });
  };

  private pointerDown = (e: PointerEvent) => {
    this.pointerKind = e.pointerType;
    this.down = e.button === 0 ? { x: e.clientX, y: e.clientY } : null;
    this.linkHit = false;
  };

  /** A link in the output was clicked (xterm's mouseup, before the click event): web pages only, in the app's link sheet. */
  private openWebLink(uri: string) {
    const url = webLink(uri);
    if (!url) return;
    this.linkHit = true;
    openLink(url, undefined, getSession(this.mid, this.sid)?.manager);
  }

  /** Driving, a plain click is a click in the terminal at that cell (`terminal_click`); a drag selects instead. */
  private click = (e: MouseEvent) => {
    const d = this.down;
    this.down = null;
    if (this.linkHit) return void (this.linkHit = false); // that click opened a link: the program does not get it too
    if (this.mouseSent) return void (this.mouseSent = false); // its press and release already went to herdr (the mouse section)
    if (!d || e.detail !== 1 || Math.hypot(e.clientX - d.x, e.clientY - d.y) > 4) return;
    if (this.view.control !== "active" || !this.has("terminal_click") || this.term?.hasSelection()) return;
    const b = this.box();
    if (!b) return;
    const x = e.clientX - b.left, y = e.clientY - b.top;
    if (x < 0 || y < 0 || x > b.width || y > b.height) return;
    send({ type: "terminal_input", ...this.ids(), click: cellAt(x, y, b.width, b.height, b.cols, b.rows) });
  };

  // ── the mouse (#2441, lib/terminal-mouse.ts; docs/integrations/terminal.md "Mouse") ──
  //
  // Driving, press / drag / release with the button and modifiers go to herdr as `terminal_input.mouse`, a cell at a
  // time; herdr hands them to a program that takes the mouse and drops them for one that does not. What the page keeps
  // (selection, the right-click menu) is decided by mouseRoute: Shift flips it. A touch keeps the older tap = `click`.

  /** Mouse events can go to herdr: driving, on a hub that takes them. */
  private mouseOn() {
    return this.view.control === "active" && this.has("terminal_mouse");
  }

  /** The cell under the pointer; outside the screen: null, or with `clamp` the nearest edge cell (a drag that left). */
  private mouseCell(e: MouseEvent, clamp: boolean): [number, number] | null {
    const b = this.box();
    if (!b) return null;
    const x = e.clientX - b.left, y = e.clientY - b.top;
    if (!clamp && (x < 0 || y < 0 || x > b.width || y > b.height)) return null;
    const [cx, cy] = clampedPoint(x, y, b.width, b.height);
    return cellAt(cx, cy, b.width, b.height, b.cols, b.rows);
  }

  private sendMouse(action: MouseAction, button: MouseButton, at: [number, number], mods: number) {
    send({ type: "terminal_input", ...this.ids(), mouse: { action, button, at, mods } });
  }

  /** Capturing, so a press that is the program's never reaches xterm (which would start a selection). */
  private mouseDownEvent = (e: MouseEvent) => {
    if (this.mouseHeld) { // another button while one is held: swallowed
      e.preventDefault();
      e.stopPropagation();
      return;
    }
    if (this.replaying) return; // the press below, handed to xterm
    this.mouseSent = false;
    const button = mouseButton(e.button);
    if (!button || !this.mouseOn()) return;
    if (mouseRoute(this.view.mouse, e, this.pointerKind) !== "program") return this.selectHere(e);
    const cell = this.mouseCell(e, false);
    if (!cell) return;
    e.preventDefault(); // no page selection, no middle-click autoscroll
    e.stopPropagation();
    this.term?.clearSelection(); // what was selected with Shift goes: the program has the pointer now
    this.term?.focus();
    this.mouseSent = true;
    this.mouseHeld = { button, cell, throttle: new CellThrottle() };
    this.mouseHeld.throttle.reset(cell);
    this.sendMouse("down", button, cell, mouseModifiers(e));
    window.addEventListener("mousemove", this.mouseMove, true);
    window.addEventListener("mouseup", this.mouseUp, true);
    window.addEventListener("blur", this.mouseLost);
  };

  /**
   * Shift+press for the page. xterm reads Shift as "extend the selection" and, with none to extend, starts nothing, so a
   * Shift+drag would select nothing: its press goes on without Shift (the drag that follows is read from coordinates).
   */
  private selectHere(e: MouseEvent) {
    const target = e.target;
    if (!e.shiftKey || e.button !== 0 || !(target instanceof Element)) return;
    e.stopPropagation();
    e.preventDefault();
    this.replaying = true;
    try {
      target.dispatchEvent(
        new MouseEvent("mousedown", {
          bubbles: true,
          cancelable: true,
          composed: true,
          view: window,
          detail: e.detail,
          button: e.button,
          buttons: e.buttons,
          clientX: e.clientX,
          clientY: e.clientY,
          screenX: e.screenX,
          screenY: e.screenY,
          ctrlKey: e.ctrlKey,
          altKey: e.altKey,
          metaKey: e.metaKey,
          shiftKey: false,
        }),
      );
    } finally {
      this.replaying = false;
    }
    this.term?.focus();
  }

  /** Motion with the button held, one event per cell; it follows the pointer out of the panel (clamped to the screen). */
  private mouseMove = (e: MouseEvent) => {
    const h = this.mouseHeld;
    if (!h) return;
    if (!this.mouseOn()) return this.mouseEnd();
    if (e.buttons === 0) return this.mouseUp(e); // the release happened where no event reached us
    const cell = this.mouseCell(e, true);
    if (!cell || !h.throttle.step(cell)) return;
    h.cell = cell;
    this.sendMouse("drag", h.button, cell, mouseModifiers(e));
  };

  private mouseUp = (e: MouseEvent) => {
    const h = this.mouseHeld;
    if (!h || (e.type === "mouseup" && mouseButton(e.button) !== h.button)) return;
    const cell = this.mouseCell(e, true) ?? h.cell;
    const on = this.mouseOn();
    this.mouseEnd();
    if (on) this.sendMouse("up", h.button, cell, mouseModifiers(e));
  };

  /** The window lost focus with a button down: let go where it was, so the program is not left with a held button. */
  private mouseLost = () => {
    const h = this.mouseHeld;
    const on = this.mouseOn();
    this.mouseEnd();
    if (h && on) this.sendMouse("up", h.button, h.cell, 0);
  };

  private mouseEnd() {
    this.mouseHeld = null;
    window.removeEventListener("mousemove", this.mouseMove, true);
    window.removeEventListener("mouseup", this.mouseUp, true);
    window.removeEventListener("blur", this.mouseLost);
  }

  /** A right click that is the program's must not also open the browser's menu (Persona's comes from the panel, below). */
  private mouseContextMenu = (e: MouseEvent) => {
    if (this.mouseOn() && contextAction(e, true, this.view.mouse, this.pointerKind) === "program") e.preventDefault();
  };

  /** Whether this right click opens Persona's menu (TerminalPanel); Shift and the mode decide while the mouse is forwarded. */
  wantsMenu(e: MouseEvent, pointerType?: string): boolean {
    return contextAction(e, this.mouseOn(), this.view.mouse, pointerType) === "menu";
  }

  /** "Mouse" in "Terminal settings" (remembered): who gets the pointer while driving. */
  setMouse(mode: MouseMode) {
    if (mode === "program") localStorage.removeItem(MOUSE_KEY);
    else localStorage.setItem(MOUSE_KEY, mode);
    this.patch({ mouse: mode });
  }

  // ── what the user chose ───────────────────────────────

  /** "Take control" / "View only" (remembered): subscribe again the other way. */
  setWants(on: boolean) {
    if (on === this.view.wants) return;
    if (on) localStorage.removeItem(WATCH_KEY);
    else localStorage.setItem(WATCH_KEY, "1");
    const control: Control = !on || this.view.control === "declined" ? "off" : this.view.control;
    if (on) this.leaveHistory();
    this.patch({ wants: on, control, why: "", note: "" });
    if (this.subscribed && this.live()) this.subscribeNow(false);
    this.inputMode();
  }

  /** Take it from the program or device that holds it (after busy, taken, lost). */
  takeOver() {
    this.leaveHistory();
    this.takeover = true;
    this.patch({ control: "starting", why: "", note: "" });
    if (this.view.mode === "stream" && this.live()) this.subscribeNow(true);
    this.inputMode();
  }

  /** Only watch what another program on the computer drives (the hub already mirrors it). */
  watchOnly() {
    this.patch({ control: "declined", why: "chosen", note: t("session-terminal-feed.watch_only_chosen") });
    this.inputMode();
    this.term?.focus();
  }

  /** "Reconnect": a fresh subscription (a whole screen to start from), or a read now. One still unanswered goes first,
   * at the hub too, so this one has it ask the node again (the same subscription again would only be told what it has). */
  retry() {
    this.nextTry = 0;
    if (this.view.mode === "stream") {
      if (this.live()) {
        if (this.asked !== null && this.subscribed) this.drop();
        this.subscribeNow(false);
      }
    } else {
      this.polling?.abort();
      this.polling = null;
      this.lastPoll = null;
      void this.poll();
    }
  }

  // ── the options in "Terminal settings" ───────────────────

  setCursorStyle(style: CursorStyle) {
    if (style === DEFAULT_CURSOR) localStorage.removeItem(CURSOR_KEY);
    else localStorage.setItem(CURSOR_KEY, style);
    this.patch({ cursorStyle: style });
    if (this.term) this.term.options.cursorStyle = style;
  }

  setCursorBlink(on: boolean) {
    if (on) localStorage.setItem(BLINK_KEY, "1");
    else localStorage.removeItem(BLINK_KEY);
    this.patch({ cursorBlink: on });
    if (this.term) this.term.options.cursorBlink = on;
  }

  /** WebGL drawing instead of the DOM renderer (off by default: the default look is unchanged). */
  setWebgl(on: boolean) {
    if (on) localStorage.setItem(WEBGL_KEY, "1");
    else localStorage.removeItem(WEBGL_KEY);
    this.patch({ webgl: on });
    if (on) void this.enableWebgl();
    else this.dropWebgl();
  }

  /** The addon loads on first use (its own chunk). A browser without WebGL, or a lost context, goes back to the default renderer. */
  private async enableWebgl() {
    if (this.webglAddon || !this.term) return;
    try {
      const { WebglAddon } = await import("@xterm/addon-webgl");
      if (this.disposed || !this.term || !this.view.webgl || this.webglAddon) return;
      const gl = new WebglAddon();
      gl.onContextLoss(() => {
        this.dropWebgl();
        this.turnOffWebgl(t("session-terminal-feed.webgl_lost"));
      });
      this.term.loadAddon(gl);
      this.webglAddon = gl;
    } catch {
      this.webglAddon = null;
      if (!this.disposed) this.turnOffWebgl(t("session-terminal-feed.webgl_unavailable"));
    }
  }

  private turnOffWebgl(why: string) {
    localStorage.removeItem(WEBGL_KEY);
    this.patch({ webgl: false });
    toast(why);
  }

  /** Lets go of the GL context (off, or lost): xterm draws with the DOM again. */
  private dropWebgl() {
    const gl = this.webglAddon;
    this.webglAddon = null;
    try {
      gl?.dispose();
    } catch {
      // already disposed by the context loss
    }
  }

  // ── search (lib/terminal-search.ts: what is searchable and in which order) ──

  /** The plain lines the page itself holds: herdr's history above, and the screen too when the page draws it. */
  private findLines(): string[] {
    return this.drawn() ? [...this.olderPlain, ...this.screenPlain()] : this.olderPlain;
  }

  /** xterm's grid is searched by its addon, except while the page draws the screen (xterm is hidden then). */
  private screenCount(): number {
    return this.search && !this.drawn() ? this.found.screen.count : 0;
  }

  private searchOptions() {
    return { caseSensitive: this.found.cs, decorations: findDecorations() };
  }

  /**
   * Looks for `query`. dir 0 starts afresh and lands on the match nearest the bottom (the newest output); 1 / -1 step to
   * the next / previous one, wrapping around, through the page's rows first and then xterm's screen.
   */
  find(query: string, caseSensitive: boolean, dir: 0 | 1 | -1) {
    const f = this.found, t = this.term, search = this.search;
    if (!query || !t || !search) return this.clearFind();
    const fresh = dir === 0 || query !== f.query || caseSensitive !== f.cs || !f.pos;
    f.query = query;
    f.cs = caseSensitive;
    f.busy = true;
    try {
      if (fresh) {
        f.drawn = this.drawn();
        f.rows = findInLines(this.findLines(), query, caseSensitive);
        f.base = this.olderPlain.length;
        f.screen = { index: -1, count: 0 };
        t.clearSelection();
        search.clearDecorations(); // also forgets the last term: the addon does not notice a changed "Match case" on its own
        if (!f.drawn) search.findPrevious(query, this.searchOptions()); // selects the last match on screen and reports how many there are
        f.pos = stepFind(null, -1, f.rows.length, this.screenCount());
        if (f.pos?.seg === "rows") {
          t.clearSelection();
          search.clearActiveDecoration();
        }
      } else if (f.pos) {
        const next = stepFind(f.pos, dir === -1 ? -1 : 1, f.rows.length, this.screenCount());
        if (next?.seg === "screen") {
          if (f.pos.seg !== "screen") t.clearSelection(); // coming from the rows: start from the top (or the bottom) of the screen
          if (dir === -1) search.findPrevious(query, this.searchOptions());
          else search.findNext(query, this.searchOptions());
          if (f.screen.index >= 0) next.i = f.screen.index;
        } else if (next) {
          t.clearSelection();
          search.clearActiveDecoration();
        }
        f.pos = next;
      }
    } finally {
      f.busy = false;
    }
    this.publishFind();
  }

  /** Closes the search: highlights go (the selected match stays selected, to copy). */
  clearFind() {
    const f = this.found;
    f.query = "";
    f.pos = null;
    f.rows = [];
    f.screen = { index: -1, count: 0 };
    this.search?.clearDecorations();
    this.patch({ find: null, marks: null });
  }

  /** The addon counted the matches on xterm's screen (also when output changed them). */
  private screenResults(index: number, count: number) {
    const f = this.found;
    f.screen = { index, count };
    if (!f.query) return;
    if (!f.busy) {
      if (f.pos?.seg === "screen") f.pos.i = Math.max(0, index);
      else if (f.pos) {
        this.term?.clearSelection(); // the current match is among the page's rows: the addon's refresh must not select another
        this.search?.clearActiveDecoration();
      }
    }
    this.publishFind();
  }

  private publishFind() {
    const f = this.found;
    if (!f.query) return void this.patch({ find: null, marks: null });
    const total = f.rows.length + this.screenCount();
    this.patch({
      find: { index: f.pos && total ? findIndex(f.pos, f.rows.length) : -1, count: total },
      marks: marksFor(f.rows, f.pos?.seg === "rows" ? f.pos.i : null),
    });
  }

  private recountSoon() {
    if (this.recountFrame) return;
    this.recountFrame = requestAnimationFrame(() => {
      this.recountFrame = 0;
      if (!this.disposed) this.recount();
    });
  }

  /** The page's rows changed (a page of history above, the drawn screen, "Wrap" switched): the same match, new numbers. */
  private recount() {
    const f = this.found;
    if (!f.query) return;
    if (this.drawn() !== f.drawn) return void this.find(f.query, f.cs, 0);
    const old = f.pos?.seg === "rows" ? f.rows[f.pos.i] : undefined;
    const shift = this.olderPlain.length - f.base;
    const rows = findInLines(this.findLines(), f.query, f.cs);
    const same = rows.length === f.rows.length && shift === 0 && rows.every((m, i) => m.row === f.rows[i].row && m.start === f.rows[i].start);
    if (same) return;
    f.rows = rows;
    f.base = this.olderPlain.length;
    if (old) {
      const i = rows.findIndex((m) => m.row === old.row + shift && m.start === old.start);
      f.pos = i >= 0 ? { seg: "rows", i } : null; // the match is gone ("Back to latest"): the next step starts afresh
    }
    this.publishFind();
  }

  // ── health ────────────────────────────────────────────

  private healthy(now: number) {
    this.lastOK = now;
    this.since = null;
    this.failures = 0;
    this.patch({ failure: null, stale: false });
  }

  private failed(why: string) {
    this.failures++;
    const now = Date.now();
    this.nextTry = now + backoff(this.failures) * 1000;
    this.patch({ failure: why, stale: true });
  }

  // ── history (iOS TerminalFeed.loadOlder) ───────────────

  /** The plain lines on screen now: xterm's grid, or the last read while polling. */
  private screenPlain(): string[] {
    const t = this.term;
    if (this.view.mode === "poll") return parseAnsiRows(this.pollText).map(plainRow);
    if (!t) return [];
    const b = t.buffer.active;
    const out: string[] = [];
    for (let y = 0; y < t.rows; y++) out.push(b.getLine(b.viewportY + y)?.translateToString(true) ?? "");
    return out;
  }

  /**
   * The page of herdr's history before the first lines shown (watching only: driving, the program scrolls its own).
   * The screen holds still meanwhile; with nothing above (a full-screen program) it keeps following.
   */
  async loadOlder() {
    const v = this.view;
    if (v.loadingOlder || !v.hasOlder || !v.loaded || !this.term || v.control === "active" || v.control === "starting") return;
    const wasPaused = v.paused;
    this.patch({ loadingOlder: true, paused: true });
    const { anchor, loaded } = historyAnchor([...this.olderPlain, ...this.screenPlain()]);
    try {
      const r = await api<{ text?: string; has_more?: boolean; at_limit?: boolean }>(`${sessionPath(this.mid, this.sid)}/screen/before`, {
        method: "POST",
        json: { anchor, lines: HISTORY_PAGE, loaded, format: "ansi" },
      });
      if (this.disposed || !this.view.paused) return;
      const rows = r.text ? parseAnsiRows(r.text) : [];
      if (rows.length) this.olderPlain = [...rows.map(plainRow), ...this.olderPlain];
      this.patch({ older: rows.length ? [...rows, ...this.view.older] : this.view.older, hasOlder: Boolean(r.has_more), atLimit: Boolean(r.at_limit) });
    } catch (e) {
      if (!this.disposed && !isAbort(e)) toast(t("session-terminal-feed.older_failed", { error: errorText(e) }), "error");
    } finally {
      if (!this.disposed) {
        this.patch({ loadingOlder: false });
        if (!this.view.older.length && !wasPaused) this.resume();
        else this.recount();
      }
    }
  }

  /** "Back to latest": history goes, the screen follows the pane again (what arrived meanwhile is drawn now). */
  resume() {
    this.olderPlain = [];
    this.recount();
    const wasPaused = this.view.paused;
    this.patch({ older: [], hasOlder: true, atLimit: false, paused: false, loadingOlder: false });
    if (!wasPaused) return;
    if (this.view.mode === "stream") {
      const held = this.held, overflow = this.heldOverflow;
      this.held = [];
      this.heldBytes = 0;
      this.heldOverflow = false;
      if (overflow) {
        this.order.reset(); // what was skipped is not on screen: continue from a whole frame
        if (this.live()) this.subscribeNow(false);
      } else {
        for (const h of held) this.draw(h.frame, h.bytes);
      }
    } else {
      this.nextTry = 0;
    }
    this.screenChanged();
  }

  /** Driving now: what was read of the pane's history at the computer's size goes. */
  private leaveHistory() {
    if (this.view.paused || this.view.older.length) this.resume();
  }

  // ── "Wrap" and the font size ─────────────────────────────

  setWrap(on: boolean) {
    if (on) localStorage.setItem(WRAP_KEY, "1");
    else localStorage.removeItem(WRAP_KEY);
    this.patch({ wrap: on });
    this.screenChanged();
  }

  setFontSize(size: number) {
    const n = Math.max(FONT_SIZES.min, Math.min(FONT_SIZES.max, Math.round(size)));
    if (n === this.view.fontSize) return;
    if (n === FONT_SIZES.default) localStorage.removeItem(FONT_KEY);
    else localStorage.setItem(FONT_KEY, String(n));
    this.patch({ fontSize: n });
    if (this.term) {
      this.term.options.fontSize = n;
      this.measure(true); // a new grid: driving, the pane takes it
      this.sizeElement();
      requestAnimationFrame(() => this.sizeElement()); // xterm sizes its screen for the new cells as it renders
    }
  }

  // ── the screen the page draws ("Wrap" while watching) ────

  /** The page draws the screen (rather than xterm): "Wrap" is on and the user only watches. */
  private drawn() {
    const c = this.view.control;
    return this.view.wrap && c !== "active" && c !== "starting";
  }

  /** Something was written (or what draws the screen changed): read the rows again, once per frame. */
  private screenChanged() {
    if (this.found.query) this.recountSoon();
    if (!this.drawn()) {
      if (this.view.screen) this.patch({ screen: null });
      return;
    }
    if (this.screenFrame) return;
    this.screenFrame = requestAnimationFrame(() => {
      this.screenFrame = 0;
      if (this.disposed || !this.drawn()) return;
      this.patch({ screen: this.readScreen() });
    });
  }

  private readScreen(): ScreenRows {
    if (this.view.mode === "poll") {
      const rows = parseAnsiRows(this.pollText);
      while (rows.length && !plainRow(rows[rows.length - 1])) rows.pop();
      return { rows, cursor: null };
    }
    const t = this.term!;
    const b = t.buffer.active;
    const rows: CellRow[] = [];
    for (let y = 0; y < t.rows; y++) {
      const line = b.getLine(b.viewportY + y);
      rows.push(line ? gridRow(t.cols, (x) => line.getCell(x)) : { runs: [] });
    }
    // the program hid it (Claude Code draws its own): xterm keeps that in its core, not its API
    const hidden = (t as unknown as { _core?: { coreService?: { isCursorHidden?: boolean } } })._core?.coreService?.isCursorHidden;
    const cursor = hidden === false ? { row: b.cursorY, col: b.cursorX } : null;
    // a shell after `clear` has its prompt at the top and empty rows under it: the screen ends at the cursor or the last text
    let end = rows.length;
    while (end > 0 && end - 1 > (cursor?.row ?? -1) && !plainRow(rows[end - 1]) && !rows[end - 1].runs.some((r) => r.style.bg !== undefined)) end--;
    return { rows: rows.slice(0, end), cursor };
  }

  // ── polling (no stream) ───────────────────────────────

  private async poll() {
    const ctrl = new AbortController();
    this.polling = ctrl;
    const rows = this.grid?.rows ?? this.term?.rows ?? 40;
    try {
      const r = await api<{ text?: string; status?: string }>(`${sessionPath(this.mid, this.sid)}/screen?format=ansi&lines=${rows}`, { signal: ctrl.signal });
      if (this.disposed || ctrl.signal.aborted || this.view.mode !== "poll" || !this.term) return;
      if (this.grid) this.resize(this.grid.cols, this.grid.rows);
      const text = r.text ?? "";
      if (text !== this.lastPoll) {
        this.lastPoll = text;
        this.pollText = text;
        this.term.reset();
        // no cursor in a read; rows end in \r\n (plain text from an old node in \n)
        this.term.write("\x1b[?25l" + text.replace(/\r?\n/g, "\r\n").replace(/(\r\n)+$/, ""));
        this.screenChanged();
      }
      const now = Date.now();
      this.healthy(now);
      this.nextTry = now + POLL_MS;
      this.patch({ offline: r.status === "offline", loaded: true });
    } catch (e) {
      if (isAbort(e) || this.disposed) return;
      this.failed(errorText(e));
      this.patch({ loaded: true });
    } finally {
      if (this.polling === ctrl) this.polling = null;
    }
  }
}
