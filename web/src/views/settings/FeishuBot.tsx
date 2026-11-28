// Settings › Connectors › Lark bot (docs/extensions/bots.md, the app's FeishuBotView): talk to Persona from Feishu. A row on the
// connectors page with the bot's state; its page (a dialog) has, with no app yet, creating one by scanning (the channel's
// bind step, #5818); the status and the on / off switch, pairing with a code sent to the bot, what the bot may do and the
// agents driven there; and, folded under Advanced, the app's credentials and how to make one by hand.
// Each change goes to the hub at once and the page shows what the hub answered; a failed one leaves it as it was.
import { sameManager } from "../../lib/manager-choice";
import { BindSteps, ChannelLink } from "./Channels";
import { Disclosure } from "./ProviderFields";
import { Fragment, useCallback, useEffect, useId, useRef, useState } from "react";
import { errorText } from "../../lib/api";
import { connectorState, connectorSummary, signsInHere, connectorTone, isEnabled, larkcliOptions, listConnectors, setConnector, type ConnectorTone, type HubConnector } from "../../lib/connectors";
import { t } from "../../i18n";
import { ago, safeWebURL } from "../../lib/format";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import {
  botStateText, botStatus, botSummary, botTone, credentialsChanged, credentialsOf, credentialsUpdate, DEFAULT_FEATURES, DEFAULT_NUDGE_AFTER, livePairing, nudgeChoices, nudgeLabel, pairBot, pairingLeft,
  featureEdit, testBot, unpairBot, updateBot, type BotCredentials, type BotFeatures, type FeishuBotStatus, type BotTopicChat, type TopicTodoMode,
} from "../../lib/feishu-bot";
import { Button, copyText, cx, Dialog, IconButton, Spinner, toast } from "../../ui/atoms";
import { BrandMark } from "../../ui/BrandMark";
import { Icon, type IconName } from "../../ui/icons";
import { Group, Pill, Row, Switch, settingsInputClass } from "./SettingsParts";
import { SignInSteps } from "./HubConnector";
import { LarkSyncSection } from "./LarkSyncSection";

export const SOURCE_REPORT_DELAYS = [0, 1, 2, 5, 10, 15, 30, 60];

const controlClass = "h-8 rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]";
const dotClass = { green: "bg-green", red: "bg-red", orange: "bg-orange" } as const;
const connectorDotClass = { green: "bg-green", orange: "bg-orange", red: "bg-red", idle: "bg-line-strong" } as const;

function shortAppID(id: string): string {
  return id.length > 14 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id;
}

function botWorks(st: FeishuBotStatus | null): boolean {
  return !!st?.enabled && st.has_secret && st.connected;
}

function readTone(c: HubConnector): ConnectorTone {
  // Missing authorization is actionable and should read as a warning in the combined row; a connector error remains red.
  if (c.status === "no_permission" || c.status === "not_signed_in" || c.status === "rate_limited") return "orange";
  return connectorTone(c);
}

function missingScopes(c: HubConnector): string[] {
  return c.missing_scopes ?? [];
}

/** The open-platform page for the missing permissions: the hub's guide_url, or a permission_url. */
function permissionLink(c: HubConnector): string {
  return safeWebURL(c.guide_url, true) || safeWebURL(c.permission_url, true) || "";
}

function combinedLarkState(bot: FeishuBotStatus | null, loading: boolean, c: HubConnector): { label: string; tone?: ConnectorTone } {
  if (!bot) return { label: loading ? t("lib-feishu-bot.state_checking") : t("lib-feishu-bot.state_unreadable") };
  if (!bot.has_secret && c.status === "not_configured") return { label: t("settings-feishubot.read_not_connected") };
  if (botWorks(bot) && c.status !== "ok") return { label: t("settings-feishubot.read_status_prefix", { status: connectorState(c) }), tone: readTone(c) };
  return { label: botSummary(bot), tone: botTone(bot) };
}

