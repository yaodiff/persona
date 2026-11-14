// Settings › Connectors' hub connectors (iOS ConnectorsSection's rows, HubConnectorView, ConnectorSignInSections,
// GitHubConnectorSections, GitHubInboxSettings; docs/extensions/connectors.md): GitHub, Telegram and any extension's connector, as
// GET /api/v1/connectors lists them. A page shows how one is doing, as whom, where its sign-in comes from and what is
// wrong (with the steps to fix it); one the user signs in to here (`login`) also gets its switch, the sign-in a step
// at a time (fields the hub asks for, GitHub's device code, or a QR code to scan), "Add to knowledge base" / "Add to Inbox", sign-out and delete. GitHub adds
// where its token comes from (a computer's gh, or the device flow), the scopes it lacks, and its inbox options.
// Messages (iMessage) adds "Hand to Persona" and deleting what the hub kept (iOS MessagesConnectorView, /api/v1/imessage…).
import { OwnerMark } from "./OwnerMark";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { errorText } from "../../lib/api";
import { otherManager, ownedSub, pinManagerAt, sameManager, useChosenManager } from "../../lib/manager-choice";
import { isOpenBeside } from "./open-beside";
import {
  canSendStep, CI_MODES, deleteIMessageData, imessageAIFooter, imessageStatus, setIMessageAI, connectorBrand, connectorLogin, connectorLogout, connectorState, connectorSummary, connectorTone, deleteConnectorArchive,
  deleteConnectorData, deletedLine, feedsFooter, fieldInput, githubAuth, githubInboxFooter, githubMachineLine, githubMachineWarns, githubOptions, larkcliOptions,
  githubScopeAdvice, isEnabled, isPolledStep, isSignedIn, isStarting, listConnectors, logoutMessage, mutableRepo, otherConnectors, setConnector, setGitHubAuth,
  signInFooter, signsInHere, stepView, storesData, switchFooter, switchLabel, undescribed,
  type ConnectorPrompt, type ConnectorTone, type GitHubAuth, type GitHubInboxOptions, type LarkCLIInboxOptions, type HubConnector, type IMessageStatus,
} from "../../lib/connectors";
import { useChannels } from "../../lib/channels";
import { ago } from "../../lib/format";
import { useLarkOn } from "../../lib/store";
import { latestRequest } from "../../lib/visible-poll";
import { Button, copyText, cx, Dialog, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { BrandMark } from "../../ui/BrandMark";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import { ChannelLink } from "./Channels";
import { QRCode, QRLinkStep, QRStep, UnsupportedStep, useStepPolling } from "./LoginSteps";
import { Group, Row, Switch } from "./SettingsParts";
import { scoped, useScope } from "../../lib/current-assistant";
import { OfflineState, useShownOffline } from "../ManagerParts";
import { SubscriptionsEntry } from "../subscriptions/SubscriptionsEntry";

const DOT: Record<ConnectorTone, string> = { green: "bg-green", orange: "bg-orange", red: "bg-red", idle: "bg-line-strong" };
const fieldClass = "h-8 w-full rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

function Mark({ id, size }: { id: string; size: number }) {
  const brand = connectorBrand(id);
  if (brand) {
    return (
      <span className="flex shrink-0 items-center justify-center rounded-[9px] bg-surface shadow-hairline" style={{ width: size, height: size }}>
        <BrandMark brand={brand} size={size} fit="tile" />
      </span>
    );
  }
  return (
    <span className="flex shrink-0 items-center justify-center rounded-[9px] bg-accent-tint text-accent-ink" style={{ width: size, height: size }}>
      <Icon name={id === "imessage" ? "chat" : "link"} size={Math.round(size * 0.45)} />
    </span>
  );
}

// ── the rows on Settings › Connectors ────────────────────

/** `openBeside`: the connector open beside the list (Settings' two columns): its row is marked current, and every row
 *  puts its state under its name, so the narrow column shows the name whole. */
export function ConnectorList({ onOpen, onLarkCli, openBeside }: { onOpen: (id: string) => void; onLarkCli?: (connector: HubConnector | null) => void; openBeside?: string | null }) {
  const [all, setAll] = useState<HubConnector[] | null>(null);
  const [error, setError] = useState("");
  const lark = useLarkOn();
  const picked = useChosenManager();
  useEffect(() => {
    listConnectors()
      .then((l) => {
        setAll(l);
        setError("");
      })
      .catch((e) => setError(errorText(e)));
  }, []);
  // the current assistant's connectors only (each assistant has its own built-ins, docs/assistant/managers.md "The current
  // assistant"); a row opens that assistant's copy. With one assistant, the list as it is.
  const scope = useScope();
  const list = useMemo(() => all && scoped(otherConnectors(all, lark), scope), [all, lark, scope]);
  // the chat connector's command-line sign-in the Feishu bot's dialog offers is the current assistant's copy
  useEffect(() => {
    if (all) onLarkCli?.(scoped(all, scope).find((c) => c.id === "larkcli") ?? null);
  }, [all, scope, onLarkCli]);
  // an offline current assistant: the hub's merged list leaves it out, so an empty list says it is offline (as the inbox does)
  const offline = useShownOffline();
  if (list?.length === 0) {
    return offline ? (
      <Group title={t("settings-hubconnector.more_title")}>
        <OfflineState offline={offline} ring="ring-surface" className="py-8" />
      </Group>
    ) : null;
  }
  return (
    <Group title={t("settings-hubconnector.more_title")} footer={t("settings-hubconnector.more_footer")}>
      {!list && !error && <div className="px-4 py-3"><Loading /></div>}
      {error && <p className="px-4 py-3 text-[12.5px] text-red">{t("settings-hubconnector.list_failed", { error })}</p>}
      {(list ?? []).map((c) => {
        // a route without @manager opens the current assistant's copy
        const current = isOpenBeside(openBeside, c.id, c.manager, picked);
        const state = (
          <span className={cx("flex min-w-0 items-center gap-1.5 text-ink-2", openBeside !== undefined ? "mt-0.5 text-submeta" : "text-[12.5px]")}>
            <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", DOT[connectorTone(c)])} />
            <span className="truncate">{connectorSummary(c)}</span>
          </span>
        );
        const name = <span className={cx("min-w-0 truncate text-[13px] text-ink", openBeside !== undefined ? "block" : "flex-1")}>{c.name}</span>;
        return (
          <button key={`${c.manager ?? ""}:${c.id}`} type="button" data-connector-row={c.id} data-manager={c.manager ?? ""} aria-current={current || undefined} onClick={() => onOpen(ownedSub(c.id, c.manager))}
            className={cx("flex w-full items-center gap-3 px-4 py-2.5 text-left whitespace-nowrap transition-colors", current ? "bg-accent-tint" : "hover:bg-hover")}>
            <Mark id={c.id} size={32} />
            {openBeside !== undefined ? (
              <span className="min-w-0 flex-1">
                {name}
                {state}
              </span>
            ) : (
              <>
                {name}
                {state}
              </>
            )}
            <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
          </button>
        );
      })}
    </Group>
  );
}

// ── one connector's page ─────────────────────────────────

/** `manager`: whose connector (another manager's, opened from its extension's page, docs/assistant/managers.md "Clients"): the
 *  merged list's copy of that manager, and every request of the page names it while it is open; none: the current assistant's (Connectors fills it in). */
/** `onBack`: none where the page's top bar has the way back (a phone). */
export function ConnectorDetail({ id, manager, onBack, onChannel }: { id: string; manager?: string; onBack?: () => void; onChannel?: (id: string, manager?: string) => void }) {
  useLayoutEffect(() => pinManagerAt(id === "imessage" ? [`/connectors/${encodeURIComponent(id)}`, "/imessage"] : [`/connectors/${encodeURIComponent(id)}`], otherManager(manager)), [id, manager]);
  const [c, setC] = useState<HubConnector | null>(null);
  const [failure, setFailure] = useState("");
  const [loading, setLoading] = useState(true);

  // Only the newest read of this connector lands: an older answer arriving late, or one for a page already left (or
  // another id), is dropped. `pending` lets the start-up poll skip a tick while a read is still on its way.
  const reads = useRef<{ request: ReturnType<typeof latestRequest<HubConnector[]>>; pending: number } | null>(null);
  const load = useCallback(async (quietly = false) => {
    const r = reads.current;
    if (!r) return;
    if (!quietly) setLoading(true);
    r.pending++;
    try {
      await r.request.run(listConnectors);
    } finally {
      r.pending--;
    }
  }, []);
  useEffect(() => {
    const request = latestRequest<HubConnector[]>(
      (list) => {
        const found = list.find((x) => x.id === id && sameManager(x.manager, manager)) ?? null;
        setC(found);
        setFailure(found ? "" : t("settings-hubconnector.not_on_hub"));
        setLoading(false);
      },
      (e) => {
        setFailure(t("settings-hubconnector.status_failed", { error: errorText(e) }));
        setLoading(false);
      },
    );
    const r = { request, pending: 0 };
    reads.current = r;
    void load();
    return () => {
      request.dispose();
      if (reads.current === r) reads.current = null;
    };
  }, [id, manager, load]);

  // Switched on, its program takes a moment to start and say it signs in: asked again every second (15 s at most),
  // a tick skipped while the last read hasn't answered.
  const starting = isStarting(c);
  useEffect(() => {
    if (!starting) return;
    let n = 0;
    const timer = setInterval(() => {
      if (++n > 15) clearInterval(timer);
      else if (!reads.current?.pending) void load(true);
    }, 1000);
    return () => clearInterval(timer);
  }, [starting, load]);

  const reload = () => load(true);
  const title = c?.name ?? (id === "github" ? "GitHub" : id);
  const rateLimited = c?.status === "rate_limited";
  const err = c?.error || failure;

  return (
    <div>
      <div className="mb-3 flex items-center gap-2">
        {onBack && <Button tone="quiet" icon="chevronLeft" onClick={onBack} className="-ml-2">{t("settings-hubconnector.back")}</Button>}
        <OwnerMark manager={manager} />
        <span className="flex-1" />
        <IconButton icon="retry" label={t("settings-hubconnector.recheck")} onClick={() => void load()} disabled={loading} />
      </div>

      <section className="mb-6 overflow-hidden rounded-card bg-surface shadow-card">
        <div className="flex items-center gap-3 px-4 py-3.5">
          <Mark id={id} size={44} />
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-[15px] font-semibold text-ink">{c?.account || title}</h2>
            <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[12px] text-ink-3">
              <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", DOT[connectorTone(c)])} />
              <span className="truncate">{c ? connectorState(c) : loading ? t("settings-hubconnector.checking") : t("settings-hubconnector.no_status")}</span>
            </div>
          </div>
          {loading && <Spinner />}
        </div>
        {c?.via && <InfoRow k={t("settings-hubconnector.signed_in_via")} v={c.via} />}
        {c?.last_sync && <InfoRow k={t("settings-hubconnector.last_sync")} v={ago(c.last_sync)} />}
        {err && <p className={cx("border-t border-line px-4 py-2.5 text-[12px] break-words select-text", rateLimited ? "text-ink-2" : "text-red")}>{err}</p>}
      </section>
      {onChannel && <ChannelLink id={id} manager={manager} onOpen={onChannel} />}
      {/* source subscriptions (#2041) are the default manager's */}
      {c && !manager && <SubscriptionsEntry pick={(l) => l.connector === id} scope={{ c: id }} />}
      {c && id !== "imessage" && !signsInHere(c) && !undescribed(c) && (
        <p className="-mt-4 mb-6 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("settings-hubconnector.hub_side_note")}</p>
      )}
      {c && id === "imessage" && <IMessageSections />}

      {c && id === "github" && signsInHere(c) && <GitHubSections c={c} reload={reload} />}
      {c && id !== "github" && (signsInHere(c) || undescribed(c)) && <SignInSections c={c} reload={reload} />}

      {c && c.status !== "ok" && !!c.guide?.length && (
        <Group title={t("settings-hubconnector.fix_title")}>
          {c.guide.map((step, i) => (
            <div key={i} className="flex items-baseline gap-3 px-4 py-2.5">
              <span className="w-4 shrink-0 text-[12px] font-semibold text-accent-ink tabular-nums">{i + 1}</span>
              <span className="min-w-0 text-[13px] leading-relaxed text-ink select-text">{step}</span>
            </div>
          ))}
          <div className="px-4 py-2.5">
            <Button tone="secondary" icon="retry" onClick={() => void load()} busy={loading}>{t("settings-hubconnector.recheck")}</Button>
          </div>
        </Group>
      )}

      {c && id === "github" && githubOptions(c.options) && <GitHubInbox key={JSON.stringify(c.options)} options={githubOptions(c.options)!} onSaved={setC} />}
      {c && id === "larkcli" && larkcliOptions(c.options) && <LarkCLITriage key={`t:${JSON.stringify(c.options)}`} options={larkcliOptions(c.options)!} onSaved={setC} />}
      {c && id === "larkcli" && larkcliOptions(c.options) && <LarkCLIDocuments key={JSON.stringify(c.options)} options={larkcliOptions(c.options)!} onSaved={setC} />}
    </div>
  );
}

function InfoRow({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex items-center gap-3 border-t border-line px-4 py-2.5">
      <span className="shrink-0 text-[13px] text-ink">{k}</span>
      <span className="min-w-0 flex-1 truncate text-right text-[12.5px] text-ink-2" title={v}>{v}</span>
    </div>
  );
}

// ── switch, feeds, sign-out and delete ───────────────────

/** What a page's actions share: busy while one runs, the hub's errors as toasts. */
function useActions(id: string, reload: () => Promise<void>) {
  const [busy, setBusy] = useState("");
  const run = async (what: string, fn: () => Promise<unknown>, done?: (r: unknown) => void) => {
    setBusy(what);
    try {
      const r = await fn();
      done?.(r);
      await reload();
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setBusy("");
    }
  };
  return { busy, run, id };
}
type Actions = ReturnType<typeof useActions>;

function SwitchGroup({ c, a }: { c: HubConnector; a: Actions }) {
  return (
    <Group footer={switchFooter(c)}>
      <div className="flex items-center gap-3 px-4 py-2.5">
        <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{switchLabel(c)}</span>
        {a.busy === "enabled" && <Spinner />}
        <Switch on={isEnabled(c)} disabled={!!a.busy} label={switchLabel(c)} onChange={(on) => void a.run("enabled", () => setConnector(c.id, { enabled: on }))} />
      </div>
    </Group>
  );
}

function FeedsGroup({ c, a }: { c: HubConnector; a: Actions }) {
  const [confirm, setConfirm] = useState(false);
  const [deleted, setDeleted] = useState("");
  if (c.archive === undefined && c.inbox === undefined) return null;
  const toggle = (title: string, on: boolean, feed: "archive" | "inbox") => (
    <div className="flex items-center gap-3 px-4 py-2.5">
      <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{title}</span>
      {a.busy === feed && <Spinner />}
      <Switch on={on} disabled={!!a.busy} label={title} onChange={(v) => void a.run(feed, () => setConnector(c.id, { [feed]: v }))} />
    </div>
  );
  return (
    <Group footer={<span className="whitespace-pre-line">{deleted || feedsFooter(c)}</span>}>
      {c.archive !== undefined && toggle(t("settings-hubconnector.to_knowledge"), c.archive, "archive")}
      {c.inbox !== undefined && toggle(t("settings-hubconnector.to_inbox"), c.inbox, "inbox")}
      {c.archive !== undefined && (
        <div className="px-4 py-2.5">
          <button type="button" disabled={!!a.busy} onClick={() => setConfirm(true)} className="text-[13px] whitespace-nowrap text-red hover:underline disabled:opacity-50">{t("settings-hubconnector.delete_archive")}</button>
        </div>
      )}
      <Confirm
        open={confirm}
        title={t("settings-hubconnector.delete_archive_title", { name: c.name })}
        message={t("settings-hubconnector.delete_archive_message", { name: c.name })}
        action={t("settings-hubconnector.delete")}
        busy={a.busy === "deleteArchive"}
        onClose={() => setConfirm(false)}
        onConfirm={() => void a.run("deleteArchive", () => deleteConnectorArchive(c.id), (r) => (setDeleted(deletedLine(r as object, false)), setConfirm(false)))}
      />
    </Group>
  );
}

/**
 * An extension's page (Settings › Extensions › …) for the connectors it contributes: each one's "Add to Inbox" switch, the hub's own
 * (the same as on the connector's page), and the way to the connector's page for the rest (persona#3178). `manager`: whose
 * extension it is (each manager has its own connectors, docs/assistant/managers.md "Clients"); none: the default's.
 */
export function ExtensionConnectorInbox({ ids, manager, onOpen }: { ids: string[]; manager?: string; onOpen?: (id: string, manager?: string) => void }) {
  const [list, setList] = useState<HubConnector[]>([]);
  const key = ids.join(",");
  const load = useCallback(async () => {
    try {
      const all = await listConnectors();
      // the merged list has each manager's copy: this extension's manager's
      setList(key.split(",").flatMap((id) => all.find((c) => c.id === id && sameManager(c.manager, manager)) ?? []));
    } catch {
      setList([]); // the connector page says what is wrong; this group just stays away
    }
  }, [key, manager]);
  useEffect(() => void load(), [load]);
  return (
    <>
      {list.map((c) => (c.inbox === undefined ? null : <ExtensionConnectorGroup key={`${c.manager ?? ""}:${c.id}`} c={c} reload={load} onOpen={onOpen} />))}
    </>
  );
}

export function ExtensionConnectorGroup({ c, reload, onOpen }: { c: HubConnector; reload: () => Promise<void>; onOpen?: (id: string, manager?: string) => void }) {
  const a = useActions(c.id, reload);
  const label = t("settings-hubconnector.to_inbox");
  return (
    <Group title={t("settings-hubconnector.extension_title", { name: c.name })} footer={c.inbox ? t("settings-hubconnector.extension_inbox_on", { name: c.name }) : t("lib-connectors.feeds_inbox_off")}>
      <div className="flex items-center gap-3 px-4 py-2.5">
        <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{label}</span>
        {a.busy === "inbox" && <Spinner />}
        <Switch on={!!c.inbox} disabled={!!a.busy} label={label} onChange={(v) => void a.run("inbox", () => setConnector(c.id, { inbox: v }, c.manager))} />
      </div>
      {onOpen && <Row label={t("settings-hubconnector.extension_open")} hint={t("settings-hubconnector.extension_open_hint")} onClick={() => (otherManager(c.manager) ? onOpen(c.id, otherManager(c.manager)) : onOpen(c.id))} />}
    </Group>
  );
}

function DataGroup({ c, a, logout }: { c: HubConnector; a: Actions; logout: boolean }) {
  const [confirm, setConfirm] = useState<"" | "logout" | "delete">("");
  const [deleted, setDeleted] = useState("");
  const showsLogout = logout && (isSignedIn(c) || !!c.via);
  const stores = storesData(c); // a chat bot (WeChat) keeps nothing to delete
  if (!showsLogout && !stores) return null;
  return (
    <Group footer={deleted || (stores ? t("settings-hubconnector.data_footer") : undefined)}>
      {showsLogout && (
        <div className="px-4 py-2.5">
          <button type="button" disabled={!!a.busy} onClick={() => setConfirm("logout")} className="text-[13px] whitespace-nowrap text-red hover:underline disabled:opacity-50">{t("settings-hubconnector.sign_out")}</button>
        </div>
      )}
      {stores && (
        <div className="px-4 py-2.5">
          <button type="button" disabled={!!a.busy} onClick={() => setConfirm("delete")} className="text-[13px] whitespace-nowrap text-red hover:underline disabled:opacity-50">{t("settings-hubconnector.delete_data")}</button>
        </div>
      )}
      <Confirm
        open={confirm === "logout"}
        title={t("settings-hubconnector.sign_out_title", { name: c.name })}
        message={logoutMessage(c)}
        action={t("settings-hubconnector.sign_out")}
        busy={a.busy === "logout"}
        onClose={() => setConfirm("")}
        onConfirm={() => void a.run("logout", () => connectorLogout(c.id), () => setConfirm(""))}
      />
      <Confirm
        open={confirm === "delete"}
        title={t("settings-hubconnector.delete_data_title", { name: c.name })}
        message={t("settings-hubconnector.delete_data_message", { name: c.name })}
        action={t("settings-hubconnector.delete")}
        busy={a.busy === "delete"}
        onClose={() => setConfirm("")}
        onConfirm={() => void a.run("delete", () => deleteConnectorData(c.id), (r) => (setDeleted(deletedLine(r as object)), setConfirm("")))}
      />
    </Group>
  );
}

function Confirm({ open, title, message, action, busy, onClose, onConfirm }: {
  open: boolean;
  title: string;
  message: string;
  action: string;
  busy: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      title={title}
      width={420}
      footer={
        <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>{t("common.cancel")}</Button>
          <Button tone="danger" busy={busy} onClick={onConfirm}>{action}</Button>
        </>
      }
    >
      <p className="text-[12.5px] leading-relaxed text-ink-2">{message}</p>
    </Dialog>
  );
}

// ── Messages (iMessage) ──────────────────────────────────

/** Messages' "Hand to Persona" switch and deleting what the hub kept (iOS MessagesConnectorView); a hub without the switch
 *  (no `ai` in GET /imessage, or no Mac chosen yet) shows only the read-only line. */
export function IMessageSections() {
  const [st, setSt] = useState<IMessageStatus | null>(null);
  const [busy, setBusy] = useState<"" | "ai" | "delete">("");
  const [confirm, setConfirm] = useState(false);
  const [deleted, setDeleted] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    imessageStatus().then(setSt, () => setSt(null));
  }, []);

  const setAI = async (on: boolean) => {
    setBusy("ai");
    try {
      setSt(await setIMessageAI(on));
      setDeleted("");
      setError("");
    } catch (e) {
      setError(t("settings-hubconnector.imessage_ai_failed", { error: errorText(e) }));
    } finally {
      setBusy("");
    }
  };
  const remove = async () => {
    setBusy("delete");
    try {
      setDeleted(deletedLine(await deleteIMessageData()));
      setError("");
      setConfirm(false);
    } catch (e) {
      setError(t("settings-hubconnector.imessage_delete_failed", { error: errorText(e) }));
      setConfirm(false);
    } finally {
      setBusy("");
    }
  };

  return (
    <IMessageGroups
      ai={st?.ai}
      busy={busy}
      deleted={deleted}
      error={error}
      confirm={confirm}
      onAI={(on) => void setAI(on)}
      onDelete={() => setConfirm(true)}
      onCancel={() => setConfirm(false)}
      onConfirm={() => void remove()}
    />
  );
}

