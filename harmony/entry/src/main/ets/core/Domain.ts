/** Pure client behavior shared by the Harmony models. This module has no platform calls. */

export interface ScopeKey {
  readonly groupId: string;
  /** The default manager is the empty string. Keep it distinct from every named manager. */
  readonly managerId: string;
}

export function sameScope(left: ScopeKey, right: ScopeKey): boolean {
  return left.groupId === right.groupId && left.managerId === right.managerId;
}

/** A collision-safe storage key for a hub group and its manager owner. */
export function scopeKey(scope: ScopeKey): string {
  return JSON.stringify([scope.groupId, scope.managerId]);
}

/** Use an item's returned owner when a merged response supplies one. */
export function ownedScopeForItem(
  groupId: string,
  selectedManagerId: string,
  returnedOwnerId?: string | null,
): ScopeKey {
  return {
    groupId,
    managerId: returnedOwnerId === undefined || returnedOwnerId === null ? selectedManagerId : returnedOwnerId,
  };
}

export interface ScopeRequestToken {
  readonly scope: ScopeKey;
  readonly generation: number;
}

/**
 * Fences late reads when the visible group/manager changes. The owner still cancels its
 * transport task; `isCurrent` prevents a transport that ignores cancellation from publishing.
 */
export class ScopeGeneration {
  private activeScope: ScopeKey | null = null;
  private generation = 0;

  select(scope: ScopeKey): ScopeRequestToken {
    if (this.activeScope === null || !sameScope(this.activeScope, scope)) {
      this.activeScope = { groupId: scope.groupId, managerId: scope.managerId };
      this.generation += 1;
    }
    return this.tokenForActiveScope();
  }

  /** Invalidate outstanding work after a pairing change, sign-out, or explicit refresh reset. */
  invalidate(): ScopeRequestToken | null {
    if (this.activeScope === null) return null;
    this.generation += 1;
    return this.tokenForActiveScope();
  }

  currentToken(): ScopeRequestToken | null {
    if (this.activeScope === null) return null;
    return this.tokenForActiveScope();
  }

  isCurrent(token: ScopeRequestToken): boolean {
    return this.activeScope !== null && this.generation === token.generation && sameScope(this.activeScope, token.scope);
  }

  private tokenForActiveScope(): ScopeRequestToken {
    const scope = this.activeScope;
    if (scope === null) throw new Error("No active scope");
    return { scope: { groupId: scope.groupId, managerId: scope.managerId }, generation: this.generation };
  }
}

export interface SessionCapabilityFlags {
  readonly read?: boolean;
  readonly send?: boolean;
  readonly interrupt?: boolean;
  readonly approve?: boolean;
  readonly terminal?: boolean;
  readonly attach?: boolean;
}

export interface SessionCapabilityInput {
  readonly sourceKind?: string | null;
  readonly capabilities?: SessionCapabilityFlags | null;
  readonly readOnly?: boolean;
  readonly readOnlyReason?: string | null;
  readonly attached?: boolean | null;
}

export interface EffectiveSessionCapabilities {
  readonly hasSource: boolean;
  readonly readOnly: boolean;
  readonly canRead: boolean;
  readonly canSend: boolean;
  readonly canInterrupt: boolean;
  readonly canApprove: boolean;
  readonly canTerminal: boolean;
  readonly canAttach: boolean;
}

/**
 * Old sessions without `source_kind` keep legacy access. A sourced session opts in one
 * capability at a time; missing fields refuse that operation. Explicit read-only and identity
 * conflict metadata narrow writes; `read_only_reason` explains the source state but is not a gate.
 * Attachment is available only until the hub reports it attached.
 */
export function sessionCapabilities(input: SessionCapabilityInput): EffectiveSessionCapabilities {
  const sourceKind = input.sourceKind ?? "";
  const hasSource = sourceKind.length > 0;
  const identityConflict = sourceKind === "identity_conflict";
  const readOnly = input.readOnly === true || identityConflict;
  const raw = input.capabilities;
  const allows = (capability: keyof SessionCapabilityFlags): boolean => !hasSource || raw?.[capability] === true;

  return {
    hasSource,
    readOnly,
    canRead: allows("read"),
    canSend: !readOnly && allows("send"),
    canInterrupt: !readOnly && allows("interrupt"),
    canApprove: !readOnly && allows("approve"),
    canTerminal: !readOnly && allows("terminal"),
    canAttach: hasSource && !readOnly && input.attached !== true && raw?.attach === true,
  };
}

export type InteractionAnswerState =
  | "pending"
  | "answering"
  | "answered"
  | "expired"
  | "resolvedElsewhere"
  | "handedToTerminal"
  | "unknown";

/** The unified answer flow only begins a new answer for a known pending card. */
export function canSubmitInteractionAnswer(state: InteractionAnswerState): boolean {
  return state === "pending";
}

