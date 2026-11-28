// Lazy-loaded unified Settings › Integrations & Extensions view (integrations_v2, #14874 / Refs #14802).
// Aggregates built-in services, channels, connectors, standalone push sources, and installed extensions
// into one deduplicated list with capability filter chips, subscription health alerts, Extension Market access,
// inline IntegrationDetail (#14868) mounting, and a lazy-loaded Data Flow view (#15423).
import { Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { hubServes, moduleFeature } from "../../core/module-host";
import { loadMailAccounts, useMailboxEnabled } from "../../lib/mail";
import { t } from "../../i18n";
import type { MailAccount } from "../../lib/agent-settings";
import { api, credentialOwner, enc, errorText, subscribeCredentialOwner } from "../../lib/api";
import { listChannels, type Channel } from "../../lib/channels";
import { connectorBrand, listConnectors, type HubConnector } from "../../lib/connectors";
import { scoped, useScope } from "../../lib/current-assistant";
import { hubHasExt } from "../../lib/extension-api";
import { onAuthChange, withAuth } from "../../lib/extension-auth";
import { watchOperations } from "../../lib/extension-operations";
import { EXTENSIONS_CHANGED } from "../../lib/extension-prompts";
import { extensionDetail, extensionTone, listExtensionsInfo, type HubExtension, type HubExtensionDetail, type JsonObject } from "../../lib/extensions";
import { ExtensionOwnerScope } from "../../lib/extension-owner";
import { botStatus, type FeishuBotStatus } from "../../lib/feishu-bot";
import { lazyPage } from "../../lib/lazy-page";
import { chooseManager, defaultManager, otherManager, pinManagerAt, useShownManager } from "../../lib/manager-choice";
import { managerOf, useRoster } from "../../lib/managers";
import { isResumed } from "../../lib/reconnect";
import { onHubEvent, useLarkOn, useStore } from "../../lib/store";
import {
  listSubscriptions,
  useInboxSourcesShown,
  useSubscriptionsShown,
  type HubSubscription,
} from "../../lib/subscriptions";
import { Button, cx, IconButton, Loading } from "../../ui/atoms";
import { BrandMark } from "../../ui/BrandMark";
import { ChannelMark } from "../../ui/ChannelMark";
import { ExtensionLogo } from "../../ui/ExtensionLogo";
import { Icon } from "../../ui/icons";
import { Segmented } from "../assistant/parts";
import { OfflineState, useShownOffline } from "../ManagerParts";
import { OwnerMark } from "./OwnerMark";
import { SentSettings } from "./SentSettings";
import { Group, Pill, Row, SettingsHint } from "./SettingsParts";
import {
  integrationManager,
  integrationScope,
  isIntegrationDetailRoute,
  type IntegrationFilter,
  type IntegrationsRoute,
  type IntegrationView,
} from "./integrations-route";
import {
  buildIntegrationCatalog,
  filterIntegrationItems,
  isIntegrationRowSelected,
  resolveIntegrationTarget,
  resolveSourceItem,
  type IntegrationItem,
  type IntegrationSubscriptionAlert,
  type IntegrationTone,
  type LarkGatewaySummary,
} from "./integrations/catalog";
import { capabilityLabel, projectedCatalogItem, projectedResource, projectionSourceErrors } from "./integrations/projectedCatalog";
import { editableSection, IntegrationReadProblem, projectedIntegrationTarget, projectedSections, type ProjectedDetail } from "./integrations/projectedDetail";
import { IntegrationDetailShell, originBadgeLabel } from "./integrations/DetailShell";
import { integrationResourceKey } from "./integrations/model";
import { useIntegrationCatalogProjection, useIntegrationDetailProjection, useIntegrationProjectionMode, useProjectedMarkets } from "./integrations/useIntegrationProjection";

const IntegrationDetail = lazyPage(() => import("./IntegrationDetail").then((m) => m.IntegrationDetail));
const MarketCatalog = lazyPage(() => import("./ExtensionMarket").then((m) => m.MarketCatalog));
const MarketItem = lazyPage(() => import("./ExtensionMarket").then((m) => m.MarketItem));
const Markets = lazyPage(() => import("./ExtensionMarket").then((m) => m.Markets));
const ExtensionUI = lazyPage(() => import("./ExtensionView").then((m) => m.ExtensionUI));

// Lazily load SourcesUI (and its SourcesGraph chunk) only when opening the Data Flow view or a push source detail.
const LazySourcesUI = lazyPage(() => import("../subscriptions/SourcesUI").then((m) => m.SourcesUI));

export interface IntegrationsProps {
  route: IntegrationsRoute;
  prefill?: JsonObject;
  onOpen: (route: IntegrationsRoute, action?: "push" | "replace" | "root") => void;
  onBack: () => void;
  /** Desktop split-pane layout (when the Settings host has room): list stays beside the open integration detail. */
  twoCol?: boolean;
  /** The page's top bar already provides Back (phone), so subviews omit their own Back button. */
  headerBack?: boolean;
  parentView?: IntegrationView;
  /** Navigate to a Settings URL linked from a channel override notice. */
  onSettings?: (url: string, manager?: string) => void;
  /** Report whether an open extension plugin_page needs the wide page container. */
  onPageWide?: (wide: boolean) => void;
}

const DOT_CLASS: Record<IntegrationTone, string> = {
  green: "bg-green",
  orange: "bg-orange",
  red: "bg-red",
  idle: "bg-line-strong",
};

const EXT_TILE: Record<ReturnType<typeof extensionTone>, string> = {
  red: "bg-red-tint text-red",
  orange: "bg-orange-tint text-orange",
  green: "bg-accent-tint text-accent-ink",
  idle: "bg-inset text-ink-3 shadow-hairline",
};

const FILTER_CHIPS: readonly { id: IntegrationFilter; labelKey: string }[] = [
  { id: "all", labelKey: "settings-integrations.filter_all" },
  { id: "channels", labelKey: "settings-integrations.filter_channels" },
  { id: "data", labelKey: "settings-integrations.filter_data" },
  { id: "tools", labelKey: "settings-integrations.filter_tools" },
];

function ItemMark({ item }: { item: IntegrationItem }) {
  if (item.kind === "extension") {
    return (
      <ExtensionLogo
        url={item.extension?.icon_url}
        manager={item.extension?.manager ?? item.manager ?? ""}
        iconSize={15}
        className={cx("flex size-8 shrink-0 items-center justify-center rounded-control", EXT_TILE[item.extension ? extensionTone(item.extension) : item.tone])}
      />
    );
  }
  if (item.kind === "source") {
    const toneClass =
      item.alertTone === "red"
        ? "bg-red-tint text-red"
        : item.alertTone === "orange"
          ? "bg-orange-tint text-orange"
          : "bg-accent-tint text-accent-ink";
    return (
      <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-control", toneClass)}>
        <Icon name="bell" size={15} />
      </span>
    );
  }
  if (item.channel) {
    return <ChannelMark c={item.channel} size={32} />;
  }
  const brand = connectorBrand(item.id === "feishu-bot" ? "lark" : item.id);
  if (brand) {
    return (
      <span className="flex size-8 shrink-0 items-center justify-center rounded-control bg-surface shadow-hairline">
        <BrandMark brand={brand} size={32} fit="tile" />
      </span>
    );
  }
  const icon = item.id === "mail" ? "mail" : item.id === "imessage" || item.id === "lark" ? "chat" : item.id === "feishu-bot" ? "send" : "link";
  return (
    <span className="flex size-8 shrink-0 items-center justify-center rounded-control bg-accent-tint text-accent-ink">
      <Icon name={icon} size={15} />
    </span>
  );
}

function SubscriptionAlertBadge({ alert }: { alert: IntegrationSubscriptionAlert }) {
  return (
    <span
      data-subscription-alert-badge={alert.state}
      className={cx(
        "inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-submeta font-medium whitespace-nowrap",
        alert.tone === "red" ? "bg-red-tint text-red" : "bg-orange-tint text-orange",
      )}
    >
      <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", DOT_CLASS[alert.tone])} />
      {alert.badgeText}
    </span>
  );
}

/** Detail pane for a standalone push source (or single source deep link), composed through IntegrationDetailShell. */
function PushSourceDetailPane({
  state,
  item,
  manager,
  onBack,
  backLabel,
  onSwitchAssistant,
  onOpenDataFlow,
  reloadKey,
  onReload,
  projection,
  resourceId,
}: {
  state: string;
  item: IntegrationItem | null;
  manager?: string;
  onBack?: () => void;
  backLabel?: string;
  onSwitchAssistant?: () => void;
  onOpenDataFlow?: () => void;
  reloadKey: number;
  onReload: () => void;
  projection?: ProjectedDetail;
  resourceId?: string;
}) {
  const title = item?.name ?? t("subscriptions.title_sources");
  const source = projectedSections(projection, "inbox_and_sync").find(({ ref }) =>
    (ref.kind === "push_source" || ref.kind === "subscription") && ref.id === resourceId);
  const readOnly = source && !editableSection(source);
  const resourceKind = projection?.detail.item.ref.kind === "subscription" || item?.ref?.kind === "subscription"
    ? "subscription"
    : "push_source";
  const resolvedId = projection?.detail.item.ref.id ?? item?.id ?? resourceId ?? state;
  const resourceKey = integrationResourceKey(manager, resourceKind, resolvedId);

  const sourceContent = (
    <>
      {readOnly && <Group><SettingsHint>{t("settings-integrations.projection_read_only")}</SettingsHint></Group>}
      {(!projection || (source && editableSection(source))) && (
        <Suspense fallback={<Loading />}>
          <LazySourcesUI key={`${manager ?? ""}:${state}:${reloadKey}`} title={title} state={state} manager={manager} />
        </Suspense>
      )}
      {onOpenDataFlow && (
        <Group anchor="source-data-flow">
          <Row
            anchor="source-data-flow-entry"
            icon="loop"
            label={t("settings-integrations.view_sources")}
            hint={t("settings-integrations.source_data_flow_hint")}
            wrapHint
            onClick={onOpenDataFlow}
          />
        </Group>
      )}
    </>
  );

  return (
    <IntegrationDetailShell
      id={item?.id ?? "source"}
      kind="source"
      resourceKey={resourceKey}
      manager={manager}
      onBack={onBack}
      backLabel={backLabel}
      onSwitchAssistant={onSwitchAssistant}
      onRefresh={onReload}
      hideHeaderCard={!item}
      mark={item ? <ItemMark item={item} /> : undefined}
      title={item?.name}
      badge={item ? originBadgeLabel(item.origin, "source", { subscription: item.ref?.kind === "subscription" }) : undefined}
      status={item ? (
        <>
          <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", DOT_CLASS[item.tone])} />
          <span className="truncate">{item.statusText}</span>
        </>
      ) : undefined}
      headerBody={item?.description ? <p className="text-meta leading-relaxed text-ink-2">{item.description}</p> : undefined}
      projection={projection}
      slots={{ inbox: sourceContent }}
    />
  );
}

/** Full-width Data Flow pane for the route's assistant. */
function SourcesFlowPane({
  onOpenSource,
  manager,
  reloadKey,
}: {
  manager?: string;
  reloadKey: number;
  onOpenSource?: (page: string) => void;
}) {
  return (
    <>
      {manager && (
        <div className="mb-3 flex items-center gap-2">
          <OwnerMark manager={manager} />
        </div>
      )}
      <Suspense fallback={<Loading />}>
        <LazySourcesUI
          key={`${manager ?? ""}:${reloadKey}`}
          title={t("subscriptions.title_sources")}
          initialView="graph"
          onOpenSource={onOpenSource}
          manager={manager}
        />
      </Suspense>
    </>
  );
}

/** A declarative page contributed by an extension, rendered under Settings › Integrations. */
function IntegrationExtensionPage({
  id,
  ui,
  manager,
  onBack,
  onWide,
}: {
  id: string;
  ui: string;
  manager?: string;
  onBack?: () => void;
  onWide?: (wide: boolean) => void;
}) {
  useLayoutEffect(() => pinManagerAt([`/extensions/${enc(id)}`, `/plugins/${enc(id)}`], manager), [id, manager]);
  const [detail, setDetail] = useState<HubExtensionDetail | null>(null);
  const [reloadCount, setReloadCount] = useState(0);
  useEffect(() => {
    extensionDetail(id).then(setDetail, () => {});
  }, [id]);
  useEffect(() => () => onWide?.(false), [onWide]);
  const title = detail?.ui?.find((u) => u.id === ui)?.title;
  return (
    <>
      <div className="mb-3 flex items-center gap-2">
        {onBack && (
          <Button tone="quiet" icon="chevronLeft" onClick={onBack} className="-ml-2">
            <span className="max-w-[240px] truncate">{detail?.name ?? id}</span>
          </Button>
        )}
        <span className="flex-1" />
        <IconButton icon="retry" label={t("settings-extensions.redraw")} onClick={() => setReloadCount((x) => x + 1)} />
      </div>
      <ExtensionOwnerScope owner={id}>
        <Suspense fallback={<Loading />}>
          <ExtensionUI key={reloadCount} extension={id} ui={ui} title={title} manager={manager} onWide={onWide} />
        </Suspense>
      </ExtensionOwnerScope>
    </>
  );
}

export function Integrations(props: IntegrationsProps) {
  const current = useShownManager();
  const owner = integrationManager(props.route.scope) ?? otherManager(current);
  const credential = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  return <IntegrationsForManager key={JSON.stringify([credential, owner ?? current])} {...props} owner={owner} current={current} />;
}

function IntegrationsForManager({
  current,
  owner,
  route,
  prefill,
  onOpen,
  onBack,
  twoCol = false,
  headerBack = false,
  parentView,
  onSettings,
  onPageWide,
}: IntegrationsProps & { owner?: string; current: string }) {
  const { view, filter } = route;
  const roster = useRoster();
  const scope = useScope();
  const shownOffline = useShownOffline();
  const offline = shownOffline?.manager.id === (owner ?? current) ? shownOffline : null;
  const resourceOwner = owner ?? (scope.several ? current : undefined);
  const go = (view: IntegrationView, manager = resourceOwner, action?: "push" | "replace" | "root") =>
    onOpen({ scope: integrationScope(manager), view, filter }, action);

  const hasExtensions = useStore((s) => hubHasExt(s.features, "manager"));
  const hasChannels = useStore((s) => s.features.includes("channels"));
  const hasConnectors = useStore((s) => s.features.includes("connectors"));
  const hasMail = useMailboxEnabled();
  const hasLark = useLarkOn();
  const larkModule = useStore((s) => hubServes(moduleFeature("lark"), s));
  const announcesLarkModule = useStore((s) => s.features.includes(moduleFeature("lark")));
  const hasSubscriptions = useSubscriptionsShown();
  const hasInboxSources = useInboxSourcesShown();
  const mode = useIntegrationProjectionMode(owner ?? current);
  const legacy = mode.mode === "legacy";
  const showDetail = isIntegrationDetailRoute(view);
  const projectCatalog = mode.mode === "projection" && (view.kind === "list" || showDetail);
  const managerId = owner ?? current;
  const catalog = useIntegrationCatalogProjection(managerId, projectCatalog);
  const { value: projectedMarkets, refresh: refreshMarkets } = useProjectedMarkets(managerId, projectCatalog && hasExtensions);
  const requested = projectedResource(view, catalog.value?.items);
  const detail = useIntegrationDetailProjection(managerId, projectCatalog && (view.kind !== "source" || catalog.value) ? requested : null);
  const refreshCatalog = catalog.refresh;
  const refreshDetail = detail.refresh;
  const reloadDetail = useCallback(() => {
    refreshDetail();
    refreshCatalog();
  }, [refreshDetail, refreshCatalog]);
  const projection = useMemo(() => detail.value && { detail: detail.value, refresh: reloadDetail, loading: detail.loading },
    [detail.value, reloadDetail, detail.loading]);

  const setFilter = (filter: IntegrationFilter) => onOpen({ ...route, filter }, "replace");

  const [extensions, setExtensions] = useState<HubExtension[]>([]);
  const [legacyMarkets, setMarkets] = useState(false);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [connectors, setConnectors] = useState<HubConnector[]>([]);
  const [subscriptions, setSubscriptions] = useState<HubSubscription[]>([]);
  const [mailAccounts, setMailAccounts] = useState<MailAccount[] | null>();
  const [larkStatus, setLarkStatus] = useState<LarkGatewaySummary | null>();
  const [feishuBotStatus, setFeishuBotStatus] = useState<FeishuBotStatus | null>(null);
  const [legacyLoaded, setLoaded] = useState(false);
  const [legacyLoading, setLoading] = useState(false);
  const [partialErrors, setPartialErrors] = useState<string[]>([]);
  const [subscriptionError, setSubscriptionError] = useState("");
  const [sourcesReload, setSourcesReload] = useState(0);

  const seq = useRef(0);
  const subscriptionSeq = useRef(0);
  const loadSubscriptionsOnly = useCallback(async () => {
    const mine = ++subscriptionSeq.current;
    if (!hasSubscriptions && !hasInboxSources) {
      setSubscriptions([]);
      setSubscriptionError("");
      return;
    }
    try {
      const list = await listSubscriptions(owner);
      if (mine !== subscriptionSeq.current) return;
      setSubscriptions(list);
      setSubscriptionError("");
    } catch (error) {
      if (mine === subscriptionSeq.current) setSubscriptionError(errorText(error));
    }
  }, [hasSubscriptions, hasInboxSources, owner]);

  const loadAll = useCallback(async () => {
    const mine = ++seq.current;
    setLoading(true);
    setPartialErrors([]);
    // Publish each result on arrival. Only the empty state and final counts need every answer.
    const publish = <T,>(apply: (result: T) => void) => (result: T) => {
      if (mine === seq.current) apply(result);
    };
    const report = publish((error: unknown) => setPartialErrors((errors) => [...errors, errorText(error)]));

    const applyExtensions = (extensions: HubExtension[], markets: boolean) => {
      setExtensions(extensions);
      setMarkets(markets);
    };
    await Promise.all([
      hasExtensions
        ? listExtensionsInfo().then(publish((result) => applyExtensions(result.extensions, result.markets)), report)
        : applyExtensions([], false),
      hasChannels ? listChannels(owner).then(publish(setChannels), report) : setChannels([]),
      hasConnectors ? listConnectors().then(publish(setConnectors), report) : setConnectors([]),
      hasMail ? loadMailAccounts(owner).then(publish(setMailAccounts), (error) => {
        report(error);
        publish(setMailAccounts)(null);
      }) : setMailAccounts(undefined),
      hasLark ? api<LarkGatewaySummary>("/lark/status").then(publish(setLarkStatus), (error) => {
        report(error);
        publish(setLarkStatus)(null);
      }) : setLarkStatus(undefined),
      larkModule ? botStatus().then(publish(setFeishuBotStatus), report) : setFeishuBotStatus(null),
      loadSubscriptionsOnly(),
    ]);

    if (mine !== seq.current) return;
    setLoaded(true);
    setLoading(false);
  }, [hasExtensions, hasChannels, hasConnectors, hasMail, hasLark, larkModule, loadSubscriptionsOnly, owner]);

  useEffect(() => {
    if (!legacy) return;
    void loadAll();
    return () => {
      seq.current++;
      subscriptionSeq.current++;
    };
  }, [legacy, loadAll]);

  // Refresh subscriptions when a subscription state changes on the hub (at most once per second).
  useEffect(() => {
    if (!legacy && !projectCatalog) return;
    if (!hasSubscriptions && !hasInboxSources) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const off = onHubEvent("subscription", () => {
      clearTimeout(timer);
      timer = setTimeout(() => projectCatalog ? refreshCatalog() : void loadSubscriptionsOnly(), 1000);
    });
    return () => {
      clearTimeout(timer);
      off();
    };
  }, [hasSubscriptions, hasInboxSources, legacy, projectCatalog, loadSubscriptionsOnly, refreshCatalog]);

  const reloadList = useCallback(() => {
    if (legacy) void loadAll();
    if (projectCatalog) {
      refreshCatalog();
      refreshDetail();
      refreshMarkets();
    }
  }, [legacy, projectCatalog, loadAll, refreshCatalog, refreshDetail, refreshMarkets]);

  // Reload list when an extension is installed, upgraded, or uninstalled.
  useEffect(() => {
    let active = true;
    let queued = false;
    const again = () => {
      if (queued) return;
      queued = true;
      queueMicrotask(() => {
        queued = false;
        if (active) reloadList();
      });
    };
    window.addEventListener(EXTENSIONS_CHANGED, again);
    const stop = watchOperations(() => {});
    return () => {
      active = false;
      window.removeEventListener(EXTENSIONS_CHANGED, again);
      stop();
    };
  }, [reloadList]);

  // Keep extension auth updates reflected immediately on list rows.
  useEffect(() => onAuthChange((id, auth, from) => {
    if (legacy) setExtensions((cur) => cur.map((x) => ((x.manager || defaultManager()) === from ? withAuth(x, id, auth) : x)));
    if (projectCatalog && from === managerId) reloadDetail();
  }), [legacy, projectCatalog, managerId, reloadDetail]);

  useEffect(() => onHubEvent("hello", (m) => !isResumed(m) && reloadList()), [reloadList]);

  const named = integrationManager(route.scope);
  const assistantDefault = defaultManager();
  const ownExtensions = useMemo(() => named
    ? extensions.filter((item) => (item.manager || assistantDefault) === named)
    : scoped(extensions, scope), [extensions, scope, named, assistantDefault]);
  const ownConnectors = useMemo(() => named
    ? connectors.filter((item) => (item.manager || assistantDefault) === named)
    : scoped(connectors, scope), [connectors, scope, named, assistantDefault]);

  const items = useMemo(() => {
    if (!legacy) return catalog.value?.items.filter((item) => item.availability.state !== "module_off").map(projectedCatalogItem) ?? [];
    return (
      buildIntegrationCatalog({
        extensions: ownExtensions,
        channels: larkModule ? channels : channels.filter((c) => c.id !== "feishu"),
        connectors: larkModule ? ownConnectors : ownConnectors.filter((c) => c.id !== "larkcli"),
        subscriptions,
        hasMail: hasMail && mailAccounts !== undefined,
        hasLark: hasLark && larkStatus !== undefined,
        hasFeishuBot: larkModule && (announcesLarkModule || feishuBotStatus !== null),
        mailAccounts,
        larkStatus,
        feishuBotStatus,
        ownerManager: owner,
      })
    );
  }, [legacy, catalog.value, ownExtensions, channels, ownConnectors, subscriptions, hasMail, hasLark, larkModule, announcesLarkModule, mailAccounts, larkStatus, feishuBotStatus, owner]);

  const counts = useMemo(
    () => legacy ? {
      all: items.length,
      channels: items.filter((it) => it.capabilities.includes("channels")).length,
      data: items.filter((it) => it.capabilities.includes("data")).length,
      tools: items.filter((it) => it.capabilities.includes("tools")).length,
    } : catalog.value?.counts,
    [legacy, items, catalog.value],
  );
  const loaded = legacy ? legacyLoaded : !!catalog.value;
  const loading = legacy ? legacyLoading : catalog.loading || mode.mode === "pending";
  const markets = legacy ? legacyMarkets : projectedMarkets;

  const filtered = useMemo(() => filterIntegrationItems(items, filter), [items, filter]);
  const builtins = useMemo(() => filtered.filter((it) => it.kind === "builtin"), [filtered]);
  const builtinExtensions = useMemo(() => filtered.filter((it) => it.kind === "extension" && it.builtinExtension), [filtered]);
  const pushSources = useMemo(() => filtered.filter((it) => it.kind === "source"), [filtered]);
  const installedExtensions = useMemo(() => filtered.filter((it) => it.kind === "extension" && !it.builtinExtension), [filtered]);

  const isSourcesView = view.kind === "data-flow";
  const sourceDetailState = view.kind === "source" ? view.state : null;
  const compactList = showDetail && twoCol;
  const listScrollRef = useRef<HTMLDivElement>(null);
  const detailScrollRef = useRef<HTMLDivElement>(null);
  const viewKey = view.kind === "source" ? view.state : "id" in view ? view.id : view.kind;

  useLayoutEffect(() => {
    if (compactList && detailScrollRef.current) {
      detailScrollRef.current.scrollTop = 0;
    }
  }, [compactList, view.kind, viewKey]);

  // Full-width subviews (Market catalog, Market item, Manage markets, Extension UI page, Sent records).
  if (view.kind === "catalog") {
    return (
      <Suspense fallback={<Loading />}>
        <MarketCatalog
          manager={owner}
          backLabel={t("settings-integrations.back")}
          onBack={headerBack ? undefined : onBack}
          onOpen={(it) => go({ kind: "item", market: it.market, id: it.id })}
          onManageMarkets={() => go({ kind: "markets" })}
        />
      </Suspense>
    );
  }
  if (view.kind === "item") {
    return (
      <Suspense fallback={<Loading />}>
        <MarketItem
          key={`${view.market}/${view.id}`}
          market={view.market}
          id={view.id}
          manager={owner}
          onBack={headerBack ? undefined : onBack}
          onInstalled={(id) => {
            if (parentView?.kind === "extension" && parentView.id === id) onBack();
            else go({ kind: "extension", id }, resourceOwner, "replace");
          }}
        />
      </Suspense>
    );
  }
  if (view.kind === "markets") {
    return (
      <Suspense fallback={<Loading />}>
        <Markets manager={owner} onBack={headerBack ? undefined : onBack} />
      </Suspense>
    );
  }
  if (view.kind === "page") {
    return (
      <IntegrationExtensionPage
        key={`${view.id}/${view.ui}`}
        id={view.id}
        ui={view.ui}
        manager={owner}
        onBack={headerBack ? undefined : onBack}
        onWide={onPageWide}
      />
    );
  }
  if (view.kind === "sent") {
    return (
      <>
        {!headerBack && (
          <Button tone="quiet" icon="chevronLeft" onClick={onBack} className="-ml-2 mb-3">
            {t("settings-integrations.back")}
          </Button>
        )}
        <h2 className="mb-2 px-1 text-card-title font-semibold text-ink">{t("settings-sent.entry_label")}</h2>
        <SentSettings />
      </>
    );
  }

  const resolvedTarget = projection ? projectedIntegrationTarget(projection)
    : legacy && showDetail && !sourceDetailState ? resolveIntegrationTarget(route, items, current) : null;
  const projectedSource = sourceDetailState && projection && projection.detail.item.ref.id === requested?.id &&
    projection.detail.item.ref.kind === requested.kind ? projectedCatalogItem(projection.detail.item) : null;
  const resolvedSourceItem = legacy ? resolveSourceItem(route, items, current) : projectedSource;

  const renderRow = (item: IntegrationItem) => {
    const selected = item.ref && projection ? item.ref.kind === projection.detail.item.ref.kind && item.ref.id === projection.detail.item.ref.id
      : item.ref && "id" in view ? view.kind === item.view.kind && view.id === item.id
      : isIntegrationRowSelected(route, item, current);
    const status = (
      <span
        className={cx(
          "flex items-center gap-1.5 whitespace-nowrap",
          compactList ? "mt-0.5 min-w-0 text-submeta" : "shrink-0 text-label",
          item.alertTone === "red" ? "text-red" : item.alertTone === "orange" ? "text-orange" : "text-ink-2",
        )}
      >
        <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", DOT_CLASS[item.tone])} />
        <span className={cx(compactList && "truncate")}>{item.statusText}</span>
      </span>
    );
    return (
      <button
        key={item.key}
        type="button"
        data-integration-row={item.key}
        data-integration-kind={item.kind}
        data-integration-source={item.origin ?? item.extension?.source}
        data-integration-id={item.id}
        data-integration-sub-alert={item.subscriptionAlert?.state}
        aria-current={selected || undefined}
        onClick={() => go(item.view, item.manager ?? resourceOwner, compactList ? "replace" : "push")}
        className={cx(
          "flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors",
          selected ? "bg-accent-tint" : "hover:bg-hover",
        )}
      >
        <ItemMark item={item} />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1">
            <span className="max-w-full truncate text-meta font-medium text-ink">{item.name}</span>
            {!compactList && item.builtinExtension && (
              <Pill>{t("settings-integrations.builtin_badge")}</Pill>
            )}
            {!compactList &&
              item.capabilities.map((cap) => (
                <Pill key={cap}>{t(capabilityLabel[cap])}</Pill>
              ))}
            {!compactList && item.subscriptionAlert && item.kind !== "source" && (
              <SubscriptionAlertBadge alert={item.subscriptionAlert} />
            )}
          </div>
          {compactList && status}
          {item.subtitle && (!compactList || item.alertTone) && (
            <div
              className={cx(
                "mt-0.5 truncate text-submeta",
                item.alertTone === "red" ? "text-red" : item.alertTone === "orange" ? "text-orange" : "text-ink-3",
              )}
            >
              {item.subtitle}
            </div>
          )}
        </div>
        {!compactList && status}
        <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
      </button>
    );
  };

  const listHeader = (
    <div className={cx(compactList ? "mb-2 flex items-center gap-1.5" : "mb-3 flex flex-wrap items-center gap-2")}>
      {!headerBack && (
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-card-title font-semibold text-ink">{t("settings-integrations.list_title")}</h1>
        </div>
      )}
      {headerBack && <span className="flex-1" />}
      {markets && (
        <Button
          tone="secondary"
          size={compactList ? "sm" : undefined}
          icon="search"
          title={compactList ? t("settings-integrations.browse_market_action") : undefined}
          testId="integrations-market-entry"
          onClick={() => go({ kind: "catalog" })}
        >
          {t(compactList ? "settings-integrations.browse_market_compact" : "settings-integrations.browse_market_action")}
        </Button>
      )}
      <IconButton
        icon="retry"
        label={t("settings-integrations.refresh")}
        onClick={() => isSourcesView ? setSourcesReload((n) => n + 1) : mode.mode === "error" ? mode.retry() : reloadList()}
        disabled={!isSourcesView && loading}
      />
    </div>
  );

  const crossAssistantSources = isSourcesView && named !== undefined && named !== current;
  const targetAssistantName = named ? managerOf(roster, named)?.name ?? named : "";

  const viewSwitch = hasInboxSources ? (
    <div className={cx(compactList ? "mb-2" : "mb-3 flex")} data-integrations-view-switch>
      <Segmented
        label={t("settings-integrations.view_label")}
        value={isSourcesView ? "sources" : "list"}
        compact={compactList}
        options={[
          { v: "list", label: t("settings-integrations.view_list") },
          { v: "sources", label: t("settings-integrations.view_sources") },
        ]}
        onChange={(v) => {
          if (v === "sources") go({ kind: "data-flow" });
          else if (parentView?.kind === "list") onBack();
          else go({ kind: "list" }, resourceOwner, "root");
        }}
      />
    </div>
  ) : null;

  // Full-width Data Flow view:
  // natural single-scroll container so SourcesGraph can use the full wide canvas, with the forwarding setting.
  if (isSourcesView) {
    return (
      <div data-integrations-sources-view>
        {listHeader}
        {!headerBack &&
          (crossAssistantSources ? (
            <div className="mb-3 flex flex-wrap items-center gap-2" data-integrations-cross-assistant-nav>
              <Button
                tone="quiet"
                icon="chevronLeft"
                testId="integrations-cross-assistant-back"
                onClick={onBack}
                className="-ml-2"
              >
                {t("settings-integrations.back")}
              </Button>
              {targetAssistantName && (
                <Button
                  tone="secondary"
                  size="sm"
                  testId="integrations-switch-assistant"
                  onClick={() => {
                    chooseManager(otherManager(named) ?? null);
                    onOpen({ scope: integrationScope(), view: { kind: "list" }, filter }, "root");
                  }}
                >
                  {t("settings-integrations.switch_assistant_integrations", { name: targetAssistantName })}
                </Button>
              )}
            </div>
          ) : (
            viewSwitch
          ))}
        <SourcesFlowPane
          manager={owner}
          reloadKey={sourcesReload}
          onOpenSource={(state) => go({ kind: "source", state })}
        />
      </div>
    );
  }

  const filterBar = (
    <div
      role="group"
      aria-label={t("settings-integrations.filter_label")}
      data-integrations-filters
      className={cx(
        compactList
          ? "-mx-1 -my-0.5 flex flex-nowrap items-center gap-1 overflow-x-auto overscroll-x-contain px-1 py-0.5 integrations-chip-scroll"
          : "mb-4 flex flex-wrap items-center gap-1.5",
      )}
    >
      {FILTER_CHIPS.map((chip) => {
        const active = filter === chip.id;
        const count = counts?.[chip.id];
        return (
          <button
            key={chip.id}
            type="button"
            data-integration-filter={chip.id}
            aria-pressed={active}
            onClick={() => setFilter(chip.id)}
            className={cx(
              "inline-flex shrink-0 items-center rounded-full whitespace-nowrap transition-colors",
              compactList ? "h-6 gap-1 px-1.5 text-submeta" : "h-7 gap-1.5 px-3 text-label",
              active
                ? "bg-ink font-medium text-surface"
                : "bg-surface text-ink-2 shadow-hairline hover:bg-hover hover:text-ink",
            )}
          >
            <span>{t(chip.labelKey)}</span>
            {loaded && count !== undefined && <span className={cx("tabular-nums", active ? "opacity-80" : "text-ink-3")}>{count}</span>}
          </button>
        );
      })}
    </div>
  );

  const loadErrors = legacy ? (subscriptionError ? [...partialErrors, subscriptionError] : partialErrors)
    : projectionSourceErrors(catalog.value?.availability ?? []);
  const catalogError = mode.mode === "error" ? mode.error : catalog.error;
  const retryCatalog = mode.mode === "error" ? mode.retry : reloadList;
  const listBody = (
    <>
      {catalogError && (
        <IntegrationReadProblem text={t("settings-integrations.projection_load_error", { error: catalogError })} retry={retryCatalog} />
      )}
      {loadErrors.length > 0 && (
        <div className="mb-4">
          {legacy ? <Group><SettingsHint alert>{t("settings-integrations.list_partial_error", { error: loadErrors.join(" · ") })}</SettingsHint></Group>
            : <IntegrationReadProblem text={t("settings-integrations.list_partial_error", { error: loadErrors.join(" · ") })} retry={retryCatalog} busy={catalog.loading} />}
        </div>
      )}

      {!loaded && !catalogError && filtered.length === 0 && (
        <Group anchor="integrations">
          <div className="px-4 py-3">
            <Loading />
          </div>
        </Group>
      )}

      {loaded && filtered.length === 0 && !catalogError && (legacy || loadErrors.length === 0) && (
        <Group anchor="integrations">
          {offline && items.length === 0 ? (
            <OfflineState offline={offline} ring="ring-surface" className="py-8" />
          ) : (
            <div className="flex flex-col items-center gap-2 px-6 py-8 text-center" data-integrations-empty>
              <span className="flex size-10 items-center justify-center rounded-card bg-inset text-ink-3 shadow-hairline">
                <Icon name="plug" size={18} />
              </span>
              <p className="text-meta font-medium text-ink">
                {t(filter === "all" ? "settings-integrations.empty_all_title" : "settings-integrations.empty_filtered_title")}
              </p>
              <p className="max-w-[420px] text-label leading-relaxed text-ink-3">
                {t(filter === "all" ? "settings-integrations.empty_all_detail" : "settings-integrations.empty_filtered_detail")}
              </p>
              <div className="mt-1 flex flex-wrap items-center justify-center gap-2">
                {filter !== "all" && (
                  <Button tone="secondary" onClick={() => setFilter("all")}>
                    {t("settings-integrations.show_all")}
                  </Button>
                )}
                {markets && (
                  <Button tone="accent" icon="search" onClick={() => go({ kind: "catalog" })}>
                    {t("settings-integrations.browse_market_action")}
                  </Button>
                )}
              </div>
            </div>
          )}
        </Group>
      )}

      {builtins.length > 0 && (
        <Group
          anchor="integrations"
          title={
            <span className="flex items-center gap-2">
              {t("settings-integrations.group_builtin")}
              <span className="font-normal">{t("settings-integrations.count", { count: builtins.length })}</span>
            </span>
          }
        >
          {builtins.map((item) => renderRow(item))}
        </Group>
      )}

      {builtinExtensions.length > 0 && (
        <Group
          anchor={builtins.length === 0 ? "integrations" : "builtin-extensions"}
          title={
            <span className="flex items-center gap-2">
              {t("settings-integrations.group_builtin_extensions")}
              <span className="font-normal">{t("settings-integrations.count", { count: builtinExtensions.length })}</span>
            </span>
          }
        >
          {builtinExtensions.map((item) => renderRow(item))}
        </Group>
      )}

      {pushSources.length > 0 && (
        <Group
          anchor="push-sources"
          title={
            <span className="flex items-center gap-2">
              {t(pushSources.some((item) => item.ref?.kind === "subscription") ? "subscriptions.title" : "settings-integrations.group_push_sources")}
              <span className="font-normal">{t("settings-integrations.count", { count: pushSources.length })}</span>
            </span>
          }
        >
          {pushSources.map((item) => renderRow(item))}
        </Group>
      )}

      {installedExtensions.length > 0 && (
        <Group
          anchor={builtins.length === 0 && builtinExtensions.length === 0 && pushSources.length === 0 ? "integrations" : "extensions"}
          title={
            <span className="flex items-center gap-2">
              {t("settings-integrations.group_extensions")}
              <span className="font-normal">{t("settings-integrations.count", { count: installedExtensions.length })}</span>
            </span>
          }
        >
          {installedExtensions.map((item) => renderRow(item))}
        </Group>
      )}

      <Group anchor="sent">
        <Row icon="send" label={t("settings-sent.entry_label")} hint={t("settings-sent.hint")} onClick={() => go({ kind: "sent" })} />
      </Group>
    </>
  );

  const detailBack = !headerBack && (!twoCol || parentView?.kind !== "list") ? onBack : undefined;
  const pushSourceBackLabel = parentView?.kind === "data-flow" ? t("subscriptions.view_graph")
    : parentView && parentView.kind !== "list" ? t("settings-extensionview.back")
    : t("settings-integrations.back");

  const detailError = mode.mode === "error" ? mode.error : detail.error ?? (sourceDetailState && !catalog.value ? catalog.error : undefined);
  const detailRetry = mode.mode === "error" ? mode.retry : reloadDetail;
  const detailProblem = detailError && (
    <IntegrationReadProblem text={t("settings-integrations.projection_detail_error", { error: detailError })} retry={detailRetry} />
  );
  const waitForProjection = showDetail && !legacy && !projection && (mode.mode !== "projection" || !!requested);
  const pendingBack = detailBack ?? (detailError && !headerBack ? onBack : undefined);

  const detailOwner = projection?.detail.item.ref.manager ?? owner;
  const switchDetailAssistant = detailOwner && detailOwner !== current
    ? () => {
        chooseManager(otherManager(detailOwner) ?? null);
        onOpen({ scope: integrationScope(), view: { kind: "list" }, filter }, "root");
      }
    : undefined;

  const openSourceDataFlow = !compactList && hasInboxSources && parentView?.kind !== "data-flow"
    ? () => go({ kind: "data-flow" }, detailOwner ?? resourceOwner)
    : undefined;

  const detailPane = waitForProjection ? (
    <>
      {pendingBack && (
        <Button tone="quiet" icon="chevronLeft" onClick={pendingBack} className="touch-hit -ml-2 mb-3">
          {t("settings-integrations.back")}
        </Button>
      )}
      {detailProblem || <Loading />}
    </>
  ) : sourceDetailState ? (
    <>
      {detailProblem}
      <PushSourceDetailPane
        state={sourceDetailState}
        item={resolvedSourceItem}
        manager={detailOwner}
        onBack={detailBack}
        backLabel={pushSourceBackLabel}
        onSwitchAssistant={switchDetailAssistant}
        onOpenDataFlow={openSourceDataFlow}
        reloadKey={sourcesReload}
        onReload={() => {
          if (projection) reloadDetail();
          setSourcesReload((n) => n + 1);
        }}
        projection={projection}
        resourceId={requested?.id}
      />
    </>
  ) : showDetail && resolvedTarget ? (
    <Suspense fallback={<Loading />}>
      {detailProblem}
      <IntegrationDetail
        target={{ ...resolvedTarget, manager: detailOwner }}
        projection={projection}
        prefill={prefill}
        integrationsV2={true}
        onBack={detailBack}
        onUnavailableBack={headerBack ? undefined : onBack}
        onSwitchAssistant={switchDetailAssistant}
        onUninstalled={onBack}
        onPage={(id, ui, manager) => go({ kind: "page", id, ui }, manager)}
        onUpgrade={(market, id, manager) => go({ kind: "item", market, id }, manager)}
        onSettings={onSettings}
        onOpenConnector={(id, manager) => go({ kind: "connector", id }, manager)}
        onOpenSources={(state, manager) => go(state ? { kind: "source", state } : { kind: "data-flow" }, manager)}
      />
    </Suspense>
  ) : null;

  if (detailPane) {
    return (
      <div
        className={compactList ? "flex h-full min-h-0 min-w-0 flex-1 overflow-hidden" : undefined}
        data-integrations-split={compactList ? "" : undefined}
      >
        {compactList && (
          <div
            data-integrations-list
            className="flex h-full min-h-0 w-[300px] shrink-0 flex-col border-r border-line"
          >
            <div
              data-integrations-list-header
              className="shrink-0 border-b border-line px-3 pt-4 pb-3"
            >
              {listHeader}
              {viewSwitch}
              {filterBar}
            </div>
            <div
              ref={listScrollRef}
              data-integrations-list-scroll
              className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-3 pt-3 pb-8"
            >
              {listBody}
            </div>
          </div>
        )}
        <div
          ref={detailScrollRef}
          data-integrations-detail-scroll={compactList ? "" : undefined}
          className={compactList ? "min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain" : undefined}
        >
          <div className={compactList ? "mx-auto max-w-[820px] px-6 pt-4 pb-12 sm:px-8" : undefined}>
            {detailPane}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div data-integrations-list>
      {listHeader}
      {viewSwitch}
      <p className="mb-4 px-1 text-meta leading-relaxed text-ink-3">
        {t("settings-integrations.list_intro")}
      </p>
      {filterBar}
      {listBody}
    </div>
  );
}

export default Integrations;
