// "Daily sync to your knowledge base" (lib/lark-sync.ts): which Feishu / Lark accounts Persona reads every day, through
// which bot's app, and how each one is doing. Both Lark settings surfaces mount it after the "read Lark as you" section:
// the legacy dialog (FeishuBot.tsx) and the integration page (integrations/FeishuBotSection.tsx). A hub without the sync
// (an older one, or one without the Lark module) sends no view, and the section draws nothing.
//
// Adding an account is two steps on this page, like the chat bot's own setup above it: (1) an app that may read as the
// owner, created by scanning or the chat bot's app given that permission, then (2) the owner allows it, in the browser
// (the platform comes back to /lark-oauth, LarkOAuthReturn.tsx), with a code on screen, or after adding the return
// address to the app's settings by hand when the hub could not.
import { useCallback, useEffect, useRef, useState } from "react";
import { formatDate, t } from "../../i18n";
import { errorText } from "../../lib/api";
import { isPolled } from "../../lib/channels";
import type { ConnectorPrompt } from "../../lib/connectors";
import { ago, clock, safeWebURL } from "../../lib/format";
import {
  addSyncScopes, approvalOf, authorizeSyncBot, bindingsOf, bindingTitle, botTitle, createSyncBot, healthOf, leaveFor,
  needsAuthorize, oauthRedirectURI, orderedBots, pollSyncAuthorize, reauthSoon, registerSyncBot, rememberReturn,
  removeSyncBinding, removeSyncBot, runSyncBinding, setSyncIMPoll, setSyncSchedule, syncView, tierText, TIER_DOCS, TIER_FULL, TIERS,
  updateSyncBinding, type LarkSyncAuthorize, type LarkSyncBinding, type LarkSyncBot, type LarkSyncView,
} from "../../lib/lark-sync";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import { Button, copyText, cx, IconButton, Spinner, toast } from "../../ui/atoms";
import { QRLinkStep, QRStep, UnsupportedStep, useStepPolling } from "./LoginSteps";
import { Group, Pill, Row, RowConfirm, RowFailed, settingsInputClass, SettingsHint, Switch } from "./SettingsParts";

const selectClass = "h-8 rounded-control bg-field px-2.5 text-meta text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";
/** How often the page looks again while a sync runs, so its counts and state follow without a reload. */
const RUNNING_POLL_MS = 5000;

/** Where the add / authorize flow is: choosing, step 1 (an app that may read as the owner), or step 2 (allowing it). */
type Flow =
  | { phase: "choose" }
  | { phase: "register"; bot: string; tier: string; prompt: ConnectorPrompt; scopes: boolean; upgrade?: boolean; again?: boolean }
  | { phase: "authorize"; bot: string; tier: string; again: boolean; answer?: LarkSyncAuthorize; prompt?: ConnectorPrompt; error?: string };