export function canAnswerInteraction(
  state: InteractionAnswerState,
  session: SessionCapabilityInput | null,
  sessionless: boolean,
): boolean {
  if (!canSubmitInteractionAnswer(state)) return false;
  if (sessionless) return true;
  if (session === null) return false;
  return sessionCapabilities(session).canApprove;
}

export type JsonValue = string | number | boolean | null | JsonObject | readonly JsonValue[];

export interface JsonObject {
  readonly [key: string]: JsonValue | undefined;
}

export interface TranscriptFold extends JsonObject {
  readonly through_seq: number;
  readonly through_id: string;
  readonly items?: number;
  readonly tools?: number;
  readonly thoughts?: number;
  readonly supervision_user?: boolean;
}

export interface TranscriptItem extends JsonObject {
  readonly id: string;
  readonly seq: number;
  readonly kind: string;
  readonly role?: string;
  readonly text?: string;
  readonly tool_use_id?: string;
  readonly toolUseId?: string;
  readonly supervision_user?: boolean;
  readonly fold?: TranscriptFold;
}

export type TranscriptGap = "none" | "fill" | "reload";

export interface TranscriptInitialState {
  readonly items: readonly TranscriptItem[];
  readonly covered: number;
  readonly history: string | null;
  readonly hasMore: boolean | null;
  readonly loadedOnce: boolean;
}

export interface TranscriptBatch {
  readonly items: readonly TranscriptItem[];
  readonly history?: string | null;
  readonly fromSeq?: number | null;
  readonly toSeq?: number | null;
  readonly backlog?: boolean;
  readonly hasMore?: boolean | null;
  readonly moreAfter?: boolean;
}

export type TranscriptAction =
  | { readonly kind: "fetch_after"; readonly afterSeq: number }
  | { readonly kind: "fetch_latest"; readonly foldWork: true }
  | { readonly kind: "subscribe"; readonly sinceSeq?: number; readonly foldWork: true }
  | { readonly kind: "fetch_older"; readonly beforeSeq: number; readonly foldWork: true }
  | { readonly kind: "fetch_range"; readonly fromSeq: number; readonly throughSeq: number };

export interface TranscriptSnapshot {
  readonly items: readonly TranscriptItem[];
  readonly covered: number;
  readonly history: string | null;
  readonly hasMore: boolean | null;
  readonly loadedOnce: boolean;
}

function cloneJson(value: JsonValue): JsonValue {
  if (Array.isArray(value)) {
    const copied: JsonValue[] = [];
    for (const entry of value) copied.push(cloneJson(entry));
    return copied;
  }
  if (typeof value === "object" && value !== null) {
    const copied: { [key: string]: JsonValue | undefined } = {};
    for (const key of Object.keys(value)) {
      const child = value[key];
      if (child !== undefined) copied[key] = cloneJson(child);
    }
    return copied;
  }
  return value;
}

function cloneItem(item: TranscriptItem): TranscriptItem {
  return cloneJson(item) as TranscriptItem;
}

function itemPart(id: string): number {
  const suffix = id.slice(id.lastIndexOf("#") + 1);
  if (!/^[+-]?\d+$/.test(suffix)) return 0;
  const part = Number(suffix);
  return Number.isInteger(part) ? part : 0;
}

function compareItems(left: TranscriptItem, right: TranscriptItem): number {
  if (left.seq !== right.seq) return left.seq - right.seq;
  return itemPart(left.id) - itemPart(right.id);
}

function isWorkItem(item: TranscriptItem): boolean {
  if (item.kind === "thinking" || item.kind === "tool_use" || item.kind === "tool_result" || item.kind === "fold") return true;
  // A fold range distinguishes assistant narration from its final answer.
  if (item.kind === "text") return item.role === "assistant";
  if (item.kind !== "image") return false;
  const toolUseId = item.tool_use_id ?? item.toolUseId;
  return typeof toolUseId === "string" && toolUseId.length > 0;
}

function foldContains(fold: TranscriptItem, other: TranscriptItem): boolean {
  const range = fold.fold;
  if (range === undefined) return false;
  const atOrAfterStart = other.seq > fold.seq ||
    (other.seq === fold.seq && itemPart(other.id) >= itemPart(fold.id));
  const atOrBeforeEnd = other.seq < range.through_seq ||
    (other.seq === range.through_seq && itemPart(other.id) <= itemPart(range.through_id));
  return atOrAfterStart && atOrBeforeEnd;
}

function hasSupervisionEvidence(item: TranscriptItem): boolean {
  return item.supervision_user === true || item.fold?.supervision_user === true;
}

function markSupervisionVisible(item: TranscriptItem): TranscriptItem {
  if (item.fold === undefined) return { ...item, supervision_user: true };
  return { ...item, supervision_user: true, fold: { ...item.fold, supervision_user: true } };
}

