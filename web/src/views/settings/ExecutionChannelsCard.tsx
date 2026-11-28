// Settings › Models › Channels (docs/web/web.md "Four dimensions in the Web"): the managed channels a model is reached through
// — a gateway or an API, its Base URL, key and headers (write-only: a view says only whether there are any), which
// engines run on it under which launcher names, which computers may use it — and the last ten changes, each one a
// point to go back to. GET / PUT / DELETE /execution/channels carry the catalog's revision: a change made on an older
// one is refused (409) and the card reads the catalog again. Nothing here stops a running session: new sessions and
// each running one's next turn use the change. A launcher that is a channel's alias is edited here, not in Providers.
import { useEffect, useId, useRef, useState } from "react";
import { t } from "../../i18n";
import { ApiError, errorText } from "../../lib/api";
import {
  deleteExecutionChannel, executionChannels, isRevisionConflict, rollbackExecutionChannels, runtimeLabel, saveExecutionChannel,
  type ChannelBinding, type ChannelCatalogView, type ChannelConfig, type ChannelRevision,
} from "../../lib/execution";
import { ago } from "../../lib/format";
import { useStore } from "../../lib/store";
import { Button, Dialog, Loading, Pill, SECRET_INPUT, toast } from "../../ui/atoms";
import { protocolLabel } from "./RuntimesCard";
import { Group, SettingsHint } from "./SettingsParts";

// ── opening one from elsewhere (a read-only launcher alias in Providers) ─────

const openers = new Set<(name: string) => void>();
let waiting: string | null = null;

/** Opens channel `name`'s editor on the card (scrolling it into view); held until the card is there. */
export function openExecutionChannel(name: string) {
  if (openers.size === 0) {
    waiting = name;
    return;
  }
  for (const o of openers) o(name);
}

export const CHANNEL_PROTOCOLS = ["anthropic_messages", "openai_responses", "openai_chat", "gemini_native"] as const;
const NAME_RE = /^[A-Za-z0-9_-]{1,40}$/;
const field = "h-9 w-full rounded-control bg-field px-3 text-meta text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

const emptyChannel = (): ChannelConfig => ({ name: "", base_url: "", endpoints: ["anthropic_messages"], bindings: [{ runtime: "claude", alias: "" }] });

function actionText(p: ChannelRevision): string {
  const name = p.channel || p.channels.join(", ");
  return p.action === "delete" ? t("execution.point_delete", { channel: name })
    : p.action === "rollback" ? t("execution.point_rollback")
      : t("execution.point_save", { channel: name });
}

