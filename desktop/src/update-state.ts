/**
 * Update state and scheduling with no Electron dependency, so every rule here is unit-tested.
 *
 * Default in one sentence: Persona checks for an update when it opens and every four hours, downloads it in the
 * background, and installs it only when the person quits or chooses "Restart to update".
 */

/** Time between scheduled checks. Fixed, with no random jitter, so the schedule is the same on every run. */
export const CHECK_INTERVAL_MS = 4 * 60 * 60 * 1000;

export type UpdatePhase = "idle" | "checking" | "downloading" | "ready";
/** The outcome of the last check the person asked for. A scheduled check never leaves one. */
export type UpdateNotice = "current" | "failed";

export interface UpdateState {
  readonly phase: UpdatePhase;
  /** The version being downloaded, or downloaded and waiting for a restart. */
  readonly version?: string;
  /** The check in flight was asked for by the person, so its outcome is shown. */
  readonly manual: boolean;
  readonly notice?: UpdateNotice;
}

export type UpdateEvent =
  | { readonly type: "check"; readonly manual: boolean }
  | { readonly type: "available"; readonly version: string }
  | { readonly type: "current" }
  | { readonly type: "downloaded"; readonly version?: string }
  | { readonly type: "failed" };

export const INITIAL_UPDATE_STATE: UpdateState = { phase: "idle", manual: false };

/** Returns the same object when nothing changes, so callers can skip redrawing. */
export function reduceUpdate(state: UpdateState, event: UpdateEvent): UpdateState {
  // A downloaded update stays offered until the app restarts; nothing later may withdraw or replace it.
  if (state.phase === "ready") return state;
  switch (event.type) {
    case "check":
      if (state.phase === "idle") return { phase: "checking", manual: event.manual };
      // Asking while a scheduled check is in flight makes that check's outcome visible instead of starting another.
      return event.manual && !state.manual ? { ...state, manual: true } : state;
    case "available":
      return { phase: "downloading", version: event.version, manual: state.manual };
    case "downloaded":
      return { phase: "ready", ...((event.version ?? state.version) !== undefined ? { version: event.version ?? state.version } : {}), manual: false };
    case "current":
      if (state.phase !== "checking") return state;
      return state.manual ? { phase: "idle", manual: false, notice: "current" } : { phase: "idle", manual: false };
    case "failed":
      // electron-updater reports one failure as both an event and a rejected promise; the second one is a no-op.
      if (state.phase === "idle") return state;
      return state.manual ? { phase: "idle", manual: false, notice: "failed" } : { phase: "idle", manual: false };
  }
}

export type UpdateAction = "check" | "restart";
export interface UpdateMenuItem {
  readonly label: string;
  /** Absent for a status line, which is shown disabled. */
  readonly action?: UpdateAction;
}

/** The locale keys the tray's update section reads (`desktop/src/locales`). */
export const UPDATE_STRING_KEYS = ["update_check", "update_checking", "update_downloading", "update_restart", "update_current", "update_failed"] as const;
export type UpdateStringKey = typeof UPDATE_STRING_KEYS[number];

/** The tray's update section: at most one status line and one action. */
export function updateMenu(state: UpdateState, strings: Readonly<Record<string, string>>): UpdateMenuItem[] {
  const text = (key: UpdateStringKey): string => (strings[key] ?? key).replace("{version}", state.version ?? "");
  switch (state.phase) {
    case "checking": return [{ label: text("update_checking") }];
    case "downloading": return [{ label: text("update_downloading") }];
    case "ready": return [{ label: text("update_restart"), action: "restart" }];
    case "idle": {
      const check: UpdateMenuItem = { label: text("update_check"), action: "check" };
      if (state.notice === "current") return [{ label: text("update_current") }, check];
      if (state.notice === "failed") return [{ label: text("update_failed") }, check];
      return [check];
    }
  }
}

/** Updates exist only for the packaged macOS and Windows apps; development runs and other systems have no feed. */
export function updatesSupported(packaged: boolean, platform: string, disabled: boolean): boolean {
  return packaged && !disabled && (platform === "darwin" || platform === "win32");
}

export interface UpdateDeps {
  /** Starts one check. Its outcome arrives through `dispatch`; a rejection counts as a failure. */
  check(): Promise<unknown>;
  /** Quits and installs the downloaded update. Only ever called from `restart()`. */
  install(): void;
  setTimer(run: () => void, milliseconds: number): unknown;
  clearTimer(handle: unknown): void;
  changed(state: UpdateState): void;
}

export class UpdateController {
  private current: UpdateState = INITIAL_UPDATE_STATE;
  private timer: unknown;
  private started = false;

  constructor(private readonly deps: UpdateDeps, private readonly interval: number = CHECK_INTERVAL_MS) {}

  get state(): UpdateState { return this.current; }

  /** Checks now, then on a fixed interval. A failed check is simply retried by the next scheduled one. */
  start(): void {
    if (this.started) return;
    this.started = true;
    this.tick();
  }

  stop(): void {
    this.started = false;
    if (this.timer !== undefined) this.deps.clearTimer(this.timer);
    this.timer = undefined;
  }

  /** The person asked: the menu shows Checking at once, and the outcome stays visible afterwards. */
  check(): void { this.request(true); }

  /** Installs only on the person's own action; nothing in this class restarts the app by itself. */
  restart(): void { if (this.current.phase === "ready") this.deps.install(); }

  dispatch(event: UpdateEvent): void {
    const next = reduceUpdate(this.current, event);
    if (next === this.current) return;
    this.current = next;
    // Once an update waits for a restart there is nothing more to look for in this run.
    if (next.phase === "ready" && this.timer !== undefined) { this.deps.clearTimer(this.timer); this.timer = undefined; }
    this.deps.changed(next);
  }

  private tick(): void {
    this.timer = undefined;
    if (!this.started || this.current.phase === "ready") return;
    this.request(false);
    this.timer = this.deps.setTimer(() => this.tick(), this.interval);
  }

  private request(manual: boolean): void {
    const idle = this.current.phase === "idle";
    this.dispatch({ type: "check", manual });
    if (!idle) return;
    void Promise.resolve().then(() => this.deps.check()).then(
      // A check that settles without saying "current" or "available" gave no answer.
      () => { if (this.current.phase === "checking") this.dispatch({ type: "failed" }); },
      () => { this.dispatch({ type: "failed" }); },
    );
  }
}