function retainSupervisionEvidence(
  item: TranscriptItem,
  promotedIds: ReadonlySet<string>,
  promotedFolds: readonly TranscriptItem[],
  promotedWork: readonly TranscriptItem[],
): TranscriptItem {
  const shouldKeep = promotedIds.has(item.id) ||
    (isWorkItem(item) && promotedFolds.some((fold) => foldContains(fold, item))) ||
    (item.kind === "fold" && promotedWork.some((work) => foldContains(item, work)));
  return shouldKeep ? markSupervisionVisible(item) : item;
}

/** Whether this fold's represented work fits inside a range already opened in this history. */
function foldWasOpened(fold: TranscriptItem, opened: TranscriptItem): boolean {
  const currentRange = fold.fold;
  const openedRange = opened.fold;
  if (currentRange === undefined || openedRange === undefined) return false;
  return currentRange.through_seq < openedRange.through_seq ||
    (currentRange.through_seq === openedRange.through_seq &&
      itemPart(currentRange.through_id) <= itemPart(openedRange.through_id));
}

function sameFold(left: TranscriptFold | undefined, right: TranscriptFold | undefined): boolean {
  if (left === undefined || right === undefined) return left === right;
  return jsonEqual(left, right);
}

function jsonEqual(left: JsonValue | undefined, right: JsonValue | undefined): boolean {
  if (left === right) return true;
  if (left === undefined || right === undefined || left === null || right === null) return false;
  if (typeof left !== typeof right) return false;
  if (Array.isArray(left)) {
    if (!Array.isArray(right) || left.length !== right.length) return false;
    for (let index = 0; index < left.length; index += 1) {
      if (!jsonEqual(left[index], right[index])) return false;
    }
    return true;
  }
  if (typeof left === "object") {
    if (Array.isArray(right) || typeof right !== "object") return false;
    const leftKeys = Object.keys(left).sort();
    const rightKeys = Object.keys(right).sort();
    if (leftKeys.length !== rightKeys.length) return false;
    for (let index = 0; index < leftKeys.length; index += 1) {
      const key = leftKeys[index];
      if (key !== rightKeys[index] || !jsonEqual(left[key], right[key])) return false;
    }
    return true;
  }
  return false;
}

/** Reduces history pages and pushed transcript batches without transport or UI state. */
export class TranscriptReducer {
  private readonly byId = new Map<string, TranscriptItem>();
  private cachedPromotionIds = new Set<string>();
  private readonly openedFoldRanges = new Map<string, TranscriptItem>();
  private _history: string | null = null;
  private _covered = 0;
  private _hasMore: boolean | null = null;
  private _loadedOnce = false;

  get history(): string | null { return this._history; }
  get covered(): number { return this._covered; }
  get hasMore(): boolean | null { return this._hasMore; }
  get loadedOnce(): boolean { return this._loadedOnce; }

  get firstSeq(): number | null {
    let first: number | null = null;
    for (const item of this.byId.values()) first = first === null ? item.seq : Math.min(first, item.seq);
    return first;
  }

  get items(): readonly TranscriptItem[] {
    return [...this.byId.values()].sort(compareItems).map(cloneItem);
  }

  snapshot(): TranscriptSnapshot {
    return {
      items: this.items,
      covered: this._covered,
      history: this._history,
      hasMore: this._hasMore,
      loadedOnce: this._loadedOnce,
    };
  }

  restore(state: TranscriptInitialState): void {
    this.byId.clear();
    this.openedFoldRanges.clear();
    this.cachedPromotionIds.clear();
    for (const item of state.items) {
      if (item.supervision_user === true) this.cachedPromotionIds.add(item.id);
      this.byId.set(item.id, cloneItem(item));
    }
    this._history = state.history;
    this._covered = Math.max(0, state.covered);
    this._hasMore = state.hasMore;
    this._loadedOnce = state.loadedOnce;
  }

  latest(batch: TranscriptBatch): TranscriptGap {
    return this.apply({ ...batch, backlog: true });
  }

  apply(batch: TranscriptBatch): TranscriptGap {
    const history = batch.history ?? null;
    const fromSeq = batch.fromSeq ?? null;
    const toSeq = batch.toSeq ?? null;
    const backlog = batch.backlog === true;

    if (history !== null && this._history !== null && history !== this._history) {
      this.restore({ items: [], history, covered: 0, hasMore: null, loadedOnce: false });
      if (fromSeq !== null && fromSeq > 0) return "reload";
    }
    if (history !== null) this._history = history;

    if (toSeq === null) {
      this.merge(batch.items, backlog, batch.hasMore ?? null);
      let maxItemSeq = 0;
      for (const item of this.byId.values()) maxItemSeq = Math.max(maxItemSeq, item.seq);
      this._covered = Math.max(this._covered, maxItemSeq);
      return batch.moreAfter === true ? "reload" : "none";
    }

    if (fromSeq !== null) {
      this.merge(batch.items, backlog, batch.hasMore ?? null);
      if (this._covered === 0 && fromSeq > 0) return this._loadedOnce ? "reload" : "none";
      if (fromSeq > this._covered) return "fill";
      this._covered = Math.max(this._covered, toSeq);
      return batch.moreAfter === true ? "fill" : "none";
    }

    const first = batch.items[0];
    if (this._covered > 0 && first !== undefined && first.seq > this._covered + 1) {
      this.replace(batch.items, history, toSeq, batch.hasMore ?? true);
      if (backlog) this._loadedOnce = true;
      return "none";
    }
    this.merge(batch.items, backlog, batch.hasMore ?? null);
    this._covered = Math.max(this._covered, toSeq);
    return "none";
  }

