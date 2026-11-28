import {
  boundStarredInput,
  buildStarredIndexMap,
  isMessageStarred,
  isPositionalItemId,
  isVerifiedStarSource,
  parseStarredIndexResponse,
  parseStarredListResponse,
  parseStarredSnapshot,
  STARRED_MESSAGES_FEATURE,
  starredRefId,
  StarredSnapshot,
  StarredSource,
  starSourceKey
} from '../core/StarredMessages';
import { tr } from '../design/Strings';
import { HttpFailure } from '../platform/HubTransport';
import { Haptics } from '../platform/Haptics';
import type { ClientModel } from './ClientModel';
import { segment } from './HubRepository';
import { toastCenter } from './ToastCenter';
import { Wire } from './Wire';

export type StarredLoadState = 'idle' | 'loading' | 'ready' | 'unsupported' | 'failed';
export type StarredOriginalStatus = 'verified' | 'unavailable' | 'failed';

const STARRED_PATH = '/api/v1/assistant/starred';

/**
 * Shared reactive store for Starred Messages matching iOS `StarredStore.swift`.
 * Index state drives message badges/menus; full snapshots are loaded only while the sheet is open.
 */
@ObservedV2
export class StarredStore {
  @Trace state: StarredLoadState = 'idle';
  @Trace canWrite: boolean = false;
  @Trace snapshots: StarredSnapshot[] = [];
  @Trace indexRevision: number = 0;
  @Trace pendingRevision: number = 0;

  private indexMap: Map<string, string> = new Map();
  private pendingKeys: Set<string> = new Set();
  private boundScope: string = '';
  private boundGeneration: number = -1;
  private boundOffered: boolean = false;
  private boundStarredRev: number = -1;
  private indexSeq: number = 0;
  private pageSeq: number = 0;
  private readers: number = 0;
  private scopeEpoch: number = 0;
  private loadedIndexForScope: boolean = false;

  isOffered(model: ClientModel): boolean {
    return model.connected && (model.demo || model.features.includes(STARRED_MESSAGES_FEATURE));
  }

  configure(model: ClientModel): boolean {
    const scope = model.scope();
    const generation = model.connectionGeneration();
    const offered = this.isOffered(model);
    const starredRev = model.starredRevision;
    const identityChanged = scope !== this.boundScope || generation !== this.boundGeneration || offered !== this.boundOffered;

    if (identityChanged) {
      this.scopeEpoch++;
      this.indexSeq++;
      this.pageSeq++;
      this.boundScope = scope;
      this.boundGeneration = generation;
      this.boundOffered = offered;
      this.boundStarredRev = starredRev;
      this.canWrite = false;
      this.snapshots = [];
      this.indexMap = new Map();
      this.pendingKeys.clear();
      this.loadedIndexForScope = false;
      this.state = offered ? 'idle' : 'unsupported';
      this.indexRevision++;
      this.pendingRevision++;
      if (offered) {
        void this.loadIndex(model);
        if (this.readers > 0) void this.loadPage(model);
      }
      return true;
    }

    if (starredRev !== this.boundStarredRev) {
      this.boundStarredRev = starredRev;
      if (offered) {
        void this.loadIndex(model);
        if (this.readers > 0) void this.loadPage(model);
      }
      return true;
    }

    if (offered && !this.loadedIndexForScope) {
      this.loadedIndexForScope = true;
      void this.loadIndex(model);
    }
    return false;
  }

  private matchesBound(model: ClientModel, epoch: number): boolean {
    return epoch === this.scopeEpoch &&
      model.connected &&
      model.scope() === this.boundScope &&
      model.connectionGeneration() === this.boundGeneration &&
      model.owns(this.boundGeneration, this.boundScope);
  }

  private isOldHubError(error: Object): boolean {
    return error instanceof HttpFailure && [404, 405, 501].includes(error.status);
  }

  private clearUnavailable(): void {
    this.indexSeq++;
    this.pageSeq++;
    this.canWrite = false;
    this.indexMap = new Map();
    this.snapshots = [];
    this.state = 'unsupported';
    this.indexRevision++;
  }

