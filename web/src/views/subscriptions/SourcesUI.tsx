// The manager's subscription pages (docs/extensions/subscriptions.md "Pages") as Settings › Inbox Sources and
// /subscriptions draw them: the declarative renderer under the reserved id, told the assistant's name as the composer
// shows it ("Ask {name}…"), which the pages' Set Up with … button takes. With a manager that has the relationship view
// (#5741), a List / Relationships switch sits over the list's first page; the view's Open Settings shows a source's page
// under a way back to it.
import { Suspense, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { ExternalSourceSettingsEntry } from "../../features/external-sources/ExternalSourceSettingsEntry";
import { otherManager, pinManagerAt } from "../../lib/manager-choice";
import { lazyPage } from "../../lib/lazy-page";
import { useInboxSourcesGraphShown } from "../../lib/subscriptions";
import { SUBSCRIPTIONS_UI } from "../../lib/subscriptions-ui";
import { ExtensionOwnerScope } from "../../lib/extension-owner";
import { Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { usePersona } from "../assistant/Persona";
import { Segmented } from "../assistant/parts";
import { askAssistant, ExtensionUI } from "../settings/ExtensionView";
import { InboxForwardSetting } from "../settings/InboxForwardSetting";

// The relationship view and its layout are a chunk of their own, loaded when it is first shown.
const SourcesGraphView = lazyPage(() => import("./SourcesGraph").then((m) => m.SourcesGraphView));

/** `onGraph`: the relationship view is shown or not (the page around it widens for it).
 *  `initialView`: defaults to `"list"` for legacy Settings › Inbox Sources; `"graph"` opens directly on SourcesGraph. */
export function SourcesUI({
  title,
  state,
  onGraph,
  onOpenSource,
  initialView = "list",
  manager,
}: {
  title: string;
  state?: string;
  onGraph?: (shown: boolean) => void;
  onOpenSource?: (page: string) => void;
  initialView?: "list" | "graph";
  manager?: string;
}) {
  useLayoutEffect(() => pinManagerAt(["/subscriptions"], otherManager(manager)), [manager]);
  const name = usePersona().name;
  const context = useMemo(() => ({ assistant: name }), [name]);
  // A page opened on one source or extension keeps to it: no switch there.
  const offered = useInboxSourcesGraphShown() && !state;
  const [graph, setGraph] = useState(initialView === "graph");
  const [opened, setOpened] = useState<string | null>(null);
  const [depth, setDepth] = useState(1);
  // Leaving (another Settings tab) gives the page its usual width back.
  const reported = useRef(onGraph);
  useEffect(() => {
    reported.current = onGraph;
  });
  useEffect(() => {
    if (offered && initialView === "graph") reported.current?.(true);
    return () => reported.current?.(false);
  }, [offered, initialView]);
  const show = (on: boolean, page: string | null = null) => {
    setGraph(on);
    setOpened(page);
    onGraph?.(on && !page);
  };
  // The reserved id scopes what a graph's `key` keeps (lib/extension-owner.tsx): these pages' graphs keep their own
  // pan, zoom and folds, apart from any extension's page that shares a key.
  const pages = (first?: string) => (
    <ExtensionOwnerScope owner={SUBSCRIPTIONS_UI}>
      <ExtensionUI key={first ?? ""} extension={SUBSCRIPTIONS_UI} ui="pages" title={title} bare manager={manager} state={first} context={context} onDepth={setDepth} />
    </ExtensionOwnerScope>
  );
  const connectedWork = !state && <ExternalSourceSettingsEntry manager={manager} />;
  if (!offered) return <div data-settings-group="inbox-sources">{pages(state)}{!state && <InboxForwardSetting manager={manager} />}{connectedWork}</div>;
  if (graph && opened)
    return (
      <div data-settings-group="inbox-sources">
        <button type="button" onClick={() => show(true)} className="mb-3 inline-flex h-8 items-center gap-1 rounded-control px-1.5 text-[13px] font-medium text-accent hover:bg-hover">
          <Icon name="chevronLeft" size={13} stroke={2.2} />
          {t("subscriptions.view_graph")}
        </button>
        {pages(opened)}
      </div>
    );
  return (
    <div data-settings-group="inbox-sources">
      {(graph || depth <= 1) && (
        <div className="mb-4 flex">
          <Segmented
            label={t("subscriptions.view")}
            value={graph ? "graph" : "list"}
            options={[
              { v: "list", label: t("subscriptions.view_list") },
              { v: "graph", label: t("subscriptions.view_graph") },
            ]}
            onChange={(v) => show(v === "graph")}
          />
        </div>
      )}
      {graph ? (
        <>
          <p className="mb-4 px-1 text-[13px] text-ink-2">{t("subscriptions.graph_lead")}</p>
          <Suspense fallback={<Loading />}>
            <SourcesGraphView assistant={name} onOpen={onOpenSource ?? ((page) => show(true, page))} onAsk={askAssistant} />
          </Suspense>
          <InboxForwardSetting manager={manager} />
          {connectedWork}
        </>
      ) : (
        <>
          {pages()}
          {depth <= 1 && <InboxForwardSetting manager={manager} />}
          {depth <= 1 && connectedWork}
        </>
      )}
    </div>
  );
}