/** `appID`: the chat bot's app, read again when it changes (created by scanning above the section). */
export function LarkSyncSection({ appID }: { appID?: string }) {
  const [view, setView] = useState<LarkSyncView | null | undefined>(undefined);
  const [loadError, setLoadError] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [confirm, setConfirm] = useState("");
  const [flow, setFlow] = useState<Flow | null>(null);
  // Answers are taken in the order their requests were made: a read that started before a change never undoes it.
  const order = useRef({ issued: 0, shown: 0 });
  const live = useLive();

  const accept = useCallback((n: number, v: LarkSyncView | null) => {
    if (!live.current || n < order.current.shown) return;
    order.current.shown = n;
    setView(v);
  }, [live]);

  const load = useCallback(async () => {
    const n = ++order.current.issued;
    try {
      accept(n, await syncView());
      setLoadError("");
    } catch (e) {
      // Not reaching the hub at all says nothing about the sync: the rest of the page says so already.
      if (!live.current) return;
      if (e instanceof TypeError) setView((v) => (v === undefined ? null : v));
      else setLoadError(errorText(e));
    }
  }, [accept, live]);

  useEffect(() => {
    void load();
  }, [load, appID]);

  // A sync in progress: look again every few seconds while the page is shown, until none runs.
  const running = !!view?.bindings.some((b) => b.running);
  useEffect(() => {
    if (!running) return;
    let first = true;
    return visiblePoll(() => {
      if (first) return void (first = false);
      void load();
    }, RUNNING_POLL_MS, documentPage);
  }, [running, load]);

  /** A change: its answer is the new view. `what` names it in the error. */
  const change = async (what: string, key: string, call: () => Promise<LarkSyncView | null>, done?: string) => {
    const n = ++order.current.issued;
    setBusy(key);
    try {
      const next = await call();
      if (next) accept(n, next);
      setError("");
      if (done) toast(done, "ok");
      return true;
    } catch (e) {
      if (live.current) setError(t("settings-larksync.error_with", { what, error: errorText(e) }));
      return false;
    } finally {
      if (live.current) setBusy("");
    }
  };

  if (view === undefined && !loadError) return null;
  if (!view) {
    if (!loadError) return null;
    return (
      <Group title={t("settings-larksync.title")}>
        <RowFailed text={t("settings-larksync.load_failed", { error: loadError })} retry={t("settings-larksync.retry")} onRetry={() => void load()} />
      </Group>
    );
  }

  const bots = orderedBots(view);
  const primary = bots.find((b) => b.primary);
  const startAuthorize = (bot: string, tier: string, again = false) => setFlow({ phase: "authorize", bot, tier, again });
  // Upgrading an existing bot/binding to full_with_im: first add the IM scopes to the app (QR), then authorize again.
  const startUpgrade = async (bot: string, again: boolean) => {
    setBusy(`upgrade:${bot}`);
    try {
      const prompt = await addSyncScopes(bot, TIER_FULL);
      if (!live.current) return;
      if (prompt.done) startAuthorize(bot, TIER_FULL, again);
      else setFlow({ phase: "register", bot, tier: TIER_FULL, prompt, scopes: true, upgrade: true, again });
      setError("");
    } catch (e) {
      if (live.current) setError(t("settings-larksync.error_with", { what: t("settings-larksync.upgrade_failed"), error: errorText(e) }));
    } finally {
      if (live.current) setBusy("");
    }
  };
  // A failed registration starts again from the choice (or retries adding scopes when upgrading); a new bot that never got its app is not kept.
  const restartRegistration = async (f: Extract<Flow, { phase: "register" }>) => {
    if (f.upgrade) {
      await startUpgrade(f.bot, !!f.again);
      return;
    }
    setFlow({ phase: "choose" });
    if (f.scopes) return;
    try {
      accept(++order.current.issued, await removeSyncBot(f.bot));
    } catch {
      // still listed, with its own Remove
    }
  };
  // A sync bot whose setup was left half way: its QR step, asked again.
  const resume = async (b: LarkSyncBot) => {
    setBusy(`bot:${b.bot_id}`);
    try {
      const prompt = await registerSyncBot(b.bot_id, "register");
      const tier = b.tier || TIER_DOCS;
      if (prompt.done) {
        startAuthorize(b.bot_id, tier);
        await load();
      } else setFlow({ phase: "register", bot: b.bot_id, tier, prompt, scopes: false });
      setError("");
    } catch (e) {
      if (live.current) setError(t("settings-larksync.error_with", { what: t("settings-larksync.resume_failed"), error: errorText(e) }));
    } finally {
      if (live.current) setBusy("");
    }
  };
  const finished = async () => {
    setFlow(null);
    toast(t("settings-larksync.connected_toast"), "ok");
    await load();
  };

  return (
    <>
      <Group title={t("settings-larksync.title")} footer={t("settings-larksync.footer", { time: hourText(view.schedule_hour) })}>
        {bots.map((b) => (
          <BotBlock
            key={b.bot_id}
            view={view}
            bot={b}
            busy={busy}
            confirm={confirm}
            setConfirm={setConfirm}
            onConnect={() => startAuthorize(b.bot_id, b.tier || TIER_DOCS)}
            onUpgradeBot={() => void startUpgrade(b.bot_id, false)}
            onResume={() => void resume(b)}
            onRemoveBot={() => void change(t("settings-larksync.remove_bot_failed"), `bot:${b.bot_id}`, () => removeSyncBot(b.bot_id)).then(() => setConfirm(""))}
            onToggle={(x, on) => void change(on ? t("settings-larksync.enable_failed") : t("settings-larksync.disable_failed"), x.key, () => updateSyncBinding(x.key, { enabled: on }))}
            onRun={(x) => void change(t("settings-larksync.run_failed"), x.key, () => runSyncBinding(x.key), t("settings-larksync.run_started"))}
            onUpgrade={(x) => void startUpgrade(x.bot_id, true)}
            onReauthorize={(x) => startAuthorize(x.bot_id, x.tier || b.tier || TIER_DOCS, true)}
            onRemove={(x) => void change(t("settings-larksync.remove_failed"), x.key, () => removeSyncBinding(x.key)).then(() => setConfirm(""))}
          />
        ))}
        <Row label={t("settings-larksync.schedule_label")} hint={t("settings-larksync.schedule_hint")} wrapHint>
          <select
            value={view.schedule_hour}
            disabled={busy === "schedule"}
            aria-label={t("settings-larksync.schedule_label")}
            onChange={(e) => void change(t("settings-larksync.save_failed"), "schedule", () => setSyncSchedule(Number(e.target.value)))}
            className={cx(selectClass, "shrink-0")}
          >
            {Array.from({ length: 24 }, (_, h) => (
              <option key={h} value={h}>{hourText(h)}</option>
            ))}
          </select>
        </Row>
        {typeof view.im_poll === "boolean" && (
          <Row label={t("settings-larksync.im_poll_label")} hint={t("settings-larksync.im_poll_hint")} wrapHint>
            <Switch
              on={view.im_poll}
              disabled={busy === "im_poll"}
              label={t("settings-larksync.im_poll_label")}
              onChange={(on) => void change(t("settings-larksync.save_failed"), "im_poll", () => setSyncIMPoll(on))}
            />
          </Row>
        )}
        {!flow && (
          <div className="px-4 py-2.5">
            <Button tone="secondary" icon="plus" onClick={() => setFlow({ phase: "choose" })}>
              {t("settings-larksync.add")}
            </Button>
          </div>
        )}
        {error && <SettingsHint alert>{error}</SettingsHint>}
      </Group>

      {flow?.phase === "choose" && (
        <ChooseStep
          primary={primary}
          onCancel={() => setFlow(null)}
          onPrompt={(bot, tier, prompt, scopes) => (prompt.done ? startAuthorize(bot, tier) : setFlow({ phase: "register", bot, tier, prompt, scopes }))}
        />
      )}
      {flow?.phase === "register" && (
        <RegisterStep
          key={flow.bot}
          flow={flow}
          isPrimary={flow.bot === primary?.bot_id}
          onPrompt={(prompt) => setFlow((f) => (f?.phase === "register" ? { ...f, prompt } : f))}
          onDone={() => startAuthorize(flow.bot, flow.tier, !!flow.again)}
          onCancel={() => setFlow(null)}
          onRestart={() => void restartRegistration(flow)}
        />
      )}
      {flow?.phase === "authorize" && (
        <AuthorizeStep key={`${flow.bot}:${flow.tier}:${flow.again}`} flow={flow} setFlow={setFlow} onDone={finished} onCancel={() => setFlow(null)} />
      )}
    </>
  );
}

