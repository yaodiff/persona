// Assistant-owned references, not checkout locations or task authorization.
import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, credentialOwner, errorText, getToken, HubTimeoutError, pastHub } from "../../lib/api";
import { parseRepository, type RepositoryIdentity as Identity } from "../../lib/repository-identity";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { Button, Dialog, Loading, toast } from "../../ui/atoms";
import { Group, Row, settingsInputClass } from "./SettingsParts";

type Binding = Identity & { id: string; name?: string; aliases?: string[]; purpose?: string; verified_at: string };
type Draft = { id?: string; url: string; provider: string; name: string; aliases: string; purpose: string };
type Reply = { manager: string; repositories?: Binding[]; repository?: Binding; identity?: Identity; receipt?: string };
const blank = (): Draft => ({ url: "", provider: "", name: "", aliases: "", purpose: "" });
const validIdentity = (value?: Identity): value is Identity => !!value && ["github", "gitlab"].includes(value.provider)
  && typeof value.instance_url === "string" && typeof value.path === "string" && typeof value.canonical_url === "string";
const validBinding = (value?: Binding): value is Binding => validIdentity(value) && typeof value.id === "string"
  && typeof value.verified_at === "string" && Number.isFinite(Date.parse(value.verified_at));

export function Repositories({ manager, owner }: { manager: string; owner: string }) {
  const [token] = useState(getToken);
  const alive = useRef(true);
  const locked = useRef(false);
  const [expanded, setExpanded] = useState(false);
  const [rows, setRows] = useState<Binding[] | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [draft, setDraft] = useState<Draft | null>(null);
  const [removing, setRemoving] = useState<Binding | null>(null);
  const identity = draft ? parseRepository(draft.url, draft.provider) : undefined;
  const original = rows?.find((value) => value.id === draft?.id);
  const needsCheck = !original || original.canonical_url !== identity?.canonical_url || original.provider !== identity?.provider;
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const current = () => alive.current && owner === credentialOwner();
  async function request(path: string, method = "GET", json?: unknown) {
    const result = await api<Reply>(`/assistant/repositories${path}`, {
      method, json, token, tokenOwner: owner, isAuthContextCurrent: current, headers: { [MANAGER_HEADER]: manager },
      timeout: method === "GET" ? undefined : pastHub(8_000),
    });
    if (result.manager !== manager) throw new Error(t("settings-repositories.load_failed"));
    return result;
  }
  async function run(kind: string, action: () => Promise<void>) {
    if (locked.current || !current()) return;
    locked.current = true; setBusy(kind); setError("");
    try { await action(); }
    catch (e) { if (current()) setError(e instanceof HubTimeoutError && kind !== "load" ? t("settings-repositories.save_unconfirmed") : errorText(e)); }
    finally { locked.current = false; if (current()) setBusy(""); }
  }
  const load = () => run("load", async () => {
    const result = await request("");
    if (!Array.isArray(result.repositories) || !result.repositories.every(validBinding)) throw new Error(t("settings-repositories.load_failed"));
    if (current()) setRows(result.repositories);
  });
  const body = () => ({
    url: identity!.canonical_url, provider: identity!.provider, name: draft!.name,
    aliases: draft!.aliases.split(",").map((v) => v.trim()).filter(Boolean), purpose: draft!.purpose,
  });
  const save = () => run("save", async () => {
    const result = await request(draft!.id ? `/${encodeURIComponent(draft!.id)}` : "", draft!.id ? "PUT" : "POST", body());
    if (!validBinding(result.repository) || result.repository.provider !== identity!.provider
      || result.repository.instance_url !== identity!.instance_url || result.repository.path !== identity!.path
      || result.repository.canonical_url !== identity!.canonical_url || (draft!.id && result.repository.id !== draft!.id)) {
      throw new Error(t("settings-repositories.save_unconfirmed"));
    }
    if (!current()) return;
    const saved = result.repository;
    setRows((values) => [...(values ?? []).filter((v) => v.id !== saved.id), saved]);
    setDraft(null); toast(t("settings-repositories.saved"), "ok");
  });
  const unbind = () => run("unbind", async () => {
    const target = removing!;
    const result = await request(`/${encodeURIComponent(target.id)}`, "DELETE");
    if (!result.receipt) throw new Error(t("settings-repositories.save_unconfirmed"));
    if (!current()) return;
    setRows((values) => values!.filter((v) => v.id !== target.id)); setRemoving(null);
    toast(t("settings-repositories.unbound"), "info", { label: t("settings-repositories.undo"), run: () => {
      void run("restore", async () => {
        const restored = await request(`/${encodeURIComponent(target.id)}/restore`, "POST", { receipt: result.receipt });
        if (!validBinding(restored.repository)) throw new Error(t("settings-repositories.save_unconfirmed"));
        if (current()) setRows((values) => [...(values ?? []).filter((v) => v.id !== target.id), restored.repository!]);
      });
    } });
  });
  const edit = (value: Binding) => {
    setError(""); setDraft({ id: value.id, url: value.canonical_url, provider: value.provider, name: value.name ?? "", aliases: value.aliases?.join(", ") ?? "", purpose: value.purpose ?? "" });
  };
  const field = (key: "url" | "name" | "aliases" | "purpose", label: string, max: number) => (
    <label className="block text-[12.5px] text-ink-2">{label}
      <input className={`${settingsInputClass} mt-1`} value={draft![key]} maxLength={max} disabled={!!busy} autoComplete="off" spellCheck={false}
        onChange={(e) => { setDraft({ ...draft!, [key]: e.target.value }); if (key === "url") setError(""); }} />
    </label>
  );
  return <Group anchor="repositories" title={t("settings-repositories.title")} footer={t("settings-repositories.hint")}>
    <Row label={t("settings-repositories.title")} hint={t("settings-repositories.scope")}>
      <Button expanded={expanded} onClick={() => { setExpanded(!expanded); if (!expanded && rows === null) void load(); }}>{t("settings-repositories.manage")}</Button>
    </Row>
    {expanded && <div className="space-y-3 px-4 py-3">
      <div className="flex flex-wrap gap-2">
        <Button disabled={!!busy || rows === null} onClick={() => { setError(""); setDraft(blank()); }}>{t("settings-repositories.bind")}</Button>
        <Button tone="quiet" disabled={!!busy} onClick={() => void load()}>{t("settings-repositories.refresh")}</Button>
      </div>
      {!!busy && <Loading label={t(busy === "save" ? needsCheck ? "settings-repositories.checking" : "settings-repositories.saving" : "common.loading")} />}
      {error && !draft && !removing && <p role="alert" className="text-[12.5px] text-red">{error}</p>}
      {rows?.length === 0 && <p className="text-[13px] text-ink-3">{t("settings-repositories.empty")}</p>}
      {rows?.map((value) => <div key={value.id} className="space-y-1 border-t border-line pt-3" data-repository={value.id}>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="min-w-0 break-words text-[13px] font-medium text-ink">{value.name || value.path}</span>
          <div className="flex gap-1">
            <Button tone="quiet" disabled={!!busy} onClick={() => edit(value)}>{t("settings-repositories.edit")}<span className="sr-only"> {value.name || value.path}</span></Button>
            <Button tone="quiet" disabled={!!busy} onClick={() => { setError(""); setRemoving(value); }}>{t("settings-repositories.unbind")}<span className="sr-only"> {value.name || value.path}</span></Button>
          </div>
        </div>
        <p className="break-all font-mono text-[11.5px] text-ink-2">{value.canonical_url}</p>
        {value.aliases?.length ? <p className="text-[12px] text-ink-2">{t("settings-repositories.aliases")}: {value.aliases.join(", ")}</p> : null}
        {value.purpose && <p className="break-words text-[12.5px] text-ink-2">{value.purpose}</p>}
        <p className="text-[11.5px] text-ink-3">{t("settings-repositories.verified", { time: new Date(value.verified_at).toLocaleString() })}</p>
      </div>)}
    </div>}
    <Dialog open={!!draft} onClose={() => { if (!busy) { setDraft(null); setError(""); } }} title={t(draft?.id ? "settings-repositories.edit_title" : "settings-repositories.bind")}
      footer={<><Button disabled={!!busy} onClick={() => { setDraft(null); setError(""); }}>{t("common.cancel")}</Button><Button tone="primary" busy={busy === "save"} disabled={!!busy || !identity} onClick={() => void save()}>{t(draft?.id ? "settings-repositories.save" : "settings-repositories.check_save")}</Button></>}>
      {draft && <div className="space-y-3">
        {field("url", t("settings-repositories.url"), 2048)}
        <label className="block text-[12.5px] text-ink-2">{t("settings-repositories.provider")}
          <select className={`${settingsInputClass} mt-1`} value={draft.provider} disabled={!!busy} onChange={(e) => { setDraft({ ...draft, provider: e.target.value }); setError(""); }}>
            <option value="">{t("settings-repositories.detect")}</option><option value="github">GitHub / GitHub Enterprise</option><option value="gitlab">GitLab</option>
          </select>
        </label>
        {identity && <div data-repository-preview role="status" aria-live="polite" className="rounded-control bg-field p-3 text-[12px] text-ink-2">
          <p>{identity.provider} · {identity.instance_url}</p><p className="break-all font-mono">{identity.path}</p>
          <p className="break-all font-mono">{identity.canonical_url}</p>
          <p>{t(needsCheck ? "settings-repositories.unchecked" : "settings-repositories.edit_check")}</p>
        </div>}
        {draft.url && !identity && <p role="status" className="text-[12px] text-ink-2">{t("settings-repositories.invalid_url")}</p>}
        {field("name", t("settings-repositories.name"), 80)}
        {field("aliases", t("settings-repositories.aliases"), 809)}
        {field("purpose", t("settings-repositories.purpose"), 500)}
        <p className="text-[11.5px] leading-relaxed text-ink-3">{t("settings-repositories.check_hint")}</p>
        {busy === "save" && <Loading label={t(needsCheck ? "settings-repositories.checking" : "settings-repositories.saving")} />}
        {error && <p role="alert" className="text-[12.5px] text-red">{error}</p>}
      </div>}
    </Dialog>
    <Dialog open={!!removing} onClose={() => { if (!busy) { setRemoving(null); setError(""); } }} title={t("settings-repositories.unbind_title")}
      footer={<><Button disabled={!!busy} onClick={() => { setRemoving(null); setError(""); }}>{t("common.cancel")}</Button><Button tone="danger" busy={busy === "unbind"} disabled={!!busy} onClick={() => void unbind()}>{t("settings-repositories.unbind_title")}</Button></>}>
      <p className="break-all font-mono text-[12px] text-ink">{removing?.canonical_url}</p>
      <p className="mt-3 text-[13px] leading-relaxed text-ink-2">{t("settings-repositories.unbind_hint")}</p>
      {error && <p role="alert" className="mt-3 text-[12.5px] text-red">{error}</p>}
    </Dialog>
  </Group>;
}
