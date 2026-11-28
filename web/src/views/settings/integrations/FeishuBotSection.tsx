// Inline Feishu Bot connection and delivery panels for IntegrationDetail (#14847).
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { t } from "../../../i18n";
import { errorText } from "../../../lib/api";
import type { HubConnector } from "../../../lib/connectors";
import {
  botStateText, botTone, credentialsChanged, credentialsOf, credentialsUpdate,
  DEFAULT_FEATURES, DEFAULT_NUDGE_AFTER, featureEdit, livePairing, nudgeChoices,
  nudgeLabel, pairBot, pairingLeft, testBot, unpairBot, updateBot,
  type BotCredentials, type FeishuBotStatus,
} from "../../../lib/feishu-bot";
import { useStore } from "../../../lib/store";
import { ago } from "../../../lib/format";
import { documentPage, visiblePoll } from "../../../lib/visible-poll";
import { Button, copyText, cx, IconButton, Spinner, toast } from "../../../ui/atoms";
import { Icon } from "../../../ui/icons";
import { BindSteps } from "../Channels";
import { FEATURES, LarkReadSection, SOURCE_REPORT_DELAYS, STEPS, TopicGroups, useBot } from "../FeishuBot";
import { LarkSyncSection } from "../LarkSyncSection";
import { FeishuGroupAccess } from "./FeishuGroupAccess";
import { Disclosure } from "../ProviderFields";
import { Group, Row, settingsInputClass, Switch } from "../SettingsParts";

const dotClass = { green: "bg-green", red: "bg-red", orange: "bg-orange" } as const;

export interface FeishuBotController {
  status: FeishuBotStatus | null;
  loading: boolean;
  saving: boolean;
  error: string;
  form: BotCredentials;
  changed: boolean;
  created: boolean;
  now: number;
  load: () => Promise<void>;
  reloadAll: (afterCreate?: boolean) => Promise<void>;
  edit: (next: BotCredentials) => void;
  run: (what: string, call: () => Promise<FeishuBotStatus>, done?: string, replace?: boolean) => Promise<boolean>;
  sendTest: () => Promise<void>;
}