/** "03:00" in the page's language and clock. */
function hourText(h: number): string {
  return formatDate(new Date(2000, 0, 1, h), { hour: "numeric", minute: "2-digit" });
}

// ── the list ───────────────────────────────────────────

function BotBlock({ view, bot, busy, confirm, setConfirm, onConnect, onUpgradeBot, onResume, onRemoveBot, onToggle, onRun, onUpgrade, onReauthorize, onRemove }: {
  view: LarkSyncView;
  bot: LarkSyncBot;
  busy: string;
  confirm: string;
  setConfirm: (key: string) => void;
  onConnect: () => void;
  onUpgradeBot: () => void;
  onResume: () => void;
  onRemoveBot: () => void;
  onToggle: (b: LarkSyncBinding, on: boolean) => void;
  onRun: (b: LarkSyncBinding) => void;
  onUpgrade: (b: LarkSyncBinding) => void;
  onReauthorize: (b: LarkSyncBinding) => void;
  onRemove: (b: LarkSyncBinding) => void;
}) {
  const bindings = bindingsOf(view, bot.bot_id);
  const approval = approvalOf(bot);
  // No app yet: the chat bot's is created above (its own setup); a sync bot's setup was left half way, to go on with
  // while its registration lasts, or to remove.
  const ready = bot.has_secret !== false;
  const botWithoutChat = (bot.tier || TIER_DOCS) !== TIER_FULL;
  const upgradingBot = busy === `upgrade:${bot.bot_id}`;
  const hint = !ready
    ? bot.primary ? t("settings-larksync.chat_bot_first") : bot.registering ? t("settings-larksync.bot_setup_open") : t("settings-larksync.bot_unfinished")
    : bindings.length ? tierText(bot.tier) : t("settings-larksync.no_accounts");
  return (
    <>
      <Row icon={bot.primary ? "chat" : "link"} label={botTitle(bot)} hint={hint} wrapHint stackOnNarrow>
        {/* On a phone the controls go under the name, the row's width, and wrap there rather than run off the edge. */}
        <div className="flex min-w-0 flex-wrap items-center gap-2 max-sm:w-full">
          {ready && botWithoutChat && !bindings.length && <Pill tone="orange">{t("settings-larksync.no_chat_badge")}</Pill>}
          {approval && <Pill tone={approval.tone}>{approval.text}</Pill>}
          {ready && botWithoutChat && !bindings.length && (
            <Button tone="secondary" icon="plus" busy={upgradingBot} disabled={!!busy} onClick={onUpgradeBot}>
              {t("settings-larksync.upgrade_to_full")}
            </Button>
          )}
          {ready && (
            <Button tone="quiet" icon="plus" onClick={onConnect}>
              {t("settings-larksync.connect_account")}
            </Button>
          )}
          {!ready && !bot.primary && bot.registering && (
            <Button tone="quiet" icon="image" busy={busy === `bot:${bot.bot_id}`} onClick={onResume}>
              {t("settings-larksync.continue_setup")}
            </Button>
          )}
          {!bot.primary && (
            <RowConfirm
              open={confirm === `bot:${bot.bot_id}`}
              busy={busy === `bot:${bot.bot_id}`}
              confirm={t("settings-larksync.remove_bot")}
              onCancel={() => setConfirm("")}
              onConfirm={onRemoveBot}
              trigger={<IconButton icon="x" label={t("settings-larksync.remove_bot_named", { name: botTitle(bot) })} onClick={() => setConfirm(`bot:${bot.bot_id}`)} />}
            />
          )}
        </div>
      </Row>
      {bindings.map((b) => (
        <BindingRow
          key={b.key}
          view={view}
          b={b}
          botTier={bot.tier}
          busy={busy === b.key}
          upgrading={upgradingBot}
          anyBusy={!!busy}
          confirming={confirm === b.key}
          setConfirm={setConfirm}
          onToggle={(on) => onToggle(b, on)}
          onRun={() => onRun(b)}
          onUpgrade={() => onUpgrade(b)}
          onReauthorize={() => onReauthorize(b)}
          onRemove={() => onRemove(b)}
        />
      ))}
    </>
  );
}

