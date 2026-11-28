// The model panel of an Agent's chat (#5032, docs/web/web.md "Model pill and model panel"), loaded on its pill's first
// tap (ModelPill.tsx AgentModelPill, which also starts reading the lists): the shared panel (ui/model-panel) fed with the
// session's providers and state (lib/session-panel.ts), its picks applied through POST …/modes. On a hub with the
// in-session switch (#5035) a pick on another launcher is recorded for the next message, which a toast says. On one that
// also moves the conversation to the other engine (#5054) a pick there asks "Switch Agent?" first (EngineSwitchDialog,
// lib/engine-switch-ask.ts), over the panel, which stays open under it: kept, nothing changes; accepted, it is recorded like
// any switch and the panel closes.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { agentName } from "../../lib/agent-kind";
import { asksEngineSwitch, skipEngineSwitchQuestion } from "../../lib/engine-switch-ask";
import type { PanelConfig, PanelOverride } from "../../lib/model-panel";
import { usePanelQuotas } from "../../lib/panel-quotas";
import { sessionLauncher } from "../../lib/model-switch";
import { LAUNCHER_SWITCH_FEATURE, launcherEngine, pickPlan, sessionPanel, speedStep, type ModeStep, type PanelOptions } from "../../lib/session-panel";
import { ENGINE_SWITCH_FEATURE, forgetModel, machineLauncherList, modelList, pendingView, readModels, sessionEngine, sessionModelHost, useModelLists } from "../../lib/session-models";
import { hubHas, useStore } from "../../lib/store";
import type { Session } from "../../lib/types";
import { ultracodeOffered } from "../../lib/ultracode";
import { toast } from "../../ui/atoms";
import { ModelPanel } from "../../ui/model-panel/ModelPanel";
import { EngineSwitchDialog } from "./EngineSwitchDialog";
import { setServiceTier, useModeSet } from "./SessionDialogs";
import { UltracodeSwitchRow } from "./Ultracode";

/** The panel for an Agent's chat: its computer's launchers as providers, picks applied through POST …/modes one at a
 *  time (a waiting switch dropped, the model, then the effort, then the speed; or one switch to another launcher), each
 *  drawn at once and put back if it fails. */
export function SessionModelPanel({ session, displayModel, anchor, onClose }: { session: Session; displayModel?: string; anchor: HTMLElement; onClose: () => void }) {
  const version = useModelLists();
  const { set, latest, switchLauncher } = useModeSet(session);
  const mid = session.machine_id;
  const own = sessionLauncher(session);
  const ownHost = sessionModelHost(session);
  const listEntry = useCallback((name: string) => modelList(mid, name, undefined, name === own ? ownHost : undefined), [mid, own, ownHost]);
  const ultracode = useStore((s) => ultracodeOffered(session, s.features));
  const switching = useStore((s) => hubHas(s.features, LAUNCHER_SWITCH_FEATURE));
  const engineSwitch = useStore((s) => hubHas(s.features, ENGINE_SWITCH_FEATURE));
  const launchers = machineLauncherList(mid);
  const { providers, state } = useMemo(
    () => sessionPanel(session, launchers, listEntry, { switching, engineSwitch, displayModel }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `version` stands for what was read
    [session, launchers, listEntry, version, switching, engineSwitch, displayModel],
  );
  // the engine of a pick's launcher, as the panel shows it (a favorite from a list not read yet knows it too)
  const options = (): PanelOptions => ({ switching, engineSwitch, engineOf: (name) => providers.find((p) => p.id === name)?.engine });

  // "Switch Agent?": the answer the pick waits for (the panel stays open under it; one pick at a time)
  type Answer = { go: boolean; skip: boolean };
  const [asking, setAsking] = useState(false);
  const waiting = useRef<((a: Answer) => void) | null>(null);
  const ask = () =>
    new Promise<Answer>((resolve) => {
      waiting.current = resolve;
      setAsking(true);
    });
  const answer = useCallback((go: boolean, skip: boolean) => {
    const resolve = waiting.current;
    waiting.current = null;
    setAsking(false);
    resolve?.({ go, skip });
  }, []);
  // a panel that goes while it asks (a width change, the pill gone): nothing changes
  useEffect(
    () => () => {
      waiting.current?.({ go: false, skip: false });
      waiting.current = null;
    },
    [],
  );
  const quotas = usePanelQuotas(mid, providers, launchers);

  const listOf = (name: string) => listEntry(name).list;
  // the engine a launcher step moves the conversation to, when it is the other one (drawn on the pending switch at once)
  const crossEngine = (launcher: string) => {
    const to = launcherEngine(launcher, listOf(launcher), options());
    return to && to !== sessionEngine(latest.current) ? to : undefined;
  };
  const apply = async (steps: ModeStep[]) => {
    for (const step of steps) {
      const ok = "service_tier" in step ? await setServiceTier(latest.current, step.service_tier) : "launcher" in step ? await switchLauncher(step, crossEngine(step.launcher)) : await set(step);
      if (!ok) return false;
    }
    return true;
  };
  const onPick = async (config: PanelConfig) => {
    let steps = pickPlan(latest.current, config, listOf, options());
    if (!steps) return false;
    const to = config.provider !== own && steps.length ? crossEngine(config.provider) : undefined;
    let skip = false;
    if (to && asksEngineSwitch(latest.current, to)) {
      const answer = await ask();
      if (!answer.go) return false;
      skip = answer.skip;
      // made again against the session as it is now (a push may have come while it asked)
      steps = pickPlan(latest.current, config, listOf, options());
      if (!steps) return false;
    }
    if (!(await apply(steps))) return false;
    // "Don't remind me again" holds once the switch it confirmed is recorded: a pick the hub refused asks again
    if (skip) skipEngineSwitchQuestion();
    // a switch waits for the next message: say so, in the words the pill now shows
    const next = config.provider !== own && steps.length ? pendingView(latest.current, listEntry) : null;
    if (next && to) toast(t("chat-modelpanel.switch_scheduled_engine", { agent: agentName(next.engine), model: next.model ?? "" }), "ok");
    else if (next) toast(t("chat-modelpanel.switch_scheduled", { target: `${next.provider} · ${next.model}` }), "ok");
    return true;
  };
  const onApplyCurrent = async (patch: PanelOverride) => {
    const s = latest.current;
    const steps: ModeStep[] = [];
    if (patch.effort && patch.effort !== s.effort) steps.push({ effort: patch.effort });
    const speed = speedStep(s, patch.speed, true);
    if (patch.speed && !speed) return false;
    if (speed) steps.push(speed);
    return apply(steps);
  };
  const onReload = (provider: string) => void readModels(mid, provider, { refresh: true, first: provider === own, host: provider === own ? ownHost : undefined });
  const onForget = (provider: string, model: string) => {
    void forgetModel(mid, provider, model, undefined, provider === own ? ownHost : undefined).then((error) => error && toast(error, "error"));
  };

  return (
    <>
      <ModelPanel
        anchor={anchor}
        onClose={onClose}
        providers={providers}
        state={state}
        scope="agent"
        onPick={onPick}
        onApplyCurrent={onApplyCurrent}
        onReload={onReload}
        onForget={onForget}
        quotas={quotas}
        currentExtra={ultracode ? <UltracodeSwitchRow session={session} /> : undefined}
        held={asking}
      />
      <EngineSwitchDialog open={asking} onAnswer={answer} />
    </>
  );
}
