// Settings › Extensions: all sources use the host-owned layout in ExtensionSettingsPage.
// This controller owns manager routing, configuration drafts, authentication and mutation results;
// extensions supply only their configuration fields and custom content (docs/extensions/extension-settings.md).
import { OwnerMark } from "./OwnerMark";
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { currentListViewKey, listViewCache, useListViewKey } from "../../lib/list-view-cache";
import { enc, errorText, isAbort } from "../../lib/api";
import { OfflineState, useShownOffline } from "../ManagerParts";
import { defaultManager, otherManager, pinManagerAt, useChosenManager } from "../../lib/manager-choice";
import { useRoster } from "../../lib/managers";
import { isResumed } from "../../lib/reconnect";
import { onHubEvent, useStore } from "../../lib/store";
import { EXTENSIONS_CHANGED } from "../../lib/extension-prompts";
import { watchOperations } from "../../lib/extension-operations";
import { extensionsRoute, routeSub } from "../../lib/extension-market";
import { composing } from "../../lib/send-history";
import {
  asksFirst, choiceLabel, clearExtensionSecret, entryID, entryItems, entryKey, fieldLabel, fieldPath, fieldTitle, isEntryList, listItems, listExtensionsInfo, listSummary,
  placeholder, extensionDetail, extensionState, extensionTone, pointText, runNote, same, secretState, setupSecret, setContribution, setExtensionEnabled,
  channelOf, machineStatus, nodeProgramStatus, pickerChoices, sandboxState, saveFooter, setExtensionAutoUpgrade, setExtensionSecret, sourceText, stateLine, valueAt,
  removeHandoffRule, setExtensionSecretReference, permissionDetail, permissionTitle, type HandoffRule, type SecretUser,
  type HubExtension, type HubExtensionDetail, type Json, type JsonObject, type ExtensionField, type ExtensionNodeState, type ExtensionSandbox, type ExtensionSecret,
  type ExtensionPermission, type PickerLauncher,
} from "../../lib/extensions";
import { loadLaunchers } from "../../lib/launcher-list";
import { hubHasExt } from "../../lib/extension-api";
import { ExtensionOwnerScope } from "../../lib/extension-owner";
import { Button, cx, Dialog, EASE, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { TriageRuleField } from "../../ui/TriageRuleField";
import { ExtensionLogo } from "../../ui/ExtensionLogo";
import { ExtensionDownloads, packageDownloads } from "../../ui/ExtensionDownloads";
import { Icon } from "../../ui/icons";
import { ChannelLink } from "./Channels";
import { ConnectorExternalSourceSettings } from "../../features/external-sources/ExternalSourceSettingsEntry";
import { MarketCatalog, MarketItem, Markets, PackMemberRow, UninstallDialog, useInstalledNames } from "./ExtensionMarket";
import { ExtensionSettingsPage } from "./ExtensionSettingsPage";
import { ExtensionInfoAccess } from "./ExtensionInfoAccess";
import { ExtensionUI } from "./ExtensionView";
import { ExtensionConnectorInbox } from "./HubConnector";
import { listAuthNote, onAuthChange, shownPages, signInAsked, takeSignIn, withAuth } from "../../lib/extension-auth";
import { AuthGroup } from "./ExtensionAuth";
import { dependencyText, dependentError, type Contribution, type Dependent } from "../../lib/extension-model";
import { Group, Pill, Row, Switch, SettingsHint } from "./SettingsParts";
import { formatList, t } from "../../i18n";
import { tRich } from "../../i18n/rich";
import { ago } from "../../lib/format";
import { scoped, useScope } from "../../lib/current-assistant";
import { SubscriptionsEntry } from "../subscriptions/SubscriptionsEntry";
import { isOpenBeside } from "./open-beside";
import {
  dropGoneExtensionDrafts, extensionDraftKey, useExtensionBinding, useExtensionConfiguration, useUnsavedExtensions,
  type ExtensionBinding, type ExtensionMutation, type Typed,
} from "./integrations/extensionDrafts";

/**
 * The tab's pages, by its `extension` parameter (lib/extension-market.ts extensionsRoute): the list, an extension, a page an extension
 * draws, and the market (its catalog, one extension of it, the markets).
 */
export function Extensions({ open, onOpen, onBack, onChannel, onConnector, headerBack, openBeside, onPageWide, prefill, manager: scope }: {
  open: string | null;
  onOpen: (id: string | null) => void;
  onBack?: () => void;
  manager?: string;
  /** The page's top bar has the way back (a phone, Settings.tsx itemParent): an extension's page, a page it draws, the
   *  market's catalog, one of its extensions and the list of markets draw no Back of their own. */
  headerBack?: boolean;
  /** The extension open beside the list (Settings' two columns), whose row the list marks. */
  openBeside?: string | null;
  /** `manager`: another manager's channel / connector (its extension's page links to its own); none: the default's. */
  onChannel?: (id: string, manager?: string) => void;
  onConnector?: (id: string, manager?: string) => void;
  onPageWide?: (wide: boolean) => void;
  /** Proposed ordinary configuration values from an extension page's settings link. */
  prefill?: JsonObject;
}) {
  const r = extensionsRoute(open);
  const go = (to: Parameters<typeof routeSub>[0]) => onOpen(routeSub(to));
  // what is installed from a market goes to the current assistant (none: the default, named by no header)
  const picked = useChosenManager();
  const installTo = scope ?? otherManager(picked ?? undefined);
  switch (r.kind) {
    case "extension":
      return (
        <OwnedExtension id={r.id} manager={r.manager ?? scope}>
          {(manager) => (
            <ExtensionDetail
              key={`${r.id}@${manager ?? ""}`}
              id={r.id}
              manager={manager}
              onBack={onBack ?? (() => onOpen(null))}
              headerBack={headerBack}
              onPage={(ui) => go({ kind: "page", id: r.id, ui, manager })}
              onUpgrade={(market, id) => go({ kind: "item", market, id, manager })}
              onChannel={onChannel}
              onConnector={onConnector}
              prefill={prefill}
            />
          )}
        </OwnedExtension>
      );
    case "page":
      return (
        <OwnedExtension id={r.id} manager={r.manager ?? scope}>
          {(manager) => <ExtensionPage key={`${open}@${manager ?? ""}`} id={r.id} ui={r.ui} manager={manager} onBack={headerBack ? undefined : onBack ?? (() => go({ kind: "extension", id: r.id, manager }))} onWide={onPageWide} />}
        </OwnedExtension>
      );
    case "catalog":
      return <MarketCatalog manager={installTo} onBack={headerBack ? undefined : onBack ?? (() => onOpen(null))} onOpen={(it) => go({ kind: "item", market: it.market, id: it.id })} onMarkets={() => go({ kind: "markets" })} />;
    case "item":
      // another manager's upgrade goes back to that manager's extension: the catalog is the default's
      return (
        <MarketItem
          key={open}
          market={r.market}
          id={r.id}
          manager={r.manager ?? installTo}
          onBack={headerBack ? undefined : onBack ?? (() => go(r.manager ? { kind: "extension", id: r.id, manager: r.manager } : { kind: "catalog" }))}
          onInstalled={(id) => go({ kind: "extension", id, manager: r.manager })}
        />
      );
    case "markets":
      return <Markets manager={installTo} onBack={headerBack ? undefined : onBack ?? (() => go({ kind: "catalog" }))} />;
    default:
      return <ExtensionList manager={scope} onOpen={onOpen} onMarket={() => go({ kind: "catalog" })} onMarkets={() => go({ kind: "markets" })} openBeside={openBeside} />;
  }
}

/**
 * Whose extension a route is: the manager it names (`<id>@<manager>`), else the current assistant's (Settings › Extensions
 * is the current assistant's, docs/assistant/managers.md "The current assistant"): a link that names none (written by
 * hand, or before several managers) opens its copy there, or says it isn't there.
 */
function OwnedExtension({ id: _id, manager, children }: { id: string; manager?: string; children: (manager: string | undefined) => React.ReactNode }) {
  const picked = useChosenManager();
  return <>{children(manager ?? otherManager(picked ?? undefined))}</>;
}

/** A page an extension draws (placement plugin_page), under the extension's name. `onBack`: none where the page's top
 *  bar has the way back (a phone), or while that page has pushed one of its own: its Back returns to the page below. */
function ExtensionPage({ id, ui, manager, onBack, onWide }: { id: string; ui: string; manager?: string; onBack?: () => void; onWide?: (wide: boolean) => void }) {
  usePinnedExtension(id, manager);
  const [d, setD] = useState<HubExtensionDetail | null>(null);
  const [n, setN] = useState(0);
  const [depth, setDepth] = useState(1);
  useEffect(() => {
    extensionDetail(id).then(setD, () => {});
  }, [id]);
  useEffect(() => () => onWide?.(false), [onWide]);
  const title = d?.ui?.find((u) => u.id === ui)?.title;
  return (
    <>
      <div className="mb-3 flex items-center gap-2">
        {onBack && depth === 1 && (
          <Button tone="quiet" icon="chevronLeft" onClick={onBack} className="-ml-2">
            <span className="max-w-[240px] truncate">{d?.name ?? id}</span>
          </Button>
        )}
        <span className="flex-1" />
        <IconButton icon="retry" label={t("settings-extensions.redraw")} onClick={() => setN((x) => x + 1)} />
      </div>
      {/* its extension id scopes what a graph's `key` keeps (lib/extension-owner.tsx) */}
      <ExtensionOwnerScope owner={id}>
        <ExtensionUI key={n} extension={id} ui={ui} title={title} manager={manager} onWide={onWide} onDepth={setDepth} />
      </ExtensionOwnerScope>
    </>
  );
}

// A plain list (joined with the Chinese enumeration comma, "a, b, c"): what these rows always showed in Chinese.
const NARROW_LIST: Intl.ListFormatOptions = { type: "conjunction", style: "narrow" };

const DOT: Record<ReturnType<typeof extensionTone>, string> = { red: "bg-red", orange: "bg-orange", green: "bg-green", idle: "bg-line-strong" };
const TILE: Record<ReturnType<typeof extensionTone>, string> = {
  red: "bg-red-tint text-red",
  orange: "bg-orange-tint text-orange",
  green: "bg-accent-tint text-accent-ink",
  idle: "bg-inset text-ink-3 shadow-hairline",
};

function StateDot({ extension }: { extension?: HubExtension | null }) {
  return <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", DOT[extensionTone(extension)])} />;
}

// ── the list ────────────────────────────────────────────

type ExtensionListProps = { onOpen: (id: string) => void; onMarket: () => void; onMarkets: () => void; openBeside?: string | null; manager?: string };
type ExtensionListView = { extensions: HubExtension[]; markets: boolean };
const extensionViews = listViewCache<ExtensionListView>();

function ExtensionList(props: ExtensionListProps) {
  const viewKey = useListViewKey();
  return <ScopedExtensionList key={viewKey} viewKey={viewKey} {...props} />;
}

/** `openBeside`: the extension open beside the list (Settings' two columns): its row is marked current, and every row
 *  puts its state under its name (with a warning, when it has one, in place of the description), so the narrow column
 *  shows the name whole. */
function ScopedExtensionList({ viewKey, onOpen, onMarket, onMarkets, openBeside, manager }: ExtensionListProps & { viewKey: string }) {
  const chosen = useChosenManager();
  const picked = manager ?? chosen;
  const [view, setView] = useState(() => manager ? undefined : extensionViews.read(viewKey));
  const extensions = view?.extensions;
  const markets = view?.markets;
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  // Reads are numbered: an answer older than the last one asked for is dropped.
  const reads = useRef(0);
  const active = useRef(false);
  useLayoutEffect(() => {
    active.current = true;
    return () => { active.current = false; };
  }, []);
  const load = useCallback(() => {
    const mine = ++reads.current;
    const current = () => active.current && mine === reads.current && viewKey === currentListViewKey();
    if (!current()) return;
    setLoading(true);
    listExtensionsInfo(current).then((r) => {
      if (!current()) return;
      setView(r);
      setError("");
      dropGoneExtensionDrafts(r.extensions);
    }, (e) => {
      if (current()) setError(errorText(e));
    }).finally(() => { if (current()) setLoading(false); });
  }, [viewKey]);
  useEffect(load, [load]);
  // An install, upgrade or uninstall that finished (here, on another device, or the hub's auto-upgrade) changes the
  // list: lib/extension-operations.ts sends EXTENSIONS_CHANGED when one succeeds. watchOperations starts its push
  // listener, which otherwise only a market page starts. Several at once read the list once.
  useEffect(() => {
    let queued = false;
    const again = () => {
      if (queued) return;
      queued = true;
      queueMicrotask(() => {
        queued = false;
        load();
      });
    };
    window.addEventListener(EXTENSIONS_CHANGED, again);
    const stop = watchOperations(() => {});
    return () => {
      window.removeEventListener(EXTENSIONS_CHANGED, again);
      stop();
    };
  }, [load]);
  // a sign-in that changed (signed in, out, expired) shows at once
  // only the row of the manager whose extension signed in (or out): another manager's of the same id stays as it is
  useEffect(() => onAuthChange((id, auth, from) => setView((cur) => cur && ({
    ...cur, extensions: cur.extensions.map((x) => (x.manager || defaultManager()) === from ? withAuth(x, id, auth) : x),
  }))), []);
  // a reconnect the hub could not resume: the extension_auth pushes missed meanwhile are not replayed (#4954)
  useEffect(() => onHubEvent("hello", (m) => !isResumed(m) && load()), [load]);
  // the current assistant's extensions only (docs/assistant/managers.md "The current assistant"); one assistant: all
  const scope = useScope();
  const own = useMemo(() => (extensions ? manager
    ? extensions.filter((extension) => (extension.manager || defaultManager()) === manager)
    : scoped(extensions, scope) : null), [extensions, manager, scope]);
  useEffect(() => {
    if (!manager && own && markets !== undefined) extensionViews.keep(viewKey, { extensions: own, markets });
  }, [manager, viewKey, own, markets]);
  const drafts = useUnsavedExtensions();
  // an offline current assistant: the hub has none of its extensions to merge in, which must not read as "none installed"
  const shownOffline = useShownOffline();
  const offline = manager && shownOffline?.manager.id !== manager ? null : shownOffline;

  return (
    <>
      <p className="mb-5 px-1 text-[12.5px] leading-relaxed text-ink-3">
        {t("settings-extensions.intro")}
      </p>
      {/* Extension market first, above the installed list (#5017): where a phone or a browser gets another extension. */}
      {markets && (
        <Group title={t("settings-extensions.market_title")} footer={t("settings-extensions.market_footer")} anchor="market">
          <Row
            label={
              <span className="flex items-center gap-2">
                <Icon name="search" size={13} className="shrink-0 text-ink-3" />
                {t("settings-extensions.browse_market")}
              </span>
            }
            onClick={onMarket}
          />
          <Row
            label={
              <span className="flex items-center gap-2">
                <Icon name="gear" size={13} className="shrink-0 text-ink-3" />
                {t("settings-extensions.manage_markets")}
              </span>
            }
            onClick={onMarkets}
          />
        </Group>
      )}
      <Group
        anchor="extensions"
        title={
          <span className="flex items-center gap-2">
            {t("settings-extensions.title")}
            {own && !(offline && own.length === 0) && <span className="font-normal">{listSummary(own)}</span>}
          </span>
        }
        right={<IconButton icon="retry" label={t("settings-extensions.refresh")} onClick={load} disabled={loading} size={24} iconSize={13} className="-mb-1" />}
        footer={markets ? t("settings-extensions.list_footer_market") : t("settings-extensions.list_footer")}
      >
        {!own && !error && (
          <div className="px-4 py-3">
            <Loading />
          </div>
        )}
        {error && (
          <div className="flex items-center gap-3 px-4 py-3">
            <p className="min-w-0 flex-1 text-[12.5px] break-words text-red">{t("settings-extensions.list_error", { error })}</p>
            <Button tone="secondary" icon="retry" onClick={load} busy={loading}>
              {t("settings-extensions.retry")}
            </Button>
          </div>
        )}
        {own?.length === 0 && offline && <OfflineState offline={offline} ring="ring-surface" className="py-8" />}
        {own?.length === 0 && !offline && (
          <div className="flex flex-col items-center gap-2 px-6 py-8 text-center">
            <span className="flex size-10 items-center justify-center rounded-[10px] bg-inset text-ink-3 shadow-hairline">
              <Icon name="extension" size={18} />
            </span>
            <p className="text-[13px] font-medium text-ink">{t("settings-extensions.empty_title")}</p>
            <p className="max-w-[420px] text-[12px] leading-relaxed text-ink-3">{t("settings-extensions.empty_detail")}</p>
          </div>
        )}
        {(own ?? []).map((p, i) => {
          const kept = drafts.has(extensionDraftKey(p.id, p.manager));
          // the kept edits' mark goes before what is still missing, not instead of it
          const needs = p.needs_setup?.length ? t("settings-extensions.needs_setup", { items: formatList(p.needs_setup, NARROW_LIST) }) : "";
          const warning = p.error || [kept ? t("settings-extensions.unsaved") : "", needs].filter(Boolean).join(" · ");
          const compact = openBeside !== undefined;
          const note = warning || (compact ? "" : p.description);
          const sub = routeSub({ kind: "extension", id: p.id, manager: p.manager && p.manager !== defaultManager() ? p.manager : undefined }) ?? p.id;
          // a route without @manager opens the current assistant's copy (OwnedExtension)
          const current = isOpenBeside(openBeside, p.id, p.manager, picked);
          const state = (
            <span className={cx("flex items-center gap-1.5 whitespace-nowrap text-ink-2", compact ? "mt-0.5 min-w-0 text-submeta" : "shrink-0 text-[12px]")}>
              <StateDot extension={p} />
              <span className={cx(compact && "truncate")}>
                {extensionState(p)}
                {listAuthNote(p.auth)}
                {p.upgrade?.state === "needs_permission" && t("settings-extensions.upgrade_pending")}
              </span>
            </span>
          );
          return (
            <button
              key={`${p.manager ?? ""}:${p.id}`}
              type="button"
              data-extension-row={p.id}
              data-manager={p.manager ?? ""}
              aria-current={current || undefined}
              onClick={() => onOpen(sub)}
              className={cx("flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors", current ? "bg-accent-tint" : "hover:bg-hover")}
              style={{ animation: `fade-up 320ms ${EASE} ${Math.min(i, 8) * 30}ms both` }}
            >
              <ExtensionLogo url={p.icon_url} manager={p.manager ?? ""} iconSize={15} className={cx("flex size-8 shrink-0 items-center justify-center rounded-[8px]", TILE[extensionTone(p)])} />
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="truncate text-[13px] text-ink">{p.name}</span>
                  {p.source !== "installed" && sourceText(p.source) && <Pill>{sourceText(p.source)}</Pill>}
                </div>
                {compact && state}
                {note && (
                  <div className={cx("mt-0.5 truncate text-[11.5px]", p.error ? "text-red" : kept || needs ? "text-orange" : "text-ink-3")}>
                    {note}
                  </div>
                )}
                {packageDownloads(p.downloads) !== null && <div className="mt-0.5"><ExtensionDownloads downloads={p.downloads} /></div>}
              </div>
              {!compact && state}
              <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
            </button>
          );
        })}
      </Group>
    </>
  );
}

// ── one extension ──────────────────────────────────────────

/** A number field as typed, by dotted path, until it reads as one. */
/** Requests about extension id (its details, settings, models, pages) go to manager while the view is open: the same
 *  id may be installed under several managers (each one's built-ins), and the default's own keep going to it elsewhere. */
function usePinnedExtension(id: string, manager: string | undefined) {
  useLayoutEffect(() => pinManagerAt([`/extensions/${enc(id)}`, `/plugins/${enc(id)}`], manager), [id, manager]);
}

type ExtensionDetailProps = {
  id: string;
  /** Whose extension it is (several managers); none: the default's. */
  manager?: string;
  onBack: () => void;
  /** The page's top bar has the way back (a phone): no Back of its own. */
  headerBack?: boolean;
  onPage: (ui: string) => void;
  onUpgrade: (market: string, id: string) => void;
  onChannel?: (id: string, manager?: string) => void;
  onConnector?: (id: string, manager?: string) => void;
  prefill?: JsonObject;
};

function ExtensionDetail(props: ExtensionDetailProps) {
  const binding = useExtensionBinding(props.id, props.manager);
  return <ExtensionResourceDetail key={binding.client.key} {...props} binding={binding} />;
}

function ExtensionResourceDetail({ binding, id, manager, onBack: back, headerBack, onPage, onUpgrade, onChannel, onConnector, prefill }: ExtensionDetailProps & { binding: ExtensionBinding }) {
  const roster = useRoster();
  const offline = roster?.managers.find((m) => m.id === binding.client.managerId)?.online === false;
  const {
    detail, error, loading, draft, typed, fieldErrors, checkFailed, saving, toggling, saveBarLabel, editable,
    canSave, problems, load, reload, authChanged, connectorRefreshKey, mutate, remove: onBack, edit, type, revert, save, saveNow,
  } = useExtensionConfiguration(id, binding, prefill, back);
  const [removing, setRemoving] = useState(false);
  const [secret, setSecret] = useState<ExtensionSecret | null>(null);
  const [dependents, setDependents] = useState<Dependent[] | null>(null);
  // Switching a pack off takes its members along (#1830).
  const [packOff, setPackOff] = useState(false);
  const [startSignIn] = useState(() => signInAsked(id));
  useEffect(() => void takeSignIn(id), [id]);

  const fields = detail?.form?.fields ?? [];
  const setupTarget = editable ? setupSecret(detail?.needs_setup, detail?.secrets) : undefined;
  const noConfiguration = !!detail && editable && !fields.length && !detail.secrets?.length && !shownPages(detail.ui, detail.auth).length && !detail.auth
    && !channelOf(detail) && !detail.contributes?.connectors?.length;

  const toggleAutoUpgrade = async (on: boolean) => {
    try {
      await mutate("management", (client) => setExtensionAutoUpgrade(id, on, client));
      toast(t(on ? "settings-extensions.auto_upgrade_on" : "settings-extensions.auto_upgrade_off"), "ok");
    } catch (error) {
      if (!isAbort(error)) toast(t(on ? "settings-extensions.auto_upgrade_on_failed" : "settings-extensions.auto_upgrade_off_failed", { error: errorText(error) }), "error");
    }
  };
  const toggle = async (on: boolean, cascade = false) => {
    try {
      const detail = await mutate("management", (client) => setExtensionEnabled(id, on, cascade, client));
      setDependents(null);
      setPackOff(false);
      toast(t(on ? "settings-extensions.enabled_name" : "settings-extensions.disabled_name", { name: detail.name }), "ok");
    } catch (error) {
      if (isAbort(error)) return;
      const deps = dependentError(error);
      setPackOff(false);
      if (!on && deps) setDependents(deps);
      else toast(t(on ? "settings-extensions.enable_failed" : "settings-extensions.disable_failed", { error: errorText(error) }), "error");
    }
  };
  const toggleContribution = async (c: Contribution, on: boolean) => {
    try {
      await mutate("management", (client) => setContribution(id, c.point, c.id, on, client));
      toast(t(on ? "settings-extensions.contribution_on" : "settings-extensions.contribution_off"), "ok");
    } catch (error) {
      if (!isAbort(error)) toast(t("settings-extensions.change_failed", { error: errorText(error) }), "error");
    }
  };
  const computers = !!detail?.contributes?.computer;
  const nodes = detail?.node_machines ? { field: detail.node_machines, states: detail.nodes ?? [] } : undefined;
  const launchers = useFormLaunchers(hasLauncherField(detail?.form?.fields), detail?.id);

  const p = detail;
  // A pack's members by their installed names (#4122); one not installed keeps its id.
  const memberNames = useInstalledNames(!!detail?.extension_pack?.length);

  return (
    <div>
      <div className="relative mb-3 flex items-center gap-2">
        {/* unsaved edits stay (the list marks them); Revert in the save bar drops them */}
        {!headerBack && (
          <Button tone="quiet" icon="chevronLeft" onClick={back} className="-ml-2">
            {t("settings-extensions.title")}
          </Button>
        )}
        <OwnerMark manager={manager} />
        <span className="flex-1" />
        <IconButton icon="retry" label={t("settings-extensions.reload")} onClick={load} disabled={loading} />
      </div>

      {/* what it is and how it is doing */}
      <section className="mb-6 overflow-hidden rounded-card bg-surface shadow-card">
        <div className="flex items-center gap-3 px-4 py-3.5">
          <ExtensionLogo url={p?.icon_url} manager={manager ?? ""} iconSize={20} className={cx("flex size-11 shrink-0 items-center justify-center rounded-[11px]", TILE[extensionTone(p)])} />
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-[15px] font-semibold text-ink">{p?.name ?? id}</h2>
            <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[12px] text-ink-3">
              <StateDot extension={p} />
              <span className="truncate">{p ? stateLine(p) : loading ? t("settings-extensions.loading") : t("settings-extensions.unavailable")}</span>
            </div>
          </div>
          {loading && !p && <Spinner />}
        </div>
        {(p?.description || (p && runNote(p)) || p?.needs_setup?.length || p?.error || error) && (
          <div className="flex flex-col gap-1.5 border-t border-line px-4 py-3">
            {p?.description && <p className="text-[12.5px] leading-relaxed text-ink-2">{p.description}</p>}
            {p && runNote(p) && <p className="text-[11.5px] text-ink-3">{runNote(p)}</p>}
            {!!p?.needs_setup?.length && (setupTarget ? (
              // What is missing is a secret to set here: the line opens it (#8853).
              <button type="button" onClick={() => setSecret(setupTarget)} className="flex items-center gap-1 text-left text-label text-orange hover:underline" data-extension-needs-setup>
                {t("settings-extensions.needs_setup", { items: formatList(p.needs_setup, NARROW_LIST) })}
                <Icon name="chevron" size={10} stroke={2.4} className="shrink-0" style={{ transform: "rotate(-90deg)" }} />
              </button>
            ) : <p className="text-label text-orange" data-extension-needs-setup>{t("settings-extensions.needs_setup", { items: formatList(p.needs_setup, NARROW_LIST) })}</p>)}
            {p?.error && <p className="text-[12px] break-words text-red select-text">{p.error}</p>}
            {error && <p className="text-[12px] break-words text-red select-text">{t("settings-extensions.detail_error", { error })}</p>}
          </div>
        )}
      </section>

      {p && (
        <ExtensionSettingsPage
          detail={p}
          memberNames={memberNames}
          manager={manager}
          busy={toggling || saving}
          offline={offline}
          onEnabled={(on) => (!on && p.pack_members_follow && p.pack_on?.length ? setPackOff(true) : void toggle(on))}
          onAutoUpgrade={(on) => void toggleAutoUpgrade(on)}
          onUpgrade={onUpgrade}
          onUninstall={() => setRemoving(true)}
          onChecked={reload}
          configurationTitle={noConfiguration}
          configuration={<>
          {p.auth && <AuthGroup id={p.id} auth={p.auth} enabled={p.enabled ?? true} start={startSignIn} onChange={authChanged} />}
          {onChannel && channelOf(p) && <ChannelLink id={channelOf(p)!} manager={manager} onOpen={onChannel} />}
          {!editable && <Group><Row label={t(p.read_only || p.source === "hub.json" ? "settings-extensions.read_only" : "settings-extensions.externally_managed")} wrapHint /></Group>}
          {noConfiguration && <Group><Row label={t("settings-extensions.no_configuration")} /></Group>}

          {shownPages(p.ui, p.auth).length > 0 && (
            <Group title={t("settings-extensions.pages_title")} footer={t("settings-extensions.pages_footer")}>
              {shownPages(p.ui, p.auth).map((u) => (
                <Row
                  key={u.id}
                  label={
                    <span className="flex items-center gap-2">
                      <Icon name="layers" size={13} className="shrink-0 text-ink-3" />
                      {u.title || u.id}
                    </span>
                  }
                  onClick={() => onPage(u.id)}
                />
              ))}
            </Group>
          )}

          {(p.enabled ?? true) && !!p.contributes?.connectors?.length && <ExtensionConnectorInbox ids={p.contributes.connectors} manager={manager} onOpen={onConnector} refreshKey={connectorRefreshKey} />}
          <ConnectorExternalSourceSettings
            ids={[...(p.contributes?.connectors ?? []), ...(p.contributes?.channel ?? [])]}
            manager={manager}
            copy={{
              channelDefaultsTitle: t("settings-extensions.source_defaults_title"),
              channelHint: t("settings-extensions.source_defaults_hint"),
              flowsTitle: t("settings-extensions.source_flows_title"),
              flowsHint: t("settings-extensions.source_flows_hint"),
              hideEmptyFlows: true,
            }}
          />
          {/* source subscriptions (#2041) of its connectors, the default manager's */}
          {(p.enabled ?? true) && !manager && !!p.contributes?.connectors?.length && <SubscriptionsEntry pick={(l) => l.extension === p.id} scope={{ ext: p.id }} description={t("settings-extensions.subscriptions_footer")} />}

          {editable && fields.length > 0 && (
            <>
              {fieldErrors[""] && <p role="alert" className="mb-3 rounded-[8px] bg-red-tint px-3 py-2 text-[12px] break-words text-red">{t("settings-extensions.not_saved", { error: fieldErrors[""] })}</p>}
              {fields.some((f) => f.type !== "object") && (
                <Group title={t("settings-extensions.fields_title")} footer={saveFooter(fields)}>
                  {fields
                    .filter((f) => f.type !== "object")
                    .map((f) => (
                      <FieldRow key={f.name} field={f} path={[f.name]} draft={draft} typed={typed} errors={fieldErrors} problems={problems} onEdit={edit} onType={type} onSaveNow={saveNow} computers={computers} nodes={nodes} launchers={launchers} secrets={p.secrets} onSecret={setSecret} />
                    ))}
                </Group>
              )}
              {fields
                .filter((f) => f.type === "object")
                .map((g) => (
                  <Group key={g.name} title={fieldLabel(g)} footer={g.description}>
                    {(g.fields ?? []).map((f) => (
                      <FieldRow key={f.name} field={f} path={[g.name, f.name]} draft={draft} typed={typed} errors={fieldErrors} problems={problems} onEdit={edit} onType={type} onSaveNow={saveNow} computers={computers} nodes={nodes} launchers={launchers} />
                    ))}
                  </Group>
                ))}
            </>
          )}

          {editable && !!p.secrets?.some((s) => !s.list) && (
            <Group title={t("settings-extensions.secrets_title")} footer={t("settings-extensions.secrets_footer")}>
              {p.secrets.filter((s) => !s.list).map((s) => (
                <Row
                  key={s.name}
                  label={
                    <span className="flex items-center gap-2">
                      <Icon name="key" size={13} className="shrink-0 text-ink-3" />
                      {s.title}
                    </span>
                  }
                  hint={s.reference ? s.reference.title || s.reference.reference : s.description}
                  onClick={() => setSecret(s)}
                >
                  <span className={cx(!s.set && s.required && "text-orange")}>{secretState(s)}</span>
                </Row>
              ))}
            </Group>
          )}

          </>}
          information={<>
          <SecretProviderGroups extension={id} users={p.secret_users} rules={p.handoff_rules} editable={editable} mutate={mutate} />
          <ExtensionInfoAccess client={binding.client} extension={id} editable={editable && !offline} />
          {!!p.depends?.length && (
            <Group title={t("settings-extensions.depends_title")} footer={t("settings-extensions.depends_footer")}>
              {p.depends.map((d) => <Row key={d.id} label={d.name || d.id} hint={d.version}><span className={cx(d.state !== "ok" && "text-orange")}>{dependencyText(d)}</span></Row>)}
            </Group>
          )}
          {!!p.extension_pack?.length && (
            <div data-pack-members>
              <Group title={t("settings-extensions.pack_title")} footer={t("settings-extensions.pack_footer")}>
                {p.extension_pack.map((m) => <PackMemberRow key={m.id} member={m} name={memberNames[m.id]} mono />)}
              </Group>
            </div>
          )}
          {!!p.contributions?.length && (
            <Group title={t("settings-extensions.contributions_title")} footer={t("settings-extensions.contributions_footer")}>
              {p.contributions.map((c) => (
                <Row key={`${c.point}/${c.id}`} label={c.title || c.id} hint={`${pointText(c.point)} · ${c.id}${c.via === "connector" ? t("settings-extensions.via_connector") : c.via === "config" ? t("settings-extensions.via_config") : ""}`}>
                  <Switch label={c.title || c.id} on={c.enabled} disabled={!editable || toggling || offline || !c.can_disable} onChange={(on) => void toggleContribution(c, on)} />
                </Row>
              ))}
            </Group>
          )}
          {!p.contributions?.length && p.contributes && Object.keys(p.contributes).length > 0 && (
            <Group title={t("settings-extensions.provides_title")}>
              {Object.keys(p.contributes)
                .sort()
                .map((point) => (
                  <Row key={point} label={pointText(point)} mono>
                    <span className="truncate">{formatList(p.contributes?.[point] ?? [], NARROW_LIST)}</span>
                  </Row>
                ))}
            </Group>
          )}

          {p.sandbox && <SandboxGroup s={p.sandbox} id={p.id} name={p.name} />}

          {!!p.permissions?.length && (
            <Permissions permissions={p.permissions} />
          )}

          </>}
          saveBar={saveBarLabel && (
            <Group>
              {checkFailed && <SettingsHint alert>{t("settings-extensions.not_saved", { error: checkFailed })}</SettingsHint>}
              <Row label={saveBarLabel}>
                <Button tone="quiet" onClick={revert} disabled={saving}>{t("settings-extensions.revert")}</Button>
                <Button tone="accent" busy={saving} disabled={!canSave || offline} onClick={() => void save()}>{t("common.save")}</Button>
              </Row>
            </Group>
          )}
        />
      )}
      {removing && p && <UninstallDialog id={p.id} name={p.name} version={p.version} pack={p.extension_pack} onClose={() => setRemoving(false)} onDone={onBack} />}
      {packOff && p && !dependents && (
        <Dialog open title={t("settings-extensions.disable_pack_title", { name: p.name })} onClose={() => !toggling && setPackOff(false)} footer={<><Button tone="quiet" disabled={toggling} onClick={() => setPackOff(false)}>{t("common.cancel")}</Button><Button tone="danger" busy={toggling} onClick={() => void toggle(false)}>{t("settings-extensions.disable_all")}</Button></>}>
          <p className="text-ink-2">{t("settings-extensions.disable_pack_body", { names: (p.pack_on ?? []).map((m) => m.name || m.id).join(t("settings-extensionmarket.list_separator")) })}</p>
        </Dialog>
      )}
      {dependents && p && (
        <Dialog open title={t("settings-extensions.disable_dependents_title", { name: p.name })} onClose={() => !toggling && setDependents(null)} footer={<><Button tone="quiet" disabled={toggling} onClick={() => setDependents(null)}>{t("common.cancel")}</Button><Button tone="danger" busy={toggling} onClick={() => void toggle(false, true)}>{t("settings-extensions.disable_all")}</Button></>}>
          <p className="text-ink-2">{t("settings-extensions.dependents_body", { names: formatList(dependents.map((d) => d.name), NARROW_LIST) })}</p>
        </Dialog>
      )}


      {secret && <SecretDialog extension={id} secret={secret.list && secret.entry_title ? { ...secret, title: `${secret.entry_title} · ${secret.title}` } : secret} onClose={() => setSecret(null)} mutate={mutate} />}
    </div>
  );
}

/** "Isolation" (Android ExtensionModelParts sandbox()): whether the sandbox is in effect and why, what went through the
 *  egress proxy, and the last address it blocked said in words, with the way to the admin page where the owner allows it
 *  or switches the sandbox (#5696). Only looked at here: widening what an extension may reach is the owner's, in /admin. */
export function SandboxGroup({ s, id, name }: { s: ExtensionSandbox; id: string; name: string }) {
  const e = s.egress;
  const blocked = e?.denied ?? 0;
  return (
    <Group title={t("settings-extensions.sandbox_title")} footer={e ? t("settings-extensions.sandbox_footer") : undefined}>
      <div className="flex items-start gap-3 px-4 py-2.5">
        <Icon name={s.enabled ? "lock" : "unlock"} size={14} className={cx("mt-0.5 shrink-0", s.active ? "text-green" : s.enabled ? "text-orange" : "text-ink-3")} />
        <div className="min-w-0 flex-1">
          <div className="text-[13px] text-ink">{sandboxState(s)}</div>
          {s.why && <div className="mt-0.5 text-[11.5px] leading-snug break-words text-ink-3">{s.why}</div>}
        </div>
      </div>
      {e && (
        <div className="flex items-center gap-3 px-4 py-2.5" data-sandbox-egress>
          <span className="min-w-0 flex-1 text-[13px] text-ink">{t("settings-extensions.sandbox_network")}</span>
          <span className="shrink-0 text-[12.5px] whitespace-nowrap tabular-nums">
            <span className="text-green">{t("settings-extensions.sandbox_allowed_count", { count: e.allowed ?? 0 })}</span>
            <span className="text-ink-3"> · </span>
            <span className={blocked ? "text-red" : "text-ink-3"}>{t("settings-extensions.sandbox_blocked_count", { count: blocked })}</span>
          </span>
        </div>
      )}
      {e?.last_denied && (
        <div className="px-4 py-2.5" data-sandbox-blocked>
          <div className="flex items-start gap-2.5 rounded-control bg-orange-tint px-3 py-2.5">
            <Icon name="warning" size={14} className="mt-0.5 shrink-0 text-orange" />
            <div className="min-w-0 flex-1">
              <p className="text-[13px] leading-snug break-words text-ink">
                {tRich("settings-extensions.sandbox_blocked_sentence", { name, host: <span className="font-mono text-[12px] select-text">{e.last_denied}</span> })}
              </p>
              {e.denied_at && <p className="mt-0.5 text-[11.5px] text-ink-3">{ago(e.denied_at)}</p>}
              <Button tone="secondary" icon="external" className="mt-2" onClick={() => window.open(adminNetworkURL(id), "_blank", "noopener")}>
                {t("settings-extensions.sandbox_go_admin")}
              </Button>
            </div>
          </div>
        </div>
      )}
    </Group>
  );
}

/** What an extension may do, in user-facing terms: internal point keys and socket filenames stay behind the explanation. */
export function Permissions({ permissions }: { permissions: ExtensionPermission[] }) {
  return (
    <Group title={t("settings-extensions.permissions_title")} footer={t("settings-extensions.permissions_footer")}>
      {permissions.map((perm) => (
        <div key={perm.key} className="px-4 py-2.5">
          <div className="text-[13px] text-ink">{permissionTitle(perm)}</div>
          {permissionDetail(perm) && <div className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{permissionDetail(perm)}</div>}
        </div>
      ))}
      {permissions.some((perm) => perm.key === "network") && <SettingsHint>{t("settings-extensions.permission_network_hint")}</SettingsHint>}
    </Group>
  );
}

/** Where the admin page shows an extension's network and isolation (its row opened at that block, #5696). */
export function adminNetworkURL(id: string): string {
  return `/admin/extensions?extension=${encodeURIComponent(id)}#network`;
}

// ── the form ────────────────────────────────────────────

const inputClass = "h-8 w-full rounded-control bg-field px-2.5 text-[12.5px] text-ink outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

export interface FieldProps {
  field: ExtensionField;
  path: string[];
  draft: JsonObject;
  typed: Typed;
  errors: Record<string, string>;
  problems: Record<string, string>;
  onEdit: (path: string[], value: Json | undefined) => void;
  onType: (path: string[], text: string, integer: boolean) => void;
  /** Saves one field at once (a machine's switch). */
  onSaveNow: (path: string[], value: Json | undefined) => void;
  /** The extension operates computers: machine rows say how Cua Driver is there. */
  computers: boolean;
  /** The plugin's program on computers: the field that picks them, and how it is on each (their rows say). */
  nodes?: { field: string; states: ExtensionNodeState[] };
  /** The hub's launchers, for a field of format launcher. */
  launchers: FormLaunchers;
  /** Live entry-secret views; no values. */
  secrets?: ExtensionSecret[];
  onSecret?: (secret: ExtensionSecret) => void;
}

interface FormLaunchers {
  list: PickerLauncher[] | null;
  loading: boolean;
}

/** One field of the form, by its type; its title above (or beside a switch), its description and error below. */
export function FieldRow(props: FieldProps) {
  const { field: f, path, draft, errors, problems, onEdit } = props;
  const key = fieldPath(path);
  const err = errors[key] ?? problems[key];
  const title = fieldTitle(f);
  const id = `extension-field-${key}`;
  const below = (
    <>
      {f.description && <p className="mt-1 text-[11.5px] leading-snug text-ink-3">{f.description}</p>}
      {err && (
        <p role="alert" className="mt-1 text-[11.5px] text-red">
          {err}
        </p>
      )}
    </>
  );

  if (f.type === "boolean") return <BooleanField field={f} path={path} draft={draft} onEdit={onEdit} title={title} below={below} />;

  return (
    <div className="px-4 py-2.5" data-field={key}>
      <label htmlFor={id} className="mb-1.5 block text-[12px] font-medium text-ink-2">
        {title}
      </label>
      <Control {...props} id={id} invalid={!!err} />
      {below}
    </div>
  );
}

/** A boolean's switch, beside its title; with x-confirm, turning it on asks the field's sentence first (turning off is not asked). */
function BooleanField({ field: f, path, draft, onEdit, title, below }: Pick<FieldProps, "field" | "path" | "draft" | "onEdit"> & { title: string; below: React.ReactNode }) {
  const [asking, setAsking] = useState(false);
  const v = valueAt(draft, path);
  const on = typeof v === "boolean" ? v : f.default === true;
  return (
    <div className="px-4 py-2.5" data-field={path.join(".")}>
      <div className="flex items-center gap-3">
        <span className="min-w-0 flex-1 text-[13px] text-ink">{title}</span>
        <Switch on={on} onChange={(x) => (asksFirst(f, x) ? setAsking(true) : onEdit(path, x))} label={fieldLabel(f)} />
      </div>
      {below}
      <TurnOnDialog name={asking ? fieldLabel(f) : null} sentence={f.confirm} onCancel={() => setAsking(false)} onConfirm={() => { onEdit(path, true); setAsking(false); }} />
    </div>
  );
}

/** x-confirm's dialog before something named is turned on: the field's sentence, Cancel and Turn On. Closed while name is null. */
function TurnOnDialog({ name, sentence, onCancel, onConfirm }: { name: string | null; sentence?: string; onCancel: () => void; onConfirm: () => void }) {
  return (
    <Dialog
      open={name !== null}
      onClose={onCancel}
      title={name !== null ? t("settings-extensions.turn_on_title", { name }) : undefined}
      footer={
        <>
          <Button tone="quiet" onClick={onCancel}>{t("common.cancel")}</Button>
          <Button tone="primary" onClick={onConfirm}>{t("settings-extensions.turn_on")}</Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{sentence}</p>
    </Dialog>
  );
}

function Control({ field: f, path, draft, typed, errors, problems, onEdit, onType, onSaveNow, computers, nodes, launchers, secrets, onSecret, id, invalid }: FieldProps & { id: string; invalid: boolean }) {
  const ring = invalid ? "shadow-[inset_0_0_0_1px_var(--red)]" : "shadow-inset-field";
  const v = valueAt(draft, path);

  if (f.type === "string" && f.name === "triage_rules") return <TriageRuleField id={id} value={typeof v === "string" ? v : ""} placeholder={placeholder(f)} maxLength={f.max_length} invalid={invalid} onChange={(text) => onEdit(path, text || undefined)} />;

  if (isEntryList(f)) {
    return <EntryListControl field={f} path={path} draft={draft} typed={typed} errors={errors} problems={problems} onEdit={onEdit} onType={onType} onSaveNow={onSaveNow} computers={computers} nodes={nodes} launchers={launchers} secrets={secrets} onSecret={onSecret} id={id} ring={ring} />;
  }

  if (f.type === "array" && f.items?.format === "machine") {
    return <MachineSwitches field={f} path={path} draft={draft} onSaveNow={onSaveNow} computers={computers} nodes={nodes?.field === f.name ? nodes.states : undefined} id={id} />;
  }
  if (f.type === "array") return <ListControl field={f} path={path} draft={draft} onEdit={onEdit} id={id} ring={ring} />;
  if (f.type === "string" && (f.format === "machine" || f.format === "launcher") && !f.enum?.length) {
    return <PickerControl field={f} path={path} draft={draft} onEdit={onEdit} launchers={launchers} id={id} ring={ring} />;
  }

  if (f.enum?.length) {
    const at = f.enum.findIndex((c) => v !== undefined && same(c.value, v));
    return (
      <select id={id} value={at < 0 ? "" : String(at)} onChange={(e) => onEdit(path, e.target.value === "" ? undefined : f.enum![Number(e.target.value)].value)} className={cx(inputClass, ring, "max-w-[320px]")}>
        <option value="">{placeholder(f)}</option>
        {f.enum.map((c, i) => (
          <option key={i} value={String(i)}>
            {choiceLabel(c)}
          </option>
        ))}
      </select>
    );
  }

  if (f.type === "integer" || f.type === "number") {
    const key = fieldPath(path);
    const text = typed[key]?.text ?? (typeof v === "number" ? String(v) : "");
    const range = [f.minimum !== undefined ? t("settings-extensions.min", { value: f.minimum }) : "", f.maximum !== undefined ? t("settings-extensions.max", { value: f.maximum }) : ""].filter(Boolean).join(t("settings-extensions.range_separator"));
    return (
      <div className="flex items-center gap-2">
        <input
          id={id}
          value={text}
          inputMode={f.type === "integer" ? "numeric" : "decimal"}
          onChange={(e) => onType(path, e.target.value, f.type === "integer")}
          placeholder={placeholder(f)}
          spellCheck={false}
          autoComplete="off"
          className={cx(inputClass, ring, "max-w-[200px] font-mono")}
        />
        {range && <span className="text-[11px] whitespace-nowrap text-ink-3">{range}</span>}
      </div>
    );
  }

  return (
    <input
      id={id}
      value={typeof v === "string" ? v : ""}
      onChange={(e) => onEdit(path, e.target.value === "" ? undefined : e.target.value)}
      type={f.format === "email" ? "email" : f.format === "uri" ? "url" : "text"}
      placeholder={placeholder(f)}
      maxLength={f.max_length}
      spellCheck={false}
      autoComplete="off"
      autoCapitalize="off"
      className={cx(inputClass, ring, f.format && "font-mono")}
    />
  );
}

export function hasLauncherField(fields?: ExtensionField[]): boolean {
  return !!fields?.some((f) => f.format === "launcher" || hasLauncherField(f.fields) || hasLauncherField(f.items?.fields));
}

/** The hub's launchers for the form's launcher pickers, read once while the page is open (GET /launchers asks every
 *  machine): null while they load, or when they could not be read. */
export function useFormLaunchers(on: boolean, page: string | undefined): FormLaunchers {
  const [state, setState] = useState<FormLaunchers>({ list: null, loading: false });
  useEffect(() => {
    if (!on) return;
    let live = true;
    setState({ list: null, loading: true });
    loadLaunchers<PickerLauncher>().then(
      (r) => live && setState({ list: r.launchers, loading: false }),
      () => live && setState({ list: null, loading: false }),
    );
    return () => {
      live = false;
    };
  }, [on, page]);
  return state;
}

/** One of the hub's machines (format machine, of the systems x-os names) or launchers (format launcher) by name, "Not set"
 *  first; a value naming none of them still shows. Launchers that are not in yet, or could not be read, are typed. */
function PickerControl({ field: f, path, draft, onEdit, launchers, id, ring }: Pick<FieldProps, "field" | "path" | "draft" | "onEdit" | "launchers"> & { id: string; ring: string }) {
  const machines = useStore((s) => s.machines);
  const v = valueAt(draft, path);
  const choices = pickerChoices(f, v, machines, launchers.list);
  if (!choices) {
    return (
      <div className="flex items-center gap-2">
        <input
          id={id}
          value={typeof v === "string" ? v : ""}
          onChange={(e) => onEdit(path, e.target.value === "" ? undefined : e.target.value)}
          placeholder={placeholder(f)}
          spellCheck={false}
          autoComplete="off"
          autoCapitalize="off"
          className={cx(inputClass, ring, "max-w-[320px] font-mono")}
        />
        {launchers.loading && <span className="text-[11px] whitespace-nowrap text-ink-3">{t("lib-extensions.launchers_loading")}</span>}
      </div>
    );
  }
  return (
    <select id={id} value={typeof v === "string" ? v : ""} onChange={(e) => onEdit(path, e.target.value === "" ? undefined : e.target.value)} className={cx(inputClass, ring, "max-w-[320px]")}>
      <option value="">{placeholder(f)}</option>
      {choices.map((c) => (
        <option key={c.value} value={c.value}>
          {c.label}
        </option>
      ))}
    </select>
  );
}

/** A list of texts (each removable, more added), or of choices (each one pressed in or out). */
/** A list of the hub's machines (format machine): a switch per machine of the systems the field names, by name and how
 *  it is, in effect at once; a value naming no machine still shows, to be turned off. */
function MachineSwitches({ field: f, path, draft, onSaveNow, computers, nodes, id }: Pick<FieldProps, "field" | "path" | "draft" | "onSaveNow" | "computers"> & { nodes?: ExtensionNodeState[]; id: string }) {
  const all = useStore((s) => s.machines);
  const items = listItems(draft, path, f);
  const os = f.items?.os ?? [];
  const machines = all.filter((m) => !os.length || os.includes(m.os ?? ""));
  const unknown = items.filter((x) => !all.some((m) => m.id === x));
  // x-confirm: a machine waiting on it before it is turned on (turning one off is not asked).
  const [asking, setAsking] = useState<{ id: string; name: string } | null>(null);
  const save = (mid: string, on: boolean) => {
    const next = items.filter((x) => x !== mid);
    if (on) next.push(mid);
    onSaveNow(path, next.length ? next : undefined);
  };
  const flip = (mid: string, name: string, on: boolean) => (asksFirst(f, on) ? setAsking({ id: mid, name }) : save(mid, on));
  // The field picking where the plugin's program runs: a machine switched on says how the program is there.
  const status = (m: (typeof machines)[number]) =>
    nodes && items.includes(m.id) ? `${m.online ? t("settings-extensions.online") : t("settings-extensions.offline")} · ${nodeProgramStatus(nodes.find((n) => n.machine_id === m.id))}` : machineStatus(m, computers);
  const rows = [...machines.map((m) => ({ id: m.id, name: m.name, status: status(m) })), ...unknown.map((x) => ({ id: x, name: x, status: t("settings-extensions.machine_not_found") }))];
  if (!rows.length) return <p className="text-[12.5px] text-ink-3">{os.join() === "darwin" ? t("settings-extensions.no_mac") : t("settings-extensions.no_machine")}</p>;
  return (
    <div id={id} role="group" className="flex flex-col divide-y divide-line rounded-control shadow-hairline">
      {rows.map((r) => (
        <div key={r.id} className="flex items-center gap-3 px-3 py-2">
          <div className="min-w-0 flex-1">
            <p className="truncate text-[13px] text-ink">{r.name}</p>
            <p className="truncate text-[11.5px] text-ink-3">{r.status}</p>
          </div>
          <Switch on={items.includes(r.id)} onChange={(on) => flip(r.id, r.name, on)} label={r.name} />
        </div>
      ))}
      <TurnOnDialog name={asking?.name ?? null} sentence={f.confirm} onCancel={() => setAsking(null)} onConfirm={() => { if (asking) save(asking.id, true); setAsking(null); }} />
    </div>
  );
}

function entryDisplayTitle(list: ExtensionField, entry: JsonObject, index: number): string {
  const fields = list.items?.fields ?? [];
  for (const preferred of [true, false]) {
    for (const field of fields) {
      if (preferred && field.name !== "name") continue;
      const value = entry[field.name];
      if (typeof value === "string" && value.trim()) return value.trim();
    }
  }
  return t("settings-extensions.entry_untitled", { count: index + 1 });
}

/** A user-managed list of flat entries. IDs and secrets never become editable text fields. */
function EntryListControl(props: FieldProps & { id: string; ring: string }) {
  const { field: f, path, draft, errors, onEdit, secrets, onSecret, id, ring } = props;
  const entries = entryItems(draft, path);
  const [removing, setRemoving] = useState<{ key: string; title: string } | null>(null);
  const store = (next: JsonObject[]) => onEdit(path, next.length || f.required ? next : undefined);
  const max = f.max_items ?? 32;
  const clearsOnRebind = !!f.items?.fields?.some((field) => field.clears_secret);
  return (
    <div id={id} role="group" aria-label={fieldLabel(f)} data-entry-list={f.name} className={cx("flex flex-col gap-2 rounded-control", ring)}>
      {entries.map((entry, index) => {
        const key = entryKey(entry);
        const savedID = entryID(entry);
        const secret = savedID ? secrets?.find((s) => s.name === savedID && s.list === f.name) : undefined;
        const title = entryDisplayTitle(f, entry, index);
        return (
          <section key={key} data-entry-key={key} className="overflow-hidden rounded-control bg-surface shadow-hairline">
            <div className="flex items-center gap-2 border-b border-line px-3 py-2">
              <div className="min-w-0 flex-1">
                <p className="truncate text-meta font-medium text-ink">{title}</p>
                <p className={cx("mt-0.5 text-submeta", secret && !secret.set && secret.required ? "text-orange" : "text-ink-3")}>
                  {secret ? secretState(secret) : t("settings-extensions.entry_save_before_secret")}
                </p>
              </div>
              <Button tone="quiet" icon="key" disabled={!secret || !onSecret} onClick={() => secret && onSecret?.(secret)}>
                {t("settings-extensions.entry_secret")}
              </Button>
              <IconButton icon="x" label={t("settings-extensions.entry_remove", { entry: title })} disabled={!!f.required && entries.length <= (f.min_items ?? 0)} onClick={() => setRemoving({ key, title })} />
            </div>
            {errors[`${f.name}[${index}]._id`] && <p role="alert" className="px-4 pt-2 text-submeta text-red">{errors[`${f.name}[${index}]._id`]}</p>}
            {(f.items?.fields ?? []).map((field) => (
              <FieldRow key={field.name} {...props} field={field} path={[...path, String(index), field.name]} />
            ))}
          </section>
        );
      })}
      <div className="px-1 pt-1">
        <Button tone="secondary" icon="plus" disabled={entries.length >= max} onClick={() => store([...entries, {}])}>
          {t("settings-extensions.entry_add")}
        </Button>
        <p className="mt-2 text-submeta leading-relaxed text-ink-3">
          {t("settings-extensions.entry_editor_hint")}
          {clearsOnRebind ? ` ${t("settings-extensions.entry_rebind_hint")}` : ""}
        </p>
      </div>
      <Dialog
        open={!!removing}
        title={removing ? t("settings-extensions.entry_remove_title", { entry: removing.title }) : undefined}
        onClose={() => setRemoving(null)}
        footer={<><Button tone="quiet" onClick={() => setRemoving(null)}>{t("common.cancel")}</Button><Button tone="danger" onClick={() => {
          if (removing) store(entries.filter((entry) => entryKey(entry) !== removing.key));
          setRemoving(null);
        }}>{t("settings-extensions.entry_remove_confirm")}</Button></>}
      >
        <p className="text-meta leading-relaxed text-ink-2">{t("settings-extensions.entry_remove_body")}</p>
      </Dialog>
    </div>
  );
}

function ListControl({ field: f, path, draft, onEdit, id, ring }: Pick<FieldProps, "field" | "path" | "draft" | "onEdit"> & { id: string; ring: string }) {
  const [adding, setAdding] = useState("");
  const items = listItems(draft, path, f);
  const store = (list: string[]) => onEdit(path, list.length ? list : undefined);
  const choices = f.items?.enum;

  if (choices?.length) {
    return (
      <div id={id} role="group" className="flex flex-wrap gap-1.5">
        {choices.map((c) => {
          const value = typeof c.value === "string" ? c.value : String(c.value);
          const on = items.includes(value);
          return (
            <button
              key={value}
              type="button"
              aria-pressed={on}
              onClick={() => store(on ? items.filter((x) => x !== value) : [...items, value])}
              className={cx(
                "flex h-7 items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 text-[12px] transition-[background-color,box-shadow,color] duration-150",
                on ? "bg-accent-tint font-medium text-accent-ink shadow-[inset_0_0_0_1px_var(--accent)]" : "text-ink-2 shadow-hairline hover:bg-hover",
              )}
            >
              {on && <Icon name="check" size={12} stroke={2.2} />}
              {choiceLabel(c)}
            </button>
          );
        })}
      </div>
    );
  }

  const add = () => {
    const v = adding.trim();
    if (!v) return;
    store([...items, v]);
    setAdding("");
  };
  return (
    <div className="flex flex-col gap-1.5">
      {items.length > 0 && (
        <ul className="flex flex-wrap gap-1.5">
          {items.map((item, i) => (
            <li key={`${i}-${item}`} className="flex h-7 items-center gap-1 rounded-full bg-inset pr-1 pl-2.5 text-[12px] text-ink shadow-hairline">
              <span className="max-w-[240px] truncate">{item}</span>
              <IconButton icon="x" label={t("settings-extensions.remove_item", { item })} size={20} iconSize={11} onClick={() => store(items.filter((_, j) => j !== i))} />
            </li>
          ))}
        </ul>
      )}
      <div className="flex max-w-[360px] items-center gap-1.5">
        <input
          id={id}
          value={adding}
          onChange={(e) => setAdding(e.target.value)}
          onKeyDown={(e) => {
            if (composing(e.nativeEvent)) return;
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
          placeholder={t("settings-extensions.add_placeholder")}
          spellCheck={false}
          autoComplete="off"
          className={cx(inputClass, ring)}
        />
        <Button tone="secondary" icon="plus" onClick={add} disabled={!adding.trim()}>
          {t("settings-extensions.add")}
        </Button>
      </div>
    </div>
  );
}

// ── a secret ────────────────────────────────────────────

/** Sets one secret: typed in hidden, saved to the hub, never shown again; one that is set can be cleared. */
export function SecretDialog({ extension, secret, onClose, mutate }: { extension: string; secret: ExtensionSecret; onClose: () => void; mutate: ExtensionMutation }) {
  // a provider's reference in place of the value (docs/extensions/secret-providers.md), where the hub takes one
  const references = useStore((s) => hubHasExt(s.features, "secret_references"));
  const [byRef, setByRef] = useState(!!secret.reference);
  const [value, setValue] = useState(secret.reference?.reference ?? "");
  // shown while typing, to check what was pasted (a token cut short is a common mistake); a saved one is never shown
  const [shown, setShown] = useState(false);
  const [busy, setBusy] = useState<"" | "save" | "clear">("");
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const errorId = useId();
  // The field is disabled while the hub answers; a refused value stays typed in it, with the focus back to change it.
  useEffect(() => {
    if (!busy && error) input.current?.focus();
  }, [busy, error]);
  const run = async (kind: "save" | "clear") => {
    const v = value.trim();
    if (kind === "save" && !v) return;
    setBusy(kind);
    setError("");
    try {
      await mutate("secret", (client) => kind === "clear" ? clearExtensionSecret(extension, secret.name, client)
        : byRef ? setExtensionSecretReference(extension, secret.name, v, client) : setExtensionSecret(extension, secret.name, v, client));
      // A saved value leaves the page.
      setValue("");
      toast(t(kind === "clear" ? "settings-extensions.secret_cleared" : byRef ? "settings-extensions.secret_reference_saved" : "settings-extensions.secret_saved", { title: secret.title }), "ok");
      onClose();
    } catch (e) {
      if (!isAbort(e)) setError(errorText(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      width={440}
      title={secret.title}
      footer={
        <>
          {secret.set && (
            <span className="mr-auto">
              {confirm ? (
                <Button tone="danger" busy={busy === "clear"} disabled={!!busy} onClick={() => void run("clear")}>
                  {t("settings-extensions.confirm_clear")}
                </Button>
              ) : (
                <Button tone="quiet" icon="trash" disabled={!!busy} onClick={() => setConfirm(true)} className="text-red enabled:hover:text-red">
                  {t("settings-extensions.clear")}
                </Button>
              )}
            </span>
          )}
          <Button tone="quiet" onClick={onClose} disabled={!!busy}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy === "save"} disabled={!value.trim() || !!busy} onClick={() => void run("save")}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          void run("save");
        }}
      >
        <div className="flex items-center gap-2 text-[12px] text-ink-3">
          <Icon name="lock" size={13} className="shrink-0" />
          {secretState(secret)}
        </div>
        {references && (
          <div className="flex items-center justify-between gap-2 text-[13px] text-ink-2">
            <span>{t("settings-extensions.use_reference")}</span>
            <Switch
              label={t("settings-extensions.use_reference")}
              on={byRef}
              disabled={!!busy}
              onChange={(on) => {
                setByRef(on);
                setValue(on ? (secret.reference?.reference ?? "") : "");
                setError("");
              }}
            />
          </div>
        )}
        <div className="flex items-center gap-1.5">
          <input
            ref={input}
            type={byRef || shown ? "text" : "password"}
            value={value}
            onChange={(e) => (setValue(e.target.value), setError(""))}
            autoFocus
            autoComplete={byRef ? "off" : "new-password"}
            spellCheck={false}
            placeholder={byRef ? t("settings-extensions.reference_placeholder") : secret.set ? t("settings-extensions.secret_replace_placeholder") : t("settings-extensions.secret_placeholder")}
            aria-label={secret.title}
            aria-invalid={!!error || undefined}
            aria-describedby={error ? errorId : undefined}
            disabled={!!busy}
            className={cx(inputClass, "h-9 min-w-0 flex-1 font-mono shadow-inset-field")}
          />
          {!byRef && (
            <IconButton
              icon={shown ? "eyeOff" : "eye"}
              label={shown ? t("settings-extensions.secret_hide") : t("settings-extensions.secret_show")}
              onClick={() => setShown((x) => !x)}
              disabled={!!busy}
            />
          )}
        </div>
        {error && (
          <p id={errorId} role="alert" className="rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] break-words text-red select-text">
            {t("settings-extensions.not_saved", { error })}
          </p>
        )}
        <p className="text-[11.5px] leading-relaxed text-ink-3">{byRef ? t("settings-extensions.reference_hint") : secret.description || t("settings-extensions.secret_hint")}</p>
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

/**
 * A secret provider's page (docs/extensions/secret-providers.md): the settings that hold its references, each the
 * user's standing approval, and the Always rules of hand-offs to Agents, each removable (the next hand-off asks again).
 */
export function SecretProviderGroups({ extension, users, rules, editable, mutate }: {
  extension: string;
  users?: SecretUser[];
  rules?: HandoffRule[];
  editable: boolean;
  mutate: ExtensionMutation;
}) {
  const machines = useStore((s) => s.machines);
  const [removing, setRemoving] = useState<HandoffRule | null>(null);
  const [busy, setBusy] = useState(false);
  const machineName = (id: string) => machines.find((m) => m.id === id)?.name || id;
  const remove = async (rule: HandoffRule) => {
    setBusy(true);
    try {
      await mutate("secret", (client) => removeHandoffRule(extension, rule.id, client));
      toast(t("settings-extensions.rule_removed"), "ok");
      setRemoving(null);
    } catch (e) {
      if (!isAbort(e)) toast(errorText(e), "error");
    } finally {
      setBusy(false);
    }
  };
  const userLabel = (u: SecretUser) =>
    u.kind === "launcher"
      ? u.machine
        ? t("settings-extensions.user_launcher_machine", { name: u.launcher_name || u.launcher || "", machine: machineName(u.machine) })
        : t("settings-extensions.user_launcher", { name: u.launcher_name || u.launcher || "" })
      : `${u.extension_name || u.extension || ""} › ${u.secret_title || u.secret || ""}${u.entry_title ? ` (${u.entry_title})` : ""}`;
  return (
    <>
      {!!users?.length && (
        <div data-secret-users>
          <Group title={t("settings-extensions.users_title")} footer={t("settings-extensions.users_footer")}>
            {users.map((u, i) => (
              <Row key={`${u.kind}/${u.extension ?? u.launcher}/${u.secret ?? u.machine ?? ""}/${i}`} label={userLabel(u)} hint={u.entry_title ? `${u.entry_title} · ${u.reference}` : u.reference} wrapHint={!!u.entry_title} />
            ))}
          </Group>
        </div>
      )}
      {!!rules?.length && (
        <div data-handoff-rules>
          <Group title={t("settings-extensions.rules_title")} footer={t("settings-extensions.rules_footer")}>
            {rules.map((r) => (
              <Row key={r.id} label={t("settings-extensions.rule_label", { launcher: r.launcher, machine: machineName(r.machine) })} hint={r.reference}>
                {editable && (
                  <Button tone="quiet" disabled={busy} onClick={() => setRemoving(r)}>
                    {t("settings-extensions.rule_remove")}
                  </Button>
                )}
              </Row>
            ))}
          </Group>
        </div>
      )}
      {removing && (
        <Dialog
          open
          title={t("settings-extensions.rule_remove_title")}
          onClose={() => !busy && setRemoving(null)}
          footer={
            <>
              <Button tone="quiet" disabled={busy} onClick={() => setRemoving(null)}>
                {t("common.cancel")}
              </Button>
              <Button tone="danger" busy={busy} onClick={() => void remove(removing)}>
                {t("settings-extensions.rule_remove")}
              </Button>
            </>
          }
        >
          <p className="text-ink-2">{t("settings-extensions.rule_remove_body", { launcher: removing.launcher, machine: machineName(removing.machine) })}</p>
        </Dialog>
      )}
    </>
  );
}