function BindingRow({ view, b, botTier, busy, upgrading, anyBusy, confirming, setConfirm, onToggle, onRun, onUpgrade, onReauthorize, onRemove }: {
  view: LarkSyncView;
  b: LarkSyncBinding;
  botTier?: string;
  busy: boolean;
  upgrading: boolean;
  anyBusy: boolean;
  confirming: boolean;
  setConfirm: (key: string) => void;
  onToggle: (on: boolean) => void;
  onRun: () => void;
  onUpgrade: () => void;
  onReauthorize: () => void;
  onRemove: () => void;
}) {
  const name = bindingTitle(view, b);
  const state = healthOf(b);
  const soon = reauthSoon(b);
  const authorize = needsAuthorize(b);
  const withoutChat = (b.tier || botTier || TIER_DOCS) !== TIER_FULL;
  return (
    <div data-lark-sync-binding={b.key}>
      <Row label={name} hint={statusLine(b)} wrapHint stackOnNarrow>
        {withoutChat && <Pill tone="orange">{t("settings-larksync.no_chat_badge")}</Pill>}
        <Pill tone={state.tone}>{state.text}</Pill>
        <Switch on={b.enabled} disabled={busy || (authorize && b.health !== "scope_missing")} label={t("settings-larksync.enabled_label", { name })} onChange={onToggle} />
      </Row>
      {b.error && !b.running && <SettingsHint caution>{b.error}</SettingsHint>}
      {soon !== null && <SettingsHint caution>{t("settings-larksync.reauth_soon", { count: soon })}</SettingsHint>}
      <div className="flex flex-wrap items-center gap-2 px-4 pb-2.5">
        {(authorize || soon !== null) && (
          <Button tone={authorize ? "accent" : "secondary"} icon="key" onClick={onReauthorize}>
            {t("settings-larksync.authorize_again")}
          </Button>
        )}
        {!authorize && (
          <Button tone="secondary" icon="retry" busy={busy && !confirming} disabled={!b.enabled || !!b.running} onClick={onRun}>
            {b.running ? t("settings-larksync.running") : t("settings-larksync.run_now")}
          </Button>
        )}
        {withoutChat && (
          <Button tone="secondary" icon="plus" busy={upgrading} disabled={anyBusy} onClick={onUpgrade}>
            {t("settings-larksync.upgrade_to_full")}
          </Button>
        )}
        <RowConfirm
          open={confirming}
          busy={busy && confirming}
          confirm={t("settings-larksync.remove")}
          onCancel={() => setConfirm("")}
          onConfirm={onRemove}
          trigger={
            <Button tone="quiet" onClick={() => setConfirm(b.key)}>
              {t("settings-larksync.remove")}
            </Button>
          }
        />
      </div>
    </div>
  );
}