export function ExecutionChannelsCard({ runtimes = ["claude", "codex", "agy"] }: { runtimes?: string[] }) {
  const [view, setView] = useState<ChannelCatalogView | null>(null);
  const [error, setError] = useState("");
  const [missing, setMissing] = useState(false);
  const [editing, setEditing] = useState<ChannelConfig | "new" | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [round, setRound] = useState(0);
  const reload = () => setRound((n) => n + 1);
  const shown = useRef<ChannelCatalogView | null>(null);
  shown.current = view;

  useEffect(() => {
    let live = true;
    executionChannels()
      .then((v) => live && (setView({ revision: v.revision ?? 0, channels: v.channels ?? [], rollbacks: v.rollbacks ?? [] }), setError("")))
      .catch((e) => {
        if (!live) return;
        if (e instanceof ApiError && e.status === 404) setMissing(true);
        else setError(errorText(e));
      });
    return () => {
      live = false;
    };
  }, [round]);

  // a read-only alias's "Edit in Channels" lands here
  useEffect(() => {
    const open = (name: string) => {
      document.getElementById("settings-execution-channels")?.scrollIntoView?.({ block: "start" });
      const c = shown.current?.channels.find((x) => x.name === name);
      if (c) setEditing(c);
      else waiting = name;
    };
    openers.add(open);
    if (waiting) {
      const w = waiting;
      waiting = null;
      open(w);
    }
    return () => void openers.delete(open);
  }, []);
  useEffect(() => {
    if (view && waiting) {
      const c = view.channels.find((x) => x.name === waiting);
      waiting = null;
      if (c) setEditing(c);
    }
  }, [view]);

  const failed = (e: unknown) => {
    if (isRevisionConflict(e)) {
      toast(t("execution.conflict"), "error");
      reload();
    } else toast(errorText(e), "error");
  };
  const remove = async (name: string) => {
    setBusy(true);
    try {
      await deleteExecutionChannel(name, view?.revision);
      toast(t("execution.channel_deleted", { channel: name }), "ok");
      setRemoving(null);
      reload();
    } catch (e) {
      failed(e);
    } finally {
      setBusy(false);
    }
  };
  const rollback = async (revision?: number) => {
    setBusy(true);
    try {
      await rollbackExecutionChannels(revision);
      toast(t("execution.rolled_back"), "ok");
      reload();
    } catch (e) {
      failed(e);
    } finally {
      setBusy(false);
    }
  };

  if (missing) return null;
  return (
    <>
      <Group
        title={t("execution.channels_title")}
        anchor="execution-channels"
        footer={t("execution.channels_footer")}
        right={view && <Button tone="accent" size="sm" icon="plus" testId="channel-add" onClick={() => setEditing("new")}>{t("execution.channel_add")}</Button>}
      >
        {!view && !error && <Loading />}
        {error && <SettingsHint alert>{error}</SettingsHint>}
        {view && view.channels.length === 0 && <SettingsHint>{t("execution.channels_empty")}</SettingsHint>}
        {view?.channels.map((c) => (
          <div key={c.name} data-channel={c.name} className="flex flex-col gap-1.5 px-4 py-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="min-w-0 truncate text-meta font-medium text-ink">{c.display_name || c.name}</span>
              {c.display_name && <span className="font-mono text-submeta text-ink-3">{c.name}</span>}
              <span className="flex-1" />
              <Pill tone={c.has_key ? "green" : undefined} size="sm">{c.has_key ? t("execution.key_saved") : t("execution.key_none")}</Pill>
              {c.has_headers && <Pill size="sm">{t("execution.headers_saved")}</Pill>}
            </div>
            <div className="truncate font-mono text-submeta text-ink-3">{c.base_url}</div>
            <div className="flex flex-wrap gap-1">
              {c.bindings.map((b) => (
                <Pill key={b.alias || b.runtime} size="sm">{`${runtimeLabel(b.runtime)} · ${b.alias}`}</Pill>
              ))}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-submeta text-ink-3">{c.share === "selected" ? t("execution.scope_selected", { count: c.scope_machines?.length ?? 0 }) : c.share === "none" ? t("execution.scope_none") : t("execution.scope_all")}</span>
              <span className="flex-1" />
              {removing === c.name ? (
                <>
                  <Button size="sm" tone="quiet" disabled={busy} onClick={() => setRemoving(null)}>{t("common.cancel")}</Button>
                  <Button size="sm" tone="danger" busy={busy} testId={`channel-delete-confirm-${c.name}`} onClick={() => void remove(c.name)}>{t("execution.channel_delete_confirm")}</Button>
                </>
              ) : (
                <>
                  <Button size="sm" tone="quiet" disabled={busy} testId={`channel-delete-${c.name}`} onClick={() => setRemoving(c.name)}>{t("execution.channel_delete")}</Button>
                  <Button size="sm" disabled={busy} testId={`channel-edit-${c.name}`} onClick={() => setEditing(c)}>{t("execution.channel_edit")}</Button>
                </>
              )}
            </div>
          </div>
        ))}
      </Group>
      {view && view.rollbacks.length > 0 && (
        <Group
          title={t("execution.history_title", { count: view.rollbacks.length })}
          footer={t("execution.history_footer")}
          right={<Button size="sm" tone="secondary" icon="undo" disabled={busy} testId="channel-undo" onClick={() => void rollback()}>{t("execution.undo_last")}</Button>}
        >
          {view.rollbacks.slice(0, 10).map((p) => (
            <div key={p.revision} data-rollback={p.revision} className="flex items-center gap-3 px-4 py-2.5">
              <div className="min-w-0 flex-1">
                <div className="truncate text-meta text-ink">{actionText(p)}</div>
                <div className="text-submeta text-ink-3">{t("execution.point_meta", { revision: p.revision, when: ago(p.at) })}</div>
              </div>
              <Button size="sm" disabled={busy} testId={`channel-rollback-${p.revision}`} onClick={() => void rollback(p.revision)}>{t("execution.rollback")}</Button>
            </div>
          ))}
        </Group>
      )}
      {editing && view && (
        <ChannelEditor
          channel={editing === "new" ? null : editing}
          revision={view.revision}
          runtimes={runtimes}
          onClose={() => setEditing(null)}
          onSaved={() => (setEditing(null), reload())}
          onConflict={reload}
        />
      )}
    </>
  );
}

