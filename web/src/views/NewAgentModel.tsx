// New Agent's model choice (#5104, after Cindy's new-task composer, which the user asked this part of the web to match;
// docs/web/web.md "New Agent"): the chat's model pill at the right of the first message box's bottom row, opening the
// unified model panel (ui/model-panel) with the chosen computer's launchers as its providers. The user picks a model; the
// launcher and its engine come with it, and the engine's mark stays on the pill and on every row. A "Follow Model
// settings" row on top goes back to the default, the profile's presets are a group of their own, the thinking effort and
// Claude Code's ultracode switch are in the chosen model's flyout. The panel's data is lib/new-agent-panel.ts.
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { startCatalogHost, type PickerTier, type UnavailableLauncher } from "../lib/launcher-models";
import type { ModelBinding } from "../lib/model-config";
import type { PanelConfig } from "../lib/model-panel";
import type { SpeedOption } from "../lib/session-speed";
import { lazyPage } from "../lib/lazy-page";
import { formChoice, newAgentPanel, newAgentPill, type ModelChoice } from "../lib/new-agent-panel";
import { launcherBlock } from "../lib/launcher-health";
import { forgetModel, modelList, readModels, subscribeModelLists, useModelLists, type ModelList } from "../lib/session-models";
import type { Launcher } from "../lib/types";
import { toast } from "../ui/atoms";
import { lazyOverlay } from "./LazyOverlay";

const ModelPanel = lazyOverlay(() => import("../ui/model-panel/ModelPanel").then((m) => m.ModelPanel));
const SpeedSwitch = lazyPage(() => import("../ui/model-panel/Columns").then((m) => m.SpeedSwitch));
import { ModelPill } from "./chat/ModelPill";

const NO_LIST: ModelList = {};

/** One launcher's list as read, drawn again only when that list changes (the pill and the form need one or two lists,
 *  not the panel's every one); "" for either: nothing. Here rather than in lib/session-models.ts, which the first
 *  screen loads: a list's entry is replaced on every change, so it is its own snapshot. */
export function useModelList(mid: string, launcher: string, host?: string): ModelList {
  const read = () => {
    if (!mid || !launcher) return NO_LIST;
    const e = modelList(mid, launcher, undefined, host);
    return e.list !== undefined || e.loading || e.error ? e : NO_LIST;
  };
  return useSyncExternalStore(subscribeModelLists, read, () => NO_LIST);
}

export interface NewAgentModelProps {
  mid: string;
  host?: string;
  startHost: string;
  launchers: Launcher[];
  /** This computer's launchers are read (the panel opens only then). */
  ready: boolean;
  /** They couldn't be read (#10952): the pill shows its failed state instead of the spinner, and a click reads again. */
  onRetry?: () => void;
  choice: ModelChoice;
  /** The launcher the start runs on: the one picked, else the one Model settings › New agent runs; "": none known. */
  runs: string;
  /** Model settings › New agent as it applies now; null: not followed. */
  configured: ModelBinding | null;
  tiers: PickerTier[];
  /** Persona-managed providers left out on this computer, with why (#8109): listed off in the panel. */
  unavailable?: UnavailableLauncher[];
  /** The "Follow Model settings" row; absent without model settings to follow (an older hub). */
  follow?: { active: boolean; detail?: string; warning?: string; label: string };
  /** What the pill says while no launcher is known to run (the model settings run something else, or none is read). */
  fallback: string;
  /** A speed choice for the current Codex model for this start only. */
  speed?: { options: SpeedOption[]; value?: string; onChange: (value: string) => void };
  /** Claude Code's ultracode switch for this start (#3428): in the chosen model's flyout. */
  ultracode?: ReactNode;
  /** The workflow mark on the pill: ultracode is on. */
  ultracodeOn?: boolean;
  onChoice: (choice: ModelChoice) => void;
  onPreset: (name: string) => void;
  onFollow: () => void;
  /** "How to fix" of a launcher that failed its check here (#1643), or of a provider in `unavailable`. */
  onHelp: (launcher: string) => void;
  /** "Configure models": the dialog closes, Settings › Model providers opens. */
  onConfigure: () => void;
}