/** "Synced 3 hours ago · 12 messages · 4 documents · Next: 03:17", only the parts the hub knows. */
function statusLine(b: LarkSyncBinding): string {
  const parts: string[] = [];
  const last = ago(b.last_run_at);
  parts.push(last ? t("settings-larksync.last_sync", { when: last }) : t("settings-larksync.never_synced"));
  if (last) {
    parts.push(t("settings-larksync.count_messages", { count: b.last_messages ?? 0 }));
    parts.push(t("settings-larksync.count_docs", { count: b.last_docs ?? 0 }));
  }
  const next = b.enabled && !needsAuthorize(b) ? clock(b.next_run_at) : "";
  if (next) parts.push(t("settings-larksync.next_sync", { when: next }));
  return parts.join(" · ");
}

// ── adding: step 1 ──────────────────────────────────────

function ChooseStep({ primary, onCancel, onPrompt }: {
  primary?: LarkSyncBot;
  onCancel: () => void;
  onPrompt: (bot: string, tier: string, prompt: ConnectorPrompt, scopes: boolean) => void;
}) {
  const [tier, setTier] = useState(TIER_FULL);
  const [label, setLabel] = useState("");
  const [region, setRegion] = useState(() => (primary?.domain?.includes("larksuite") ? "lark" : "feishu"));
  const [busy, setBusy] = useState<"" | "new" | "chat">("");
  const [error, setError] = useState("");

  const start = async (which: "new" | "chat") => {
    setBusy(which);
    setError("");
    try {
      if (which === "new") {
        const r = await createSyncBot({ label: label.trim(), tier, region });
        onPrompt(r.bot_id, tier, r.prompt, false);
      } else if (primary) {
        onPrompt(primary.bot_id, tier, await addSyncScopes(primary.bot_id, tier), true);
      }
    } catch (e) {
      setError(errorText(e));
      setBusy("");
    }
  };

  return (
    <Group title={t("settings-larksync.add_title")} footer={t("settings-larksync.add_footer")}>
      <SettingsHint>{t("settings-larksync.add_intro")}</SettingsHint>
      <Row label={t("settings-larksync.tier_label")} hint={tier === TIER_DOCS ? t("settings-larksync.tier_docs_hint") : t("settings-larksync.tier_full_hint")} wrapHint stackOnNarrow>
        <select value={tier} aria-label={t("settings-larksync.tier_label")} onChange={(e) => setTier(e.target.value)} className={cx(selectClass, "shrink-0")}>
          {TIERS.map((x) => (
            <option key={x} value={x}>{tierText(x)}</option>
          ))}
        </select>
      </Row>
      {tier === TIER_DOCS && <SettingsHint caution>{t("settings-larksync.tier_docs_no_chat")}</SettingsHint>}
      <Row label={t("settings-larksync.name_label")} hint={t("settings-larksync.name_hint")} wrapHint stackOnNarrow>
        <input
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          aria-label={t("settings-larksync.name_label")}
          placeholder={t("settings-larksync.name_placeholder")}
          spellCheck={false}
          className={cx(settingsInputClass, "sm:w-56")}
        />
      </Row>
      <Row label={t("settings-larksync.region_label")}>
        <select value={region} aria-label={t("settings-larksync.region_label")} onChange={(e) => setRegion(e.target.value)} className={cx(selectClass, "shrink-0")}>
          <option value="feishu">{t("settings-larksync.region_feishu")}</option>
          <option value="lark">{t("settings-larksync.region_lark")}</option>
        </select>
      </Row>
      {error && <SettingsHint alert>{error}</SettingsHint>}
      <div className="flex flex-wrap items-center gap-2 px-4 py-2.5">
        <Button tone="accent" icon="image" busy={busy === "new"} disabled={!!busy} onClick={() => void start("new")}>
          {t("settings-larksync.create_bot")}
        </Button>
        {primary && primary.has_secret !== false && (
          <Button tone="secondary" busy={busy === "chat"} disabled={!!busy} onClick={() => void start("chat")}>
            {t("settings-larksync.use_chat_bot")}
          </Button>
        )}
        <Button tone="quiet" disabled={!!busy} onClick={onCancel}>
          {t("common.cancel")}
        </Button>
      </div>
    </Group>
  );
}