  /** Reload the newest page while retaining newer pushes from the same transcript history. */
  replace(items: readonly TranscriptItem[], history: string | null, toSeq: number | null, hasMore: boolean): void {
    const keep: TranscriptItem[] = [];
    const sameHistory = history === null || history === this._history;
    if (sameHistory && toSeq !== null) {
      for (const item of this.byId.values()) if (item.seq > toSeq) keep.push(item);
    }
    this.openedFoldRanges.clear();
    this.byId.clear();
    this._hasMore = hasMore;
    if (history !== null) this._history = history;
    this.merge(items);
    this.merge(keep);
    let maxItemSeq = 0;
    for (const item of this.byId.values()) maxItemSeq = Math.max(maxItemSeq, item.seq);
    this._covered = toSeq ?? maxItemSeq;
  }

  /** Older pages add rows and change only an explicitly supplied pagination flag. */
  older(items: readonly TranscriptItem[], hasMore: boolean | null = null): void {
    if (hasMore !== null) this._hasMore = hasMore;
    this.merge(items);
  }

  /** Replace one fold with its fetched work, ignoring fold placeholders in range responses. */
  unfold(fold: TranscriptItem, work: readonly TranscriptItem[]): void {
    const retained = this.byId.get(fold.id);
    if (retained === undefined) return;
    this.byId.delete(fold.id);
    this.openedFoldRanges.set(fold.id, cloneItem(retained));
    this.merge(work.filter((item) => item.kind !== "fold"));
  }

  private merge(items: readonly TranscriptItem[], backlog = false, hasMore: boolean | null = null): void {
    const previous = [...this.byId.values()];
    const cached = new Map<string, TranscriptItem>();
    for (const item of previous) cached.set(item.id, item);
    const evidence = [...previous, ...this.openedFoldRanges.values(), ...items].filter(hasSupervisionEvidence);
    const promotedIds = new Set([...evidence.filter((item) => item.supervision_user === true).map((item) => item.id), ...this.cachedPromotionIds]);
    this.cachedPromotionIds.clear();
    const promotedFolds = evidence.filter((item) => item.fold !== undefined);
    const promotedWork = evidence.filter(isWorkItem);
    this.byId.clear();
    for (const [id, existing] of cached) {
      const promoted = retainSupervisionEvidence(existing, promotedIds, promotedFolds, promotedWork);
      this.byId.set(id, cloneItem(promoted));
    }
    const promotedItems = items.map((item) => retainSupervisionEvidence(item, promotedIds, promotedFolds, promotedWork));
    const batchWork = promotedItems.filter((item) => item.kind !== "fold" && isWorkItem(item));
    const freshItems = promotedItems.filter((item) => {
      if (item.kind !== "fold") return true;
      const opened = this.openedFoldRanges.get(item.id);
      if (opened !== undefined && foldWasOpened(item, opened)) return false;
      return !batchWork.some((work) => foldContains(item, work));
    });
    const newFolds: TranscriptItem[] = [];
    for (const item of freshItems) {
      if (item.kind !== "fold") continue;
      const old = this.byId.get(item.id);
      if (old === undefined || !sameFold(old.fold, item.fold)) newFolds.push(item);
    }

    if (newFolds.length > 0) {
      for (const [id, existing] of this.byId) {
        if (isWorkItem(existing) && newFolds.some((fold) => foldContains(fold, existing))) this.byId.delete(id);
      }
    }

    if (batchWork.length > 0) {
      for (const [id, existing] of this.byId) {
        if (existing.kind === "fold" && batchWork.some((work) => foldContains(existing, work))) this.byId.delete(id);
      }
    }

    let existingFirst = this.firstSeq;
    if (newFolds.length > 0) existingFirst = this.firstSeq;
    for (const item of freshItems) {
      const old = this.byId.get(item.id);
      if (old === undefined) {
        this.byId.set(item.id, cloneItem(item));
      } else if (!jsonEqual(old, item)) {
        this.byId.set(item.id, cloneItem(item));
      }
    }

    let reachesStart = false;
    if (items.length > 0) {
      let minSeq = items[0].seq;
      for (const item of items) minSeq = Math.min(minSeq, item.seq);
      reachesStart = minSeq <= (existingFirst ?? items[0].seq);
    }
    if (hasMore !== null && backlog && (!this._loadedOnce || this.byId.size <= items.length) && (hasMore || reachesStart)) {
      this._hasMore = hasMore;
    }
    if (backlog) this._loadedOnce = true;
  }
}