/** The groups as drawn, apart from loading them (for the tests). */
export function IMessageGroups({ ai, busy, deleted, error, confirm, onAI, onDelete, onCancel, onConfirm }: {
  ai?: boolean;
  busy: "" | "ai" | "delete";
  deleted: string;
  error: string;
  confirm: boolean;
  onAI: (on: boolean) => void;
  onDelete: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const label = t("settings-hubconnector.imessage_ai");
  return (
    <>
      {ai !== undefined && (
        <Group footer={imessageAIFooter(ai)}>
          <div className="flex items-center gap-3 px-4 py-2.5">
            <Icon name="spark" size={15} className="shrink-0 text-accent" />
            <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{label}</span>
            {busy === "ai" && <Spinner />}
            <Switch on={ai} disabled={!!busy} label={label} onChange={onAI} />
          </div>
          <div className="px-4 py-2.5">
            <button
              type="button"
              disabled={ai || !!busy}
              onClick={onDelete}
              className="text-[13px] whitespace-nowrap text-red enabled:hover:underline disabled:opacity-50"
            >
              {t("settings-hubconnector.imessage_delete")}
            </button>
          </div>
          {deleted && <p className="px-4 py-2.5 text-[12px] text-ink-2">{deleted}</p>}
          {error && <p role="alert" className="px-4 py-2.5 text-[12px] break-words text-red">{error}</p>}
          <Confirm
            open={confirm}
            title={t("settings-hubconnector.imessage_delete_title")}
            message={t("settings-hubconnector.imessage_delete_message")}
            action={t("settings-hubconnector.delete")}
            busy={busy === "delete"}
            onClose={onCancel}
            onConfirm={onConfirm}
          />
        </Group>
      )}
      <p className="-mt-4 mb-6 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("settings-hubconnector.imessage_read_only")}</p>
    </>
  );
}

// ── signing in, a step at a time ─────────────────────────

/** A connector the user signs in to here (Telegram, an extension's): switch, sign-in, feeds, sign-out and delete. */
function SignInSections({ c, reload }: { c: HubConnector; reload: () => Promise<void> }) {
  const a = useActions(c.id, reload);
  return (
    <>
      <SwitchGroup c={c} a={a} />
      {isEnabled(c) && !isSignedIn(c) && (signsInHere(c) ? (
        <SignInSteps c={c} reload={reload} footer={signInFooter(c)} />
      ) : (
        // Just switched on: its program has not started yet (the page asks again every second).
        <Group><p className="px-4 py-3 text-[12.5px] text-ink-3">{t("settings-hubconnector.starting", { name: c.name })}</p></Group>
      ))}
      <FeedsGroup c={c} a={a} />
      <DataGroup c={c} a={a} logout />
    </>
  );
}

export function SignInSteps({ c, reload, footer, startLabel, startTone = "accent", bare, autoStart }: {
  c: HubConnector;
  reload: () => Promise<void>;
  footer?: string;
  startLabel?: string;
  /** The start button's tone: the default accent, or quiet where signing in is a secondary action. */
  startTone?: "accent" | "quiet";
  /** Draw only the step content so a caller can place it inside its own section. */
  bare?: boolean;
  /** Start signing in once, as soon as this is drawn: the step that follows another the user just finished (#5818). */
  autoStart?: boolean;
}) {
  const [prompt, setPrompt] = useState<ConnectorPrompt | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const live = useRef(true);
  useEffect(() => {
    live.current = true; // again after StrictMode's extra unmount in development
    return () => void (live.current = false);
  }, []);
  // Also a channel (WeChat): a step this page cannot draw can be done in Settings › Channels instead.
  const isChannel = useChannels().some((x) => x.id === c.id);

  /** True when signing in is done. */
  const send = useCallback(async (step: string, vals: Record<string, string>, quiet = false): Promise<boolean> => {
    if (!quiet) setBusy(true);
    try {
      const p = await connectorLogin(c.id, step, vals);
      if (!live.current) return false;
      if (p.done) {
        setPrompt(null);
        setValues({});
        toast(t("settings-hubconnector.signed_in", { name: p.account || c.name }), "ok");
        await reload();
        return true;
      }
      setPrompt((cur) => {
        if (p.step !== cur?.step) setValues({}); // a new step: its fields start empty
        return p;
      });
      if (!quiet) await reload(); // the status above follows (credentials saved: api_id shown)
    } catch (e) {
      if (live.current) toast(errorText(e), "error");
    } finally {
      if (!quiet && live.current) setBusy(false);
    }
    return false;
  }, [c.id, c.name, reload]);
  const started = useRef(false);
  useEffect(() => {
    if (!autoStart || started.current) return;
    started.current = true;
    void send("", {});
  }, [autoStart, send]);

  // A code on screen (GitHub's device flow) or a QR code to scan (WeChat): ask again every interval the hub gave
  // until the user has entered / scanned it.
  useStepPolling(isPolledStep(prompt) ? prompt : null, (step) => send(step, {}, true), prompt?.kind === "qr" ? 3 : 5);

  const cancel = () => (setPrompt(null), setValues({}));
  const body = !prompt ? (
    <div className="px-4 py-3">
      <Button tone={startTone} busy={busy} onClick={() => void send("", {})}>{startLabel ?? t("settings-hubconnector.sign_in_to", { name: c.name })}</Button>
    </div>
  ) : (
    <SignInStep
      name={c.name}
      prompt={prompt}
      values={values}
      busy={busy}
      isChannel={isChannel}
      onChange={(name, v) => setValues((cur) => ({ ...cur, [name]: v }))}
      onSubmit={() => void send(prompt.step ?? "", values)}
      onRestart={() => (cancel(), void send("", {}))}
      onCancel={cancel}
    />
  );
  if (bare) return body;
  return (
    <Group title={t("settings-hubconnector.sign_in")} footer={footer}>
      {body}
    </Group>
  );
}

/** One sign-in step as the hub sent it: its hint and error, then fields, a device code, a QR code, or why it can't be drawn. */
export function SignInStep({ name, prompt, values, busy, isChannel, onChange, onSubmit, onRestart, onCancel }: {
  name: string;
  prompt: ConnectorPrompt;
  values: Record<string, string>;
  busy: boolean;
  isChannel?: boolean;
  onChange: (field: string, value: string) => void;
  onSubmit: () => void;
  onRestart: () => void;
  onCancel: () => void;
}) {
  const view = stepView(prompt);
  const canSend = canSendStep(prompt, values) && !busy;
  return (
    <>
      {(prompt.hint || prompt.error) && (
        <div className="flex flex-col gap-1 px-4 py-2.5">
          {prompt.hint && <p className="text-[12.5px] leading-relaxed text-ink-2">{prompt.hint}</p>}
          {prompt.error && <p role="alert" className="text-[12px] text-red">{prompt.error}</p>}
        </div>
      )}
      {view === "device_code" ? (
        <div className="flex flex-col gap-3 px-4 py-3">
          {prompt.qr?.rows.length ? (
            // The link carries the code (Lark's, #5818): scanning it on the phone needs nothing typed; the code and
            // the link stay below for doing it on this computer.
            <div className="flex flex-col items-center gap-2 pb-1">
              <QRCode rows={prompt.qr.rows} label={t("settings-hubconnector.qr_label")} />
              <p className="text-center text-[12.5px] text-ink-2">{t("settings-hubconnector.scan_to_authorize")}</p>
            </div>
          ) : null}
          <div className="flex items-center gap-3">
            <span className="min-w-0 flex-1 truncate font-mono text-[26px] font-semibold tracking-[0.12em] text-ink select-all">{prompt.user_code}</span>
            <IconButton
              icon="copy"
              label={t("settings-hubconnector.copy_code")}
              onClick={() => void copyText(prompt.user_code ?? "", { done: t("settings-hubconnector.code_copied"), failed: t("settings-hubconnector.copy_failed") })}
            />
          </div>
          <p className="flex items-center gap-2 text-[12.5px] text-ink-3"><Spinner size={11} />{t("settings-hubconnector.waiting_auth", { name })}</p>
          {prompt.url && (
            <div>
              <Button tone={prompt.qr?.rows.length ? "secondary" : "accent"} icon="external" onClick={() => window.open(prompt.url, "_blank", "noopener,noreferrer")}>{t("settings-hubconnector.open_to_enter_code", { name })}</Button>
            </div>
          )}
        </div>
      ) : view === "qr" ? (
        <QRStep rows={prompt.qr!.rows} app={name} label={t("settings-hubconnector.qr_label")} onCancel={onCancel} />
      ) : view === "qr_link" ? (
        <QRLinkStep url={prompt.url!} app={name} onCancel={onCancel} />
      ) : view === "unsupported" ? (
        <UnsupportedStep elsewhere={isChannel ? t("settings-hubconnector.bind_in_channels", { name }) : undefined} onRestart={onRestart} onCancel={onCancel} />
      ) : (
        <form
          className="flex flex-col"
          onSubmit={(e) => {
            e.preventDefault();
            if (canSend) onSubmit();
          }}
        >
          {(prompt.fields ?? []).map((f) => {
            const input = fieldInput(f);
            return (
              <label key={f.name} className="flex items-center gap-3 border-b border-line px-4 py-2">
                <span className="w-[96px] shrink-0 truncate text-[13px] text-ink">{f.label}</span>
                <input
                  value={values[f.name] ?? ""}
                  onChange={(e) => onChange(f.name, e.target.value)}
                  type={input.type}
                  inputMode={input.inputMode}
                  autoComplete={input.autoComplete}
                  placeholder={f.placeholder ?? f.label}
                  spellCheck={false}
                  autoCapitalize="off"
                  disabled={busy}
                  className={cx(fieldClass, "font-mono")}
                />
              </label>
            );
          })}
          <div className="flex items-center gap-2 px-4 py-2.5">
            <Button type="submit" tone="accent" disabled={!canSend} busy={busy}>{busy ? t("settings-hubconnector.verifying") : t("settings-hubconnector.next")}</Button>
            <Button tone="quiet" onClick={onCancel} disabled={busy}>{t("common.cancel")}</Button>
          </div>
        </form>
      )}
    </>
  );
}

// ── GitHub ───────────────────────────────────────────────

function GitHubSections({ c, reload }: { c: HubConnector; reload: () => Promise<void> }) {
  const a = useActions(c.id, reload);
  const [auth, setAuth] = useState<GitHubAuth | null>(null);
  const [authError, setAuthError] = useState("");
  const [choosing, setChoosing] = useState<string | null>(null);
  const [signIn, setSignIn] = useState(0); // remounts the sign-in when the source changes

  const loadAuth = useCallback(() => {
    githubAuth().then((x) => (setAuth(x), setAuthError("")), (e) => setAuthError(errorText(e)));
  }, []);
  useEffect(loadAuth, [loadAuth]);

  const choose = async (machine: string | null) => {
    setChoosing(machine ?? "device");
    try {
      setAuth(await setGitHubAuth(machine));
      setSignIn((n) => n + 1);
      await reload();
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setChoosing(null);
    }
  };

  const row = (key: string, icon: "machine" | "key", title: string, sub: string, warn: boolean, selected: boolean, onPick: () => void) => (
    <button
      key={key}
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={choosing !== null}
      onClick={onPick}
      className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors enabled:hover:bg-hover disabled:cursor-default"
    >
      <Icon name={icon} size={15} className="shrink-0 text-ink-3" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] text-ink">{title}</span>
        <span className={cx("mt-0.5 block truncate text-[11.5px]", warn ? "text-orange" : "text-ink-3")}>{sub}</span>
      </span>
      {choosing === key ? <Spinner /> : selected ? <Icon name="check" size={14} stroke={2.6} className="shrink-0 text-accent-ink" /> : null}
    </button>
  );

  const missing = auth?.missing ?? [];
  return (
    <>
      <SwitchGroup c={c} a={a} />
      <Group title={t("settings-hubconnector.source_title")} footer={t("settings-hubconnector.source_footer")}>
        <div role="radiogroup" aria-label={t("settings-hubconnector.source_title")} className="divide-y divide-line">
          {!auth && !authError && <p className="flex items-center gap-2 px-4 py-3 text-[12.5px] text-ink-3"><Spinner size={11} />{t("settings-hubconnector.source_loading")}</p>}
          {authError && <p className="px-4 py-3 text-[12.5px] text-red">{t("settings-hubconnector.source_failed", { error: authError })}</p>}
          {auth?.machines.map((m) => row(m.id, "machine", m.name, githubMachineLine(m), githubMachineWarns(m), auth.source === "machine" && auth.machine === m.id, () => void choose(m.id)))}
          {auth && row("device", "key", t("settings-hubconnector.device_flow"), t("settings-hubconnector.device_flow_detail"), false, auth.source === "device", () => void choose(null))}
        </div>
      </Group>
      {auth?.source === "device" && isEnabled(c) && !isSignedIn(c) && (
        <SignInSteps key={signIn} c={c} reload={async () => (await reload(), loadAuth())} footer={t("settings-hubconnector.device_flow_footer")} />
      )}
      {auth && missing.length > 0 && isSignedIn(c) && (
        <Group title={t("settings-hubconnector.missing_scopes")}>
          <div className="flex items-start gap-2.5 px-4 py-3">
            <Icon name="info" size={14} className="mt-0.5 shrink-0 text-orange" />
            <p className="min-w-0 text-[13px] leading-relaxed text-ink select-text">{githubScopeAdvice(auth, missing)}</p>
          </div>
          {missing.flatMap((m) => m.affects).map((x) => <p key={x} className="px-4 py-2 text-[12.5px] text-ink-2">{x}</p>)}
        </Group>
      )}
      <FeedsGroup c={c} a={a} />
      <DataGroup c={c} a={a} logout={false} />
    </>
  );
}

