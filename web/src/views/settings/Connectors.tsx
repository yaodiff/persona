import type { ModuleMailboxInboxSwitchProps, ModuleMailboxSettingsProps } from "../../core/module-host";
import { useMailbox, useMailboxEnabled } from "../../lib/mail";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { otherManager, ownedSub, parseOwnedSub, pinManagerAt, useChosenManager } from "../../lib/manager-choice";
import { t } from "../../i18n";
import { ExternalSourceSettingsEntry } from "../../features/external-sources/ExternalSourceSettingsEntry";
import { api, ApiError, errorText } from "../../lib/api";
import type { HubConnector } from "../../lib/connectors";
import { ago } from "../../lib/format";
import { accountTitle, type LarkAccount, type LarkAccountList } from "../../lib/lark-accounts";
import { useLarkOn, useStore } from "../../lib/store";
import { Button, cx, IconButton } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { Count, LarkAvatar } from "../inbox/InboxParts";
import { ConnectorDetail, ConnectorList } from "./HubConnector";
import { LarkDisplaySettings } from "./LarkDisplaySettings";
import { SettingsRows } from "../../core/SettingsRows";
import { Group, Pill, Row } from "./SettingsParts";
import { SentSettings } from "./SentSettings";
import { SENT_SUB } from "./nav";
import { FeishuBot } from "./FeishuBot";

interface LarkStatus {
  configured: boolean;
  connected: boolean;
  name?: string;
  error?: string;
  last_event_at?: number;
}

/** `headerBack`: the page's top bar has the way back (a phone), so an item's page draws no Back of its own. `openBeside`:
 *  the item open beside this list (Settings' two columns), whose row the list marks. */
export function Connectors({ open, onOpen, onChannel, headerBack, openBeside, manager: scope }: { open: string | null; onOpen: (id: string | null) => void; onChannel?: (id: string, manager?: string) => void; headerBack?: boolean; openBeside?: string | null; manager?: string }) {
  // An older hub has no connectors API (feature "connectors"): only mail and Feishu, as before.
  const hasConnectors = useStore((s) => s.features.includes("connectors"));
  // Mail only on a hub that has the mail module (its flag module.mail; an older hub without module flags has it).
  const mail = useMailboxEnabled();
  // Lark (the Lark gateway through /api/v1/lark/…) only on a hub that has it (feature `lark`); "Lark bot" is the hub's own.
  const lark = useLarkOn();
  const picked = useChosenManager();
  const [larkcli, setLarkcli] = useState<HubConnector | null | undefined>();
  const { id, manager } = parseOwnedSub(open ?? "");
  const owner = manager ?? scope ?? otherManager(picked ?? undefined);
  useLayoutEffect(() => {
    if (!manager) return;
    if (id === "feishu-bot") return pinManagerAt(["/bots/feishu", "/channels/feishu", "/connectors/larkcli"], manager);
    if (id === "mail") return pinManagerAt(["/mail", "/connectors/mail"], manager);
    if (id === "lark") return pinManagerAt(["/lark"], manager);
  }, [id, manager]);
  if (open === SENT_SUB || id === "mail" || id === "lark") return (
    <>
      {!headerBack && <Button tone="quiet" icon="chevronLeft" onClick={() => onOpen(null)} className="-ml-2 mb-3">{t("settings-hubconnector.back")}</Button>}
      {id === "mail" && <Mail key={open} manager={owner} />}
      {id === "lark" && <Feishu key={open} manager={owner} />}
      {open === SENT_SUB && <>
        <h2 className="mb-2 px-1 text-[15px] font-semibold text-ink">{t("settings-sent.entry_label")}</h2>
        <SentSettings />
      </>}
    </>
  );
  if (open && id !== "feishu-bot") {
    return <ConnectorDetail key={open} id={id} manager={owner} onBack={headerBack ? undefined : () => onOpen(null)} onChannel={onChannel} />;
  }
  const bot = <FeishuBot key={owner} manager={owner} onChannel={onChannel}
    larkcli={manager ? undefined : larkcli} onOpen={(next) => onOpen(ownedSub(next, owner))}
    onRouteClose={id === "feishu-bot" ? () => onOpen(null) : undefined} />;
  if (id === "feishu-bot" && manager) return bot;
  const beside = parseOwnedSub(openBeside ?? "").id;
  return (
    <>
      <p className="mb-5 px-1 text-[12.5px] leading-relaxed text-ink-3">
        {t("settings-connectors.intro")}
      </p>
      {mail && beside !== "mail" && <Mail manager={scope} />}
      {lark && beside !== "lark" && <Feishu manager={scope} />}
      {beside !== "feishu-bot" && bot}
      {hasConnectors && <ConnectorList manager={scope} onOpen={onOpen} onLarkCli={setLarkcli} openBeside={openBeside} />}
      <Group anchor="sent">
        <Row icon="send" label={t("settings-sent.entry_label")} hint={t("settings-sent.hint")} onClick={() => onOpen(SENT_SUB)} />
      </Group>
    </>
  );
}

export function Mail(props: ModuleMailboxSettingsProps) {
  const { mailbox } = useMailbox();
  return mailbox ? <mailbox.Settings {...props} /> : null;
}

export function MailInboxSwitch(props: ModuleMailboxInboxSwitchProps) {
  const { mailbox } = useMailbox();
  return mailbox ? <mailbox.InboxSwitch {...props} /> : null;
}

// ── Lark ────────────────────────────────────────────────

const USES: { icon: IconName; title: string; detail: string }[] = [
  { icon: "inbox", title: t("settings-connectors.use_inbox"), detail: t("settings-connectors.use_inbox_detail") },
  { icon: "book", title: t("settings-connectors.use_knowledge"), detail: t("settings-connectors.use_knowledge_detail") },
  { icon: "spark", title: t("settings-connectors.use_assistant"), detail: t("settings-connectors.use_assistant_detail") },
  { icon: "agent", title: "/agent", detail: t("settings-connectors.use_agent_detail") },
];