export const FEATURES: { key: keyof BotFeatures; icon: IconName; title: string; detail: string }[] = [
  { key: "main", icon: "spark", title: t("settings-feishubot.feature_main_title"), detail: t("settings-feishubot.feature_main_detail") },
  { key: "cards", icon: "hand", title: t("settings-feishubot.feature_cards_title"), detail: t("settings-feishubot.feature_cards_detail") },
  { key: "all_cards", icon: "bell", title: t("settings-feishubot.feature_all_cards_title"), detail: t("settings-feishubot.feature_all_cards_detail") },
  { key: "pushes", icon: "send", title: t("settings-feishubot.feature_pushes_title"), detail: t("settings-feishubot.feature_pushes_detail") },
  { key: "answer_new_card", icon: "bell", title: t("settings-feishubot.feature_answer_new_card_title"), detail: t("settings-feishubot.feature_answer_new_card_detail") },
  { key: "read_group", icon: "search", title: t("settings-feishubot.feature_read_group_title"), detail: t("settings-feishubot.feature_read_group_detail") },
  { key: "read_group_direct", icon: "chat", title: t("settings-feishubot.feature_read_group_direct_title"), detail: t("settings-feishubot.feature_read_group_direct_detail") },
  { key: "topic_cot", icon: "spark", title: t("settings-feishubot.feature_topic_cot_title"), detail: t("settings-feishubot.feature_topic_cot_detail") },
  { key: "dm_cot", icon: "spark", title: t("settings-feishubot.feature_dm_cot_title"), detail: t("settings-feishubot.feature_dm_cot_detail") },
  { key: "cot_answer", icon: "spark", title: t("settings-feishubot.feature_cot_answer_title"), detail: t("settings-feishubot.feature_cot_answer_detail") },
  { key: "sync", icon: "loop", title: t("settings-feishubot.feature_sync_title"), detail: t("settings-feishubot.feature_sync_detail") },
  { key: "reports", icon: "list", title: t("settings-feishubot.feature_reports_title"), detail: t("settings-feishubot.feature_reports_detail") },
  { key: "board", icon: "pin", title: t("settings-feishubot.feature_board_title"), detail: t("settings-feishubot.feature_board_detail") },
  { key: "workbench", icon: "home", title: t("settings-feishubot.feature_workbench_title"), detail: t("settings-feishubot.feature_workbench_detail") },
  { key: "only_conclusions", icon: "check", title: t("settings-feishubot.feature_only_conclusions_title"), detail: t("settings-feishubot.feature_only_conclusions_detail") },
  { key: "report_steps", icon: "list", title: t("settings-feishubot.feature_report_steps_title"), detail: t("settings-feishubot.feature_report_steps_detail") },
  { key: "agents", icon: "agent", title: t("settings-feishubot.feature_agents_title"), detail: t("settings-feishubot.feature_agents_detail") },
];

export const STEPS = [
  t("settings-feishubot.step_create_app"),
  t("settings-feishubot.step_permissions"),
  t("settings-feishubot.step_sync_permissions"),
  t("settings-feishubot.step_events"),
  t("settings-feishubot.step_publish"),
];

/** The bot's state, kept by whoever shows it (the row and its page share one). */
export function useBot() {
  const [status, setStatus] = useState<FeishuBotStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const requests = useRef({ issued: 0, confirmed: 0, loading: 0, live: false });
  // Reads are ordered by when they started, but only successful responses advance the confirmed state.
  // A confirmed write takes a new position at receipt time, fencing every read already in flight.
  const accept = useCallback((next: FeishuBotStatus, sequence = ++requests.current.issued) => {
    const state = requests.current;
    if (!state.live || sequence < state.confirmed) return false;
    state.confirmed = sequence;
    setStatus(next);
    setError("");
    if (sequence >= state.loading) setLoading(false);
    return true;
  }, []);
  const readStatus = useCallback(async (feedback = false) => {
    const sequence = ++requests.current.issued;
    if (feedback) {
      requests.current.loading = sequence;
      setLoading(true);
    }
    try {
      const next = await botStatus();
      if (!accept(next, sequence)) return;
      return { status: next, current: () => requests.current.live && requests.current.confirmed === sequence };
    } catch (e) {
      if (feedback && requests.current.live && sequence >= requests.current.confirmed) {
        setError(t("settings-feishubot.status_error", { error: errorText(e) }));
      }
      throw e;
    } finally {
      if (feedback && requests.current.live && requests.current.loading === sequence) setLoading(false);
    }
  }, [accept]);
  const load = useCallback(() => readStatus(true).then(() => {}, () => {}), [readStatus]);
  useEffect(() => {
    requests.current.live = true;
    void load();
    return () => { requests.current.live = false; };
  }, [load]);
  return { status, accept, readStatus, loading, error, setError, load };
}

export function FeishuBot({ onChannel, larkcli, onOpen, manager, onRouteClose }: { manager?: string; onRouteClose?: () => void; onChannel?: (id: string, manager?: string) => void; larkcli?: HubConnector | null; onOpen?: (id: string) => void } = {}) {
  const bot = useBot();
  const [open, setOpen] = useState(false);
  const tone = botTone(bot.status);
  return (
    <>
      {larkcli ? (
        <Group title={t("settings-feishubot.title")} footer={t("settings-feishubot.combined_footer")}>
          <CombinedLarkRow bot={bot} connector={larkcli} onOpen={() => setOpen(true)} />
        </Group>
      ) : (
        <Group title={t("settings-feishubot.title")} footer={t("settings-feishubot.footer")}>
          <button type="button" onClick={() => setOpen(true)} className="flex w-full items-center gap-3 whitespace-nowrap px-4 py-3 text-left transition-colors hover:bg-hover">
            <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-accent-tint text-accent-ink">
              <Icon name="send" size={16} />
            </span>
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[13.5px] font-medium text-ink">{bot.status?.bot_name || t("settings-feishubot.title")}</span>
              <span className="mt-0.5 flex items-center gap-1.5 text-[11.5px] text-ink-3">
                <span className={cx("size-1.5 shrink-0 rounded-full", tone ? dotClass[tone] : "bg-line-strong")} />
                <span className="truncate">{bot.error ? t("settings-feishubot.status_unreadable") : botStateText(bot.status, bot.loading)}</span>
              </span>
            </span>
            {bot.status && <Pill tone={tone}>{botSummary(bot.status)}</Pill>}
            <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
          </button>
        </Group>
      )}
      {onChannel && <ChannelLink id="feishu" manager={manager} onOpen={onChannel} />}
      {(open || onRouteClose) && <FeishuBotDialog bot={bot} manager={manager} larkcli={larkcli} onClose={onRouteClose ?? (() => setOpen(false))} onOpen={onOpen} />}
    </>
  );
}