  async loadIndex(model: ClientModel): Promise<void> {
    this.configureIfNeeded(model);
    if (!this.isOffered(model)) return;
    this.loadedIndexForScope = true;
    const epoch = this.scopeEpoch;
    const seq = ++this.indexSeq;
    const generation = model.connectionGeneration();
    const scope = model.scope();
    try {
      const result = await model.repo().call(
        STARRED_PATH + '?index=1',
        'GET',
        {},
        '',
        () => model.owns(generation, scope)
      );
      if (!this.matchesBound(model, epoch) || seq !== this.indexSeq) return;
      const parsed = parseStarredIndexResponse(result);
      this.canWrite = parsed.canWrite;
      this.indexMap = buildStarredIndexMap(parsed.refs);
      if (this.state === 'unsupported') this.state = 'idle';
      this.indexRevision++;
    } catch (error) {
      if (!this.matchesBound(model, epoch) || seq !== this.indexSeq) return;
      if (this.isOldHubError(error as Object)) {
        this.clearUnavailable();
      }
    }
  }

  async loadPage(model: ClientModel): Promise<void> {
    this.configureIfNeeded(model);
    if (!this.isOffered(model)) {
      this.state = 'unsupported';
      return;
    }
    const epoch = this.scopeEpoch;
    const seq = ++this.pageSeq;
    const generation = model.connectionGeneration();
    const scope = model.scope();
    if (this.snapshots.length === 0) this.state = 'loading';
    try {
      const result = await model.repo().call(
        STARRED_PATH,
        'GET',
        {},
        '',
        () => model.owns(generation, scope)
      );
      if (!this.matchesBound(model, epoch) || seq !== this.pageSeq) return;
      const parsed = parseStarredListResponse(result);
      this.canWrite = parsed.canWrite;
      this.snapshots = parsed.stars;
      this.state = 'ready';
    } catch (error) {
      if (!this.matchesBound(model, epoch) || seq !== this.pageSeq) return;
      if (this.isOldHubError(error as Object)) {
        this.clearUnavailable();
      } else {
        this.state = 'failed';
      }
    }
  }

  startReading(model: ClientModel): void {
    this.readers++;
    const changed = this.configure(model);
    if (!changed) {
      void this.loadIndex(model);
      void this.loadPage(model);
    }
  }

  stopReading(): void {
    this.readers = Math.max(0, this.readers - 1);
    if (this.readers > 0) return;
    this.pageSeq++;
    this.snapshots = [];
    this.state = this.boundOffered ? 'idle' : 'unsupported';
  }

  private configureIfNeeded(model: ClientModel): void {
    if (
      model.scope() !== this.boundScope ||
      model.connectionGeneration() !== this.boundGeneration ||
      this.isOffered(model) !== this.boundOffered
    ) {
      this.configure(model);
    }
  }

  canWriteFor(model: ClientModel): boolean {
    if (!this.isOffered(model)) return false;
    if (model.scope() !== this.boundScope || model.connectionGeneration() !== this.boundGeneration) return false;
    return this.canWrite;
  }

  isStarred(model: ClientModel, source: StarredSource): boolean {
    if (model.scope() !== this.boundScope || model.connectionGeneration() !== this.boundGeneration) return false;
    return isMessageStarred(this.indexMap, source);
  }

  isPending(model: ClientModel, source: StarredSource): boolean {
    if (model.scope() !== this.boundScope || model.connectionGeneration() !== this.boundGeneration) return false;
    return this.pendingKeys.has(starSourceKey(source));
  }

  referenceId(model: ClientModel, source: StarredSource): string | undefined {
    if (model.scope() !== this.boundScope || model.connectionGeneration() !== this.boundGeneration) return undefined;
    return starredRefId(this.indexMap, source);
  }

  async add(model: ClientModel, draft: StarredSnapshot): Promise<boolean> {
    this.configureIfNeeded(model);
    const epoch = this.scopeEpoch;
    const key = starSourceKey(draft);
    if (
      !this.matchesBound(model, epoch) ||
      !this.isOffered(model) ||
      !this.canWrite ||
      !isVerifiedStarSource(draft) ||
      this.pendingKeys.has(key) ||
      model.operationBlocked(STARRED_PATH)
    ) {
      return false;
    }

    const generation = model.connectionGeneration();
    const scope = model.scope();
    this.pendingKeys.add(key);
    this.pendingRevision++;

    try {
      const response = await model.repo().call(
        STARRED_PATH,
        'POST',
        boundStarredInput(draft),
        '',
        () => model.owns(generation, scope)
      );
      if (!this.matchesBound(model, epoch)) return false;
      const saved = parseStarredSnapshot(response['star']);
      if (!saved || !saved.id) {
        toastCenter.error(tr('failed'));
        return false;
      }
      this.indexSeq++;
      this.pageSeq++;
      const nextMap = new Map(this.indexMap);
      nextMap.set(starSourceKey(saved), saved.id);
      this.indexMap = nextMap;
      if (this.readers > 0 && !this.snapshots.some((item: StarredSnapshot) => item.id === saved.id)) {
        this.snapshots = [saved, ...this.snapshots];
      }
      this.indexRevision++;
      Haptics.success();
      toastCenter.success(tr('starred_saved_toast'));
      return true;
    } catch (error) {
      if (this.matchesBound(model, epoch)) {
        model.reportError(error as Error);
        Haptics.error();
        toastCenter.error(tr(model.error || 'failed'));
      }
      return false;
    } finally {
      if (epoch === this.scopeEpoch) {
        this.pendingKeys.delete(key);
        this.pendingRevision++;
      }
    }
  }