export function ChannelEditor({ channel, revision, runtimes, onClose, onSaved, onConflict }: {
  channel: ChannelConfig | null;
  revision: number;
  runtimes: string[];
  onClose: () => void;
  onSaved: () => void;
  /** Another change came first (409): the card reads the catalog again. */
  onConflict: () => void;
}) {
  const machines = useStore((s) => s.machines);
  const ids = useId();
  const [draft, setDraft] = useState<ChannelConfig>(() => (channel ? { ...channel, api_key: "", headers: "", bindings: channel.bindings.map((b) => ({ ...b })) } : emptyChannel()));
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const [conflict, setConflict] = useState(false);
  const set = (patch: Partial<ChannelConfig>) => setDraft((d) => ({ ...d, ...patch }));
  const setBinding = (i: number, patch: Partial<ChannelBinding>) => set({ bindings: draft.bindings.map((b, j) => (j === i ? { ...b, ...patch } : b)) });
  const isNew = channel === null;

  const check = (): string => {
    if (!NAME_RE.test(draft.name.trim())) return t("execution.err_name");
    if (!draft.base_url.trim()) return t("execution.err_base_url");
    if (draft.endpoints.length === 0) return t("execution.err_endpoints");
    if (draft.bindings.length === 0 || draft.bindings.some((b) => !b.runtime.trim() || !b.alias.trim())) return t("execution.err_bindings");
    return "";
  };
  const save = async () => {
    const p = check();
    setProblem(p);
    if (p) return;
    setBusy(true);
    setConflict(false);
    try {
      await saveExecutionChannel({ ...draft, name: draft.name.trim() }, revision);
      toast(t("execution.channel_saved", { channel: draft.name.trim() }), "ok");
      onSaved();
    } catch (e) {
      if (isRevisionConflict(e)) {
        setConflict(true);
        setProblem(t("execution.conflict"));
        onConflict();
      } else setProblem(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const toggleEndpoint = (p: string, on: boolean) => set({ endpoints: on ? [...new Set([...draft.endpoints, p])] : draft.endpoints.filter((x) => x !== p) });
  const toggleMachine = (id: string, on: boolean) => {
    const now = draft.scope_machines ?? [];
    set({ scope_machines: on ? [...new Set([...now, id])] : now.filter((x) => x !== id) });
  };
  const label = (key: string, text: string) => <span id={`${ids}-${key}`} className="mb-1 block text-label font-medium text-ink-2">{text}</span>;

  return (
    <Dialog
      open
      onClose={onClose}
      width={560}
      title={channel ? t("execution.channel_edit_title", { channel: channel.display_name || channel.name }) : t("execution.channel_new_title")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" busy={busy} disabled={busy || conflict} testId="channel-save" onClick={() => void save()}>{t("execution.channel_save")}</Button>
        </>
      }
    >
      <div data-channel-editor="" className="flex max-h-[64vh] flex-col gap-3 overflow-y-auto pr-1">
        <p className="text-submeta text-ink-3">{t("execution.channels_live_note")}</p>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            {label("name", t("execution.channel_name"))}
            <input data-testid="channel-name" className={field} value={draft.name} disabled={!isNew || busy} onChange={(e) => set({ name: e.target.value })} spellCheck={false} />
          </label>
          <label className="block">
            {label("display", t("execution.channel_display_name"))}
            <input data-testid="channel-display" className={field} value={draft.display_name ?? ""} disabled={busy} onChange={(e) => set({ display_name: e.target.value })} />
          </label>
        </div>
        <label className="block">
          {label("url", t("execution.channel_base_url"))}
          <input data-testid="channel-base-url" className={field} value={draft.base_url} disabled={busy} onChange={(e) => set({ base_url: e.target.value })} spellCheck={false} inputMode="url" />
        </label>
        <label className="block">
          {label("key", t("execution.channel_key"))}
          <input {...SECRET_INPUT} autoComplete="off" type="password" data-testid="channel-key" className={field} value={draft.api_key ?? ""} disabled={busy}
            placeholder={channel?.has_key ? t("execution.secret_kept") : ""} onChange={(e) => set({ api_key: e.target.value })} />
        </label>
        <label className="block">
          {label("headers", t("execution.channel_headers"))}
          <textarea {...SECRET_INPUT} data-testid="channel-headers" rows={2} className={`${field} h-auto py-2 font-mono`} value={draft.headers ?? ""} disabled={busy}
            placeholder={channel?.has_headers ? t("execution.secret_kept") : t("execution.headers_placeholder")} onChange={(e) => set({ headers: e.target.value })} />
        </label>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            {label("auth", t("execution.channel_auth"))}
            <select className={field} value={draft.auth ?? ""} disabled={busy} onChange={(e) => set({ auth: e.target.value })}>
              <option value="">{t("execution.automatic")}</option>
              <option value="x-api-key">x-api-key</option>
              <option value="bearer">Bearer</option>
            </select>
          </label>
          <label className="block">
            {label("proxy", t("execution.channel_proxy"))}
            <input className={field} value={draft.proxy ?? ""} disabled={busy} onChange={(e) => set({ proxy: e.target.value })} spellCheck={false} />
          </label>
        </div>
        <fieldset className="flex flex-col gap-1">
          <legend className="mb-1 text-label font-medium text-ink-2">{t("execution.channel_endpoints")}</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {CHANNEL_PROTOCOLS.map((p) => (
              <label key={p} className="flex items-center gap-1.5 text-meta text-ink">
                <input type="checkbox" data-testid={`channel-endpoint-${p}`} checked={draft.endpoints.includes(p)} disabled={busy} onChange={(e) => toggleEndpoint(p, e.target.checked)} />
                {protocolLabel(p)}
              </label>
            ))}
          </div>
        </fieldset>
        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 text-label font-medium text-ink-2">{t("execution.channel_bindings")}</legend>
          {draft.bindings.map((b, i) => (
            <div key={i} data-binding={i} className="grid grid-cols-2 gap-2 rounded-control border border-line p-2 sm:grid-cols-[1fr_1fr_1fr_auto]">
              <select aria-label={t("execution.runtime")} className={field} value={b.runtime} disabled={busy} onChange={(e) => setBinding(i, { runtime: e.target.value })}>
                {[...new Set([b.runtime, ...runtimes].filter(Boolean))].map((r) => <option key={r} value={r}>{runtimeLabel(r)}</option>)}
              </select>
              <input aria-label={t("execution.binding_alias")} data-testid={`binding-alias-${i}`} placeholder={t("execution.binding_alias")} className={field} value={b.alias} disabled={busy} onChange={(e) => setBinding(i, { alias: e.target.value })} spellCheck={false} />
              <input aria-label={t("execution.binding_model")} placeholder={t("execution.binding_model")} className={field} value={b.model ?? ""} disabled={busy} onChange={(e) => setBinding(i, { model: e.target.value })} spellCheck={false} />
              <Button size="sm" tone="quiet" disabled={busy || draft.bindings.length <= 1} onClick={() => set({ bindings: draft.bindings.filter((_, j) => j !== i) })}>{t("execution.binding_remove")}</Button>
            </div>
          ))}
          <Button size="sm" tone="quiet" icon="plus" className="self-start" disabled={busy} onClick={() => set({ bindings: [...draft.bindings, { runtime: runtimes[0] ?? "claude", alias: "" }] })}>{t("execution.binding_add")}</Button>
        </fieldset>
        <fieldset className="flex flex-col gap-1">
          <legend className="mb-1 text-label font-medium text-ink-2">{t("execution.channel_scope")}</legend>
          <select className={field} value={draft.share || "all"} disabled={busy} onChange={(e) => set({ share: e.target.value })}>
            <option value="all">{t("execution.scope_all")}</option>
            <option value="selected">{t("execution.scope_pick")}</option>
            <option value="none">{t("execution.scope_none")}</option>
          </select>
          {draft.share === "selected" && (
            <div className="flex flex-wrap gap-x-4 gap-y-1 pt-1">
              {machines.map((m) => (
                <label key={m.id} className="flex items-center gap-1.5 text-meta text-ink">
                  <input type="checkbox" checked={(draft.scope_machines ?? []).includes(m.id)} disabled={busy} onChange={(e) => toggleMachine(m.id, e.target.checked)} />
                  {m.name}
                </label>
              ))}
            </div>
          )}
        </fieldset>
        {problem && <p role="alert" className="text-label text-red">{problem}</p>}
        {conflict && <Button size="sm" tone="secondary" className="self-start" onClick={onClose}>{t("execution.conflict_reload")}</Button>}
      </div>
    </Dialog>
  );
}
