import { ApiError, HubTimeoutError, isTransient, subscribeCredentialOwner } from "../../../lib/api";
import { ConfigurationError } from "../../../lib/configuration/contract";
import { AssistantScopeUnverified, type ScopedAssistantClient } from "../../../lib/scoped-assistant-api";
import { t } from "../../../i18n";

type ResourceSnapshot<Remote, Draft> = {
  remote: Remote | null;
  draft: Draft | undefined;
  loading: boolean;
  readError: unknown;
  saving: string | null;
  queued: number;
  saveError: { label: string; error: unknown } | null;
  unconfirmed: boolean;
};

export const emptyConfiguration: ResourceSnapshot<never, never> = {
  remote: null, draft: undefined, loading: false, readError: null, saving: null, queued: 0, saveError: null, unconfirmed: false,
};

type PreparedSave<Remote, Draft> = {
  send: () => Promise<Remote | void>;
  matches?: (remote: Remote) => boolean;
  acknowledge?: (draft: Draft, remote: Remote) => Draft | undefined;
};

type ResourceSave<Remote, Draft> = {
  label: string;
  key?: string | object;
  /** A single field this job writes without a separate Save action. */
  immediateField?: string;
  refreshBefore?: boolean;
  prepare: (remote: Remote) => PreparedSave<Remote, Draft>;
};

type Job<Remote, Draft> = {
  save: ResourceSave<Remote, Draft>;
  promise: Promise<Remote | undefined>;
  accept: (result: Remote | undefined) => void;
  reject: (error: unknown) => void;
};

export function abortedConfiguration() {
  return new DOMException("aborted", "AbortError");
}

function invalidConfiguration() {
  return new ConfigurationError("invalid_document", t("settings-configuration.error_invalid_document"));
}

function conflict(error: unknown) {
  return (error instanceof ApiError || error instanceof ConfigurationError) &&
    (error.code === "configuration_conflict" || error.code === "settings_moved");
}

function uncertain(error: unknown) {
  return isTransient(error) || error instanceof HubTimeoutError ||
    error instanceof AssistantScopeUnverified ||
    (error instanceof ApiError && error.status === 502) ||
    (error instanceof ConfigurationError && ["configuration_unavailable", "invalid_document"].includes(error.code));
}

/** One tab's confirmed values and local work for a bound assistant resource. No view owns its requests. */
export class ResourceConfiguration<Remote extends { id: string }, Draft = never> {
  private state: ResourceSnapshot<Remote, Draft> = emptyConfiguration;
  private listeners = new Set<() => void>();
  private requestSeq = 0;
  private retired = false;
  private refreshRequested = false;
  private queue: Job<Remote, Draft>[] = [];
  private comparison?: PreparedSave<Remote, Draft>;
  private latestPatch?: (remote: Remote) => Remote;

  constructor(readonly id: string, readonly client: ScopedAssistantClient, private adapter: {
    read: () => Promise<Remote>;
    rebase?: (draft: Draft, remote: Remote) => { draft: Draft | undefined; conflict?: ConfigurationError };
    isDirty?: (draft: Draft) => boolean;
    draftChanged?: (unsaved: boolean) => void;
  }) {}

  snapshot = () => this.state;