export function NewAgentModel(props: NewAgentModelProps) {
  const { mid, host, startHost, launchers, ready, onRetry, choice, runs, configured, fallback, ultracodeOn } = props;
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const close = useCallback(() => setAnchor(null), []);
  // a different computer closes it (its providers are another computer's)
  const opened = useRef(mid);
  useEffect(() => {
    if (opened.current !== mid) setAnchor(null);
    opened.current = mid;
  }, [mid]);
  // the pill follows the one list it names; the panel, mounted only while open, follows every launcher's
  const catalogHost = startCatalogHost(launchers.find((l) => l.name === runs), host, startHost);
  const pill = newAgentPill({ launchers, runs, list: useModelList(mid, runs, catalogHost), choice, configured, fallback, startHost });
  // no launchers read: no model to name, only that it failed ("Model loading failed" after the warning)
  return (
    <>
      <ModelPill
        current={onRetry ? { ...pill, model: undefined } : pill}
        loading={!onRetry && (pill.loading || !ready)}
        failed={Boolean(onRetry) || pill.failed}
        open={Boolean(anchor)}
        disabled={!ready && !onRetry}
        onOpen={onRetry ? () => onRetry() : (el) => setAnchor((a) => (a ? null : el))}
        workflow={ultracodeOn ? {} : undefined}
      />
      {anchor && ready && <NewAgentPanel {...props} anchor={anchor} onClose={close} />}
    </>
  );
}

/** The panel while it is open: the computer's launchers with their lists, read again behind the ones shown once per
 *  opening (the one that runs first; not the ones that can't start here, whose rows are off anyway; at most three at a
 *  time, as each may start its agent's CLI on the computer). */
function NewAgentPanel({ mid, host, startHost, launchers, choice, runs, configured, tiers, unavailable, follow, speed, ultracode, onChoice, onPreset, onFollow, onHelp, onConfigure, anchor, onClose }: NewAgentModelProps & { anchor: HTMLElement; onClose: () => void }) {
  const version = useModelLists();
  const omp = launchers.find((l) => l.name === "omp");
  const catalogHost = (name: string) => name === "omp" ? startCatalogHost(omp, host, startHost) : host;
  useEffect(() => {
    for (const l of launchers) if (l.name === runs || !launcherBlock(l)) void readModels(mid, l.name, { again: true, first: l.name === runs, host: catalogHost(l.name) });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- refresh on opening or when the selected host changes
  }, [host, startHost]);
  const { providers, state, presets } = useMemo(
    () => newAgentPanel({ launchers, listOf: (name) => modelList(mid, name, undefined, catalogHost(name)), choice, configured, runs, tiers, unavailable, startHost }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `version` stands for what was read
    [launchers, mid, host, choice.launcher, choice.model, choice.effort, configured, runs, tiers, unavailable, version, startHost],
  );

  const onPick = async (config: PanelConfig) => {
    onChoice(formChoice(config));
    return true;
  };
  const onApplyCurrent = async (patch: { effort?: string; fast?: boolean }) => {
    if (patch.effort !== undefined) onChoice({ ...choice, effort: patch.effort });
    return true;
  };
  const onForget = (provider: string, model: string) => {
    if (provider === choice.launcher && model === choice.model) onChoice({ ...choice, model: "" });
    void forgetModel(mid, provider, model, undefined, catalogHost(provider)).then((error) => error && toast(error, "error"));
  };

  return (
    <ModelPanel
      anchor={anchor}
      onClose={onClose}
      providers={providers}
      state={state}
      scope="agent"
      onPick={onPick}
      onApplyCurrent={onApplyCurrent}
      onReload={(provider) => void readModels(mid, provider, { refresh: true, first: provider === runs, host: catalogHost(provider) })}
      onForget={onForget}
      currentExtra={speed ? <SpeedSwitch options={speed.options} speed={speed.value} disabled={false} onSpeed={speed.onChange} /> : ultracode}
      currentExtraReset={speed?.value && speed.value !== "standard" ? () => speed.onChange("standard") : undefined}
      follow={follow && { ...follow, onFollow: () => (onFollow(), true) }}
      presets={presets}
      onPreset={(key) => (onPreset(key), true)}
      engines
      onHelp={(launcher) => {
        onClose();
        onHelp(launcher);
      }}
      onConfigure={onConfigure}
    />
  );
}