/** Shared Feishu Bot state powering both Account & Connection and Chat & Delivery sections inline. */
export function useFeishuBotController(enabled: boolean, reloadLarkcli: () => Promise<void>, reloadChannel: () => Promise<void>): FeishuBotController {
  const bot = useBot();
  const { status, accept, readStatus, loading, error, setError, load } = bot;
  const [form, setForm] = useState<BotCredentials>(() => (status ? credentialsOf(status) : { appID: "", appSecret: "", domain: "feishu" }));
  const [saving, setSaving] = useState(false);
  const [created, setCreated] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const changed = credentialsChanged(status, form);
  const pairing = livePairing(status, now);

  const changedRef = useRef(changed);
  changedRef.current = changed;
  const editedRef = useRef(false);
  const edit = useCallback((next: BotCredentials) => {
    editedRef.current = true;
    setForm(next);
  }, []);

  const adoptForm = useCallback((s: FeishuBotStatus, replace = false) => {
    if (replace || !editedRef.current || !changedRef.current) {
      editedRef.current = false;
      setForm(credentialsOf(s));
    }
  }, []);

  useEffect(() => {
    if (status && (!editedRef.current || !changed)) setForm(credentialsOf(status));
    // Only sync when saved credentials change on the hub.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status?.app_id, status?.domain]);

  useEffect(() => {
    if (!enabled || !pairing || status?.owner) return;
    const tick = setInterval(() => setNow(Date.now()), 1000);
    let started = false;
    const stop = visiblePoll(() => {
      if (started) void readStatus().then((r) => { if (r?.current()) adoptForm(r.status); }, () => {});
    }, 3000, documentPage);
    started = true;
    return () => {
      clearInterval(tick);
      stop();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, pairing?.code, status?.owner]);

  const reloadAll = useCallback(async (afterCreate = false) => {
    const [next] = await Promise.all([
      readStatus().then((result) => {
        if (result?.current()) adoptForm(result.status);
        return result;
      }),
      reloadLarkcli().catch(() => {}),
      reloadChannel(),
    ]);
    if (afterCreate && next?.current()) setCreated(true);
  }, [readStatus, adoptForm, reloadLarkcli, reloadChannel]);

  const run = useCallback(async (what: string, call: () => Promise<FeishuBotStatus>, done?: string, replace = false) => {
    setSaving(true);
    try {
      const next = await call();
      if (accept(next)) {
        adoptForm(next, replace);
        void reloadChannel();
      }
      setError("");
      if (done) toast(done, "ok");
      return true;
    } catch (e) {
      setError(t("settings-feishubot.error_with", { what, error: errorText(e) }));
      return false;
    } finally {
      setSaving(false);
    }
  }, [accept, adoptForm, reloadChannel, setError]);

  const sendTest = useCallback(async () => {
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
  }, [setError]);

  return { status, loading, saving, error, form, changed, created, now, load, reloadAll, edit, run, sendTest };
}

/** Account & Connection panel for built-in Feishu Bot (app creation, lark-cli auth, bot status, pairing, credentials). */
export function FeishuBotConnectionPanel({
  ctl,
  larkcli,
  onOpenConnector,
}: {
  ctl: FeishuBotController;
  larkcli?: HubConnector | null;
  onOpenConnector?: (id: string) => void;
}) {
  const { status, loading, saving, error, form, changed, created, now, load, reloadAll, edit, run, sendTest } = ctl;
  const [confirmUnpair, setConfirmUnpair] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const tone = botTone(status);
  const pairing = livePairing(status, now);
  const noApp = !!status && !status.has_secret && !status.from_config;

  return (
    <>
      {noApp && (
        <BindSteps
          c={{ id: "feishu", name: t("settings-feishubot.lark_name"), bind: "fields" }}
          title={t("settings-feishubot.create_title")}
          footer={t(larkcli ? "settings-feishubot.create_footer_shared" : "settings-feishubot.create_footer")}
          startLabel={t("settings-feishubot.create_start")}
          onDone={() => reloadAll(true)}
        />
      )}

      {larkcli && (
        <LarkReadSection
          connector={larkcli}
          status={status}
          waitsForApp={noApp && status?.larkcli?.mode === "default" && !status.larkcli.app_id}
          autoStart={created && larkcli.status !== "ok"}
          reload={reloadAll}
          onOpen={() => onOpenConnector?.("larkcli")}
        />
      )}

      <LarkSyncSection appID={status?.app_id} />

      <Group title={larkcli ? t("settings-feishubot.bot_section") : undefined} footer={t("settings-feishubot.footer")}>
        <div className="flex items-center gap-3 px-4 py-3">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-control bg-accent-tint text-accent-ink">
            <Icon name="send" size={16} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="truncate text-meta font-medium text-ink">{status?.bot_name || t("settings-feishubot.title")}</div>
            <div className="mt-0.5 flex items-center gap-1.5 text-submeta text-ink-3">
              <span className={cx("size-1.5 shrink-0 rounded-full", tone ? dotClass[tone] : "bg-line-strong")} />
              <span className="truncate" title={botStateText(status, loading)}>{botStateText(status, loading)}</span>
            </div>
          </div>
          {loading || saving ? <Spinner /> : <IconButton icon="retry" label={t("settings-feishubot.recheck")} onClick={() => void load()} />}
        </div>
        <Row label={t("settings-feishubot.enable")}>
          <Switch
            on={status?.enabled ?? false}
            disabled={!status || saving}
            label={t("settings-feishubot.enable")}
            onChange={(on) => void run(on ? t("settings-feishubot.enable_failed") : t("settings-feishubot.disable_failed"), () => updateBot({ enabled: on }))}
          />
        </Row>
        {!!status?.last_event_at && <Row label={t("settings-feishubot.last_event")}>{ago(new Date(status.last_event_at * 1000).toISOString())}</Row>}
        {error && <p role="alert" className="px-4 py-2.5 text-label break-words text-red">{error}</p>}
      </Group>

      {status?.has_secret && (
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
                  <span className="min-w-0 flex-1 text-label leading-snug text-ink-2">{t("settings-feishubot.unpair_confirm")}</span>
                  <Button tone="quiet" onClick={() => setConfirmUnpair(false)}>{t("common.cancel")}</Button>
                  <Button
                    tone="danger"
                    busy={saving}
                    onClick={() => void run(t("settings-feishubot.unpair_failed"), unpairBot, t("settings-feishubot.unpaired")).then((ok) => ok && setConfirmUnpair(false))}
                  >
                    {t("settings-feishubot.unpair")}
                  </Button>
                </div>
              ) : (
                <div className="flex justify-end gap-2 px-4 py-2.5">
                  <Button tone="quiet" icon="x" onClick={() => setConfirmUnpair(true)} className="text-red enabled:hover:text-red">
                    {t("settings-feishubot.unpair")}
                  </Button>
                  <Button icon="send" disabled={saving || !status.connected} onClick={() => void sendTest()}>
                    {t("settings-feishubot.send_test")}
                  </Button>
                </div>
              )}
            </>
          ) : pairing ? (
            <div className="flex items-end gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="text-label text-ink-3">{t("settings-feishubot.pair_code_prompt")}</div>
                <div className="mt-1 font-mono text-2xl font-semibold tracking-widest text-ink tabular-nums select-all">{pairing.code}</div>
                <div className="mt-0.5 text-submeta text-ink-3 tabular-nums">{pairingLeft(pairing.expires_at, now)}</div>
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
      )}

      <div className="mb-6">
        <Disclosure
          title={t(noApp ? "settings-feishubot.advanced_have_app" : "settings-feishubot.advanced_other_app")}
          open={advanced}
          onToggle={() => setAdvanced((v) => !v)}
        >
          <Group
            title={larkcli ? t("settings-feishubot.credentials") : undefined}
            footer={
              <>
                {status?.from_config ? t("settings-feishubot.credentials_from_config") : t("settings-feishubot.credentials_secret_note")}
                {larkcli && <><br />{t("settings-feishubot.credentials_shared_note")}</>}
              </>
            }
          >
            <Row label="App ID">
              <input
                value={form.appID}
                onChange={(e) => edit({ ...form, appID: e.target.value })}
                placeholder={t("settings-feishubot.required")}
                aria-label="App ID"
                autoComplete="off"
                spellCheck={false}
                className={cx(settingsInputClass, "max-w-[240px] font-mono")}
              />
            </Row>
            <Row label="App Secret">
              <input
                type="password"
                value={form.appSecret}
                onChange={(e) => edit({ ...form, appSecret: e.target.value })}
                placeholder={status?.has_secret ? t("settings-feishubot.secret_saved") : t("settings-feishubot.required")}
                aria-label="App Secret"
                autoComplete="new-password"
                className={cx(settingsInputClass, "max-w-[240px]")}
              />
            </Row>
            <Row label={t("settings-feishubot.platform")}>
              <select
                value={form.domain}
                onChange={(e) => edit({ ...form, domain: e.target.value })}
                aria-label={t("settings-feishubot.platform")}
                className={cx(settingsInputClass, "max-w-[240px]")}
              >
                <option value="feishu">{t("settings-feishubot.platform_feishu")}</option>
                <option value="lark">Lark</option>
                {form.domain !== "feishu" && form.domain !== "lark" && <option value={form.domain}>{form.domain}</option>}
              </select>
            </Row>
            <div className="flex justify-end px-4 py-2.5">
              <Button
                tone="accent"
                busy={saving && changed}
                disabled={!changed || !form.appID.trim() || saving}
                onClick={() => void run(t("settings-feishubot.save_failed"), () => updateBot(credentialsUpdate(form)), t("settings-feishubot.credentials_saved"), true)}
              >
                {t("settings-feishubot.verify_save")}
              </Button>
            </div>
          </Group>
          <Group title={t("settings-feishubot.howto")} footer={t("settings-feishubot.howto_footer")}>
            {STEPS.map((s, i) => (
              <div key={i} className="flex items-baseline gap-3 px-4 py-2">
                <span className="w-3 shrink-0 text-submeta font-semibold text-accent tabular-nums">{i + 1}</span>
                <span className="text-submeta leading-relaxed text-ink-3">{s}</span>
              </div>
            ))}
          </Group>
        </Disclosure>
      </div>
    </>
  );
}

/** Chat & Delivery panel for built-in Feishu Bot (topic groups, bot features, nudge/report timing, agents). */
export function FeishuBotDeliveryPanel({ ctl }: { ctl: FeishuBotController }) {
  const { status, saving, run } = ctl;
  const fullReplyDetail = useId();
  const machines = useStore((s) => s.machines);
  if (!status?.has_secret) return null;

  const features = status.features ?? DEFAULT_FEATURES;
  const nudgeAfter = status.nudge_after ?? DEFAULT_NUDGE_AFTER;
  const reportDelay = status.source_report_delay_minutes;
  const fullReply = status.source_report_full_reply;
  const agents = status.agents ?? [];

  return (
    <>
      {status.owner && (
        <TopicGroups
          status={status}
          saving={saving}
          save={(topic_chats) => run(t("settings-feishubot.save_failed"), () => updateBot({ topic_chats }))}
        />
      )}
      {status.owner && <FeishuGroupAccess status={status} saving={saving} />}

      <Group title={t("settings-feishubot.features")} footer={t("settings-feishubot.features_footer")}>
        {FEATURES.filter((f) => (f.key !== "report_steps" && f.key !== "answer_new_card" && f.key !== "topic_cot" && f.key !== "dm_cot" && f.key !== "cot_answer" && f.key !== "read_group") || features[f.key] !== undefined).map((f) => {
          const off =
            (f.key === "all_cards" && !features.cards) ||
            (f.key === "only_conclusions" && (features.reports === false || (features.report_steps === false && !features.only_conclusions))) ||
            (f.key === "report_steps" && features.reports === false) ||
            (f.key === "cot_answer" && !features.dm_cot && features.topic_cot === false);
          return (
            <div key={f.key} className={cx("flex items-start gap-3 px-4 py-2.5", off && "opacity-50")}>
              <Icon name={f.icon} size={15} className="mt-0.5 shrink-0 text-accent" />
              <div className="min-w-0 flex-1">
                <div className="text-meta text-ink">{f.title}</div>
                <div className="mt-0.5 text-submeta leading-snug text-ink-3">{f.detail}</div>
              </div>
              <Switch
                on={features[f.key] ?? false}
                disabled={saving || off}
                label={f.title}
                onChange={(on) => void run(t("settings-feishubot.save_failed"), () => updateBot({ features: featureEdit(features, f.key, on) }))}
              />
            </div>
          );
        })}
        <div className="flex items-start gap-3 px-4 py-2.5">
          <Icon name="clock" size={15} className="mt-0.5 shrink-0 text-accent" />
          <div className="min-w-0 flex-1">
            <div className="text-meta text-ink">{t("settings-feishubot.nudge_title")}</div>
            <div className="mt-0.5 text-submeta leading-snug text-ink-3">{t("settings-feishubot.nudge_detail")}</div>
          </div>
          <select
            value={nudgeAfter}
            disabled={saving}
            aria-label={t("settings-feishubot.nudge_title")}
            onChange={(e) => void run(t("settings-feishubot.save_failed"), () => updateBot({ nudge_after: Number(e.target.value) }))}
            className={cx(settingsInputClass, "w-auto shrink-0")}
          >
            {nudgeChoices(nudgeAfter).map((m) => (
              <option key={m} value={m}>{nudgeLabel(m)}</option>
            ))}
          </select>
        </div>
        {fullReply !== undefined && (
          <div className="flex items-start gap-3 px-4 py-2.5">
            <Icon name="chat" size={15} className="mt-0.5 shrink-0 text-accent" />
            <div className="min-w-0 flex-1">
              <div className="text-meta text-ink">{t("settings-feishubot.source_report_full_title")}</div>
              <div id={fullReplyDetail} className="mt-0.5 text-submeta leading-snug text-ink-3">{t("settings-feishubot.source_report_full_detail")}</div>
            </div>
            <Switch
              on={fullReply}
              disabled={saving}
              describedBy={fullReplyDetail}
              label={t("settings-feishubot.source_report_full_title")}
              onChange={(on) => void run(t("settings-feishubot.save_failed"), () => updateBot({ source_report_full_reply: on }))}
            />
          </div>
        )}
        {reportDelay !== undefined && (
          <div className="flex items-start gap-3 px-4 py-2.5">
            <Icon name="clock" size={15} className="mt-0.5 shrink-0 text-accent" />
            <div className="min-w-0 flex-1">
              <div className="text-meta text-ink">{t("settings-feishubot.source_report_delay_title")}</div>
              <div className="mt-0.5 text-submeta leading-snug text-ink-3">{t("settings-feishubot.source_report_delay_detail")}</div>
            </div>
            <select
              value={reportDelay}
              disabled={saving}
              aria-label={t("settings-feishubot.source_report_delay_title")}
              onChange={(e) => void run(t("settings-feishubot.save_failed"), () => updateBot({ source_report_delay_minutes: Number(e.target.value) }))}
              className={cx(settingsInputClass, "w-auto min-h-11 shrink-0")}
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

      {agents.length > 0 && (
        <Group title={t("settings-feishubot.agents")}>
          {agents.map((a) => (
            <Row key={a.session_id} label={a.title}>
              <span className="max-w-[160px] truncate">{machines.find((m) => m.id === a.machine_id)?.name || a.machine_id}</span>
            </Row>
          ))}
        </Group>
      )}
    </>
  );
}