function CombinedLarkRow({ bot, connector, onOpen }: { bot: ReturnType<typeof useBot>; connector: HubConnector; onOpen: () => void }) {
  const state = combinedLarkState(bot.status, bot.loading, connector);
  const appID = bot.status?.app_id ? shortAppID(bot.status.app_id) : "";
  return (
    <button type="button" onClick={onOpen} className="flex w-full items-center gap-3 whitespace-nowrap px-4 py-3 text-left transition-colors hover:bg-hover">
      <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-surface shadow-hairline">
        <BrandMark brand="lark" size={36} fit="tile" />
      </span>
      <span className="min-w-0 flex-1 truncate text-[13.5px] font-medium text-ink">
        {bot.status?.bot_name || t("settings-feishubot.lark_name")}
        {appID && <span className="font-normal text-ink-3"> · {appID}</span>}
      </span>
      <Pill tone={state.tone === "idle" ? undefined : state.tone}>{state.label}</Pill>
      <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
    </button>
  );
}

function FeishuBotDialog({ bot, manager, larkcli, onClose, onOpen }: { manager?: string; bot: ReturnType<typeof useBot>; larkcli?: HubConnector | null; onClose: () => void; onOpen?: (id: string) => void }) {
  const { status, accept, readStatus, loading, error, setError, load } = bot;
  const [dialogLarkcli, setDialogLarkcli] = useState<HubConnector | null | undefined>(larkcli);
  const [form, setForm] = useState<BotCredentials>(() => (status ? credentialsOf(status) : { appID: "", appSecret: "", domain: "feishu" }));
  const [saving, setSaving] = useState(false);
  const [confirmUnpair, setConfirmUnpair] = useState(false);
  // The credentials form and the manual steps: for an app of your own, folded until asked for.
  const [advanced, setAdvanced] = useState(false);
  // The app was just created here by scanning: reading Lark as you is the next step, started without another press.
  const [created, setCreated] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const changed = credentialsChanged(status, form);
  const pairing = livePairing(status, now);
  const features = status?.features ?? DEFAULT_FEATURES;
  const nudgeAfter = status?.nudge_after ?? DEFAULT_NUDGE_AFTER;
  const reportDelay = status?.source_report_delay_minutes;
  const fullReply = status?.source_report_full_reply;
  const fullReplyDetail = useId();
  // Read at answer time, not when the request (or the pairing poll) started: typing meanwhile must count.
  const changedRef = useRef(changed);
  changedRef.current = changed;
  // Whether the user has typed in the credentials. Until then the form follows the hub: a dialog opened by URL before the
  // status arrived starts empty, and an empty form differs from the saved credentials without anything being typed.
  const editedRef = useRef(false);
  const edit = (next: BotCredentials) => {
    editedRef.current = true;
    setForm(next);
  };

  const fetchLarkcli = useCallback(async () => {
    const all = await listConnectors();
    const found = all.find((connector) => connector.id === "larkcli" && sameManager(connector.manager, manager));
    // A re-read that no longer lists it keeps what the page showed; only a first read may conclude "none".
    setDialogLarkcli((prev) => found ?? (prev === undefined ? null : prev));
  }, [manager]);

  useEffect(() => {
    if (larkcli !== undefined) {
      setDialogLarkcli(larkcli);
      return;
    }
    setDialogLarkcli(undefined);
    let active = true;
    void fetchLarkcli()
      .then(() => {})
      .catch(() => {
        if (active) setDialogLarkcli(null);
      });
    return () => {
      active = false;
    };
  }, [fetchLarkcli, larkcli]);

  // A newer status: fill the form unless something is being typed (replace: the answer to saving the credentials).
  const adoptForm = (s: FeishuBotStatus, replace = false) => {
    if (replace || !editedRef.current || !changedRef.current) {
      editedRef.current = false;
      setForm(credentialsOf(s));
    }
  };

  const reloadAll = useCallback(async (afterCreate = false) => {
    // Keep the bot's successful response even if the optional connector read fails, but only start authorization
    // after both reads succeed and no newer confirmed status or write has superseded this setup.
    const [next] = await Promise.all([readStatus().then((result) => {
      if (result?.current()) adoptForm(result.status);
      return result;
    }), fetchLarkcli()]);
    if (afterCreate && next?.current()) setCreated(true);
  }, [readStatus, fetchLarkcli, changed]);
  useEffect(() => {
    if (status && (!editedRef.current || !changed)) setForm(credentialsOf(status));
    // only when the hub's credentials change, not on each keystroke
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status?.app_id, status?.domain]);

  // Waiting for the code: tick the countdown and look again every 3 s until someone uses it, while the tab is shown, and
  // once right away on coming back. The status that carried the code is fresh, so visiblePoll's immediate first read is skipped.
  useEffect(() => {
    if (!pairing || status?.owner) return;
    const tick = setInterval(() => setNow(Date.now()), 1000);
    let started = false;
    const stop = visiblePoll(() => { if (started) void readStatus().then((result) => { if (result?.current()) adoptForm(result.status); }, () => {}); }, 3000, documentPage);
    started = true;
    return () => (clearInterval(tick), stop());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pairing?.code, status?.owner]);

  const run = async (what: string, call: () => Promise<FeishuBotStatus>, done?: string, replace = false) => {
    setSaving(true);
    try {
      const next = await call();
      if (accept(next)) adoptForm(next, replace);
      setError("");
      if (done) toast(done, "ok");
      return true;
    } catch (e) {
      setError(t("settings-feishubot.error_with", { what, error: errorText(e) }));
      return false;
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    setSaving(true);
    try {
      await testBot();
      toast(t("settings-feishubot.test_sent"), "ok");
      setError("");
    } catch (e) {
      setError(t("settings-feishubot.test_failed", { error: errorText(e) }));
    } finally {
      setSaving(false);
    }
  };

  const tone = botTone(status);
  const agents = status?.agents ?? [];
  // No app yet: creating one by scanning comes first (manager.json's credentials are not this page's to replace).
  const noApp = !!status && !status.has_secret && !status.from_config;

  return (
    <Dialog open onClose={onClose} title={dialogLarkcli ? t("settings-feishubot.lark_name") : t("settings-feishubot.title")} width={560}>
      {noApp && (
        <BindSteps
          c={{ id: "feishu", name: t("settings-feishubot.lark_name"), bind: "fields" }}
          title={t("settings-feishubot.create_title")}
          footer={t(dialogLarkcli ? "settings-feishubot.create_footer_shared" : "settings-feishubot.create_footer")}
          startLabel={t("settings-feishubot.create_start")}
          onDone={() => reloadAll(true)}
        />
      )}

      {dialogLarkcli && (
        <LarkReadSection
          connector={dialogLarkcli}
          status={status}
          // lark-cli follows the bot's app, and there is none yet, nor one of lark-cli's own: create it above first.
          waitsForApp={noApp && status?.larkcli?.mode === "default" && !status.larkcli.app_id}
          autoStart={created && dialogLarkcli.status !== "ok"}
          reload={reloadAll}
          onOpen={() => {
            onClose();
            onOpen?.("larkcli");
          }}
        />
      )}

      {/* reading Feishu as you every day, into the knowledge base: absent on a hub without it */}
      <LarkSyncSection appID={status?.app_id} />

      <Group title={dialogLarkcli ? t("settings-feishubot.bot_section") : undefined} footer={t("settings-feishubot.footer")}>
        <div className="flex items-center gap-3 px-4 py-3">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-accent-tint text-accent-ink">
            <Icon name="send" size={16} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="truncate text-[13.5px] font-medium text-ink">{status?.bot_name || t("settings-feishubot.title")}</div>
            <div className="mt-0.5 flex items-center gap-1.5 text-[11.5px] text-ink-3">
              <span className={cx("size-1.5 shrink-0 rounded-full", tone ? dotClass[tone] : "bg-line-strong")} />
              <span className="truncate" title={botStateText(status, loading)}>{botStateText(status, loading)}</span>
            </div>
          </div>
          {loading || saving ? <Spinner /> : <IconButton icon="retry" label={t("settings-feishubot.recheck")} onClick={() => void load()} />}
        </div>
        <Row label={t("settings-feishubot.enable")}>
          <Switch on={status?.enabled ?? false} disabled={!status || saving} label={t("settings-feishubot.enable")} onChange={(on) => void run(on ? t("settings-feishubot.enable_failed") : t("settings-feishubot.disable_failed"), () => updateBot({ enabled: on }))} />
        </Row>
        {!!status?.last_event_at && <Row label={t("settings-feishubot.last_event")}>{ago(new Date(status.last_event_at * 1000).toISOString())}</Row>}
        {error && <p role="alert" className="px-4 py-2.5 text-[12px] break-words text-red">{error}</p>}
      </Group>

      {status?.has_secret && (
        <>
          <Group
            title={t("settings-feishubot.pairing")}
            footer={status.owner || pairing ? undefined : status.enabled ? t("settings-feishubot.pair_hint") : t("settings-feishubot.pair_enable_first")}
          >
            {status.owner ? (
              <>
                <Row label={t("settings-feishubot.paired")}>{status.owner.name || t("settings-feishubot.lark_user")}</Row>
                {status.target && <Row label={t("settings-feishubot.target")}>{status.target}</Row>}
                {confirmUnpair ? (
                  <div className="flex items-center gap-2 bg-red-tint/50 px-4 py-2.5">
                    <span className="min-w-0 flex-1 text-[12px] leading-snug text-ink-2">{t("settings-feishubot.unpair_confirm")}</span>
                    <Button tone="quiet" onClick={() => setConfirmUnpair(false)}>{t("common.cancel")}</Button>
                    <Button tone="danger" busy={saving} onClick={() => void run(t("settings-feishubot.unpair_failed"), unpairBot, t("settings-feishubot.unpaired")).then((ok) => ok && setConfirmUnpair(false))}>
                      {t("settings-feishubot.unpair")}
                    </Button>
                  </div>
                ) : (
                  <div className="flex justify-end gap-2 px-4 py-2.5">
                    <Button tone="quiet" icon="x" onClick={() => setConfirmUnpair(true)} className="text-red enabled:hover:text-red">
                      {t("settings-feishubot.unpair")}
                    </Button>
                    <Button icon="send" disabled={saving || !status.connected} onClick={() => void test()}>
                      {t("settings-feishubot.send_test")}
                    </Button>
                  </div>
                )}
              </>
            ) : pairing ? (
              <div className="flex items-end gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="text-[12px] text-ink-3">{t("settings-feishubot.pair_code_prompt")}</div>
                  <div className="mt-1 font-mono text-[28px] font-semibold tracking-[0.12em] text-ink tabular-nums select-all">{pairing.code}</div>
                  <div className="mt-0.5 text-[11.5px] text-ink-3 tabular-nums">{pairingLeft(pairing.expires_at, now)}</div>
                </div>
                <Button icon="copy" onClick={() => void copyText(pairing.code).then(() => toast(t("settings-feishubot.code_copied"), "ok"))}>
                  {t("settings-feishubot.copy_code")}
                </Button>
              </div>
            ) : (
              <div className="flex justify-end px-4 py-2.5">
                <Button tone="accent" icon="link" disabled={saving || !status.enabled} onClick={() => void run(t("settings-feishubot.pair_code_failed"), pairBot)}>
                  {t("settings-feishubot.pair")}
                </Button>
              </div>
            )}
          </Group>

          {status.owner && <TopicGroups status={status} saving={saving} save={(topic_chats) => run(t("settings-feishubot.save_failed"), () => updateBot({ topic_chats }))} />}

          <Group title={t("settings-feishubot.features")} footer={t("settings-feishubot.features_footer")}>
            {FEATURES.filter((f) => (f.key !== "report_steps" && f.key !== "answer_new_card" && f.key !== "topic_cot" && f.key !== "dm_cot" && f.key !== "cot_answer" && f.key !== "read_group" && f.key !== "read_group_direct") || features[f.key] !== undefined).map((f) => {
              // "All cards" needs "cards"; "only conclusions" needs task progress, without which the hub sends no conclusions either,
              // and trims only the execution steps, which a current hub pushes only with "report_steps" on (#3218).
              // "report_steps" needs task progress too; a hub from before it leaves the key out and the switch is hidden,
              // as are "answer_new_card", "topic_cot", "dm_cot", "cot_answer", "read_group" and "read_group_direct" on hubs from before them.
              // "read_group_direct" only says where a /read's answer goes, so it waits dimmed while /read is off (#14437).
              // "cot_answer" only applies to a turn that gets a native agent message, so it waits dimmed while neither dm_cot nor topic_cot is on.
              const off =
                (f.key === "all_cards" && !features.cards) ||
                (f.key === "only_conclusions" && (features.reports === false || (features.report_steps === false && !features.only_conclusions))) ||
                (f.key === "report_steps" && features.reports === false) ||
                (f.key === "read_group_direct" && !features.read_group) ||
                (f.key === "cot_answer" && !features.dm_cot && features.topic_cot === false);
              return (
                <div key={f.key} className={cx("flex items-start gap-3 px-4 py-2.5", off && "opacity-50")}>
                  <Icon name={f.icon} size={15} className="mt-0.5 shrink-0 text-accent" />
                  <div className="min-w-0 flex-1">
                    <div className="text-[13px] text-ink">{f.title}</div>
                    <div className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{f.detail}</div>
                  </div>
                  <Switch on={features[f.key] ?? false} disabled={saving || off} label={f.title} onChange={(on) => void run(t("settings-feishubot.save_failed"), () => updateBot({ features: featureEdit(features, f.key, on) }))} />
                </div>
              );
            })}
            <div className="flex items-start gap-3 px-4 py-2.5">
              <Icon name="clock" size={15} className="mt-0.5 shrink-0 text-accent" />
              <div className="min-w-0 flex-1">
                <div className="text-[13px] text-ink">{t("settings-feishubot.nudge_title")}</div>
                <div className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{t("settings-feishubot.nudge_detail")}</div>
              </div>
              <select
                value={nudgeAfter}
                disabled={saving}
                aria-label={t("settings-feishubot.nudge_title")}
                onChange={(e) => void run(t("settings-feishubot.save_failed"), () => updateBot({ nudge_after: Number(e.target.value) }))}
                className={cx(controlClass, "shrink-0")}
              >
                {nudgeChoices(nudgeAfter).map((m) => (
                  <option key={m} value={m}>
                    {nudgeLabel(m)}
                  </option>
                ))}
              </select>
            </div>
            {fullReply !== undefined && (
              <div className="flex items-start gap-3 px-4 py-2.5">
                <Icon name="chat" size={15} className="mt-0.5 shrink-0 text-accent" />
                <div className="min-w-0 flex-1">
                  <div className="text-sm text-ink">{t("settings-feishubot.source_report_full_title")}</div>
                  <div id={fullReplyDetail} className="mt-0.5 text-xs leading-snug text-ink-3">{t("settings-feishubot.source_report_full_detail")}</div>
                </div>
                <Switch on={fullReply} disabled={saving} describedBy={fullReplyDetail} label={t("settings-feishubot.source_report_full_title")} onChange={(on) => void run(t("settings-feishubot.save_failed"), () => updateBot({ source_report_full_reply: on }))} />
              </div>
            )}
            {reportDelay !== undefined && (
              <div className="flex items-start gap-3 px-4 py-2.5">
                <Icon name="clock" size={15} className="mt-0.5 shrink-0 text-accent" />
                <div className="min-w-0 flex-1">
                  <div className="text-sm text-ink">{t("settings-feishubot.source_report_delay_title")}</div>
                  <div className="mt-0.5 text-xs leading-snug text-ink-3">{t("settings-feishubot.source_report_delay_detail")}</div>
                </div>
                <select
                  value={reportDelay}
                  disabled={saving}
                  aria-label={t("settings-feishubot.source_report_delay_title")}
                  onChange={(e) => void run(t("settings-feishubot.save_failed"), () => updateBot({ source_report_delay_minutes: Number(e.target.value) }))}
                  className={cx(controlClass, "min-h-11 shrink-0")}
                >
                  {[...new Set([...SOURCE_REPORT_DELAYS, reportDelay])].sort((a, b) => a - b).map((minutes) => (
                    <option key={minutes} value={minutes}>
                      {minutes === 0 ? t("settings-feishubot.source_report_delay_now") : t("settings-feishubot.source_report_delay_minutes", { minutes })}
                    </option>
                  ))}
                </select>
              </div>
            )}
          </Group>
        </>
      )}

      {agents.length > 0 && (
        <Group title={t("settings-feishubot.agents")}>
          {agents.map((a) => (
            <Row key={a.session_id} label={a.title}>
              <span className="max-w-[160px] truncate">{a.machine_id}</span>
            </Row>
          ))}
        </Group>
      )}

      <Disclosure
        title={t(noApp ? "settings-feishubot.advanced_have_app" : "settings-feishubot.advanced_other_app")}
        open={advanced}
        onToggle={() => setAdvanced((v) => !v)}
      >
        <Group
          title={dialogLarkcli ? t("settings-feishubot.credentials") : undefined}
          footer={
            <>
              {status?.from_config ? t("settings-feishubot.credentials_from_config") : t("settings-feishubot.credentials_secret_note")}
              {dialogLarkcli && <><br />{t("settings-feishubot.credentials_shared_note")}</>}
            </>
          }
        >
          <Row label="App ID">
            <input value={form.appID} onChange={(e) => edit({ ...form, appID: e.target.value })} placeholder={t("settings-feishubot.required")} aria-label="App ID" autoComplete="off" spellCheck={false} className={cx(controlClass, "w-[240px] font-mono")} />
          </Row>
          <Row label="App Secret">
            <input
              type="password"
              value={form.appSecret}
              onChange={(e) => edit({ ...form, appSecret: e.target.value })}
              placeholder={status?.has_secret ? t("settings-feishubot.secret_saved") : t("settings-feishubot.required")}
              aria-label="App Secret"
              autoComplete="new-password"
              className={cx(controlClass, "w-[240px]")}
            />
          </Row>
          <Row label={t("settings-feishubot.platform")}>
            <select value={form.domain} onChange={(e) => edit({ ...form, domain: e.target.value })} aria-label={t("settings-feishubot.platform")} className={cx(controlClass, "w-[240px]")}>
              <option value="feishu">{t("settings-feishubot.platform_feishu")}</option>
              <option value="lark">Lark</option>
              {form.domain !== "feishu" && form.domain !== "lark" && <option value={form.domain}>{form.domain}</option>}
            </select>
          </Row>
          <div className="flex justify-end px-4 py-2.5">
            <Button tone="accent" busy={saving && changed} disabled={!changed || !form.appID.trim() || saving} onClick={() => void run(t("settings-feishubot.save_failed"), () => updateBot(credentialsUpdate(form)), t("settings-feishubot.credentials_saved"), true)}>
              {t("settings-feishubot.verify_save")}
            </Button>
          </div>
        </Group>
        <Group title={t("settings-feishubot.howto")} footer={t("settings-feishubot.howto_footer")}>
          {STEPS.map((s, i) => (
            <div key={i} className="flex items-baseline gap-3 px-4 py-2">
              <span className="w-3 shrink-0 text-[11.5px] font-semibold text-accent tabular-nums">{i + 1}</span>
              <span className="text-[11.5px] leading-relaxed text-ink-3">{s}</span>
            </div>
          ))}
        </Group>
      </Disclosure>
    </Dialog>
  );
}

export function LarkReadSection({ connector, status, waitsForApp, autoStart, reload, onOpen }: {
  connector: HubConnector;
  status: FeishuBotStatus | null;
  waitsForApp?: boolean;
  autoStart?: boolean;
  reload: () => Promise<void>;
  onOpen: () => void;
}) {
  const [rechecking, setRechecking] = useState(false);
  const [switching, setSwitching] = useState(false);
  const on = isEnabled(connector);
  // The same switch as the connector page's "Sync …": off stops reading Lark as you; the login and what was kept stay.
  const toggle = async (enabled: boolean) => {
    setSwitching(true);
    try {
      await setConnector(connector.id, { enabled });
      await reload();
    } catch (e) {
      toast(t("settings-feishubot.error_with", { what: t("settings-feishubot.read_section"), error: errorText(e) }), "error");
    } finally {
      setSwitching(false);
    }
  };
  // "All comments on documents I own" sits here, next to the account, rather than only on the connector's page.
  const owned = larkcliOptions(connector.options);
  const setOwned = async (v: boolean) => {
    if (!owned) return;
    setSwitching(true);
    try {
      await setConnector(connector.id, { options: { ...owned, owned_all_comments: v } });
      await reload();
    } catch (e) {
      toast(t("settings-feishubot.error_with", { what: t("settings-hubconnector.lark_docs_owned"), error: errorText(e) }), "error");
    } finally {
      setSwitching(false);
    }
  };
  const scopes = missingScopes(connector);
  const link = permissionLink(connector);
  const lacksPermission = connector.status === "no_permission" || scopes.length > 0;
  const tone = readTone(connector);
  const check = async () => {
    setRechecking(true);
    try {
      await reload();
    } finally {
      setRechecking(false);
    }
  };
  const profile = status?.larkcli;
  const [editingProfile, setEditingProfile] = useState(false);
  // Which lark-cli profile reads Lark right now, in words: following the bot's app, the one picked here, or the one
  // manager.json fixes (then it cannot be changed from the app).
  const profileInUse = !profile
    ? ""
    : profile.mode === "bot" || (profile.mode === "default" && !profile.choice)
      ? t(profile.mode === "bot" ? "settings-feishubot.larkcli_profile_auto_short" : "settings-feishubot.larkcli_profile_default")
      : profile.mode === "config"
        ? t("settings-feishubot.larkcli_profile_fixed", { name: profile.profile })
        : profile.profile || profile.choice;
  const saveProfile = async (choice: string) => {
    setSwitching(true);
    try {
      await updateBot({ larkcli_profile: choice });
      await reload();
    } catch (e) {
      toast(t("settings-feishubot.error_with", { what: t("settings-feishubot.larkcli_profile_title"), error: errorText(e) }), "error");
    } finally {
      setSwitching(false);
    }
  };
  return (
    <Group
      title={t("settings-feishubot.read_section")}
      footer={on ? t("settings-feishubot.larkcli_dialog_footer") : t("settings-feishubot.read_off_footer")}
      right={<Switch on={on} onChange={(v) => void toggle(v)} disabled={switching} label={t("settings-feishubot.read_section")} />}
    >
      <div className="flex items-center gap-3 px-4 py-3">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-surface shadow-hairline">
          <BrandMark brand="lark" size={36} fit="tile" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13.5px] font-medium text-ink">{t("settings-feishubot.read_as_you")}</div>
          <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-3">
            <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", connectorDotClass[tone])} />
            <span className="truncate">{connectorState(connector)}{connector.status === "ok" && connector.account ? ` · ${connectorSummary(connector)}` : ""}</span>
          </div>
          {profile && (
            <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-3">
              <span className="truncate">{t("settings-feishubot.larkcli_profile_using", { name: profileInUse })}</span>
              {profile.mode !== "config" && (
                <button type="button" className="shrink-0 whitespace-nowrap text-accent hover:underline" onClick={() => setEditingProfile((v) => !v)}>
                  {t(editingProfile ? "settings-feishubot.larkcli_profile_done" : "settings-feishubot.larkcli_profile_change")}
                </button>
              )}
            </div>
          )}
        </div>
      </div>
      {lacksPermission && (
        <div className="flex flex-col gap-2 border-t border-line px-4 py-2.5">
          <div className="text-[12.5px] font-medium text-orange">{t("settings-feishubot.read_missing_title")}</div>
          {scopes.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {scopes.map((scope) => <span key={scope} className="rounded-control bg-orange-tint px-1.5 py-0.5 font-mono text-[11.5px] text-orange">{scope}</span>)}
            </div>
          ) : connector.error ? (
            <p className="text-[12.5px] break-words text-ink-2 select-text">{connector.error}</p>
          ) : null}
          <div className="flex flex-wrap items-center gap-2 pt-0.5">
            {link && <Button tone="secondary" onClick={() => window.open(link, "_blank", "noopener,noreferrer")}>{t("settings-feishubot.read_open_platform")}</Button>}
            <Button tone="quiet" icon="retry" busy={rechecking} onClick={() => void check()}>{t("settings-feishubot.recheck")}</Button>
          </div>
        </div>
      )}
      {waitsForApp ? (
        <p className="border-t border-line px-4 py-2.5 text-[12.5px] leading-relaxed text-ink-3">{t("settings-feishubot.read_needs_app")}</p>
      ) : signsInHere(connector) && (
        // Signed in: signing in again is a quiet secondary action; otherwise it is what this section is for.
        <SignInSteps c={connector} reload={reload} autoStart={autoStart} startLabel={t(connector.status === "ok" ? "settings-feishubot.read_reauthorize" : "settings-feishubot.read_authorize")} startTone={connector.status === "ok" ? "quiet" : "accent"} bare />
      )}
      {profile && editingProfile && profile.mode !== "config" && (
        <Row label={t("settings-feishubot.larkcli_profile_title")}>
          <select
            aria-label={t("settings-feishubot.larkcli_profile_title")}
            value={profile.choice}
            disabled={switching}
            onChange={(e) => void saveProfile(e.target.value)}
            className={cx(controlClass, "max-w-[300px]")}
          >
            <option value="">{t("settings-feishubot.larkcli_profile_auto")}</option>
            {profile.profiles.map((p) => (
              <option key={p.name} value={p.name}>
                {p.name} · {p.user || t("settings-feishubot.larkcli_profile_not_logged_in")} · {p.app_id ? shortAppID(p.app_id) : t("settings-feishubot.larkcli_profile_no_app")}
              </option>
            ))}
          </select>
        </Row>
      )}
      {on && owned && (
        <Row label={t("settings-hubconnector.lark_docs_owned")} hint={t("settings-hubconnector.lark_docs_owned_sub")} wrapHint>
          <Switch on={owned.owned_all_comments} disabled={switching} label={t("settings-hubconnector.lark_docs_owned")} onChange={(v) => void setOwned(v)} />
        </Row>
      )}
      <Row label={t("settings-feishubot.read_more")} hint={t("settings-feishubot.read_more_hint")} onClick={onOpen} />
    </Group>
  );
}


