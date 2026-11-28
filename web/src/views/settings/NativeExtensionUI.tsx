// Actual extension-page navigation, with owner-bound socket sessions retained below the top of the stack.
import { useCallback, useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import type { LegacyExtensionUI } from "./ExtensionView";
import { composeAssistant, pageHref } from "./ExtensionView";
import { appUIStreamPage, type AppUIStreamPage, type NativePagePermission } from "../../lib/extension-ui-app-stream";
import { NATIVE_STREAM_COMPONENTS, type StreamPageIdentity } from "../../lib/extension-ui-stream";
import { composeText, MAX_STACK, uiContext, type UIPush } from "../../lib/extension-ui";
import { credentialOwner, enc, errorText } from "../../lib/api";
import { endInteraction, getState, useStore } from "../../lib/store";
import { apiOp } from "../../lib/ops";
import { listManagers } from "../../lib/managers";
import { confirmedDefaultManager, onDefaultManager } from "../../lib/manager-choice";
import { uuid } from "../../lib/format";
import { takeOverlayEntry, withoutOverlayState } from "../../lib/back-close";
import { navigate } from "../../lib/router";
import { safeWebURL } from "../../lib/format";
import { openLink } from "../../lib/links";
import { wideView } from "../../lib/code-view";
import { currentLanguage, t } from "../../i18n";
import { Button, Dialog, Loading, toast } from "../../ui/atoms";
import { ExtensionStreamSections } from "../extensions/ExtensionStreamSections";
import type { Action, UIStreamValue } from "../../lib/extension-wire";
import { NATIVE_UI_VERSION } from "../../lib/extension-view-cache";

type Props = Parameters<typeof LegacyExtensionUI>[0];
type Page = { key: string; ui: string; state?: string; title?: string; legacy?: boolean };
export function NativeExtensionUI(props: Props & { legacy: (props: Props) => ReactNode; onLegacyFallback?: () => void }) {
  const [manager, setManager] = useState<string | undefined>(() => confirmedDefaultManager());
  const [unavailable, setUnavailable] = useState(false);
  const [stack, setStack] = useState<Page[]>(() => [{ key: uuid(), ui: props.ui, state: props.state, title: props.title }]);
  const stackRef = useRef(stack); stackRef.current = stack;
  const historied = props.history ?? (props.pages !== "open");
  // Settings draws the route back only at the first page; a pushed page's own Back replaces it.
  useEffect(() => { props.onDepth?.(stack.length); }, [props.onDepth, stack.length]);
  useEffect(() => {
    if (manager) return;
    let live = true;
    const owner = credentialOwner();
    // Accept the shared roster default if watchRoster() resolves it first, without waiting on a duplicate GET /managers.
    const off = onDefaultManager(() => {
      if (!live) return;
      const confirmed = confirmedDefaultManager();
      if (credentialOwner() === owner && confirmed) {
        live = false;
        setManager(confirmed);
      }
    });
    void listManagers().then((roster) => {
      if (!live) return;
      if (credentialOwner() === owner && roster.default) setManager(roster.default);
      else setUnavailable(true);
    }).catch(() => { if (live) setUnavailable(true); });
    return () => { live = false; off(); };
  }, [manager]);
  useEffect(() => {
    if (!historied) return;
    const pop = (event: PopStateEvent) => {
      const depth = typeof event.state?.extPage === "number" ? event.state.extPage : 1;
      setStack((s) => s.slice(0, Math.max(1, Math.min(s.length, depth))));
    };
    addEventListener("popstate", pop); return () => removeEventListener("popstate", pop);
  }, [historied]);
  const push = useCallback((from: string, p: UIPush, legacy = false) => {
    const current = stackRef.current;
    const top = current[current.length - 1];
    if (top.key !== from) return;
    if (props.pages === "open") { navigate(pageHref(props.extension, p.ui || top.ui, p.state)); return; }
    const page = { ...p, ui: p.ui || top.ui, key: uuid(), legacy: legacy || top.legacy };
    const next = [...(current.length >= MAX_STACK ? current.slice(0, MAX_STACK - 1) : current), page];
    if (historied && next.length > current.length) {
      const nextState = { ...(withoutOverlayState(history.state) ?? {}), extPage: next.length };
      if (takeOverlayEntry()) history.replaceState(nextState, "", location.href);
      else history.pushState(nextState, "", location.href);
    }
    stackRef.current = next; setStack(next);
  }, [props.pages, props.extension, historied]);
  const pop = useCallback((from: string) => {
    if (stackRef.current.at(-1)?.key !== from || stackRef.current.length <= 1) return;
    if (historied) history.back();
    else { const next = stackRef.current.slice(0, -1); stackRef.current = next; setStack(next); }
  }, [historied]);
  if (unavailable) return props.legacy(props);
  if (!manager) return props.placeholder ?? <Loading />;
  // only the first page has a last view (ExtensionUI's `remember`)
  return <>{stack.map((page, index) => <div key={page.key} hidden={index !== stack.length - 1}>
    <ConnectedPage {...props} placeholder={index ? undefined : props.placeholder} onView={index ? undefined : props.onView} onLegacyFallback={index ? undefined : props.onLegacyFallback} page={page} manager={manager} active={index === stack.length - 1} push={(p, legacy) => push(page.key, p, legacy)} pop={() => pop(page.key)}
      back={index > 0 ? stack[index - 1].title || t("settings-extensionview.back") : undefined} />
  </div>)}</>;
}
function ConnectedPage(props: Props & { legacy: (props: Props) => ReactNode; onLegacyFallback?: () => void; page: Page; manager: string; active: boolean; push: (p: UIPush, legacy?: boolean) => void; pop: () => void; back?: string }) {
  const [adapter, setAdapter] = useState<AppUIStreamPage>();
  const [fallback, setFallback] = useState(!!props.page.legacy);
  const callbacks = useRef(props); callbacks.current = props;
  const [confirmation, setConfirmation] = useState<{ action: Action; finish: (allow: boolean) => void }>();
  const confirmationRef = useRef(confirmation); confirmationRef.current = confirmation;
  const [identity] = useState<StreamPageIdentity>(() => ({ owner: credentialOwner(), hubGroup: getState().mesh.find((h) => h.self)?.hub_id || location.origin, extension: props.extension, contribution: props.page.ui, navigation: props.page.key }));
  const contextKey = JSON.stringify([props.context ?? {}, currentLanguage()]);
  useEffect(() => {
    if (props.page.legacy) return;
    setFallback(false);
    const page = appUIStreamPage({
      identity, state: props.page.state ?? "",
      context: {
        ...uiContext(props.context),
        // Electron uses the web renderer for protocol 1. HTTP dashboards have a separate desktop context.
        platform: "web", locale: currentLanguage(), ui_version: NATIVE_UI_VERSION,
        stream: { protocol: 1, components: [...NATIVE_STREAM_COMPONENTS] },
      },
    }, props.manager,
      (result) => {
        if (result.toast) toast(result.toast, "ok");
        if (result.open_url) {
          // A page's own quick prompt goes into the assistant's main composer without sending it (#16697); every
          // other answer's URL opens as a link.
          const prompt = composeText(result.open_url);
          if (prompt) composeAssistant(prompt);
          else {
            const safe = safeWebURL(result.open_url, true);
            if (safe) openLink(safe);
          }
        }
        if (result.push) callbacks.current.push(result.push);
        if (result.pop) callbacks.current.pop();
      },
      (error, legacy) => { if (legacy) setFallback(true); else toast(t("settings-extensionview.action_failed", { error: error.message }), "error"); });
    setAdapter(page);
    return () => { confirmationRef.current?.finish(false); page.dispose(); };
    // The page's captured context and manager remain part of its authenticated navigation partition.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity, props.manager, contextKey]);
  const revision = useRef(props.revision);
  useEffect(() => {
    if (revision.current === props.revision) return;
    revision.current = props.revision;
    adapter?.session.refresh();
  }, [props.revision, adapter]);
  useEffect(() => {
    if (!fallback) return;
    adapter?.dispose();
    props.onLegacyFallback?.();
  }, [fallback, adapter, props.onLegacyFallback]);
  const confirm = useCallback((action: Action, _displayed: unknown, _value: UIStreamValue) => new Promise<boolean>((finish) => {
    confirmationRef.current?.finish(false); setConfirmation({ action, finish });
  }), []);
  const finish = (allow: boolean) => { confirmation?.finish(allow); setConfirmation(undefined); };
  const fail = useCallback(() => setFallback(true), []);
  const error = useCallback((e: unknown) => toast(t("settings-extensionview.action_failed", { error: e instanceof Error ? e.message : String(e) }), "error"), []);
  const jump = useCallback((action: { push?: UIPush }) => { if (action.push) callbacks.current.push(action.push); }, []);
  const legacy = props.legacy({ ...props, onDepth: undefined, ui: props.page.ui, state: props.page.state, title: props.page.title,
    active: props.active, history: false, navigation: { push: (p) => callbacks.current.push(p, true), pop: props.pop } });
  return <>
    {props.back && <Button tone="quiet" icon="chevronLeft" onClick={props.pop}>{props.back}</Button>}
    {fallback ? legacy : adapter ? <><WidePage adapter={adapter} active={props.active} onWide={props.onWide} />
      {props.onView && <KeptPage adapter={adapter} manager={props.manager} onView={props.onView} />}
      <ExtensionStreamSections identity={identity} availableAdapter={adapter.session} fallback={null} loading={props.placeholder ?? <Loading />} active={props.active} compact={props.compact}
        confirm={confirm} navigate={jump} readResult={adapter.readResult} onGap={fail} onError={error} />
      <HostConfirmation adapter={adapter} active={props.active} />
    </> : props.placeholder ?? <Loading />}
    <Dialog open={!!confirmation} onClose={() => finish(false)} title={t("settings-extensionview.confirm_title")}>
      <p>{confirmation?.action.confirm}</p><div className="flex gap-2"><Button tone="quiet" onClick={() => finish(false)}>{t("common.cancel")}</Button><Button onClick={() => finish(true)}>{t("settings-extensionview.ok")}</Button></div>
    </Dialog>
  </>;
}
function HostConfirmation({ adapter, active }: { adapter: AppUIStreamPage; active: boolean }) {
  useSyncExternalStore(adapter.session.subscribe, adapter.session.getState, adapter.session.getState);
  const interactions = useStore((s) => s.interactions);
  const ix = active ? Object.values(interactions).find((ix) => adapter.ownsHostConfirmation(ix as NativePagePermission)) : undefined;
  return ix ? <PagePermission key={ix.id} adapter={adapter} ix={ix as NativePagePermission} /> : null;
}
export function PagePermission({ adapter, ix }: { adapter: AppUIStreamPage; ix: NativePagePermission }) {
  const [busy, setBusy] = useState(false);
  const answer = async (allow: boolean) => {
    if (busy || !adapter.ownsHostConfirmation(ix)) return;
    setBusy(true);
    try {
      // The existing hub endpoint routes this pending ID to its owning manager and rechecks auth.
      // Do not manufacture an Agent session or bypass the generic Agent approval guard.
      await apiOp(`/interactions/${enc(ix.id)}/resolve`, { method: "POST", json: { allow },
        headers: ix.created_at ? { "X-Persona-Interaction-Created-At": ix.created_at } : undefined });
      endInteraction(ix.id, undefined, ix.created_at);
    } catch (error) { toast(t("settings-extensionview.action_failed", { error: errorText(error) }), "error"); }
    finally { setBusy(false); }
  };
  return <Dialog open onClose={() => { void answer(false); }} title={t("settings-extensionview.confirm_title")}
    footer={<><Button tone="quiet" disabled={busy} onClick={() => { void answer(false); }}>{t("chat-interactions.deny")}</Button>
      <Button busy={busy} onClick={() => { void answer(true); }}>{t("settings-extensionview.ok")}</Button></>}>
    <p>{ix.summary}</p>
  </Dialog>;
}
/** The page as drawn, for its next opening (ExtensionUI's `remember`), unless a field holds what was typed and not sent. */
function KeptPage({ adapter, manager, onView }: { adapter: AppUIStreamPage; manager: string; onView: NonNullable<Props["onView"]> }) {
  const { display } = useSyncExternalStore(adapter.session.subscribe, adapter.session.getState, adapter.session.getState);
  useEffect(() => {
    if (display && !display.sections.some((s) => s.items.some((it) => it.key && adapter.session.getDraft(it.key) !== undefined))) onView(display, NATIVE_UI_VERSION, manager);
  }, [display, adapter, manager, onView]);
  return null;
}
function WidePage({ adapter, active, onWide }: { adapter: AppUIStreamPage; active: boolean; onWide?: (wide: boolean) => void }) {
  const state = useSyncExternalStore(adapter.session.subscribe, adapter.session.getState, adapter.session.getState);
  useEffect(() => { if (active && state.display) onWide?.(wideView(state.display)); }, [active, state.display, onWide]);
  return null;
}