function RegisterStep({ flow, isPrimary, onPrompt, onDone, onCancel, onRestart }: {
  flow: Extract<Flow, { phase: "register" }>;
  isPrimary: boolean;
  onPrompt: (p: ConnectorPrompt) => void;
  onDone: () => void;
  onCancel: () => void;
  onRestart: () => void;
}) {
  const live = useLive();
  // Asked again every interval while a code or QR code is on screen, until the owner confirmed it in the app.
  const send = async (step: string): Promise<boolean> => {
    try {
      const p = await registerSyncBot(flow.bot, step);
      if (!live.current) return true;
      if (p.done) {
        onDone();
        return true;
      }
      onPrompt(p);
    } catch {
      // a missed poll: the next one asks again
    }
    return false;
  };
  // A failed registration (expired, declined) comes back with its error and stays so until started again: not asked again.
  useStepPolling(isPolled(flow.prompt) && !flow.prompt.error ? flow.prompt : null, send, 3);
  const title = !flow.scopes
    ? t("settings-larksync.step_create")
    : isPrimary ? t("settings-larksync.step_scopes") : t("settings-larksync.step_bot_scopes");
  return (
    <Group title={title} footer={t("settings-larksync.step_create_footer")}>
      <PromptBody prompt={flow.prompt} onCancel={onCancel} onRestart={onRestart} />
    </Group>
  );
}

// ── adding: step 2 ──────────────────────────────────────

