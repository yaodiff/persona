import { useEffect, useRef, useState } from "react";
import { formatDate, formatList, t } from "../../i18n";
import { api, ApiError, enc } from "../../lib/api";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { Button, Dialog, Loading, toast } from "../../ui/atoms";
import { Group, SettingsHint } from "./SettingsParts";

interface StandingAuthorization {
  id: string;
  machine_id: string;
  directory: string;
  actions: string[];
  conditions: string[];
  quote: string;
  source: string;
  granted_at: string;
  reversible: boolean;
}

type Registry = { revision: number; authorizations: StandingAuthorization[] };
type ListState = { kind: "loading" } | { kind: "failed"; forbidden: boolean } | { kind: "ready"; registry: Registry };
type Revocation = { grant: StandingAuthorization; status: "confirm" | "busy" | "failed" } | null;

const actionKeys: Record<string, string> = {
  merge: "settings-standing.action_merge",
  push: "settings-standing.action_push",
  "deploy-test": "settings-standing.action_deploy_test",
  "deploy-production": "settings-standing.action_deploy_production",
  send: "settings-standing.action_send",
  delete: "settings-standing.action_delete",
  spend: "settings-standing.action_spend",
};
const actionLabel = (action: string) => Object.hasOwn(actionKeys, action) ? t(actionKeys[action]) : action;
const conditionLabel = (condition: string) => condition === "ci_green" ? t("settings-standing.condition_ci_green")
  : condition === "no_conflicts" ? t("settings-standing.condition_no_conflicts") : condition;

// A grant's source is words, not a code (#7181, as the apps' StandingAuthorizationText.source): the hub's own sentence
// for a suggested rule confirmed with Always (assistant-rule_suggest.json "source", in the owner's language when it was
// made) or what the assistant wrote. The ones known in en, zh-Hans or ja are worded in the page's language; anything else
// is shown as written.
const suggestedRule = [
  /^Suggested rule: the same answer (\d+) times in 14 days, confirmed with Always\.$/,
  /^建议的规则：14 天内 (\d+) 次相同的回答，点「总是」确认。$/, // i18n-ignore: the hub's sentence
  /^提案されたルール：14 日間に同じ回答が (\d+) 回あり、「常に」で確認されました。$/, // i18n-ignore: the hub's sentence
];
const mainChat = ["main conversation", "the main conversation", "main chat", "the main chat",
  "主对话", "主会话", "主聊天", "メインチャット", "メインの会話", "メイン会話"]; // i18n-ignore: the words a grant's source may hold
const aChat = ["conversation", "a conversation", "chat", "a chat", "对话", "会话", "会話", "チャット"]; // i18n-ignore: the words a grant's source may hold
const settings = ["settings", "设置", "設定"]; // i18n-ignore: the words a grant's source may hold
export function sourceLabel(source: string): string {
  const text = source.trim();
  for (const pattern of suggestedRule) {
    const match = pattern.exec(text);
    if (match) return t("settings-standing.source_suggested_rule", { n: Number(match[1]) });
  }
  const key = text.toLowerCase().replace(/^[.。 ]+|[.。 ]+$/g, "");
  if (mainChat.includes(key)) return t("settings-standing.source_main_chat");
  if (aChat.includes(key)) return t("settings-standing.source_chat");
  if (settings.includes(key)) return t("settings-standing.source_settings");
  return text;
}

