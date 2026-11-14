import { TranscriptReducer, TranscriptSync, TranscriptItem, TranscriptBatch, TranscriptAction, TranscriptSnapshot } from '../core/Domain';
import { HubRepository, segment } from './HubRepository';
import { ResourceRow, Wire } from './Wire';
import { LocalStore } from '../platform/LocalStore';
import { boundedTranscriptCache } from '../core/TranscriptCache';

// A visible conversation owns its subscription, paging and continuity state.
export class TranscriptModel {
  private reducer: TranscriptReducer = new TranscriptReducer();
  private sync: TranscriptSync = new TranscriptSync(this.reducer);
  private active: boolean = true;
  private fillRunning: boolean = false;
  private refreshing: boolean = false;
  private refreshAgain: boolean = false;
  private fillRequested: boolean = false;
  private pendingFetches: Promise<void> = Promise.resolve();
  private readonly row: ResourceRow;
  private readonly repository: HubRepository;
  private readonly local: LocalStore;
  private readonly key: string;
  private readonly publish: (items: readonly TranscriptItem[], hasMore: boolean) => void;
  private readonly failure: (error: Error) => void;

  constructor(row: ResourceRow, repository: HubRepository, local: LocalStore, key: string,
    publish: (items: readonly TranscriptItem[], hasMore: boolean) => void, failure: (error: Error) => void) {
    this.row = row; this.repository = repository; this.local = local; this.key = key; this.publish = publish; this.failure = failure;
  }
  private path(query: string): string {
    return '/api/v1/sessions/' + segment(this.row.machine) + '/' + segment(this.row.session || this.row.id) + '/items?' + query;
  }
  private batch(data: Record<string, Object>): TranscriptBatch {
    // Presence matters: an older server omitted sequencing and has_more rather than returning zero/false.
    const canonical: Record<string, Object> = {};
    canonical['items'] = Wire.rows(data, 'items');
    if (Wire.has(data, 'history')) canonical['history'] = Wire.text(data, 'history');
    if (Wire.has(data, 'from_seq')) canonical['fromSeq'] = Wire.number(data, 'from_seq');
    if (Wire.has(data, 'to_seq')) canonical['toSeq'] = Wire.number(data, 'to_seq');
    if (Wire.has(data, 'backlog')) canonical['backlog'] = Wire.bool(data, 'backlog');
    if (Wire.has(data, 'has_more')) canonical['hasMore'] = Wire.bool(data, 'has_more');
    if (Wire.has(data, 'more_after')) canonical['moreAfter'] = Wire.bool(data, 'more_after');
    return JSON.parse(JSON.stringify(canonical)) as TranscriptBatch;
  }
  async open(): Promise<void> {
    const cached = await this.local.get('transcript:' + this.key);
    if (!this.active) return;
    if (cached) {
      try { this.reducer.restore(JSON.parse(cached) as TranscriptSnapshot); this.emit(); } catch (_) {}
    }
    await this.drive(this.sync.reconnect());
    await this.latest();
  }
  async latest(): Promise<void> {
    if (!this.active) return;
    const pending = this.pendingFetches;
    let release: () => void = () => {};
    this.pendingFetches = new Promise<void>(resolve => { release = resolve; });
    if (this.refreshing) { this.refreshAgain = true; release(); return; }
    this.refreshing = true;
    try {
      const data = await this.repository.call(this.path('lines=80&fold_work=1&lazy_images=1'));
      if (!this.active) return;
      await this.drive(this.sync.settle(this.reducer.latest(this.batch(data)))); this.emit();
    } catch (error) { if (this.active) this.failure(error as Error); }
    finally {
      this.refreshing = false;
      // A latest-page response may close the fill but leave a push heard while it was in flight unfetched. Do not
      // leave the transport's gate held while that follow-up is dropped; a failed latest also releases the gate.
      this.sync.finishFill();
      if (this.active && (this.sync.behind || this.fillRequested || this.reducer.items.some((item) => item.seq > this.reducer.covered))) {
        this.fillRequested = false;
        this.fillNow();
      }
      if (this.refreshAgain && this.active) { this.refreshAgain = false; this.latest(); }
      release();
    }
  }
  receive(data: Record<string, Object>): void {
    if (!this.active || Wire.text(data, 'machine_id') !== this.row.machine || Wire.text(data, 'session_id') !== (this.row.session || this.row.id)) return;
    const batch = this.batch(data);
    if (Wire.text(data, 'type') === 'items_replace') this.sync.reload(batch.items, batch.history || null, batch.toSeq || null, batch.hasMore === true);
    else {
      const gap = this.reducer.apply(batch);
      if (gap === 'fill') this.fillRequested = true;
      this.drive(this.sync.settle(gap)).catch((error: Error) => this.failure(error));
    }
    this.emit();
  }
  async older(): Promise<void> { await this.drive(this.sync.loadOlder()); }
  async unfold(id: string): Promise<void> { await this.drive(this.sync.openFold(id)); }
  private async drive(actions: readonly TranscriptAction[]): Promise<void> {
    if (!this.active) return;
    for (const action of actions) {
      if (!this.active) return;
      if (action.kind === 'fetch_after') {
        this.fillRequested = true;
        if (this.fillRunning) continue;
        this.fillRunning = true;
        try {
          let next: number | undefined = action.afterSeq;
          while (next !== undefined && this.active) {
            const data = await this.repository.call(this.path('after_seq=' + next + '&lazy_images=1'));
            if (!this.active) return;
            const result = this.sync.after(this.batch(data));
            this.emit();
            await this.drive(result.actions.filter((candidate) => candidate.kind !== 'fetch_after'));
            const following = result.actions.find((candidate) => candidate.kind === 'fetch_after');
            next = following?.kind === 'fetch_after' ? following.afterSeq : undefined;
          }
        } catch (error) {
          this.sync.finishFill();
          if (error instanceof Error) throw error;
          throw new Error('failed');
        } finally { this.fillRunning = false; }
      } else if (action.kind === 'subscribe') {
        const payload: Record<string, Object> = Wire.payload(['type', 'machine_id', 'session_id', 'fold_work'], ['subscribe', this.row.machine, this.row.session || this.row.id, true]);
        if (action.sinceSeq !== undefined) payload['since_seq'] = action.sinceSeq;
        this.repository.transport.send(JSON.stringify(payload));
      } else if (action.kind === 'fetch_latest') {
        await this.pendingFetches;
        await this.latest();
      } else if (action.kind === 'fetch_older') {
        try {
          const data = await this.repository.call(this.path('before_seq=' + action.beforeSeq + '&lines=80&fold_work=1&lazy_images=1'));
          if (!this.active) return;
          const batch = this.batch(data); this.sync.older(batch.items, batch.hasMore === true); this.emit();
        } catch (error) { this.sync.finishOlder(); if (error instanceof Error) throw error; throw new Error('failed'); }
      } else if (action.kind === 'fetch_range') {
        try {
          const data = await this.repository.call(this.path('from_seq=' + action.fromSeq + '&through_seq=' + action.throughSeq + '&lazy_images=1'));
          if (!this.active) return;
          const batch = this.batch(data); await this.drive(this.sync.range(batch.items, Wire.bool(data, 'more'))); this.emit();
        } catch (error) { this.sync.cancelFold(); if (error instanceof Error) throw error; throw new Error('failed'); }
      }
    }
  }
  private async fillNow(): Promise<void> {
    if (!this.active || this.fillRunning) return;
    this.fillRunning = true;
    try {
      const data = await this.repository.call(this.path('after_seq=' + this.reducer.covered + '&lazy_images=1'));
      if (!this.active) return;
      this.emit();
      await this.drive(this.sync.after(this.batch(data)).actions);
    } catch (error) {
      this.sync.finishFill();
      if (error instanceof Error) throw error;
      throw new Error('failed');
    } finally { this.fillRunning = false; }
  }

  private emit(): void {
    if (!this.active) return;
    this.publish(this.reducer.items, this.reducer.hasMore === true);
    this.local.put('transcript:' + this.key, boundedTranscriptCache(this.reducer.snapshot())).catch((error: Error) => { if (this.active) this.failure(error); });
  }
  close(): void {
    if (!this.active) return; this.active = false;
    try { this.repository.transport.send(JSON.stringify(Wire.payload(['type', 'machine_id', 'session_id'], ['unsubscribe', this.row.machine, this.row.session || this.row.id]))); } catch (_) {}
  }
}