function LarkAccountRow({ a, isDefault }: { a: LarkAccount; isDefault: boolean }) {
  const detail = a.quarantined ? t("settings-connectors.account_quarantined") : !a.signed_in ? t("settings-connectors.signed_out") : a.tenant || t("settings-connectors.signed_in");
  return (
    <div data-account={a.id} className="flex items-center gap-3 px-4 py-2.5">
      <LarkAvatar name={accountTitle(a)} size={30} avatarURL={a.avatar_url} avatar={a.avatar} account={a.id} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] text-ink">{accountTitle(a)}</div>
        <div className={cx("mt-0.5 truncate text-[11.5px]", a.signed_in ? "text-ink-3" : "text-red")}>{detail}</div>
      </div>
      {isDefault && <Pill>{t("settings-connectors.default")}</Pill>}
      {a.signed_in && a.unread > 0 && <Count n={a.unread} />}
    </div>
  );
}

export function Feishu({ manager, reloadKey }: { manager?: string; reloadKey?: number } = {}) {
  const [st, setSt] = useState<LarkStatus | null>(null);
  const [accounts, setAccounts] = useState<LarkAccountList | null>(null);
  const [error, setError] = useState("");
  const [absent, setAbsent] = useState(false);
  const [loading, setLoading] = useState(true);
  const read = useRef(0);
  const load = () => {
    const n = ++read.current;
    setLoading(true);
    // the Feishu accounts on the hub's Lark gateway (docs/extensions/feishu.md "Multiple accounts"); a hub from before them has none to list
    api<LarkAccountList>("/lark/accounts").then((r) => n === read.current && setAccounts(r)).catch(() => n === read.current && setAccounts(null));
    api<LarkStatus>("/lark/status")
      .then((r) => n === read.current && (setSt(r), setAbsent(false), setError("")))
      // the current assistant has no such connector (its manager has no route for it): the group shows "not set up"
      .catch((e) => n === read.current && (e instanceof ApiError && e.status === 404 ? (setSt({ configured: false, connected: false }), setAbsent(true), setError("")) : (setAbsent(false), setError(errorText(e)))))
      .finally(() => n === read.current && setLoading(false));
  };
  useEffect(load, [reloadKey]);

  const state = !st ? (loading ? t("settings-connectors.checking") : t("settings-connectors.status_unknown")) : !st.configured ? t("settings-connectors.not_configured") : st.connected ? t("settings-connectors.connected") : t("settings-connectors.unreachable");
  const dot = !st ? "bg-line-strong" : st.connected ? "bg-green" : st.configured ? "bg-red" : "bg-line-strong";
  const last = st?.last_event_at ? ago(new Date(st.last_event_at * 1000).toISOString()) : "";

  return (
    <>
    <Group
      anchor="lark"
      title={t("settings-connectors.lark")}
      footer={st?.configured === false ? t("settings-connectors.lark_footer_unconfigured") : t("settings-connectors.lark_footer")}
    >
      <div className="flex items-center gap-3 px-4 py-3">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-accent-tint text-accent-ink">
          <Icon name="chat" size={16} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13.5px] font-medium text-ink">{st?.name || t("settings-connectors.lark")}</div>
          <div className="mt-0.5 flex items-center gap-1.5 text-[11.5px] text-ink-3">
            <span className={cx("size-1.5 shrink-0 rounded-full", dot)} />
            <span className="truncate">{state}</span>
          </div>
        </div>
        <IconButton icon="retry" label={t("settings-connectors.recheck")} onClick={load} disabled={loading} />
      </div>
      {last && (
        <div className="flex items-center gap-3 px-4 py-2.5">
          <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{t("settings-connectors.last_event")}</span>
          <span className="shrink-0 text-[12.5px] text-ink-2">{last}</span>
        </div>
      )}
      {(st?.error || error) && <p className="px-4 py-2.5 text-[12px] break-words text-red">{st?.error || t("settings-connectors.lark_status_failed", { error })}</p>}
      {accounts && accounts.accounts.length > 1 && (
        <>
          <div className="px-4 pt-2.5 pb-1 text-[11.5px] font-medium text-ink-3">{t("settings-connectors.accounts")}</div>
          {accounts.accounts.map((a) => <LarkAccountRow key={a.id} a={a} isDefault={a.id === accounts.default} />)}
          <p className="px-4 pt-1 pb-2.5 text-[11.5px] leading-snug text-ink-3">{t("settings-connectors.accounts_note")}</p>
        </>
      )}
      {/* the lark module's rows (Show messages on home, #13404): core/module-host.ts `settingsRows` */}
      <SettingsRows place="connectors.lark" />
      {!absent && <LarkDisplaySettings />}
      <div className="px-4 pt-2.5 pb-1 text-[11.5px] font-medium text-ink-3">{t("settings-connectors.lark_uses")}</div>
      {USES.map((u) => (
        <div key={u.title} className="flex items-start gap-3 px-4 py-2.5">
          <Icon name={u.icon} size={15} className="mt-0.5 shrink-0 text-accent" />
          <div className="min-w-0">
            <div className="text-[13px] text-ink">{u.title}</div>
            <div className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{u.detail}</div>
          </div>
        </div>
      ))}
    </Group>
    {st?.configured && <ExternalSourceSettingsEntry provider="lark" manager={manager}
      accounts={accounts?.accounts.map((account) => ({ channel: account.id > 1 ? String(account.id) : "", name: accountTitle(account) }))} />}
    </>
  );
}