/** The parent is keyed by both manager and credential generation, like the other Persona settings. */
export function StandingAuthorizations({ manager }: { manager: string }) {
  const [list, setList] = useState<ListState>({ kind: "loading" });
  const [reload, setReload] = useState(0);
  const [revocation, setRevocation] = useState<Revocation>(null);
  const live = useRef(false);
  const writing = useRef(false);
  useEffect(() => { live.current = true; return () => { live.current = false; }; }, []);
  useEffect(() => {
    const controller = new AbortController();
    setList({ kind: "loading" });
    api<Registry>("/assistant/standing-authorizations", { headers: { [MANAGER_HEADER]: manager }, signal: controller.signal }).then(
      registry => {
        if (controller.signal.aborted) return;
        setList(Array.isArray(registry.authorizations) && typeof registry.revision === "number"
          ? { kind: "ready", registry } : { kind: "failed", forbidden: false });
      },
      error => { if (!controller.signal.aborted) setList({ kind: "failed", forbidden: error instanceof ApiError && error.status === 403 }); },
    );
    return () => controller.abort();
  }, [manager, reload]);

  const revoke = async () => {
    if (!revocation || writing.current) return;
    writing.current = true;
    const grant = revocation.grant;
    setRevocation({ grant, status: "busy" });
    try {
      const result = await api<{ ok: boolean; revision: number }>(`/assistant/standing-authorizations/${enc(grant.id)}`, {
        method: "DELETE", headers: { [MANAGER_HEADER]: manager },
      });
      if (!result.ok) throw new Error("Revocation was not acknowledged");
      if (!live.current) return;
      setList(previous => previous.kind === "ready" ? { kind: "ready", registry: {
        revision: result.revision, authorizations: previous.registry.authorizations.filter(entry => entry.id !== grant.id),
      } } : previous);
      setRevocation(null);
      toast(t("settings-standing.revoked"), "ok");
    } catch {
      if (live.current) setRevocation({ grant, status: "failed" });
    } finally {
      writing.current = false;
    }
  };
  const close = () => { if (!writing.current) setRevocation(null); };

  return <>
    <Group anchor="standing-authorizations" title={t("settings-standing.title")} footer={t("settings-standing.footer")}
      right={<Button className="min-h-11" icon="retry" disabled={list.kind === "loading" || !!revocation} onClick={() => setReload(n => n + 1)}>{t("settings-standing.refresh")}</Button>}>
      <SettingsHint>{t("settings-standing.description")}</SettingsHint>
      {list.kind === "loading" ? <div className="p-4"><Loading /></div>
        : list.kind === "failed" ? <SettingsHint alert>{t(list.forbidden ? "settings-standing.forbidden" : "settings-standing.load_failed")}</SettingsHint>
        : list.registry.authorizations.length === 0 ? <SettingsHint>{t("settings-standing.empty")}</SettingsHint>
        : list.registry.authorizations.map(grant => <article key={grant.id} aria-label={grant.directory} className="space-y-3 p-4">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <h3 className="min-w-0 flex-1 break-words font-mono text-sm text-ink [overflow-wrap:anywhere]">{grant.directory}</h3>
            <Button className="min-h-11" disabled={!!revocation} onClick={() => setRevocation({ grant, status: "confirm" })}>{t("settings-standing.revoke")}</Button>
          </div>
          <dl className="grid min-w-0 grid-cols-1 gap-3 text-sm text-ink sm:grid-cols-2">
            <Detail label={t("settings-standing.machine")}>{grant.machine_id}</Detail>
            <Detail label={t("settings-standing.directory")}>{grant.directory}</Detail>
            <Detail label={t("settings-standing.actions")}><ul>{grant.actions.map(action => <li key={action}>{actionLabel(action)}</li>)}</ul></Detail>
            <Detail label={t("settings-standing.conditions")}>{grant.conditions?.length ? <ul>{grant.conditions.map((condition, i) => <li key={i}>{conditionLabel(condition)}</li>)}</ul> : t("settings-standing.no_conditions")}</Detail>
            <Detail label={t("settings-standing.quote")} wide>{grant.quote}</Detail>
            <Detail label={t("settings-standing.granted_at")}><time dateTime={grant.granted_at} title={grant.granted_at}>{formatDate(grant.granted_at, { dateStyle: "medium", timeStyle: "short" })}</time></Detail>
            <Detail label={t("settings-standing.source")}>{sourceLabel(grant.source)}</Detail>
            <Detail label={t("settings-standing.reversible")}>{t(grant.reversible ? "settings-standing.reversible_yes" : "settings-standing.reversible_no")}</Detail>
          </dl>
        </article>)}
    </Group>
    <Dialog open={!!revocation} onClose={close} title={t("settings-standing.confirm_title")} footer={<>
      <Button className="min-h-11" disabled={revocation?.status === "busy"} onClick={close}>{t("common.cancel")}</Button>
      <Button className="min-h-11" tone="danger" busy={revocation?.status === "busy"} onClick={() => void revoke()}>{t("settings-standing.confirm_revoke")}</Button>
    </>}>
      {revocation && <div className="space-y-3 break-words text-sm text-ink [overflow-wrap:anywhere]">
        <p>{t("settings-standing.confirm_effect")}</p>
        <p className="font-mono">{revocation.grant.machine_id} · {revocation.grant.directory}</p>
        <p>{formatList(revocation.grant.actions.map(actionLabel))}</p>
        <p className="whitespace-pre-wrap text-ink-2">{revocation.grant.quote}</p>
        {revocation.status === "failed" && <p role="alert" className="text-red">{t("settings-standing.delete_failed")}</p>}
      </div>}
    </Dialog>
  </>;
}

function Detail({ label, children, wide }: { label: string; children: React.ReactNode; wide?: boolean }) {
  return <div className={wide ? "min-w-0 sm:col-span-2" : "min-w-0"}>
    <dt className="mb-1 text-xs text-ink-3">{label}</dt>
    <dd className="whitespace-pre-wrap break-words [overflow-wrap:anywhere]">{children}</dd>
  </div>;
}