  isSavingField(path: string) {
    return this.queue.some((job) => job.save.immediateField === path);
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  subscribeRemote = (listener: () => void) => {
    let previous = this.state.remote;
    return this.subscribe(() => {
      if (previous === this.state.remote) return;
      previous = this.state.remote;
      listener();
    });
  };

  private set(next: Partial<ResourceSnapshot<Remote, Draft>>) {
    const wasDirty = this.dirty();
    this.state = { ...this.state, ...next };
    const dirty = this.dirty();
    if (wasDirty !== dirty) this.adapter.draftChanged?.(dirty);
    this.listeners.forEach((listener) => listener());
  }

  private dirty() {
    return this.state.draft !== undefined && (this.adapter.isDirty?.(this.state.draft) ?? true);
  }

  private current(seq: number) {
    return !this.retired && seq === this.requestSeq;
  }

  edit = (change: (draft: Draft | undefined, remote: Remote) => Draft | undefined, label = "config") => {
    if (this.retired || !this.state.remote) return;
    this.set({
      draft: change(this.state.draft, this.state.remote),
      ...(this.state.saveError?.label === label && { saveError: null }),
    });
  };

  patchRemote(change: (remote: Remote) => Remote) {
    if (this.retired) return;
    this.latestPatch = change;
    if (this.state.remote) this.set({ remote: change(this.state.remote) });
  }

  private carryForward(remote: Remote, patchAtStart: typeof this.latestPatch) {
    return this.latestPatch && this.latestPatch !== patchAtStart ? this.latestPatch(remote) : remote;
  }

  private validate(remote: Remote) {
    if (remote?.id !== this.id) throw invalidConfiguration();
  }

  private accept(remote: Remote, save?: PreparedSave<Remote, Draft>) {
    const draft = this.state.draft;
    const reconciled = draft !== undefined && !save?.acknowledge ? this.adapter.rebase?.(draft, remote) : undefined;
    this.set({
      remote,
      draft: reconciled ? reconciled.draft : draft !== undefined && save?.acknowledge ? save.acknowledge(draft, remote) : draft,
      readError: null,
      ...(reconciled?.conflict && { saveError: { label: "config", error: reconciled.conflict } }),
    });
  }

  refresh = async () => {
    if (this.retired) return;
    if (this.queue.length) {
      this.refreshRequested = true;
      return;
    }
    await this.read(++this.requestSeq).catch(() => {});
  };

  private async read(seq: number) {
    const patchAtStart = this.latestPatch;
    this.refreshRequested = false;
    this.set({ loading: true });
    try {
      const answer = await this.adapter.read();
      if (!this.current(seq)) return;
      this.validate(answer);
      const remote = this.carryForward(answer, patchAtStart);
      const comparison = this.comparison;
      const matched = comparison?.matches?.(remote);
      this.comparison = undefined;
      this.accept(remote, matched ? comparison : undefined);
      this.set({ unconfirmed: false, ...(matched && { saveError: null }) });
      return remote;
    } catch (error) {
      if (this.current(seq)) {
        this.set({ readError: error });
        throw error;
      }
    } finally {
      if (this.current(seq)) this.set({ loading: false });
    }
  }

  save(save: ResourceSave<Remote, Draft>): Promise<Remote | undefined> {
    if (this.retired) return Promise.reject(abortedConfiguration());
    const last = this.queue.at(-1);
    if (save.key !== undefined && last?.save.label === save.label && last.save.key === save.key) return last.promise;
    let accept!: Job<Remote, Draft>["accept"];
    let reject!: Job<Remote, Draft>["reject"];
    const promise = new Promise<Remote | undefined>((resolve, refuse) => {
      accept = resolve;
      reject = refuse;
    });
    this.queue.push({ save, promise, accept, reject });
    if (this.queue.length === 1) void this.drain();
    else this.set({ queued: this.queue.length - 1 });
    return promise;
  }

  private async drain() {
    while (this.queue.length) {
      const job = this.queue[0];
      const { label } = job.save;
      const seq = ++this.requestSeq;
      this.refreshRequested ||= this.state.loading;
      this.set({
        saving: label, queued: this.queue.length - 1, loading: false,
        ...(this.state.saveError?.label === label && { saveError: null }),
      });
      try {
        if (job.save.refreshBefore || this.comparison) await this.read(seq);
        if (!this.current(seq)) return;
        const remote = this.state.remote;
        if (!remote) throw new ConfigurationError("configuration_unavailable");
        const prepared = job.save.prepare(remote);
        const result = await this.send(seq, prepared);
        if (!this.current(seq)) return;
        job.accept(result);
      } catch (error) {
        if (!this.current(seq)) return;
        this.set({ saveError: { label, error } });
        job.reject(error);
        // A conflict or an unknown outcome calls for a fresh decision from the user.
        if (this.comparison || conflict(error) || uncertain(error)) {
          this.queue.splice(1).forEach((waiting) => waiting.reject(error));
        }
      }
      this.queue.shift();
    }
    this.set({ saving: null, queued: 0 });
    if (this.refreshRequested) void this.refresh();
  }

  private async send(seq: number, prepared: PreparedSave<Remote, Draft>) {
    const patchAtStart = this.latestPatch;
    try {
      const result = await prepared.send();
      if (!this.current(seq)) return;
      if (!result) {
        if (result !== undefined || prepared.matches) throw invalidConfiguration();
        return;
      }
      this.validate(result);
      const remote = this.carryForward(result, patchAtStart);
      this.accept(remote, prepared);
      return remote;
    } catch (error) {
      if (!this.current(seq)) return;
      if (prepared.matches && uncertain(error)) {
        this.comparison = prepared;
        this.set({ unconfirmed: true });
        const fresh = await this.read(seq).catch(() => undefined);
        if (fresh && prepared.matches(fresh)) return fresh;
      } else if (conflict(error)) {
        await this.read(seq).catch(() => {});
      }
      throw error;
    }
  }

  retire = () => {
    this.retired = true;
    this.comparison = undefined;
    this.latestPatch = undefined;
    this.queue.splice(0).forEach((job) => job.reject(abortedConfiguration()));
    this.set(emptyConfiguration);
    this.listeners.clear();
  };
}

/** The scoped client's key includes the hub context, credential epoch, assistant, kind and resource ID. */
export function resourceConfigurations<Resource extends { retire: () => void }>(create: (id: string, client: ScopedAssistantClient) => Resource) {
  const entries = new Map<string, Resource>();
  const remove = (key: string) => {
    const entry = entries.get(key);
    entries.delete(key);
    entry?.retire();
  };
  if (typeof window !== "undefined") subscribeCredentialOwner(() => entries.forEach((_, key) => remove(key)));
  return {
    remove,
    get(id: string, client: ScopedAssistantClient): Resource {
      const existing = entries.get(client.key);
      if (existing) return existing;
      const entry = create(id, client);
      entries.set(client.key, entry);
      return entry;
    },
  };
}