export interface TranscriptSyncResult {
  readonly gap: TranscriptGap;
  readonly actions: readonly TranscriptAction[];
}

/** Pure fetch decisions. Callers own timers, task cancellation, and HTTP/WebSocket execution. */
export class TranscriptSync {
  private filling = false;
  private fillPages = 0;
  private heard = 0;
  private asked = 0;
  private loadingOlder = false;
  private openingFold: TranscriptItem | null = null;
  private rangePages = 0;
  private readonly rangeWork: TranscriptItem[] = [];

  readonly reducer: TranscriptReducer;

  constructor(reducer: TranscriptReducer) {
    this.reducer = reducer;
  }

  settle(gap: TranscriptGap): readonly TranscriptAction[] {
    if (gap === "fill") { this.heard++; return this.continueFill(); }
    if (gap === "reload") return [{ kind: "fetch_latest", foldWork: true }];
    return [];
  }

  /** iOS starts immediately; an in-flight fill collapses duplicates and reads again after a move heard in flight. */
  continueFill(): readonly TranscriptAction[] {
    if (this.filling) return [];
    this.filling = true;
    this.fillPages = 0;
    this.asked = this.heard;
    return [{ kind: "fetch_after", afterSeq: this.reducer.covered }];
  }

  after(batch: TranscriptBatch): TranscriptSyncResult {
    const before = this.reducer.covered;
    const toSeq = batch.toSeq;
    const gap = this.reducer.apply(batch);
    this.fillPages += 1;
    const progressed = this.reducer.covered > before || toSeq !== undefined && toSeq !== null && toSeq > before;
    const again = gap === "none" && this.heard > this.asked;
    const actions: readonly TranscriptAction[] = gap === "reload" || ((gap === "fill" || again) && this.fillPages >= 8)
      ? [{ kind: "fetch_latest", foldWork: true }]
      : ((gap === "fill" && progressed) || again)
        ? [{ kind: "fetch_after", afterSeq: this.reducer.covered }]
        : [];
    if (actions.length > 0) this.asked = this.heard;
    if (actions.length === 0 || actions[0].kind === "fetch_latest") this.finishFill();
    return { gap, actions };
  }

  get behind(): boolean { return this.heard > this.asked; }

  finishFill(): void {
    this.filling = false;
    this.fillPages = 0;
  }

  reload(items: readonly TranscriptItem[], history: string | null, toSeq: number | null, hasMore: boolean): void {
    this.reducer.replace(items, history, toSeq, hasMore);
    this.finishFill();
  }

  reconnect(): readonly TranscriptAction[] {
    const covered = this.reducer.covered;
    return covered > 0
      ? [{ kind: "subscribe", sinceSeq: covered, foldWork: true }]
      : [{ kind: "subscribe", foldWork: true }];
  }

  loadOlder(): readonly TranscriptAction[] {
    if (this.loadingOlder || this.reducer.hasMore !== true) return [];
    const first = this.reducer.firstSeq;
    if (first === null) return [];
    this.loadingOlder = true;
    return [{ kind: "fetch_older", beforeSeq: first, foldWork: true }];
  }

  older(items: readonly TranscriptItem[], hasMore: boolean): void {
    this.reducer.older(items, hasMore);
    this.finishOlder();
  }

  finishOlder(): void { this.loadingOlder = false; }

  openFold(id: string): readonly TranscriptAction[] {
    if (this.openingFold !== null) return [];
    const fold = this.reducer.items.find((item) => item.id === id && item.kind === "fold");
    if (fold === undefined || fold.fold === undefined) return [];
    this.openingFold = fold;
    this.rangePages = 0;
    this.rangeWork.length = 0;
    return [{ kind: "fetch_range", fromSeq: fold.seq, throughSeq: fold.fold.through_seq }];
  }

  range(items: readonly TranscriptItem[], more: boolean): readonly TranscriptAction[] {
    const fold = this.openingFold;
    if (fold === null || fold.fold === undefined) throw new Error("Range response without an open fold");
    for (const item of items) this.rangeWork.push(cloneItem(item));
    this.rangePages += 1;
    const last = items[items.length - 1];
    if (more && last !== undefined && this.rangePages < 100) {
      return [{ kind: "fetch_range", fromSeq: last.seq + 1, throughSeq: fold.fold.through_seq }];
    }
    this.reducer.unfold(fold, this.rangeWork);
    this.cancelFold();
    return [];
  }

  cancelFold(): void {
    this.openingFold = null;
    this.rangePages = 0;
    this.rangeWork.length = 0;
  }
}

export interface NarrationInspectionStep {
  readonly kind: "narration";
  readonly id: string;
  readonly role: "text" | "thinking" | "notice";
  readonly item: TranscriptItem;
}