/** GitHub's "Inbox": which CI failures come in, and repositories that bring nothing in (PUT …/github/settings {options}). */
function GitHubInbox({ options, onSaved }: { options: GitHubInboxOptions; onSaved: (c: HubConnector) => void }) {
  const [o, setO] = useState(options);
  const [repo, setRepo] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const save = async (next: GitHubInboxOptions) => {
    const before = o;
    setO(next);
    setSaving(true);
    try {
      onSaved(await setConnector("github", { options: next }));
      setError("");
    } catch (e) {
      setO(before);
      setError(t("settings-hubconnector.save_failed", { error: errorText(e) }));
    } finally {
      setSaving(false);
    }
  };
  const add = () => {
    const r = mutableRepo(repo, o.muted_repos);
    if (!r) return;
    setRepo("");
    void save({ ...o, muted_repos: [...o.muted_repos, r] });
  };
  return (
    <Group title={t("settings-hubconnector.inbox")} footer={githubInboxFooter(o)}>
      <Row label={t("settings-hubconnector.ci_failures")}>
        {saving && <Spinner />}
        <select value={o.ci} disabled={saving} onChange={(e) => void save({ ...o, ci: e.target.value })} aria-label={t("settings-hubconnector.ci_failures")} className={cx(fieldClass, "w-auto min-w-[140px]")}>
          {CI_MODES.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
        </select>
      </Row>
      {o.muted_repos.map((r) => (
        <div key={r} className="flex items-center gap-3 px-4 py-2">
          <Icon name="bellOff" size={14} className="shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-ink">{r}</span>
          <IconButton icon="trash" label={t("settings-hubconnector.unmute", { repo: r })} disabled={saving} onClick={() => void save({ ...o, muted_repos: o.muted_repos.filter((x) => x !== r) })} />
        </div>
      ))}
      <form
        className="flex items-center gap-2 px-4 py-2.5"
        onSubmit={(e) => {
          e.preventDefault();
          add();
        }}
      >
        <input value={repo} onChange={(e) => setRepo(e.target.value)} placeholder={t("settings-hubconnector.mute_placeholder")} aria-label={t("settings-hubconnector.mute_repo")} spellCheck={false} autoCapitalize="off" className={cx(fieldClass, "font-mono")} />
        <Button type="submit" tone="secondary" icon="plus" disabled={!mutableRepo(repo, o.muted_repos) || saving}>{t("settings-hubconnector.add")}</Button>
      </form>
      {error && <p className="px-4 py-2.5 text-[12px] text-red">{error}</p>}
    </Group>
  );
}

/** lark-cli's watched documents (PUT …/larkcli/settings {options}, sent whole so the chat options stay): links and Wiki
 * spaces whose comments that mention you come in, and whether every comment on a document you own does. */
function LarkCLIDocuments({ options, onSaved }: { options: LarkCLIInboxOptions; onSaved: (c: HubConnector) => void }) {
  const [o, setO] = useState(options);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const save = async (next: LarkCLIInboxOptions) => {
    const before = o;
    setO(next);
    setSaving(true);
    try {
      onSaved(await setConnector("larkcli", { options: next }));
      setError("");
    } catch (e) {
      setO(before);
      setError(t("settings-hubconnector.save_failed", { error: errorText(e) }));
    } finally {
      setSaving(false);
    }
  };
  return (
    <Group title={t("settings-hubconnector.lark_docs")} footer={t("settings-hubconnector.lark_docs_footer")}>
      <LinkList label={t("settings-hubconnector.lark_docs_links")} placeholder={t("settings-hubconnector.lark_docs_link_placeholder")} values={o.documents} disabled={saving} onChange={(documents) => void save({ ...o, documents })} />
      <LinkList label={t("settings-hubconnector.lark_docs_spaces")} placeholder={t("settings-hubconnector.lark_docs_space_placeholder")} values={o.wiki_spaces} disabled={saving} onChange={(wiki_spaces) => void save({ ...o, wiki_spaces })} />
      {error && <p className="px-4 py-2.5 text-[12px] text-red">{error}</p>}
    </Group>
  );
}

/** A list of links with a remove button each and a field to add one. */
function LinkList({ label, placeholder, values, disabled, onChange }: { label: string; placeholder: string; values: string[]; disabled: boolean; onChange: (next: string[]) => void }) {
  const [value, setValue] = useState("");
  const link = value.trim();
  const canAdd = link !== "" && !values.includes(link);
  return (
    <>
      {values.map((v) => (
        <div key={v} className="flex items-center gap-3 px-4 py-2">
          <Icon name="link" size={14} className="shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-ink">{v}</span>
          <IconButton icon="trash" label={t("settings-hubconnector.lark_docs_remove", { link: v })} disabled={disabled} onClick={() => onChange(values.filter((x) => x !== v))} />
        </div>
      ))}
      <form
        className="flex items-center gap-2 px-4 py-2.5"
        onSubmit={(e) => {
          e.preventDefault();
          if (!canAdd) return;
          setValue("");
          onChange([...values, link]);
        }}
      >
        <input value={value} onChange={(e) => setValue(e.target.value)} placeholder={placeholder} aria-label={label} spellCheck={false} autoCapitalize="off" className={cx(fieldClass, "font-mono")} />
        <Button type="submit" tone="secondary" icon="plus" disabled={!canAdd || disabled}>{t("settings-hubconnector.add")}</Button>
      </form>
    </>
  );
}

/** lark-cli's triage rules: what counts as Needs you for its items, in the user's words (sent with the other options). */
function LarkCLITriage({ options, onSaved }: { options: LarkCLIInboxOptions; onSaved: (c: HubConnector) => void }) {
  const [text, setText] = useState(options.triage_rules);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const changed = text.trim() !== options.triage_rules.trim();
  const save = async () => {
    setSaving(true);
    try {
      onSaved(await setConnector("larkcli", { options: { ...options, triage_rules: text } }));
      setError("");
    } catch (e) {
      setError(t("settings-hubconnector.save_failed", { error: errorText(e) }));
    } finally {
      setSaving(false);
    }
  };
  return (
    <Group title={t("settings-hubconnector.lark_triage")} footer={t("settings-hubconnector.lark_triage_footer")}>
      <div className="flex flex-col gap-2 px-4 py-3">
        <textarea value={text} onChange={(e) => setText(e.target.value)} maxLength={4000} rows={4} placeholder={t("settings-hubconnector.lark_triage_placeholder")} aria-label={t("settings-hubconnector.lark_triage")} disabled={saving} className={cx(fieldClass, "min-h-24 resize-y py-2")} />
        <div className="flex justify-end">
          <Button tone="primary" busy={saving} disabled={!changed || saving} onClick={() => void save()}>{t("settings-hubconnector.lark_triage_save")}</Button>
        </div>
        {error && <p className="text-[12px] text-red">{error}</p>}
      </div>
    </Group>
  );
}