  async remove(model: ClientModel, source: StarredSource, explicitId?: string, withUndo: boolean = true): Promise<boolean> {
    this.configureIfNeeded(model);
    const epoch = this.scopeEpoch;
    const key = starSourceKey(source);
    const id = explicitId || this.indexMap.get(key) || '';
    const path = STARRED_PATH + '/' + segment(id);
    if (
      !id ||
      !this.matchesBound(model, epoch) ||
      !this.isOffered(model) ||
      !this.canWrite ||
      this.pendingKeys.has(key) ||
      model.operationBlocked(path)
    ) {
      return false;
    }

    const generation = model.connectionGeneration();
    const scope = model.scope();
    this.pendingKeys.add(key);
    this.pendingRevision++;

    try {
      const response = await model.repo().call(
        path,
        'DELETE',
        {},
        '',
        () => model.owns(generation, scope)
      );
      if (!this.matchesBound(model, epoch)) return false;
      this.indexSeq++;
      this.pageSeq++;
      if (this.indexMap.get(key) === id) {
        const nextMap = new Map(this.indexMap);
        nextMap.delete(key);
        this.indexMap = nextMap;
      }
      this.snapshots = this.snapshots.filter((item: StarredSnapshot) => item.id !== id);
      this.indexRevision++;
      Haptics.selection();

      const removed = parseStarredSnapshot(response['removed']);
      if (withUndo && removed) {
        toastCenter.success(tr('starred_removed_toast'), tr('undo'), () => {
          if (this.matchesBound(model, epoch)) {
            void this.add(model, removed);
          }
        });
      } else if (withUndo) {
        toastCenter.success(tr('starred_removed_toast'));
      }
      return true;
    } catch (error) {
      if (this.matchesBound(model, epoch)) {
        model.reportError(error as Error);
        Haptics.error();
        toastCenter.error(tr(model.error || 'failed'));
      }
      return false;
    } finally {
      if (epoch === this.scopeEpoch) {
        this.pendingKeys.delete(key);
        this.pendingRevision++;
      }
    }
  }

  async verifyOriginal(model: ClientModel, star: StarredSnapshot): Promise<StarredOriginalStatus> {
    const epoch = this.scopeEpoch;
    if (!this.matchesBound(model, epoch) || !isVerifiedStarSource(star)) return 'unavailable';
    const generation = model.connectionGeneration();
    const scope = model.scope();

    if (model.demo) {
      const existsInChat = model.conversations.concat(model.sessions).some((row) =>
        row.machine === star.machine_id && (row.session || row.id) === star.session_id
      );
      return existsInChat ? 'verified' : 'unavailable';
    }

    try {
      const path = '/api/v1/sessions/' + segment(star.machine_id) + '/' + segment(star.session_id) +
        '/items/' + segment(star.item_id) + '/full';
      const item = await model.repo().call(path, 'GET', {}, '', () => model.owns(generation, scope));
      if (!this.matchesBound(model, epoch)) return 'failed';
      const id = Wire.text(item, 'id');
      if (id && id !== star.item_id) return 'unavailable';
      if (isPositionalItemId(star.item_id)) {
        const fp = Wire.text(item, 'source_fingerprint').toLowerCase();
        if (!fp || fp !== (star.source_fingerprint || '').toLowerCase()) return 'unavailable';
      }
      return 'verified';
    } catch (error) {
      if (error instanceof HttpFailure && (error.status === 404 || (error.status === 410 && error.code === 'conversation_deleted'))) {
        return 'unavailable';
      }
      return 'failed';
    }
  }
}

export const starredStore: StarredStore = new StarredStore();