/** Group policies use the same acknowledged save as the other bot settings. */
export function TopicGroups({ status, saving, save }: { status: FeishuBotStatus; saving: boolean; save: (groups: BotTopicChat[]) => Promise<boolean> }) {
  const groups = status.topic_chats ?? [];
  const [adding, setAdding] = useState(false);
  const [chatID, setChatID] = useState("");
  const [name, setName] = useState("");
  const [strict, setStrict] = useState(false);
  const add = async () => {
    if (await save([...groups, { chat_id: chatID.trim(), name: name.trim(), require_owner_mention: strict }])) {
      setAdding(false);
      setChatID("");
      setName("");
      setStrict(false);
    }
  };
  return (
    <Group title={t("settings-feishubot.topic_groups")} footer={t("settings-feishubot.topic_policy_footer")} anchor="topic-groups">
      <Row label={t("settings-feishubot.topic_home")} hint={t("settings-feishubot.topic_home_hint")} wrapHint stackOnNarrow>
        <select aria-label={t("settings-feishubot.topic_home")} className={settingsInputClass} disabled={saving || !groups.length} value={groups.find((g) => g.home)?.chat_id ?? ""}
          onChange={(e) => void save(groups.map((g) => ({ ...g, home: g.chat_id === e.target.value })))}>
          <option value="">{t(groups.some((g) => g.home) ? "settings-feishubot.topic_home_default" : "settings-feishubot.topic_home_none")}</option>
          {groups.map((g) => <option key={g.chat_id} value={g.chat_id}>{g.name || g.chat_id}</option>)}
        </select>
      </Row>
      {groups.map((g) => {
        const group = g.name || g.chat_id;
        return (
          <Fragment key={g.chat_id}>
            <Row label={group} hint={t("settings-feishubot.topic_owner_mention")} wrapHint>
              <Switch on={g.require_owner_mention ?? false} disabled={saving} label={t("settings-feishubot.topic_mention_for", { name: group })}
                onChange={(required) => void save(groups.map((item) => item.chat_id === g.chat_id ? { ...item, require_owner_mention: required } : item))} />
            </Row>
            {g.todo_mode !== undefined && (
              <Row label={t("settings-feishubot.topic_todo_for", { name: group })} wrapHint stackOnNarrow>
                <select aria-label={t("settings-feishubot.topic_todo_for", { name: group })} className={`${settingsInputClass} min-h-11`} disabled={saving} value={g.todo_mode}
                  onChange={(e) => {
                    const mode = e.target.value as TopicTodoMode;
                    void save(groups.map((item) => ({
                      ...item,
                      home: undefined,
                      todo_mode: item.chat_id === g.chat_id ? mode : undefined,
                    })));
                  }}>
                  <option value="answer_only">{t("settings-feishubot.topic_todo_answer_only")}</option>
                  <option value="main_decides">{t("settings-feishubot.topic_todo_main_decides")}</option>
                  <option value="always_todo">{t("settings-feishubot.topic_todo_always")}</option>
                </select>
              </Row>
            )}
          </Fragment>
        );
      })}
      {adding ? (
        <div className="space-y-3 px-4 py-3">
          <label className="block text-label text-ink">{t("settings-feishubot.topic_group_id")}<input className={settingsInputClass} value={chatID} disabled={saving} onChange={(e) => setChatID(e.target.value)} /></label>
          <label className="block text-label text-ink">{t("settings-feishubot.topic_group_name")}<input className={settingsInputClass} value={name} disabled={saving} onChange={(e) => setName(e.target.value)} /></label>
          <Row label={t("settings-feishubot.topic_owner_mention")} wrapHint>
            <Switch on={strict} disabled={saving} label={t("settings-feishubot.topic_owner_mention")} onChange={setStrict} />
          </Row>
          <div className="flex flex-wrap justify-end gap-2">
            <Button disabled={saving} onClick={() => setAdding(false)}>{t("common.cancel")}</Button>
            <Button tone="accent" busy={saving} disabled={saving || !chatID.trim() || groups.some((g) => g.chat_id === chatID.trim())} onClick={() => void add()}>{t("settings-feishubot.topic_add")}</Button>
          </div>
        </div>
      ) : <div className="flex justify-end px-4 py-2.5"><Button disabled={saving || groups.length >= 50} onClick={() => setAdding(true)}>{t("settings-feishubot.topic_add")}</Button></div>}
    </Group>
  );
}