export interface ToolInspectionStep {
  readonly kind: "tool";
  readonly id: string;
  readonly use: TranscriptItem;
  readonly results: readonly TranscriptItem[];
  readonly result: TranscriptItem | null;
  readonly images: readonly TranscriptItem[];
  readonly running: boolean;
  readonly failed: boolean;
}

export interface FoldInspectionStep {
  readonly kind: "fold";
  readonly id: string;
  readonly item: TranscriptItem;
  readonly toolCount: number;
  readonly thoughtCount: number;
}

export type WorkInspectionStep = NarrationInspectionStep | ToolInspectionStep | FoldInspectionStep;

export interface ToolTurnProjection {
  readonly turnId: string;
  readonly workKey: string;
  readonly running: boolean;
  readonly summaryVisible: boolean;
  readonly collapsedByDefault: true;
  readonly expanded: boolean;
  readonly userMessages: readonly TranscriptItem[];
  readonly userImages: readonly TranscriptItem[];
  readonly visibleAssistantText: TranscriptItem | null;
  readonly visibleTextKind: "interim" | "final" | null;
  /** Full intermediate narration and calls; the UI shows these only when `expanded` is true. */
  readonly inspectionSteps: readonly WorkInspectionStep[];
  readonly toolCount: number;
  readonly thoughtCount: number;
  readonly unresolvedToolCount: number;
}

interface MutableToolInspection {
  readonly item: TranscriptItem;
  readonly results: TranscriptItem[];
  readonly images: TranscriptItem[];
}

function itemToolUseId(item: TranscriptItem): string | null {
  const raw = item.tool_use_id ?? item.toolUseId;
  return typeof raw === "string" && raw.length > 0 ? raw : null;
}

function isEmptyText(item: TranscriptItem): boolean {
  return typeof item.text !== "string" || item.text.trim().length === 0;
}

function emptyThinkingBetweenCalls(items: readonly TranscriptItem[], index: number): boolean {
  let before = false;
  let after = false;
  for (let cursor = index - 1; cursor >= 0; cursor -= 1) {
    if (items[cursor].kind === "tool_use") { before = true; break; }
  }
  for (let cursor = index + 1; cursor < items.length; cursor += 1) {
    if (items[cursor].kind === "tool_use") { after = true; break; }
  }
  return before && after;
}

/**
 * Projects one already-scoped turn for the chat list. The last assistant text replaces earlier
 * live narration in place; on completion it is the final answer. Everything else stays in one
 * manually expandable work summary, collapsed unless the caller restored an explicit user choice.
 */
export function projectToolTurn(
  turnId: string,
  inputItems: readonly TranscriptItem[],
  running: boolean,
  expanded = false,
): ToolTurnProjection {
  const items = inputItems.map(cloneItem).sort(compareItems);
  let visibleAssistantText: TranscriptItem | null = null;
  const userMessages: TranscriptItem[] = [];
  const userImages: TranscriptItem[] = [];
  for (const item of items) {
    if (item.kind === "text" && item.role === "assistant") visibleAssistantText = item;
    if (item.role !== "user" || item.kind === "tool_result" || item.kind === "fold") continue;
    if (item.kind === "image") {
      if (itemToolUseId(item) === null) userImages.push(item);
      continue;
    }
    userMessages.push(item);
  }

  const byToolUseId = new Map<string, MutableToolInspection>();
  const byItemId = new Map<string, MutableToolInspection>();
  for (const item of items) {
    if (item.kind !== "tool_use") continue;
    const record: MutableToolInspection = { item, results: [], images: [] };
    byItemId.set(item.id, record);
    const useId = itemToolUseId(item);
    if (useId !== null) byToolUseId.set(useId, record);
  }
  for (const item of items) {
    const useId = itemToolUseId(item);
    if (useId === null) continue;
    const record = byToolUseId.get(useId);
    if (record === undefined) continue;
    if (item.kind === "tool_result") record.results.push(item);
    if (item.kind === "image") record.images.push(item);
  }

  const inspectionSteps: WorkInspectionStep[] = [];
  let toolCount = 0;
  let thoughtCount = 0;
  let unresolvedToolCount = 0;
  for (let index = 0; index < items.length; index += 1) {
    const item = items[index];
    if (visibleAssistantText !== null && item.id === visibleAssistantText.id) continue;
    if (item.kind === "tool_use") {
      const record = byItemId.get(item.id);
      if (record === undefined) continue;
      const results = [...record.results].sort(compareItems);
      const images = [...record.images].sort(compareItems);
      const result = results.length === 0 ? null : results[results.length - 1];
      inspectionSteps.push({
        kind: "tool",
        id: item.id,
        use: item,
        results,
        result,
        images,
        running: result === null,
        failed: result?.is_error === true,
      });
      toolCount += 1;
      if (result === null) unresolvedToolCount += 1;
      continue;
    }
    if (item.kind === "tool_result") continue;
    if (item.kind === "image" && itemToolUseId(item) !== null) continue;
    if (item.kind === "thinking") {
      if (isEmptyText(item) && !emptyThinkingBetweenCalls(items, index)) continue;
      inspectionSteps.push({ kind: "narration", id: item.id, role: "thinking", item });
      thoughtCount += 1;
      continue;
    }
    if (item.kind === "text" && item.role === "assistant") {
      inspectionSteps.push({ kind: "narration", id: item.id, role: "text", item });
      continue;
    }
    if (item.kind === "notice") {
      inspectionSteps.push({ kind: "narration", id: item.id, role: "notice", item });
      continue;
    }
    if (item.kind === "fold") {
      const fold = item.fold;
      const foldedTools = fold?.tools ?? 0;
      const foldedThoughts = fold?.thoughts ?? 0;
      inspectionSteps.push({ kind: "fold", id: item.id, item, toolCount: foldedTools, thoughtCount: foldedThoughts });
      toolCount += foldedTools;
      thoughtCount += foldedThoughts;
    }
  }

  return {
    turnId,
    workKey: `w-${turnId}`,
    running,
    summaryVisible: running || inspectionSteps.length > 0,
    collapsedByDefault: true,
    expanded,
    userMessages,
    userImages,
    visibleAssistantText,
    visibleTextKind: visibleAssistantText === null ? null : running ? "interim" : "final",
    inspectionSteps,
    toolCount,
    thoughtCount,
    unresolvedToolCount,
  };
}