function AuthorizeStep({ flow, setFlow, onDone, onCancel }: {
  flow: Extract<Flow, { phase: "authorize" }>;
  setFlow: React.Dispatch<React.SetStateAction<Flow | null>>;
  onDone: () => Promise<void>;
  onCancel: () => void;
}) {
  const live = useLive();
  const [busy, setBusy] = useState(false);
  // Only the latest request's answer counts: each one prepares a new authorization and retires the one before.
  const asked = useRef(0);
  const patch = useCallback(
    (p: Partial<Extract<Flow, { phase: "authorize" }>>) => setFlow((f) => (f?.phase === "authorize" && f.bot === flow.bot ? { ...f, ...p } : f)),
    [setFlow, flow.bot],
  );

  const ask = useCallback(async () => {
    const n = ++asked.current;
    const current = () => live.current && n === asked.current;
    setBusy(true);
    try {
      const answer = await authorizeSyncBot(flow.bot, oauthRedirectURI(), flow.tier);
      if (current()) patch({ answer, prompt: answer.prompt, error: undefined });
    } catch (e) {
      if (current()) patch({ error: errorText(e) });
    } finally {
      if (current()) setBusy(false);
    }
  }, [flow.bot, flow.tier, live, patch]);

  // The hub prepares the authorization as soon as the step opens: the answer says which way it goes.
  useEffect(() => {
    void ask();
  }, [ask]);

  // The device flow's poll answers only how it went: done, a failure (the authorization under way is dropped: start
  // again), or still waiting, maybe asked to wait longer. The QR code on screen stays the first answer's.
  const device = flow.answer?.mode === "device" ? flow.prompt : undefined;
  useStepPolling(device && isPolled(device) && !device.error ? device : null, async () => {
    try {
      const p = await pollSyncAuthorize(flow.bot);
      if (!live.current) return true;
      if (p.done) {
        await onDone();
        return true;
      }
      if (p.error) {
        patch({ prompt: { ...device, error: p.error } });
        return true;
      }
      if (p.interval && p.interval !== device?.interval) {
        patch({ prompt: { ...device, interval: p.interval } });
        return true;
      }
    } catch {
      // a missed poll: the next one asks again
    }
    return false;
  }, 3);

  const go = () => {
    const raw = flow.answer?.url;
    if (!raw) return void ask();
    const url = safeWebURL(raw, true);
    if (!url) return;
    rememberReturn();
    leaveFor(url);
  };

  const title = flow.again ? t("settings-larksync.authorize_title") : t("settings-larksync.step_authorize");
  const answer = flow.answer;
  return (
    <Group title={title}>
      {flow.error ? (
        <>
          <SettingsHint alert>{flow.error}</SettingsHint>
          <div className="flex items-center gap-2 px-4 pb-2.5">
            <Button tone="secondary" icon="retry" busy={busy} onClick={() => void ask()}>{t("settings-larksync.retry")}</Button>
            <Button tone="quiet" onClick={onCancel}>{t("common.cancel")}</Button>
          </div>
        </>
      ) : !answer ? (
        <p className="flex items-center gap-2 px-4 py-3 text-meta text-ink-3">
          <Spinner size={11} />
          {t("settings-larksync.authorize_preparing")}
        </p>
      ) : answer.mode === "device" && flow.prompt ? (
        <>
          <SettingsHint>{t("settings-larksync.authorize_device")}</SettingsHint>
          <PromptBody prompt={flow.prompt} onCancel={onCancel} onRestart={() => void ask()} />
        </>
      ) : answer.mode === "manual" ? (
        <ManualStep answer={answer} busy={busy} onContinue={go} onCancel={onCancel} />
      ) : (
        <>
          <SettingsHint>{t("settings-larksync.authorize_browser")}</SettingsHint>
          <div className="flex flex-wrap items-center gap-2 px-4 pb-2.5">
            <Button tone="accent" icon="logout" busy={busy} onClick={go}>{t("settings-larksync.authorize_continue")}</Button>
            <Button tone="quiet" onClick={onCancel}>{t("common.cancel")}</Button>
          </div>
        </>
      )}
    </Group>
  );
}

/** The app could not be set up for the return address: the owner adds it in the app's settings, then goes on. */
function ManualStep({ answer, busy, onContinue, onCancel }: { answer: LarkSyncAuthorize; busy: boolean; onContinue: () => void; onCancel: () => void }) {
  const uri = answer.redirect_uri || oauthRedirectURI();
  const consoleURL = safeWebURL(answer.console_url, true);
  return (
    <>
      <SettingsHint>{t("settings-larksync.manual_intro")}</SettingsHint>
      <div className="flex items-center gap-2 px-4 pb-2.5">
        <span data-lark-sync-redirect className="min-w-0 flex-1 truncate font-mono text-label text-ink select-all">{uri}</span>
        <IconButton
          icon="copy"
          label={t("settings-larksync.copy_address")}
          onClick={() => void copyText(uri, { done: t("settings-larksync.copied"), failed: t("settings-larksync.copy_failed") })}
        />
      </div>
      <div className="flex flex-wrap items-center gap-2 px-4 pb-2.5">
        {consoleURL && (
          <Button tone="secondary" icon="link" href={consoleURL}>{t("settings-larksync.open_app_settings")}</Button>
        )}
        <Button tone="accent" busy={busy} onClick={onContinue}>{t("settings-larksync.manual_continue")}</Button>
        <Button tone="quiet" onClick={onCancel}>{t("common.cancel")}</Button>
      </div>
    </>
  );
}

