// Settings › Channels (docs/extensions/channels.md "UI", iOS ChannelsView): every way of talking to Persona, connected or not, and
// each one's page — its switch, binding a step at a time (a QR code to scan in the chat app, a pairing code to send the
// bot, credentials), where its messages go, what it does, a test message to the owner — and, for a market's, the way
// to install it. Read-only ones (the apps, /agent, voice) say where they are set up.
import { OwnerMark } from "./OwnerMark";
import { Suspense, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import {
  bindChannel, bindTitle, useChannels, canBind, capabilityText, channelSections, channelStateText, channelSubtitle,
  channelTone, forgetChannels, isBound, isPolled, listChannels, targetText,
  type Channel, type ChannelPrompt, type ChannelTarget, type ChannelTargetOverride, type ChannelTone,
} from "../../lib/channels";
import { canSendStep, fieldInput } from "../../lib/connectors";
import { ConnectorExternalSourceSettings } from "../../features/external-sources/ExternalSourceSettingsEntry";
import { lazyPage } from "../../lib/lazy-page";
import { otherManager, ownedSub, useChosenManager } from "../../lib/manager-choice";
import { isOpenBeside } from "./open-beside";
import { ago, safeWebURL } from "../../lib/format";
import { useStore } from "../../lib/store";
import { Button, copyText, cx, Dialog, EASE, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { ChannelMark } from "../../ui/ChannelMark";
import { Icon } from "../../ui/icons";
import { QRStep, useStepPolling } from "./LoginSteps";
import { Group, Pill, Row, SettingsHint, Switch } from "./SettingsParts";
import type { ChannelController } from "./useChannelController";
import { byTime } from "../../lib/times";

const DOT: Record<ChannelTone, string> = { green: "bg-green", red: "bg-red", idle: "bg-line-strong" };
const fieldClass = "h-8 w-full rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

function targetOverrideText(override: ChannelTargetOverride): string {
  switch (override.mode) {
    case "groups_and_topics":
      return t("settings-channels.target_override_groups_and_topics");
    case "topics":
      return t("settings-channels.target_override_topics");
    case "threads":
      return t("settings-channels.target_override_threads");
    default:
      return t("settings-channels.target_override_generic");
  }
}

/** `compact`: under the name in the narrow column beside an open channel, in the size the other lists use there. */
export function ChannelStateInline({ c, compact }: { c: Channel; compact?: boolean }) {
  return (
    <span className={cx("flex items-center gap-1.5 whitespace-nowrap", compact ? "min-w-0 text-submeta" : "shrink-0 text-label", c.state === "error" ? "text-red" : "text-ink-2")}>
      <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", DOT[channelTone(c)])} />
      <span className={cx(compact && "truncate")}>{channelStateText(c)}</span>
    </span>
  );
}

// ── the links from Settings › Connectors ─────────────────

/** Another manager's channels (its extension's page links to its own), read with its name; [] until they are. */
function useOtherChannels(manager: string | undefined, has: boolean): Channel[] {
  const [list, setList] = useState<Channel[]>([]);
  useEffect(() => {
    setList([]);
    if (!manager || !has) return;
    let live = true;
    listChannels(manager).then((l) => live && setList(l), () => {});
    return () => void (live = false);
  }, [manager, has]);
  return list;
}

/** On a connector's page (WeChat's, the Feishu bot's): it is a way of talking to Persona, set up under "Channels". Only for a
 * channel there is, on a hub that has them. `manager`: whose connector the page is (another manager's channel is its
 * own, docs/assistant/managers.md "Clients"); none: the default's. */
export function ChannelLink({ id, manager, onOpen }: { id: string; manager?: string; onOpen: (id: string, manager?: string) => void }) {
  const has = useStore((s) => s.features.includes("channels"));
  const other = otherManager(manager);
  const own = useChannels();
  const theirs = useOtherChannels(other, has);
  const list = other ? theirs : own;
  const c = list.find((x) => x.id === id);
  if (!has || !c) return null;
  return (
    <Group footer={t("settings-channels.link_footer", { name: c.name })}>
      <Row
        label={
          <span className="flex items-center gap-2">
            <ChannelMark c={c} size={18} />
            {t("settings-channels.link_label", { name: c.name })}
          </span>
        }
        onClick={() => (other ? onOpen(id, other) : onOpen(id))}
      />
    </Group>
  );
}

// ── the list ─────────────────────────────────────────────

/** `openBeside`: the channel open beside the list (Settings' two columns): its row is marked current, and every row puts
 *  its state under its name, so the narrow column shows the name whole. */
export function ChannelList({ onOpen, onMarketItem, openBeside, manager }: { onOpen: (id: string) => void; onMarketItem: (market: string, plugin: string) => void; openBeside?: string | null; manager?: string }) {
  // the current assistant's channels (docs/assistant/managers.md "The current assistant"), asked of it by name (none for
  // the default); a row opens its copy (`<id>@<manager>`)
  const chosen = useChosenManager();
  const picked = manager ?? chosen;
  const owner = manager ?? otherManager(chosen ?? undefined);
  const [list, setList] = useState<Channel[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const load = () => {
    setLoading(true);
    listChannels(owner)
      .then((l) => (setList(l), setError("")))
      .catch((e) => setError(errorText(e)))
      .finally(() => setLoading(false));
  };
  useEffect(load, []);
  const sections = useMemo(() => channelSections(list ?? []), [list]);

  return (
    <>
      <div className="mb-3 flex items-center gap-2">
        <span className="flex-1" />
        <IconButton icon="retry" label={t("settings-channels.refresh_channels")} onClick={load} disabled={loading} />
      </div>
      <h1 className="mb-1 px-1 text-[17px] font-semibold text-ink">{t("settings-channels.title")}</h1>
      <p className="mb-5 px-1 text-[12.5px] leading-relaxed text-ink-3">
        {t("settings-channels.intro")}
      </p>
      {!list && !error && (
        <Group>
          <div className="px-4 py-3">
            <Loading />
          </div>
        </Group>
      )}
      {error && (
        <Group>
          <div className="flex items-center gap-3 px-4 py-3">
            <p className="min-w-0 flex-1 text-[12.5px] break-words text-red">{t("settings-channels.load_failed", { error })}</p>
            <Button tone="secondary" icon="retry" onClick={load} busy={loading}>{t("settings-channels.retry")}</Button>
          </div>
        </Group>
      )}
      {sections.map((s) => (
        <Group key={s.title} title={s.title} footer={s.footer}>
          {s.channels.map((c, i) => {
            const compact = openBeside !== undefined;
            // a route without @manager opens the current assistant's copy, this row's
            const current = c.source !== "market" && isOpenBeside(openBeside, c.id, owner, picked);
            const state = c.source === "market" ? (
              <span className={cx("shrink-0 whitespace-nowrap text-accent-ink", compact ? "text-submeta" : "text-[12px]")}>{t("settings-channels.install")}</span>
            ) : (
              c.kind !== "app" && <ChannelStateInline c={c} compact={compact} />
            );
            return (
              <button
                key={c.id}
                type="button"
                aria-current={current || undefined}
                onClick={() => (c.source === "market" && c.market && c.plugin ? onMarketItem(c.market, c.plugin) : onOpen(ownedSub(c.id, owner)))}
                className={cx("flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors", current ? "bg-accent-tint" : "hover:bg-hover")}
                style={{ animation: `fade-up 320ms ${EASE} ${Math.min(i, 8) * 30}ms both` }}
              >
                <ChannelMark c={c} size={32} />
                <div className="min-w-0 flex-1">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-[13px] text-ink">{c.name}</span>
                    {c.source === "plugin" && c.kind === "im" && <Pill>{t("settings-channels.extension")}</Pill>}
                  </div>
                  {channelSubtitle(c) && (
                    <div className={cx("mt-0.5 truncate text-[11.5px]", c.state === "error" ? "text-red" : "text-ink-3")}>{channelSubtitle(c)}</div>
                  )}
                  {compact && state && <div className="mt-0.5 flex min-w-0">{state}</div>}
                </div>
                {!compact && state}
                <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
              </button>
            );
          })}
        </Group>
      ))}
    </>
  );
}

// ── one channel ──────────────────────────────────────────

/** Binding card rendered for editable channels that bind directly (used by ChannelPage and IntegrationDetail). */
export function ChannelBindingBlock({ ctl, showAccountMark = true }: { ctl: ChannelController; showAccountMark?: boolean }) {
  const [unbinding, setUnbinding] = useState(false);
  const d = ctl.detail;
  useEffect(() => {
    setUnbinding(false);
  }, [ctl.resourceKey]);
  if (!d) return null;
  const editable = !d.read_only && d.source !== "market";
  if (!editable) return null;

  return (
    <>
      {canBind(d) && <BindSteps key={ctl.resourceKey} c={d} bind={ctl.bind} onDone={ctl.load} />}
      {isBound(d) && (
        <Group title={t("settings-channels.binding")} footer={t("settings-channels.binding_footer")}>
          <Row label={t("settings-channels.bound_account")}>
            {showAccountMark ? (
              <span className="flex items-center gap-2">
                <ChannelMark c={d} size={18} />
                <span className="truncate">{d.account?.name ?? t("settings-channels.bound")}</span>
              </span>
            ) : (
              d.account?.name ?? t("settings-channels.bound")
            )}
          </Row>
          <div className="px-4 py-2.5">
            <Button tone="quiet" icon="logout" onClick={() => setUnbinding(true)} disabled={!!ctl.busy} className="-ml-2 text-red">
              {t("settings-channels.unbind")}
            </Button>
          </div>
        </Group>
      )}
      <Dialog
        open={unbinding}
        onClose={() => setUnbinding(false)}
        title={t("settings-channels.unbind_title", { name: d.name })}
        footer={
          <>
            <Button tone="quiet" onClick={() => setUnbinding(false)}>{t("common.cancel")}</Button>
            <Button
              tone="danger"
              busy={ctl.busy === "unbind"}
              disabled={!!ctl.busy && ctl.busy !== "unbind"}
              onClick={() =>
                void ctl.unbind().then((ok) => {
                  if (!ok) return;
                  setUnbinding(false);
                  toast(t("settings-channels.unbound"), "ok");
                })
              }
            >
              {t("settings-channels.unbind")}
            </Button>
          </>
        }
      >
        <p className="px-5 py-4 text-meta leading-relaxed text-ink-2">{t("settings-channels.unbind_body", { name: d.name })}</p>
      </Dialog>
    </>
  );
}

/** The default destination and any active routing override. */
export function ChannelTargetControls({ ctl, onSettings }: { ctl: ChannelController; onSettings?: (url: string) => void }) {
  const [picking, setPicking] = useState(false);
  const d = ctl.detail;
  useEffect(() => {
    setPicking(false);
  }, [ctl.resourceKey]);
  if (!d) return null;

  const editable = !d.read_only && d.source !== "market";
  return (
    <>
      {editable && d.target && (
        <Group title={t("settings-channels.target_title")} footer={t("settings-channels.target_footer")}>
          <Row label={t("settings-channels.target_row")} onClick={() => !ctl.busy && setPicking(true)}>
            <span className="max-w-[220px] truncate">{targetText(d.target)}</span>
          </Row>
          {d.target_override && (
            <SettingsHint>
              {targetOverrideText(d.target_override)}
              {d.settings_url && onSettings && (
                <>
                  {" "}
                  <button
                    type="button"
                    onClick={() => onSettings(d.settings_url!)}
                    className="font-medium whitespace-nowrap text-accent-ink hover:underline"
                  >
                    {d.source === "plugin"
                      ? t("settings-channels.target_override_extension_settings")
                      : t("settings-channels.target_override_settings")}
                  </button>
                </>
              )}
            </SettingsHint>
          )}
        </Group>
      )}
      <TargetPicker
        open={picking}
        current={d.target}
        onClose={() => setPicking(false)}
        onPick={(target) => {
          setPicking(false);
          void ctl.change("target", { target });
        }}
      />
    </>
  );
}

/** Shared channel controls: error alert, enable switch, target picker, features, recent activity, capabilities, and test action. */
export function ChannelDeliveryControls({
  ctl,
  onSettings,
  afterEnable,
  afterTarget,
}: {
  ctl: ChannelController;
  onSettings?: (url: string) => void;
  afterEnable?: ReactNode;
  afterTarget?: ReactNode;
}) {
  const d = ctl.detail;
  if (!d) return null;

  const editable = !d.read_only && d.source !== "market";
  const replyOnly = !!d.capabilities.reply_only;

  return (
    <>
      {d.state === "error" && d.error && (
        <div role="alert" className="mb-4 flex items-start gap-2 rounded-card bg-red-tint px-3.5 py-2.5 text-meta text-red">
          <Icon name="info" size={14} className="mt-px shrink-0" />
          <span className="min-w-0 break-words">{d.error}</span>
        </div>
      )}

      {editable && (
        <Group footer={d.source === "plugin" ? t("settings-channels.disable_footer") : undefined}>
          <Row label={t("settings-channels.enable", { name: d.name })}>
            <Switch
              on={d.enabled}
              onChange={(on) => void ctl.change("enabled", { enabled: on })}
              disabled={!!ctl.busy}
              label={t("settings-channels.enable", { name: d.name })}
            />
          </Row>
        </Group>
      )}

      {afterEnable}

      <ChannelTargetControls ctl={ctl} onSettings={onSettings} />

      {afterTarget}

      {editable && (d.features?.length ?? 0) > 0 && (
        <Group title={t("settings-channels.features")}>
          {d.features!.map((f) => (
            <Row key={f.id} label={f.title} hint={f.description} wrapHint>
              <Switch
                on={f.on}
                onChange={(on) => void ctl.change(`f:${f.id}`, { features: { [f.id]: on } })}
                disabled={!!ctl.busy}
                label={f.title}
              />
            </Row>
          ))}
        </Group>
      )}

      {(d.waiting.answers > 0 || d.waiting.asks > 0 || (d.conversations?.length ?? 0) > 0) && (
        <Group title={t("settings-channels.recent")}>
          {(d.waiting.answers > 0 || d.waiting.asks > 0) && (
            <Row label={t("settings-channels.waiting")}>
              {[
                d.waiting.answers > 0 && t("settings-channels.waiting_answers", { count: d.waiting.answers }),
                d.waiting.asks > 0 && t("settings-channels.waiting_asks", { count: d.waiting.asks }),
              ].filter(Boolean).join(" · ")}
            </Row>
          )}
          {d.conversations?.map((cv) => (
            <Row
              key={cv.session_id}
              label={cv.title || t("settings-channels.a_chat")}
              hint={t("settings-channels.chat_hint", { when: ago(cv.at) })}
            />
          ))}
        </Group>
      )}

      <Group title={t("settings-channels.capabilities")}>
        {capabilityText(d.capabilities).map((cap) => (
          <div key={cap} className="flex items-center gap-2 px-4 py-2 text-meta text-ink-2">
            <Icon name="check" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
            {cap}
          </div>
        ))}
        {d.surfaces && d.surfaces.length > 0 && (
          <Row label={t("settings-channels.surfaces")}>{d.surfaces.join(t("settings-channels.list_separator"))}</Row>
        )}
      </Group>

      {editable && d.state === "connected" && (d.capabilities.push || replyOnly) && (
        <Group footer={replyOnly ? t("settings-channels.check_connection_footer") : t("settings-channels.test_footer")}>
          <div className="px-4 py-2.5">
            <Button
              tone="secondary"
              icon={replyOnly ? "check" : "send"}
              busy={ctl.busy === "test"}
              disabled={!!ctl.busy && ctl.busy !== "test"}
              onClick={() => void ctl.runTest()}
            >
              {replyOnly ? t("settings-channels.check_connection") : t("settings-channels.send_test")}
            </Button>
            {ctl.testFeedback && (
              <p
                role={ctl.testFeedback.tone === "error" ? "alert" : "status"}
                className={cx("mt-2 text-submeta break-words", ctl.testFeedback.tone === "error" ? "text-red" : "text-ink-2")}
              >
                {ctl.testFeedback.text}
              </p>
            )}
          </div>
        </Group>
      )}
    </>
  );
}

/** `manager`: whose channel (another manager's, opened from its extension's page): the page's requests name it while it
 *  is open; none: the current assistant's. */
/** `headerBack`: the page's top bar has the way back (a phone), so it draws no Back of its own. */
export type ChannelPageProps = { id: string; manager?: string; onBack: () => void; onSettings: (url: string) => void; headerBack?: boolean };

const ChannelResource = lazyPage(() => import("./ChannelResource").then((m) => m.ChannelResource));

export function ChannelPage({ id, manager: named, ...props }: ChannelPageProps) {
  // a route that names none (written by hand, or before several managers): the current assistant's, as for an extension
  const chosen = useChosenManager();
  const manager = named ?? otherManager(chosen ?? undefined);
  return (
    <Suspense fallback={<ChannelPageContent {...props} manager={manager} ctl={null} />}>
      <ChannelResource {...props} id={id} manager={manager} />
    </Suspense>
  );
}

export function ChannelPageContent({ ctl, manager, onBack, onSettings, headerBack }: Omit<ChannelPageProps, "id"> & { ctl: ChannelController | null }) {
  const d = ctl?.detail;

  const head = (
    <div className="mb-3 flex items-center gap-2">
      {!headerBack && <Button tone="quiet" icon="chevronLeft" onClick={onBack} className="-ml-2">{t("settings-channels.title")}</Button>}
      <OwnerMark manager={manager} />
      <span className="flex-1" />
      <IconButton icon="retry" label={t("settings-channels.refresh")} onClick={() => void ctl?.load()} />
    </div>
  );
  if (!ctl || !d) {
    return (
      <>
        {head}
        <Group>
          <div className="px-4 py-3">{ctl?.error ? <p className="text-meta text-red">{t("settings-channels.channel_load_failed", { error: ctl.error })}</p> : <Loading />}</div>
        </Group>
      </>
    );
  }
  return (
    <>
      {head}
      <div className="mb-5 flex items-center gap-3 px-1">
        <ChannelMark c={d} size={44} />
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-[17px] font-semibold text-ink">{d.name}</h1>
          <div className="mt-0.5 flex min-w-0 items-center gap-2 text-label text-ink-3">
            {d.kind !== "app" && <ChannelStateInline c={d} />}
            {d.account?.name && <span className="truncate">{d.account.name}</span>}
            {d.last_event_at && <span className="shrink-0">{t("settings-channels.last_event", { when: ago(d.last_event_at) })}</span>}
          </div>
        </div>
      </div>
      {d.description && <p className="-mt-2 mb-5 px-1 text-meta leading-relaxed text-ink-3">{d.description}</p>}

      <ChannelDeliveryControls
        ctl={ctl}
        onSettings={onSettings}
        afterEnable={<ChannelBindingBlock ctl={ctl} showAccountMark={false} />}
      />

      {d.settings_url && (
        <Group footer={d.read_only ? t("settings-channels.settings_elsewhere") : undefined}>
          <Row label={t("settings-channels.more_settings")} onClick={() => onSettings(d.settings_url!)} />
        </Group>
      )}

      {d.installed && d.kind !== "app" && <ConnectorExternalSourceSettings ids={[d.id]} manager={manager} />}
    </>
  );
}

/** "Send messages to": the main conversation, a new one, or one of the assistant's conversations. */
export function TargetPicker({ open, current, onClose, onPick }: { open: boolean; current?: ChannelTarget; onClose: () => void; onPick: (t: ChannelTarget) => void }) {
  const sessions = useStore((s) => s.sessions);
  const conversations = useMemo(
    () =>
      Object.values(sessions)
        .flat()
        .filter((s) => s.assistant && !s.archived)
        .sort((a, b) => byTime(b.updated_at, a.updated_at))
        .slice(0, 12),
    [sessions],
  );
  const option = (key: string, label: string, hint: string | undefined, on: boolean, target: ChannelTarget) => (
    <button key={key} type="button" onClick={() => onPick(target)} className="flex w-full items-center gap-3 px-5 py-2.5 text-left transition-colors hover:bg-hover">
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] text-ink">{label}</div>
        {hint && <div className="mt-0.5 truncate text-[11.5px] text-ink-3">{hint}</div>}
      </div>
      {on && <Icon name="check" size={14} stroke={2.2} className="shrink-0 text-accent" />}
    </button>
  );
  return (
    <Dialog open={open} onClose={onClose} title={t("settings-channels.target_title")}>
      <div className="max-h-[60vh] divide-y divide-line overflow-y-auto">
        {option("main", t("settings-channels.main_chat"), t("settings-channels.main_chat_hint"), current?.kind === "main", { kind: "main" })}
        {option("new", t("settings-channels.new_chat"), t("settings-channels.new_chat_hint"), current?.kind === "new", { kind: "new" })}
        {conversations.map((s) =>
          option(s.session_id, s.title || t("settings-channels.a_chat"), s.last_text, current?.kind === "conversation" && current.session_id === s.session_id, {
            kind: "conversation",
            machine_id: s.machine_id,
            session_id: s.session_id,
          }),
        )}
      </div>
    </Dialog>
  );
}

// ── binding, a step at a time ────────────────────────────

/**
 * Binding `c` a step at a time. The Lark bot's page reuses it to create the bot's app by scanning (#5818), with its own
 * `title`, `footer` and `startLabel` for the first button; everything after the first press is the same steps.
 */
export function BindSteps({ c, bind, onDone, title, footer: startFooter, startLabel }: {
  c: Pick<Channel, "id" | "name" | "bind">;
  bind?: ChannelController["bind"];
  onDone: () => Promise<void>;
  title?: string;
  footer?: string;
  startLabel?: string;
}) {
  const [prompt, setPrompt] = useState<ChannelPrompt | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => void (live.current = false);
  }, []);

  /** True when binding is done. */
  const send = useCallback(async (step: string, vals: Record<string, string>, quiet = false): Promise<boolean> => {
    if (!quiet) setBusy(true);
    try {
      const p = await (bind ? bind(step, vals) : bindChannel(c.id, step, vals));
      if (!live.current) return false;
      if (p.done) {
        setPrompt(null);
        setValues({});
        forgetChannels();
        toast(p.account ? t("settings-channels.bound_account_toast", { account: p.account }) : t("settings-channels.bound_channel_toast", { name: c.name }), "ok");
        await onDone();
        return true;
      }
      setPrompt((cur) => {
        if (p.step !== cur?.step) setValues({});
        return p;
      });
    } catch (e) {
      if (live.current && !quiet) toast(errorText(e), "error");
    } finally {
      if (!quiet && live.current) setBusy(false);
    }
    return false;
  }, [bind, c.id, c.name, onDone]);

  // A QR code or a code on screen: ask again every interval until it is scanned / sent.
  useStepPolling(isPolled(prompt) ? prompt : null, (step) => send(step, {}, true), 3);

  const footer = startFooter ?? (c.bind === "qr" ? t("settings-channels.qr_footer", { name: c.name }) : undefined);
  if (!prompt) {
    return (
      <Group title={title ?? t("settings-channels.binding")} footer={footer ?? t("settings-channels.bind_footer")}>
        <div className="px-4 py-3">
          <Button tone="accent" icon={c.bind === "qr" || startLabel ? "image" : "link"} busy={busy} onClick={() => void send("", {})}>
            {startLabel ?? bindTitle(c)}
          </Button>
        </div>
      </Group>
    );
  }
  const canSend = canSendStep(prompt, values) && !busy;
  const promptLink = safeWebURL(prompt.link);
  return (
    <Group title={title ?? bindTitle(c)}>
      {(prompt.hint || prompt.error) && (
        <div className="flex flex-col gap-1 px-4 py-2.5">
          {prompt.hint && <p className="text-[12.5px] leading-relaxed text-ink-2">{prompt.hint}</p>}
          {prompt.error && <p role="alert" className="text-[12px] text-red">{prompt.error}</p>}
        </div>
      )}
      {promptLink && (
        <div className="px-4 pb-2.5 pt-0.5">
          <a
            href={promptLink}
            target="_blank"
            rel="noopener noreferrer"
            className="touch-hit inline-flex items-center gap-1 self-start whitespace-nowrap text-[12.5px] font-medium text-accent hover:underline"
          >
            {prompt.link_text || promptLink}
            <Icon name="logout" size={12} />
          </a>
        </div>
      )}
      {prompt.kind === "qr" && prompt.qr ? (
        <QRStep rows={prompt.qr.rows} app={c.name} label={t("settings-channels.qr_label")} onCancel={() => setPrompt(null)} />
      ) : prompt.kind === "pair_code" || prompt.kind === "device_code" ? (
        <div className="flex flex-col gap-3 px-4 py-3">
          <div className="flex items-center gap-3">
            <span className="min-w-0 flex-1 truncate font-mono text-[28px] font-semibold tracking-[0.18em] text-ink select-all">{prompt.user_code}</span>
            <IconButton
              icon="copy"
              label={t("settings-channels.copy_code")}
              onClick={() => void copyText(prompt.user_code ?? "", { done: t("settings-channels.copied"), failed: t("settings-channels.copy_failed") })}
            />
          </div>
          <p className="flex items-center gap-2 text-[12.5px] text-ink-3">
            <Spinner size={11} />
            {prompt.kind === "pair_code" ? t("settings-channels.waiting_pair", { name: c.name }) : t("settings-channels.waiting_auth")}
          </p>
          <div>
            <Button tone="quiet" onClick={() => setPrompt(null)} className="-ml-2">{t("common.cancel")}</Button>
          </div>
        </div>
      ) : (
        <form
          className="flex flex-col"
          onSubmit={(e) => {
            e.preventDefault();
            if (canSend) void send(prompt.step ?? "", values);
          }}
        >
          {(prompt.fields ?? []).map((f) => {
            const input = fieldInput(f);
            return (
              <label key={f.name} className="flex items-center gap-3 border-b border-line px-4 py-2">
                <span className="w-[96px] shrink-0 truncate text-[13px] text-ink">{f.label}</span>
                <input
                  value={values[f.name] ?? ""}
                  onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
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
            <Button type="submit" tone="accent" disabled={!canSend} busy={busy}>{busy ? t("settings-channels.verifying") : t("settings-channels.next")}</Button>
            <Button tone="quiet" onClick={() => (setPrompt(null), setValues({}))} disabled={busy}>{t("common.cancel")}</Button>
          </div>
        </form>
      )}
    </Group>
  );
}