export type OperationReceiptState = "pending" | "done" | "failed" | "unknown";

export interface PendingOperationInput {
  readonly operationId: string;
  readonly credentialId: string;
  readonly scope: ScopeKey;
  readonly kind: string;
  readonly target: JsonObject;
  readonly payload: JsonObject;
  readonly createdAt: string;
}

export interface PendingOperation {
  readonly operationId: string;
  readonly credentialId: string;
  readonly scope: ScopeKey;
  readonly kind: string;
  readonly target: JsonObject;
  readonly payload: JsonObject;
  readonly createdAt: string;
}

function freezeJson(value: JsonValue): JsonValue {
  const copied = cloneJson(value);
  if (Array.isArray(copied)) {
    return Object.freeze(copied.map((entry) => freezeJson(entry)));
  }
  if (typeof copied === "object" && copied !== null) {
    const frozen: { [key: string]: JsonValue | undefined } = {};
    for (const key of Object.keys(copied)) {
      const child = copied[key];
      if (child !== undefined) frozen[key] = freezeJson(child);
    }
    return Object.freeze(frozen);
  }
  return copied;
}

function freezeJsonObject(value: JsonObject): JsonObject {
  return freezeJson(value) as JsonObject;
}

/** Copies and freezes the exact target and payload that were persisted before sending. */
export function createPendingOperation(input: PendingOperationInput): PendingOperation {
  if (input.operationId.trim().length === 0) throw new Error("Operation id is required");
  if (input.credentialId.trim().length === 0) throw new Error("Credential id is required");
  if (input.kind.trim().length === 0) throw new Error("Operation kind is required");
  const scope = Object.freeze({ groupId: input.scope.groupId, managerId: input.scope.managerId });
  return Object.freeze({
    operationId: input.operationId,
    credentialId: input.credentialId,
    scope,
    kind: input.kind,
    target: freezeJsonObject(input.target),
    payload: freezeJsonObject(input.payload),
    createdAt: input.createdAt,
  });
}

export type ReceiptGuidance = "wait" | "accept_result" | "show_failure" | "query_or_check_by_hand";

export function receiptGuidance(state: OperationReceiptState): ReceiptGuidance {
  switch (state) {
    case "pending": return "wait";
    case "done": return "accept_result";
    case "failed": return "show_failure";
    case "unknown": return "query_or_check_by_hand";
  }
}

/** A transport retry is never automatic for an operation receipt. */
export function mayAutomaticallyRetryOperation(_state: OperationReceiptState): boolean {
  return false;
}

/** Only an explicit user confirmation after a failed receipt or manual outcome check permits a new op. */
export function mayStartReplacementOperation(state: OperationReceiptState, userConfirmedNoEffect: boolean): boolean {
  return userConfirmedNoEffect && (state === "failed" || state === "unknown");
}

export type ResolveOutcome = "success" | "gone" | "prompt_changed" | "error";

export interface InteractionResolveReply {
  readonly outcome: ResolveOutcome;
  readonly already?: boolean;
  readonly resolution?: JsonObject | null;
}

export interface InteractionResolveInput {
  readonly apiAvailable: boolean;
  readonly networkUp: boolean;
  readonly requestedResolution: JsonObject;
  readonly reply: InteractionResolveReply;
}

export interface InteractionResolveDecision {
  readonly allowed: boolean;
  /** The exact body attempted, or null when no request was sent. */
  readonly requestResolution: JsonObject | null;
  /** For an `already` reply this is the hub's answer, never the local attempted answer. */
  readonly effectiveResolution: JsonObject | null;
  readonly alreadyResolved: boolean;
  readonly cardPresent: boolean;
  readonly refreshPendingCards: boolean;
}