/** A step's prompt from the hub: its words, then the QR code, the link it encodes, or a code to enter. */
function PromptBody({ prompt, onCancel, onRestart }: { prompt: ConnectorPrompt; onCancel: () => void; onRestart: () => void }) {
  const app = t("settings-larksync.lark_app");
  const code = prompt.user_code;
  const promptLink = safeWebURL(prompt.link, true);
  const promptURL = safeWebURL(prompt.url, true);
  // A terminal failure (expired, declined): said, with a way to start again; the old QR code is no use any more.
  if (prompt.error) {
    return (
      <div className="flex flex-col gap-2.5 px-4 py-3">
        <p role="alert" className="text-label text-red">{prompt.error}</p>
        <div className="flex flex-wrap items-center gap-2">
          <Button tone="secondary" icon="retry" onClick={onRestart}>{t("settings-larksync.start_again")}</Button>
          <Button tone="quiet" onClick={onCancel}>{t("common.cancel")}</Button>
        </div>
      </div>
    );
  }
  return (
    <>
      {prompt.hint && (
        <div className="flex flex-col gap-1 px-4 py-2.5">
          <p className="text-meta leading-relaxed text-ink-2">{prompt.hint}</p>
        </div>
      )}
      {prompt.kind === "qr" && prompt.qr?.rows.length ? (
        <>
          {(code || promptLink) && (
            // on the device that shows the code, scanning it is not possible: the link (and the code it asks for) instead
            <div className="flex flex-wrap items-center gap-2 px-4 pt-1">
              {code && (
                <>
                  <span className="font-mono text-card-title font-semibold tracking-widest text-ink select-all">{code}</span>
                  <IconButton icon="copy" label={t("settings-larksync.copy_code")} onClick={() => void copyText(code, { done: t("settings-larksync.copied"), failed: t("settings-larksync.copy_failed") })} />
                </>
              )}
              {promptLink && (
                <Button tone="secondary" icon="link" href={promptLink}>{prompt.link_text || t("settings-larksync.open_link")}</Button>
              )}
            </div>
          )}
          <QRStep rows={prompt.qr.rows} app={app} label={t("settings-larksync.qr_label")} onCancel={onCancel} />
        </>
      ) : prompt.kind === "qr" && prompt.url ? (
        <QRLinkStep url={prompt.url} app={app} onCancel={onCancel} />
      ) : (prompt.kind === "device_code" || prompt.kind === "pair_code") && code ? (
        <div className="flex flex-col gap-3 px-4 py-3">
          <div className="flex items-center gap-3">
            <span className="min-w-0 flex-1 truncate font-mono text-card-title font-semibold tracking-widest text-ink select-all">{code}</span>
            <IconButton icon="copy" label={t("settings-larksync.copy_code")} onClick={() => void copyText(code, { done: t("settings-larksync.copied"), failed: t("settings-larksync.copy_failed") })} />
          </div>
          {promptURL && (
            <Button tone="secondary" icon="link" href={promptURL} className="self-start">{t("settings-larksync.open_link")}</Button>
          )}
          <p className="flex items-center gap-2 text-meta text-ink-3">
            <Spinner size={11} />
            {t("settings-larksync.waiting_confirm")}
          </p>
          <div>
            <Button tone="quiet" onClick={onCancel} className="-ml-2">{t("common.cancel")}</Button>
          </div>
        </div>
      ) : (
        <UnsupportedStep onRestart={onRestart} onCancel={onCancel} />
      )}
    </>
  );
}

function useLive() {
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => void (live.current = false);
  }, []);
  return live;
}