/** Matches current optimistic card behavior while preserving an authoritative already-result. */
export function resolveInteractionResult(input: InteractionResolveInput): InteractionResolveDecision {
  if (!input.apiAvailable || !input.networkUp) {
    return {
      allowed: false,
      requestResolution: null,
      effectiveResolution: null,
      alreadyResolved: false,
      cardPresent: true,
      refreshPendingCards: false,
    };
  }

  const attempted = freezeJsonObject(input.requestedResolution);
  if (input.reply.outcome === "error") {
    return {
      allowed: true,
      requestResolution: attempted,
      effectiveResolution: null,
      alreadyResolved: false,
      cardPresent: true,
      refreshPendingCards: true,
    };
  }

  if (input.reply.outcome === "gone" || input.reply.outcome === "prompt_changed") {
    return {
      allowed: true,
      requestResolution: attempted,
      effectiveResolution: null,
      alreadyResolved: false,
      cardPresent: false,
      refreshPendingCards: true,
    };
  }

  const alreadyResolved = input.reply.already === true;
  return {
    allowed: true,
    requestResolution: attempted,
    effectiveResolution: alreadyResolved && input.reply.resolution != null
      ? freezeJsonObject(input.reply.resolution)
      : alreadyResolved ? null : attempted,
    alreadyResolved,
    cardPresent: false,
    refreshPendingCards: alreadyResolved && input.reply.resolution == null,
  };
}

export interface ComposerDraft {
  readonly scope: ScopeKey;
  readonly targetKind: string;
  readonly targetId: string;
  readonly text: string;
  readonly attachmentRefs: readonly string[];
  readonly caret: number;
  readonly updatedAt: string;
}

export interface ContextReference {
  readonly id: string;
  readonly title: string;
  readonly kind?: string;
  readonly url?: string;
  readonly body?: string;
}

export interface DraftContextState {
  readonly draft: ComposerDraft;
  readonly contexts: readonly ContextReference[];
}

/** Adds context references independently while preserving the existing draft byte-for-byte. */
export function mergeDraftContext(
  draft: ComposerDraft,
  existing: readonly ContextReference[],
  additions: readonly ContextReference[],
): DraftContextState {
  const contexts = new Map<string, ContextReference>();
  for (const ref of existing) if (!contexts.has(ref.id)) contexts.set(ref.id, ref);
  for (const ref of additions) if (!contexts.has(ref.id)) contexts.set(ref.id, ref);
  const copiedDraft: ComposerDraft = Object.freeze({
    scope: Object.freeze({ groupId: draft.scope.groupId, managerId: draft.scope.managerId }),
    targetKind: draft.targetKind,
    targetId: draft.targetId,
    text: draft.text,
    attachmentRefs: Object.freeze([...draft.attachmentRefs]),
    caret: draft.caret,
    updatedAt: draft.updatedAt,
  });
  const copiedContexts = [...contexts.values()].map((ref) => Object.freeze({ ...ref }));
  return { draft: copiedDraft, contexts: Object.freeze(copiedContexts) };
}

export interface RouteDescriptor {
  readonly groupId: string;
  readonly kind: string;
  readonly machineId?: string;
  readonly sessionId?: string;
  readonly conversationId?: string;
  readonly requestId?: string;
  readonly source?: string;
}

interface ScopedNavigationState {
  selectedTab: string | null;
  readonly stacks: Map<string, RouteDescriptor[]>;
}

/** Per-manager, per-tab route stacks with one selected tab stored for each scope. */
export class ScopedTabRoutes {
  private readonly scopes = new Map<string, ScopedNavigationState>();

  selectedTab(scope: ScopeKey, fallback: string): string {
    return this.scopes.get(scopeKey(scope))?.selectedTab ?? fallback;
  }

  selectTab(scope: ScopeKey, tab: string): void {
    this.state(scope).selectedTab = tab;
  }

  stack(scope: ScopeKey, tab: string): readonly RouteDescriptor[] {
    const routes = this.scopes.get(scopeKey(scope))?.stacks.get(tab) ?? [];
    return routes.map((route) => ({ ...route }));
  }

  setStack(scope: ScopeKey, tab: string, routes: readonly RouteDescriptor[]): void {
    this.state(scope).stacks.set(tab, routes.map((route) => Object.freeze({ ...route })));
  }

  clearScope(scope: ScopeKey): void {
    this.scopes.delete(scopeKey(scope));
  }

  private state(scope: ScopeKey): ScopedNavigationState {
    const key = scopeKey(scope);
    const existing = this.scopes.get(key);
    if (existing !== undefined) return existing;
    const created: ScopedNavigationState = { selectedTab: null, stacks: new Map<string, RouteDescriptor[]>() };
    this.scopes.set(key, created);
    return created;
  }
}
