// Start an agent on one of your computers: machine, folder (browse-first breadcrumb path picker with secondary Recent view,
// #14595), the first message, and at the right of its
// box the model pill (#5104, as Cindy's new task: the user picks a model in the unified model panel, the launcher and its
// engine come with it; views/NewAgentModel.tsx), where the profile's presets (#2225), the thinking effort and Claude Code's
// ultracode switch (#3428: off each time, never remembered) also are. On top, the recent setups (#2134): a tap fills every
// field with one that started an agent before, and never starts.
import { Suspense, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { hubLabel } from "../lib/mesh";
import { api, ApiError, credentialOwner, enc, errorText, getToken, isAbort, subscribeCredentialOwner } from "../lib/api";
import { defaultManager, otherManager, useKnownDefault } from "../lib/manager-choice";
import { base64File, pickAttachment, releaseAttachments, type LocalAttachment } from "../lib/attachments";
import { MAX_PICTURE_SOURCE } from "../lib/image-limits";
import { safeDraftRemove, safeDraftWrite, type DraftImage } from "../lib/drafts";
import { withContexts } from "../lib/context-xml";
import type { ContextRef } from "../lib/links";
import { ForwardBar } from "./chat/ForwardBar";
import { lazyPage } from "../lib/lazy-page";
import { browserStartKeys, launchAgent, presetPrompt, type NewAgentPreset } from "../lib/new-agent";
import { whenStarted } from "../lib/new-agent-started";
import { startCompare } from "../lib/compare-runner";
import { MAX_RUNS } from "../lib/compare-runs";
import type { WorkspaceTarget } from "../lib/workspace-target";
import { useWorkspaceCatalog, workspaceTarget, type HostWorkspace } from "../lib/workspace-catalog";
import { directoryKey } from "../lib/workspace-directory";
import { startChoices, startGroups } from "../lib/workspace-start";
import { WorkspaceChoices } from "./NewAgentWorkspaces";
import { AttachmentTray } from "../ui/AttachmentTray";
import { agentId, providerAgent } from "../lib/agent-kind";
import { modelPurpose, modelSummary, type ModelBinding } from "../lib/model-config";
import { formatFollowDetail, resolveFollowBinding, resolvedStartModel } from "../lib/new-agent-follow";
import {
  defaultModel, effortChoices, effortRule, fitEffort, followedBinding, inlineTextStart, launcherAgent, listedModel, listedModes, machineLaunchers, nativeOmpStart, pickerLauncher, startCatalogHost, startPicks, tierLeftOut, type PickerTier,
  type UnavailableLauncher,
} from "../lib/launcher-models";
import { choiceCarries, type ModelChoice } from "../lib/new-agent-panel";
import { readModels } from "../lib/session-models";
import { startSpeedOptions, startSpeedTier } from "../lib/session-speed";
import { BYPASS_MODE, BYPASS_WARNING, effortTitle, listedModeOption, modeTitle, PERMISSION_MODES } from "../lib/session-actions";
import {
  clearRecentSetups, forgetSetup, formHolds, loadRecentSetups, machineModel, recentCards, rememberSetup, setupKey, setupTime, type RecentCard, type RecentSetup,
} from "../lib/recent-setups";
import { baseName, shortPath } from "../lib/format";
import { orderRecentFolders, pathBreadcrumbs } from "../lib/folder-picker";
import { startingWith, typedFolder } from "../lib/typed-folder";
import { addAgentWorktree, agentBranch, agentNameHint, suggestedPath, worktreeStatus, type WorktreeStatus } from "../lib/worktree";
import { getState, useStore } from "../lib/store";
import type { Launcher, Machine } from "../lib/types";
import { Button, cx, Dialog, IconButton, Menu, Spinner, Switch, toast, useMenu } from "../ui/atoms";
import { SegmentedTrack } from "../ui/Segmented";
import { Pill } from "./settings/SettingsParts";
import { startBlocked, ultracodeStartOffered } from "../lib/ultracode";
import { UltracodeStartRow } from "./chat/Ultracode";
import { Icon } from "../ui/icons";
import { ProviderName } from "../ui/ProviderName";
import { t } from "../i18n";
import { useAddComputer } from "./machines/AddComputerEntry";
import { useScopedMachines } from "../lib/current-assistant";
import { currentHosts, engineName, startHosts } from "../lib/engines";
import { CODEX_APP_SOURCE, projectsFolder, startsInDesktopApp } from "../lib/desktop-start";
import { initialLauncher, launcherBlock } from "../lib/launcher-health";
import { opensAsOrcaChat, type DefaultOpens } from "../lib/orca-chat";
import { navigate } from "../lib/router";
import { hasFinePointer } from "../lib/viewport";
import { providersPath } from "../lib/readiness";
import { settingsHref } from "./settings/nav";
import { LauncherBlockedHelp } from "./LauncherBlockedHelp";
import { launcherTitle, namedLauncher, namesFromProviders, providerIdNote, providerLabel, providerTitle, type Named } from "../lib/provider-names";
import { WorkSessionForm } from "./WorkSessionForm";
import { NewAgentModel, useModelList } from "./NewAgentModel";
import { ExecutionCheck } from "./execution/ExecutionCheck";
import { ProjectDocumentContext, ProjectDocumentSource, projectDocumentText, type ProjectDocumentSnapshot } from "./NewAgentProjectDocument";

// The terminal engines a document's start goes to as its first message, as any first message does: Orca's terminals too
// (#17053), which open in the chosen folder since #17036. Paseo and the other runtimes are not terminals.
const PROJECT_DOCUMENT_HOSTS = new Set(["herdr", "herdr-core", "orca", "tern", "rex"]);

// several forwarded at once (#3838): their bar, from the group's chunk, which Forward to has loaded by then
const ForwardGroupBar = lazyPage(() => import("./chat/ForwardGroup").then((m) => m.ForwardGroupBar));
// a start in a desktop app's parts (#5480), loaded when such a launcher runs: their words stay off the first screen
const DesktopTemporaryRow = lazyPage(() => import("./NewAgentDesktop").then((m) => m.DesktopTemporaryRow));
const DesktopFolderNote = lazyPage(() => import("./NewAgentDesktop").then((m) => m.DesktopFolderNote));
const NewFolderButton = lazyPage(() => import("./NewAgentDesktop").then((m) => m.NewFolderButton));
const NewFolderNaming = lazyPage(() => import("./NewAgentDesktop").then((m) => m.NewFolderNaming));

/** A forwarded picture (lib/drafts.ts DraftImage, base64) as a file for the first message's tray. */
const draftImageFile = (img: DraftImage) => base64File(img.name, img.mime, img.data);

/** Classifies a launcher's permission-mode controls so preset picks and rendering share one rule (#8261, #17239). */
function launcherPermissionKinds(agent: string, entry: Launcher | undefined, nativeOmp = false) {
  const listed = listedModes(entry);
  const agyModes = agent === "agy" ? listed : undefined;
  const claudeModes = effortRule(agent) === "any" && (Boolean(agyModes) || !listed);
  const providerModes = claudeModes || nativeOmp ? undefined : listed;
  return { agyModes, claudeModes, providerModes };
}

interface Dirs {
  path: string;
  parent?: string;
  entries: { name: string; path: string; is_git?: boolean }[];
  recent?: { path: string; name: string; last_used?: string }[];
}

type NewAgentProps = { open: boolean; onClose: () => void; preset?: NewAgentPreset | null };

export function NewAgent(props: NewAgentProps) {
  if (props.preset?.workSession) return <WorkSessionForm open={props.open} onClose={props.onClose} />;
  return <AgentForm {...props} />;
}

function AgentForm({ open, onClose, preset }: NewAgentProps) {
  // with several assistants, the current one's computers: the agent started here shows in its list (docs/assistant/managers.md "Clients")
  const machines = useScopedMachines();
  const online = useMemo(() => machines.filter((m) => m.online), [machines]);
  // no computer at all (docs/assistant/onboarding.md 4.5): the "Computer" field says why it is empty and adds one, nothing else to fill in
  const loaded = useStore((s) => s.loaded);
  const none = loaded && machines.length === 0;
  const adding = useAddComputer();
  const [mid, setMid] = useState(() => localStorage.getItem("new.machine") ?? "");
  const [cwd, setCwd] = useState("");
  // The folder was picked in this opening (typed, a row, a card, a page's own), not filled in from the last start: a start
  // in a desktop app with none is a temporary session (lib/desktop-start.ts, #5480), as on iOS.
  const [cwdPicked, setCwdPicked] = useState(false);
  const pickCwd = (dir: string) => {
    setCwd(dir);
    setCwdPicked(true);
  };
  // a new folder is being named for a desktop-app start, in the folder browsed (NewAgentDesktop.tsx)
  const [naming, setNaming] = useState(false);
  const [workspace, setWorkspace] = useState<WorkspaceTarget | null>(null);
  // the workspace was chosen from this sheet's list (#7259), not handed in by a space or row menu: another computer, a
  // recent setup or a folder may still replace it
  const [workspacePicked, setWorkspacePicked] = useState(false);
  const [launcher, setLauncher] = useState("");
  const [launchers, setLaunchers] = useState<Launcher[]>([]);
  // the providers left out on this computer, with why (#8109): listed off in the model panel, "How to fix" opens one
  const [skipped, setSkipped] = useState<UnavailableLauncher[]>([]);
  // the model configuration's providers' names (#5029): titles for a launcher ID this computer's list doesn't have (a
  // preset's, a recent setup's, the default's)
  const [providerNames, setProviderNames] = useState<Named[]>([]);
  const [unified, setUnified] = useState(true);
  const [modelDefault, setModelDefault] = useState("");
  // "Models"' "New agent" as it applies now (provider, model, effort); null: the hub has no model configuration
  const [configured, setConfigured] = useState<ModelBinding | null>(null);
  const [loadingModels, setLoadingModels] = useState(false);
  const [launchersOf, setLaunchersOf] = useState(""); // the machine `launchers` and `configured` are for
  // the computer whose launchers couldn't be read, and why (#10952): the pill says so and a click reads them again
  const [unread, setUnread] = useState<{ mid: string; reason: string } | null>(null);
  const readLaunchersAgain = useRef(() => {});
  // the grayed-out launcher whose "how to fix" is open (#1643); "": none
  const [helpFor, setHelpFor] = useState("");
  // this start's model (an id from the list; "": the default) and effort ("": the launcher's), never saved
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  // the presets this computer offers (tiers of the profile in use), and the one picked: it fills the launcher, model and
  // effort as picking them by hand would; a pick by hand after it unmarks it. Never saved.
  const [tiers, setTiers] = useState<PickerTier[]>([]);
  const [tier, setTier] = useState<PickerTier | null>(null);
  // the four dimensions' check of what this start runs (views/execution/ExecutionCheck.tsx): blocked disables Start
  const [execBlocked, setExecBlocked] = useState(false);
  // a launcher picked on the computer chosen before that this one can't run (#5104): the form went back to the default
  const [dropped, setDropped] = useState<{ name: string; computer: string } | null>(null);
  // the lists read again in this opening (once each: a list read since stays as it is)
  const readThisTime = useRef(new Set<string>());
  // ultracode for this start: off whenever the sheet opens, never saved (the server's rule: no choice carries over)
  const [ultracode, setUltracode] = useState(false);
  const [speedChoice, setSpeedChoice] = useState<{ key: string; value: string }>();
  const features = useStore((s) => s.features);
  // Claude Code's permission mode for this start ("": the launcher's), remembered per computer like iOS's mode.<machine>
  const [mode, setMode] = useState("");
  // the mode picked for a launcher that lists its own (Paseo's other providers, #8261): its own ("") or Full access,
  // picked on this sheet and never remembered, so a mode left from a Claude Code start never reaches such a start
  const [providerMode, setProviderMode] = useState("");
  // where a start that keeps the default opens, as the hub says (#17650, lib/orca-chat.ts); for the computer `launchersOf` names
  const [defaultOpens, setDefaultOpens] = useState<DefaultOpens | undefined>();
  // Pre-preset mode values so dropping or switching away from a preset restores them without leaking across launchers.
  const presetModeRestore = useRef<{ mode: string; providerMode: string } | null>(null);
  const chooseMode = (next: string) => {
    presetModeRestore.current = null;
    setMode(next);
  };
  const chooseProviderMode = (next: string) => {
    presetModeRestore.current = null;
    setProviderMode(next);
  };
  const [host, setHost] = useState(""); // the engine host to open in (#2576); "": the machine's default
  const [dirs, setDirs] = useState<Dirs | null>(null);
  // the listed folder's subfolders kept: those starting with this ("": all), for a typed path naming no folder yet
  const [dirsPrefix, setDirsPrefix] = useState("");
  const [browse, setBrowse] = useState("");
  // Browse-first path picker (#14595): Recent folders sit behind a secondary toggle rather than crowding the browser.
  const [folderView, setFolderView] = useState<"browse" | "recent">("browse");
  const breadcrumbRef = useRef<HTMLElement>(null);
  // The folder the list follows (#9452, lib/typed-folder.ts): the folder field's text, filled in (the last start's, a card's, a
  // recent row's) or typed once typing pauses. Browsing from the list takes over until the field changes again.
  const [follow, setFollow] = useState({ text: "", typed: false });
  const typing = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(typing.current), []);
  const typeCwd = (text: string) => {
    pickCwd(text);
    setFolderView("browse");
    clearTimeout(typing.current);
    typing.current = setTimeout(() => {
      setFollow({ text, typed: true });
      setBrowse("");
    }, 250);
  };
  // the list shows a folder filled in: its subfolders in Browse view
  const followFilled = (dir: string) => {
    clearTimeout(typing.current);
    setFollow({ text: dir, typed: false });
    setBrowse("");
    setFolderView("browse");
  };
  // Single-state folder navigation (#14595): navigating in the picker always keeps cwd, breadcrumbs, and subfolders in lockstep.
  const navigateFolder = (dir: string) => {
    clearTimeout(typing.current);
    pickCwd(dir);
    setBrowse(dir);
    setFolderView("browse");
  };
  const clearCwd = () => {
    setCwd("");
    setCwdPicked(false);
    followFilled("");
  };
  const [loadingDirs, setLoadingDirs] = useState(false);
  const [prompt, setPrompt] = useState(() => localStorage.getItem("draft:new-agent") ?? "");
  const [projectDocument, setProjectDocument] = useState<{ snapshot: ProjectDocumentSnapshot; owner: string; manager: string } | null>(null);
  const keys = useMemo(browserStartKeys, []);
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  // messages forwarded to a new Agent (#3899, #3838, lib/forward.ts): their card over the first message, which carries
  // them in front
  const [forwards, setForwards] = useState<ContextRef[]>([]);
  const [dragging, setDragging] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const submitted = useRef(false);
  const preparation = useRef<AbortController | null>(null);
  const documentPreparation = useRef<AbortController | null>(null);
  const promptRevision = useRef(0);
  const credential = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const knownDefault = useKnownDefault();
  const pickedMachine = machines.find((m) => m.id === mid);
  const manager = pickedMachine?.manager || knownDefault || defaultManager();
  const purposeManager = otherManager(pickedMachine?.manager);
  const targetOnline = online.some((m) => m.id === mid);
  const workspaceCurrent = !workspace || (workspace.owner === credential && workspace.manager === manager
    && workspace.machineId === mid && workspace.cwd === cwd && Boolean(workspace.host.trim() && workspace.workspaceId.trim()));
  // The chosen computer's Orca and Paseo workspaces, by project (#7259): the catalog the sidebar shares, read while the
  // sheet is open; only its own assistant's (a computer of another one has none listed here).
  const catalog = useWorkspaceCatalog(open);
  const workspaceGroups = useMemo(
    () => (catalog.owner === credential && catalog.manager === manager ? startGroups(startChoices(catalog.workspaces, mid)) : []),
    [catalog, credential, manager, mid],
  );
  const retirePreparation = () => {
    const previous = preparation.current;
    preparation.current = null;
    documentPreparation.current = null;
    previous?.abort();
  };
  const dismiss = () => {
    retirePreparation();
    setProjectDocument(null);
    // Keep admission closed until another opening, including events in the same React batch.
    submitted.current = true;
    onClose();
  };
  // a preset's folder, put in once its computer is the chosen one (choosing a computer loads that one's last folder)
  const presetCwd = useRef<{ mid: string; cwd: string } | null>(null);
  // The recent setups (#2134), read again each time the sheet opens; the card a tap filled the form from (its mark stays
  // while the form still holds what it put in).
  const [recents, setRecents] = useState(loadRecentSetups);
  const [pickedSetup, setPickedSetup] = useState("");
  // a card for another computer (or one whose launchers are still being read): its folder and mode go in with the
  // computer, its launcher and model once that computer's launchers are read
  const pendingSetup = useRef<RecentSetup | null>(null);
  // the computer the open sheet shows, and the model choice as it stands: a pick made on one computer goes to the next
  // one picked when that one can run it (Cindy keeps a choice the target can run and falls back to its default otherwise)
  const shownMid = useRef("");
  const choiceNow = useRef<ModelChoice>({ launcher: "", model: "", effort: "" });
  choiceNow.current = { launcher, model, effort };
  // the choice was made by hand in this opening (a model, a preset, the follow row): only that one goes along; one a
  // computer's pick merely restored gives way to the next computer's own (#2134)
  const touched = useRef(false);
  // that choice on its way to the computer picked next, until its launchers are read (another pick before then passes it on)
  const carrying = useRef<ModelChoice | null>(null);
  const formTop = useRef<HTMLDivElement>(null);
  const firstMessage = useRef<HTMLTextAreaElement>(null);
  const attachmentsRef = useRef(attachments);
  attachmentsRef.current = attachments;

  useLayoutEffect(() => {
    if (!open) return;
    const body = formTop.current?.closest<HTMLElement>("[data-dialog-body]");
    if (body) body.scrollTop = 0;
  }, [open]);

  useEffect(() => {
    if (!open) {
      setForwards([]); // a forward goes with the sheet it came in, not into the next one
      shownMid.current = "";
      touched.current = false;
      carrying.current = null;
      readThisTime.current.clear();
      return;
    }
    submitted.current = false;
    setEffort("");
    setProviderMode("");
    presetModeRestore.current = null;
    setTier(null);
    setDropped(null);
    setRecents(loadRecentSetups());
    setPickedSetup("");
    setUltracode(false);
    setSpeedChoice(undefined);
    setInWorktree(false);
    setWtBranch("");
    setWtError("");
    setRunTimes(1);
    setFolderView("browse");
  }, [open]);
  // a worktree of its own (#6115): offered for a folder in a Git repository (what the computer says of it), off each
  // time the sheet opens
  const [inWorktree, setInWorktree] = useState(false);
  const [wtBranch, setWtBranch] = useState("");
  const [wtBusy, setWtBusy] = useState(false);
  const [wtError, setWtError] = useState("");
  // Compare runs (#8916): the same start 2–4 times, each in a worktree of its own; offered only with the worktree switch
  // on, 1 (a start as ever) each time the sheet opens
  const [runTimes, setRunTimes] = useState(1);
  // Cleanup fences completions even when the transport ignores abort. A target change starts a new admission scope.
  useLayoutEffect(() => {
    submitted.current = false;
    setWtBusy(false);
    setWtError("");
    return retirePreparation;
  }, [open, preset, mid, cwd, cwdPicked, workspace, manager, credential, targetOnline]);
  useLayoutEffect(() => {
    if (!open) setProjectDocument(null);
  }, [open]);
  // a Paseo or Orca workspace picked from the list (#8856, #17050) can still start in a new worktree of its repository,
  // which that engine makes: Paseo as a workspace of its own, Orca as a child of the one picked
  const enginePicked = workspacePicked && (workspace?.host === "paseo" || workspace?.host === "orca");
  const pickedEngine = enginePicked ? workspace?.host : undefined;
  const [repo, setRepo] = useState<WorktreeStatus | null>(null);
  useEffect(() => {
    setRepo(null);
    const dir = cwd.trim();
    if (!open || !mid || !dir || (workspace && !pickedEngine)) return;
    const c = new AbortController();
    // the engine it opens in says where its worktree goes (Paseo and Orca make their own)
    const kind = pickedEngine ?? (host || undefined);
    const timer = setTimeout(() => void worktreeStatus(mid, dir, c.signal, kind).then((st) => { if (!c.signal.aborted) setRepo(st); }), 400);
    return () => {
      clearTimeout(timer);
      c.abort();
    };
  }, [open, mid, cwd, workspace, pickedEngine, manager, credential, host]);
  const worktreeOffered = (!workspace || enginePicked) && Boolean(repo?.path);
  // Back to a folder: typed, browsed or a recent one, on the computer's default engine.
  const clearWorkspace = () => {
    retirePreparation();
    presetCwd.current = null;
    setWorkspace(null);
    setWorkspacePicked(false);
    setHost("");
    setInWorktree(false);
    setRepo(null);
  };
  // A workspace from the list (#7259): the start opens in it, on its own engine, in its folder.
  const pickWorkspace = (w: HostWorkspace) => {
    retirePreparation();
    presetCwd.current = null;
    setWorkspace(workspaceTarget(w, catalog.owner, catalog.manager));
    setWorkspacePicked(true);
    pickCwd(w.cwd);
    setHost("");
    setInWorktree(false);
    setRepo(null);
  };
  useEffect(() => {
    if (!open) return;
    presetCwd.current = null;
    setWorkspacePicked(false);
    if (!preset) {
      setWorkspace(null);
      return;
    }
    if (preset.prompt) {
      promptRevision.current++;
      setPrompt((draft) => presetPrompt(preset.prompt, draft));
    }
    if (preset.forwards?.length) setForwards(preset.forwards);
    if (preset.images?.length) {
      const picked: LocalAttachment[] = [];
      for (const img of preset.images) {
        try { picked.push(pickAttachment(draftImageFile(img))); }
        catch (e) { toast(errorText(e), "error"); }
      }
      setAttachments((current) => [...current, ...picked]);
    }
    const target = preset.workspace;
    setWorkspace(target ? { ...target, owner: target.owner ?? credentialOwner(),
      manager: target.manager ?? (getState().machines.find((m) => m.id === target.machineId)?.manager || defaultManager()) } : null);
    if (target) {
      presetCwd.current = { mid: target.machineId, cwd: target.cwd };
      setMid(target.machineId);
      pickCwd(target.cwd);
      setInWorktree(false);
      return;
    }
    if (preset.machineId) {
      setMid(preset.machineId);
      if (preset.cwd !== undefined) {
        presetCwd.current = { mid: preset.machineId, cwd: preset.cwd };
        pickCwd(preset.cwd);
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, preset]);
  useEffect(() => {
    if (prompt) safeDraftWrite("draft:new-agent", () => localStorage.setItem("draft:new-agent", prompt));
    else safeDraftRemove("draft:new-agent");
  }, [prompt]);
  useEffect(() => () => releaseAttachments(attachmentsRef.current), []);

  const addFiles = (files: FileList | File[]) => {
    const picked: LocalAttachment[] = [];
    for (const file of Array.from(files)) {
      try { picked.push(pickAttachment(file, MAX_PICTURE_SOURCE)); }
      catch (e) { toast(errorText(e), "error"); }
    }
    setAttachments((current) => [...current, ...picked]);
  };

  useEffect(() => {
    // Keep an explicit target if it disconnects. Starting elsewhere requires a new computer selection.
    if (open && !workspace && !preset?.workspace && !preset?.machineId && !presetCwd.current && !online.some(m => m.id === mid) && online[0]) setMid(online[0].id);
  }, [open, online, mid, workspace, preset]);

  // The launcher a computer's pick puts in once its launchers are read, and the model that goes with it (#2134): a card's own
  // when its launcher is still there; else the choice made on the computer shown before when this one can run it (#5104);
  // else the model configuration (`effective`; null: not followed) or the last one used here, unless this computer can't
  // run it now (#1643: then the first that can), with this computer's latest model for it in the recent setups. No model
  // list for it (the configuration runs no launcher): no model.
  const restoreLauncher = (machine: string, card: RecentSetup | null, ls: Launcher[], effective: ModelBinding | null, carry: ModelChoice | null = null) => {
    if (!card && carry && (carry.launcher || effective !== null) && choiceCarries(carry, ls)) {
      touched.current = true; // still the user's: it goes on to the computer after this one too
      setLauncher(carry.launcher);
      setModel(carry.launcher ? carry.model : "");
      setEffort(carry.launcher ? carry.effort : "");
      return;
    }
    if (!card && carry?.launcher) {
      // what was picked can't run here: this computer's default, said under the box
      setDropped({ name: launcherTitle(carry.launcher, [...ls, ...providerNames]), computer: machines.find((m) => m.id === machine)?.name ?? machine });
      setEffort("");
    }
    const own = card && (card.launcher ? ls.some((l) => l.name === card.launcher) : effective !== null);
    const chosen = own ? card.launcher : effective !== null ? "" : initialLauncher(localStorage.getItem(`new.launcher.${machine}`), ls);
    const runs = pickerLauncher(chosen, followedBinding(chosen, effective));
    const picked = !runs ? "" : own ? card.model : chosen ? machineModel(loadRecentSetups(), machine, chosen) : "";
    setLauncher(chosen);
    setModel(picked);
  };

  useEffect(() => {
    if (!open || !mid) return;
    localStorage.setItem("new.machine", mid);
    // another computer picked while the sheet is open: the choice made on the one before goes along if this one can run it
    const before = shownMid.current;
    shownMid.current = mid;
    const carry = before && before !== mid ? (carrying.current ?? (touched.current ? choiceNow.current : null)) : null;
    carrying.current = carry;
    touched.current = false;
    setLaunchers([]);
    setSkipped([]);
    setProviderNames([]);
    setHelpFor("");
    setDropped(null);
    let controller = new AbortController();
    // nothing of the computer before goes to this one unless restoreLauncher puts it back (a read that fails leaves none)
    setLauncher("");
    setModel("");
    setTiers([]);
    setTier(null);
    presetModeRestore.current = null;
    setModelDefault("");
    setConfigured(null);
    // A read that fails settles too (#10952): the pill leaves its spinner for its failed state, the line under the box
    // says why, and Start runs the computer's default. Retry reads again with the same card and carried choice.
    const read = () => {
      const { signal } = controller;
      setUnread(null);
      setLoadingModels(true);
      Promise.all([
        machineLaunchers(mid, { signal }),
        modelPurpose("agent.new", purposeManager).catch((e) => {
          if (e instanceof ApiError && e.status === 404) return null;
          throw e;
        }),
      ])
        .then(([r, config]) => {
          if (signal.aborted) return;
          const ls = r.launchers ?? [];
          const hasUnified = config !== null || Boolean(r.default);
          const effective = hasUnified ? resolveFollowBinding(r.default, config ? (config.purpose.effective ?? {}) : null) : null;
          setLaunchers(ls);
          setSkipped(r.unavailable ?? []);
          setTiers(r.tiers ?? []);
          setDefaultOpens(r.default_opens);
          setLaunchersOf(mid);
          setUnified(hasUnified);
          setConfigured(effective);
          if (config) {
            setModelDefault(modelSummary(config.purpose, config.providers));
            setProviderNames(namesFromProviders(config.providers));
          }
          const card = pendingSetup.current?.machine === mid ? pendingSetup.current : null;
          if (card) pendingSetup.current = null;
          carrying.current = null;
          restoreLauncher(mid, card, ls, effective, carry);
          const expectedSource = preset?.launcher === "codex-app" ? "codex_app" : preset?.launcher === "claude-desktop" ? "claude_desktop" : undefined;
          if (!card && !carry && !touched.current && preset?.machineId === mid && preset.launcher && ls.some((l) => l.name === preset.launcher && (!expectedSource || l.source === expectedSource))) {
            setLauncher(preset.launcher);
            setModel("");
            setEffort("");
          }
        })
        // the computer read last only: another one picked meanwhile aborted this read
        .catch((e) => { if (!signal.aborted) setUnread({ mid, reason: errorText(e) }); })
        .finally(() => { if (!signal.aborted) setLoadingModels(false); });
    };
    read();
    readLaunchersAgain.current = () => {
      controller.abort();
      controller = new AbortController();
      read();
    };
    const p = presetCwd.current;
    if (p?.mid === mid) presetCwd.current = null;
    const card = pendingSetup.current?.machine === mid ? pendingSetup.current : null;
    const filled = card ? card.cwd : p?.mid === mid ? p.cwd : (localStorage.getItem(`new.cwd.${mid}`) ?? "");
    setCwd(filled);
    // the last start's folder is not picked; a card's is (a card of a temporary session has none)
    setCwdPicked(card ? Boolean(card.cwd) : p?.mid === mid);
    setMode(card?.agent_type === "claude" ? card.mode : (localStorage.getItem(`new.mode.${mid}`) ?? ""));
    followFilled(filled);
    setNaming(false);
    return () => {
      controller.abort();
      readLaunchersAgain.current = () => {};
      setLaunchersOf("");
      setUnread(null);
    };
  }, [open, mid, purposeManager]);
  const launchersFailed = unread?.mid === mid ? unread.reason : "";
  // Retry, from the pill or the line under the box: the line goes while it reads, so the caret goes to the message box
  const retryLaunchers = () => {
    readLaunchersAgain.current();
    firstMessage.current?.focus({ preventScroll: true });
  };

  // The model list of the launcher that will run (the chosen one, or the model configuration's), read on each opening and
  // kept for the page's life with the chat's (lib/session-models.ts): the pill names its model, a start sends what it
  // takes. Starting never waits for it.
  const followed = followedBinding(launcher, unified ? configured : null);
  const listLauncher = launchersOf === mid ? pickerLauncher(launcher, followed) : "";
  const listEntry = launchers.find((l) => l.name === listLauncher);
  const desktopStart = startsInDesktopApp(listEntry);
  const opensIn = desktopStart ? undefined : workspace?.host ?? (host || undefined);
  const picked = online.find((m) => m.id === mid);
  const hosts = picked ? startHosts(picked) : [];
  const hostDefault = picked ? currentHosts(picked).default : "";
  const startHost = workspace?.host ?? (host || hostDefault);
  const catalogHost = startCatalogHost(listEntry, opensIn, startHost);
  useEffect(() => {
    if (!open || !mid || !listLauncher) return;
    const key = JSON.stringify([mid, listLauncher, catalogHost]);
    void readModels(mid, listLauncher, { again: !readThisTime.current.has(key), first: true, host: catalogHost });
    readThisTime.current.add(key);
  }, [open, mid, listLauncher, catalogHost]);
  const models = useModelList(open ? mid : "", listLauncher, catalogHost);
  // an older hub has no list (null): no model is picked
  useEffect(() => {
    if (models.list === null) setModel("");
  }, [models.list]);
  // A choice made by hand (a model or the follow row picked in the panel, a preset, Try anyway): the launcher, model and
  // effort it runs on. A preset is marked until something else is picked (picking its own setup again keeps it).
  const pickModel = (c: ModelChoice, preset: PickerTier | null = null) => {
    touched.current = true;
    carrying.current = null;
    setHelpFor("");
    setDropped(null);
    const changed = c.launcher !== launcher || c.model !== model || c.effort !== effort;
    if (preset || changed) setTier(preset);
    if (!preset && changed && presetModeRestore.current) {
      setMode(presetModeRestore.current.mode);
      setProviderMode(presetModeRestore.current.providerMode);
      presetModeRestore.current = null;
    }
    setLauncher(c.launcher);
    setModel(c.model);
    setEffort(c.effort);
  };
  // a preset: the launcher, model, effort and optional permission mode it fills in (the model is checked once the list is in)
  const pickTier = (x: PickerTier) => {
    const base = presetModeRestore.current ?? { mode, providerMode };
    pickModel({ launcher: x.launcher, model: x.model ?? "", effort: x.effort ?? "" }, x);
    const targetLauncher = launchers.find((l) => l.name === x.launcher);
    const targetAgent = launcherAgent(x.launcher, launchers, undefined);
    const { agyModes: targetAgyModes, claudeModes: targetClaudeModes, providerModes: targetProviderModes } = launcherPermissionKinds(targetAgent, targetLauncher);
    if (targetProviderModes) {
      setMode(base.mode);
      setProviderMode(x.permission_mode && targetProviderModes.includes(x.permission_mode) ? x.permission_mode : "");
      presetModeRestore.current = base;
    } else if (targetClaudeModes) {
      setProviderMode(base.providerMode);
      if (x.permission_mode) {
        setMode(!targetAgyModes || targetAgyModes.includes(x.permission_mode) ? x.permission_mode : "");
        presetModeRestore.current = base;
      } else {
        setMode(base.mode);
        presetModeRestore.current = null;
      }
    } else {
      setMode(base.mode);
      setProviderMode(base.providerMode);
      presetModeRestore.current = null;
    }
  };
  const followDefault = () => pickModel({ launcher: "", model: "", effort: "" });

  // Where it opens, when the machine enables more than one engine (#2576).
  useEffect(() => { setHost(""); }, [open, mid]);
  const agent = launcherAgent(listLauncher, launchers, models.list);
  const nativeOmp = nativeOmpStart(agent, listEntry, startHost);
  const previousCatalog = useRef({ host: opensIn, nativeOmp, launcher: listLauncher, agent });
  useLayoutEffect(() => {
    const previous = previousCatalog.current;
    previousCatalog.current = { host: opensIn, nativeOmp, launcher: listLauncher, agent };
    const changedAgyHost = previous.host !== opensIn && (agent === "agy" || previous.agent === "agy");
    const leftNativeOmp = previous.nativeOmp && !nativeOmp;
    if (open && previous.launcher === listLauncher && (changedAgyHost || leftNativeOmp)) {
      // A model from this host's picker may no longer be available on the next host.
      setModel("");
      setEffort("");
      setTier(null);
    }
  }, [open, opensIn, nativeOmp, listLauncher, agent]);
  // what a start on it takes besides the model (#8261): Claude Code's modes and efforts (or Antigravity's subset when
  // its runtime narrows permission_modes), or the permission modes a launcher lists (Paseo's other providers) and the
  // efforts its model lists, or Codex's reported efforts alone
  const { agyModes, claudeModes, providerModes } = launcherPermissionKinds(agent, listEntry, nativeOmp);
  const claudeModeOptions = agyModes ? PERMISSION_MODES.filter((m) => agyModes.includes(m.id)) : PERMISSION_MODES;
  const effectiveClaudeMode = agyModes && mode && !agyModes.includes(mode) ? "" : mode;
  // Codex CLI can omit the first message when prompt_optional is advertised. The App needs one to expose its new chat.
  const codex = agent === "codex";
  const needsFirst = codex && (desktopStart || listEntry?.prompt_optional !== true);
  const documentHost = opensIn ?? hostDefault;
  // An explicit launcher family must agree with the catalog. Older Claude entries use launcherAgent's default.
  const documentAgent = listEntry?.agent_type || listEntry?.agent || agent;
  const documentScope = picked && listEntry && !loadingModels && !workspace && !desktopStart
    && documentAgent === agent && (agent === "claude" || codex) && PROJECT_DOCUMENT_HOSTS.has(documentHost)
    ? JSON.stringify([credential, manager, mid, documentHost, listLauncher, agent]) : null;
  const documentContextCurrent = !projectDocument || (projectDocument.owner === credential && projectDocument.manager === manager);
  useLayoutEffect(() => {
    if (!documentPreparation.current) return;
    retirePreparation();
    submitted.current = false;
    setWtBusy(false);
    setWtError("");
  }, [documentScope]);
  const documentBlock = useMemo(() => projectDocument ? projectDocumentText(projectDocument.snapshot) : "", [projectDocument]);
  const documentMessage = useMemo(() => {
    if (!documentBlock) return "";
    const draft = forwards.length ? withContexts(forwards, prompt, attachments.some((file) => file.mime.startsWith("image/"))) : prompt.trim();
    return draft ? `${draft}\n\n${documentBlock}` : documentBlock;
  }, [documentBlock, forwards, prompt, attachments]);
  let documentBlocked: string | null = null;
  if (projectDocument && !documentContextCurrent) documentBlocked = t("newagent.project_document_changed_connection");
  else if (projectDocument && !documentScope) documentBlocked = t("newagent.project_document_unsupported");
  // the launcher this start runs (the chosen one, or the one Model settings' default runs) when this computer can't start
  // it now (#1643): said under the box with a way to see how to fix it; Start stays enabled
  const runs = launchersOf === mid ? listLauncher : "";
  const runsBlock = runs ? launcherBlock(launchers.find((l) => l.name === runs)) : null;
  // A start in a desktop app (lib/desktop-start.ts, #5480 Claude's, #5726 the Codex App): no folder picked is a temporary
  // session, browsing starts in the folder holding the app's projects (worked out when the computer or the launcher
  // changes, not on every push), and a new folder may be named.
  const desktopSource = listEntry?.source;
  const codexApp = desktopSource === CODEX_APP_SOURCE;
  const desktopScratch = desktopStart && (!cwdPicked || !cwd.trim()) && !workspace;
  const presetProject = desktopStart && preset?.machineId === mid && preset.launcher === listLauncher ? preset.desktopProjectId : undefined;
  const projectRoots = catalog.owner === credential && catalog.manager === manager
    ? catalog.workspaces.filter((w) => w.machine_id === mid && w.source === desktopSource && w.source_id === presetProject).map((w) => w.cwd) : [];
  const projectForDirectory = (directory: string) => {
    const selectedDirectory = directoryKey(directory);
    return presetProject && selectedDirectory && (projectRoots.length ? projectRoots : [preset?.cwd ?? ""]).some((path) => {
      const root = directoryKey(path);
      return root !== null && (selectedDirectory === root || selectedDirectory.startsWith(root.endsWith("/") ? root : `${root}/`));
    }) ? presetProject : undefined;
  };
  const desktopProjectId = projectForDirectory(desktopScratch ? "" : cwd.trim());
  const presetSource = preset?.launcher === "codex-app" ? "codex_app" : preset?.launcher === "claude-desktop" ? "claude_desktop" : undefined;
  const presetUnavailable = Boolean(presetSource && preset?.machineId === mid && !loadingModels && !touched.current
    && !launchers.some((l) => l.name === preset.launcher && l.source === presetSource));
  // not for a start handed in for something of its own (Inbox's "New agent…", #5933): that waits for one Agent
  const runsOffered = inWorktree && worktreeOffered && !desktopScratch && !preset?.onStarted;
  const runCount = runsOffered ? runTimes : 1;
  const desktopProjects = useMemo(
    () => (desktopStart ? projectsFolder(getState().sessions[mid] ?? [], codexApp) ?? "" : ""),
    [mid, desktopStart, codexApp],
  );
  // the folder listed: the one browsed to, else the field's (none for a temporary session, whose field is empty), else
  // where browsing starts (home, or the app's projects for a start in it)
  const browseStart = desktopStart ? desktopProjects : "";
  const following = browse || desktopScratch ? null : typedFolder(follow.text);
  const listing = browse || following?.path || browseStart;
  // where to look when that is no folder: for a typed path, beside its last part; for one filled in (a folder since
  // removed), where browsing starts
  const near = following && (follow.typed ? following.near : { path: browseStart, prefix: "" });
  const nearPath = near?.path;
  const nearPrefix = near?.prefix ?? "";
  useEffect(() => {
    if (!open || !mid || workspace) return;
    const controller = new AbortController();
    const read = (path: string) => api<Dirs>(`/machines/${enc(mid)}/dirs?path=${enc(path)}`, { signal: controller.signal });
    setLoadingDirs(true);
    read(listing)
      .then((dirs) => ({ dirs, prefix: "" }))
      // the field's path naming no folder (yet): beside it, or where browsing starts
      .catch((error) => {
        if (isAbort(error) || nearPath === undefined) throw error;
        return read(nearPath).then((dirs) => ({ dirs, prefix: nearPrefix }));
      })
      .then(({ dirs, prefix }) => {
        if (controller.signal.aborted) return;
        setDirs(dirs);
        setDirsPrefix(prefix);
      })
      .catch((error) => {
        if (controller.signal.aborted || isAbort(error)) return;
        setDirs(null);
        setDirsPrefix("");
      })
      .finally(() => { if (!controller.signal.aborted) setLoadingDirs(false); });
    return () => controller.abort();
  }, [open, mid, listing, nearPath, nearPrefix, workspace, manager, credential]);
  useEffect(() => { if (!desktopStart) setNaming(false); }, [desktopStart]);
  const recentFolders = useMemo(() => orderRecentFolders(dirs?.recent), [dirs?.recent]);
  const showRecent = folderView === "recent" && recentFolders.length > 0;
  const breadcrumbs = useMemo(() => pathBreadcrumbs(dirs?.path ?? ""), [dirs?.path]);
  const visibleEntries = useMemo(() => startingWith(dirs?.entries, dirsPrefix), [dirs?.entries, dirsPrefix]);
  useLayoutEffect(() => {
    const nav = breadcrumbRef.current;
    if (nav) nav.scrollLeft = nav.scrollWidth;
  }, [dirs?.path, showRecent]);
  const defaultRuns = unified && launchersOf === mid ? pickerLauncher("", followedBinding("", configured)) : "";
  const defaultEntry = launchers.find((l) => l.name === defaultRuns);
  const defaultBlock = defaultRuns ? launcherBlock(defaultEntry) : null;
  const helpLauncher = helpFor ? launchers.find((l) => l.name === helpFor) : undefined;
  const helpBlock = helpLauncher ? launcherBlock(helpLauncher) : null;
  // what a launcher ID is called (#5029): this computer's launchers first, then the model configuration's providers
  const names: Named[] = [...launchers, ...providerNames];
  const rule = effortRule(agent, listEntry);
  const efforts = effortChoices(models.list, model, followed, rule);
  const effortsKey = efforts.join(" ");
  const effortsSettled = rule !== "listed" || models.list !== undefined || Boolean(models.error);
  useEffect(() => {
    if (effortsSettled) setEffort((e) => fitEffort(e, effortsKey ? effortsKey.split(" ") : []));
  }, [effortsKey, effortsSettled]);
  // offered with Claude Code whose effort can be set here; grayed when this computer's Claude Code is known to be too old
  const ultracodeShown = !desktopStart && !loadingModels && efforts.length > 0 && ultracodeStartOffered(features, agent);
  // what of the picked preset this launcher can't take, said once its list is in
  const startModelOptions = { agent, list: models.list, followed, launcher: listEntry, nativeOmp };
  const picks = startPicks({ ...startModelOptions, model, effort });
  const startModel = resolvedStartModel({ model: picks.model, followed, launcher: listEntry, list: models.list });
  const speedModel = !loadingModels && !desktopStart && agent === "codex" && features.includes("session_start_service_tier") && models.list?.supports_start_service_tier
    ? (startModel ? listedModel(models.list.models, startModel) : defaultModel(models.list.models))
    : undefined;
  const speedOptions = startSpeedOptions(speedModel);
  const speedKey = speedOptions && JSON.stringify([mid, startHost, listLauncher, speedModel?.id]);
  const speed = speedOptions?.some(({ id }) => id === speedChoice?.value) && speedChoice?.key === speedKey
    ? speedChoice?.value
    : "standard";
  useEffect(() => {
    setSpeedChoice((choice) => choice?.key === speedKey ? choice : undefined);
  }, [speedKey]);
  const tierMissed = tier && listLauncher === tier.launcher && (models.list !== undefined || (agent === "omp" && models.error)) ? tierLeftOut(tier, startModelOptions) : [];
  // the follow row's second line: what Model settings › New agent runs (the unified effective binding, by display names)
  const defaultList = useModelList(open ? mid : "", defaultRuns, startCatalogHost(defaultEntry, opensIn, startHost)).list;
  const followDetail = formatFollowDetail(unified ? configured : null, defaultRuns, defaultList, names) || modelDefault;
  const ultracodeBlocked = ultracodeShown ? startBlocked(picked?.claude_version) : "";
  // with the computer's switch on, a start that keeps the default opens as its Orca chat (#17650): the hub says which, the
  // form only checks that this start still keeps the default
  const orcaChat = launchersOf === mid && !desktopStart && !projectDocument ? opensAsOrcaChat(defaultOpens, {
    launcher, tier: Boolean(tier), model, host: opensIn, mode: claudeModes ? effectiveClaudeMode : "",
    ultracode: ultracodeShown && !ultracodeBlocked && ultracode, serviceTier: startSpeedTier(speedModel, speed),
  }) : null;

  // The recent setups' cards (#2134): not when another page opened the sheet on a folder of its own (a space's or a
  // knowledge entry's New agent), which a card would replace.
  const cards = useMemo(() => recentCards(recents, machines), [recents, machines]);
  const presetFolder = Boolean(preset?.machineId && preset.cwd !== undefined);
  // Opened on a place (a project's +, a space's or a board card's New agent, #13523): where it starts is settled, so the
  // caret goes to the first message, as picking a recent setup puts it. With a fine pointer only: on a touch screen the
  // keyboard would come up over the sheet before it is read (the Dialog keeps it down for that, ui/overlay.ts).
  const placed = presetFolder || Boolean(preset?.workspace);
  useEffect(() => {
    if (open && placed && hasFinePointer()) firstMessage.current?.focus();
  }, [open, placed]);
  const settled = launchersOf === mid && !loadingModels;
  const pickedCard = pickedSetup ? cards.find((c) => setupKey(c.setup) === pickedSetup) : undefined;
  const pickedShown = pickedCard && (!settled || formHolds(pickedCard.setup, { machine: mid, cwd, launcher, model, mode, effort })) ? pickedSetup : "";
  // A tap fills every field and puts the focus in the first message; Start stays the user's.
  const pickSetup = ({ setup, online: up }: RecentCard) => {
    if (!up || !online.some((m) => m.id === setup.machine)) return;
    setPickedSetup(setupKey(setup));
    setTier(null);
    presetModeRestore.current = null;
    touched.current = false;
    carrying.current = null;
    setEffort(setup.agent_type === "claude" ? setup.effort : "");
    pendingSetup.current = null;
    if (workspacePicked) clearWorkspace();
    if (setup.machine !== mid) {
      pendingSetup.current = setup;
      setMid(setup.machine);
    } else {
      setCwd(setup.cwd);
      setCwdPicked(Boolean(setup.cwd));
      followFilled(setup.cwd);
      if (setup.agent_type === "claude") setMode(setup.mode);
      if (launchersOf === mid) restoreLauncher(mid, setup, launchers, unified ? configured : null);
      else pendingSetup.current = setup; // the launchers being read put it in
    }
    setDropped(null);
    setHelpFor("");
    firstMessage.current?.focus({ preventScroll: true });
  };

  const start = () => {
    if (!open || submitted.current || loadingModels || presetUnavailable || !workspaceCurrent || !online.some((m) => m.id === mid) || !(cwd.trim() || desktopScratch) || (needsFirst && !prompt.trim() && !projectDocument)) return;
    if (documentBlocked || (projectDocument && (projectDocument.owner !== credentialOwner()
      || projectDocument.manager !== (getState().machines.find((m) => m.id === mid)?.manager || defaultManager())))) return;
    // Read ownership at admission too: credential/store changes can precede React's next render.
    if (workspace && (workspace.owner !== credentialOwner()
      || workspace.manager !== (getState().machines.find((m) => m.id === mid)?.manager || defaultManager()))) return;
    submitted.current = true;
    const controller = new AbortController();
    preparation.current = controller;
    documentPreparation.current = projectDocument ? controller : null;
    const owner = credentialOwner();
    const token = getToken();
    const revision = promptRevision.current;
    // who opened New agent for something of its own hears the Agent started (Inbox's "New agent…", #5933)
    const onStarted = preset?.onStarted;
    const sent = documentMessage || (forwards.length ? withContexts(forwards, prompt, attachments.some((a) => a.mime.startsWith("image/"))) : prompt.trim());
    const snapshot = { text: sent, attachments: [...attachments] };
    const current = () => preparation.current === controller && !controller.signal.aborted && owner === credentialOwner()
      && getState().machines.some((m) => m.id === mid && m.online)
      && manager === (getState().machines.find((m) => m.id === mid)?.manager || defaultManager());
    // a temporary session keeps the last folder for the next start; its folder is the computer's to pick (#5480)
    const dir = desktopScratch ? "" : cwd.trim();
    if (dir) localStorage.setItem(`new.cwd.${mid}`, dir);
    if (launcher && !unified) localStorage.setItem(`new.launcher.${mid}`, launcher);
    // a Codex keeps its own modes; the choice stays for this computer's next Claude Code. A launcher that lists its own
    // takes one of those alone, never the remembered one (its own goes as no mode).
    const permissionMode = claudeModes ? effectiveClaudeMode
      : providerModes ? (providerMode && providerModes.includes(providerMode) ? providerMode : "")
      : tier && listLauncher === tier.launcher && tier.permission_mode ? tier.permission_mode : "";
    const presetFilteredMode = Boolean(agyModes && tier && listLauncher === tier.launcher && tier.permission_mode && !agyModes.includes(tier.permission_mode) && presetModeRestore.current);
    if (claudeModes && !presetFilteredMode && (!agyModes || !mode || agyModes.includes(mode))) {
      if (mode) localStorage.setItem(`new.mode.${mid}`, mode);
      else localStorage.removeItem(`new.mode.${mid}`);
    }
    // this start as a recent setup (#2134): the folder picked (not the worktree made from it), the model and effort sent;
    // mode and effort only for Claude Code
    const claude = agent === "claude";
    const setup = { machine: mid, cwd: dir, launcher, model: picks.model ?? "", agent_type: agent, mode: claude ? permissionMode : "", effort: claude ? (picks.effort ?? "") : "" };
    // only an explicit "on" is sent: off is what a new Claude is anyway
    const ultra = ultracodeShown && !ultracodeBlocked && ultracode ? true : undefined;
    const inline = !workspace && !desktopStart && launchersOf === mid && agent === "traex"
      ? inlineTextStart(launchers.find((l) => l.name === listLauncher), opensIn ?? hostDefault, snapshot.text, snapshot.attachments.length)
      : undefined;
    // what every start of this sheet goes with (each run of a comparison too)
    const launcherDefaultEffort = agent === "agy" && !!launcher && !picks.effort;
    const serviceTier = startSpeedTier(speedModel, speed);
    const target = { launcher: projectDocument || desktopStart || serviceTier ? listLauncher : launcher, ...picks, serviceTier,
      nameHint: projectDocument ? `${projectDocument.snapshot.project}/${projectDocument.snapshot.document} r${projectDocument.snapshot.revision}` : undefined,
      launcherDefaultEffort: launcherDefaultEffort || undefined, permissionMode: permissionMode || undefined,
      host: projectDocument ? documentHost : opensIn, ultracode: ultra,
      ...(codex ? { agentType: "codex", inlinePrompt: true } : inline) };
    const launchDisplay = {
      displayLauncher: listLauncher || undefined,
      displayEngine: listLauncher ? agentId(providerAgent(agent, listLauncher)) : undefined,
      displayModel: startModel,
    };
    const launch = (dir: string, workspaceId: string | undefined) => {
      if (!current()) return;
      // The launch owns its previews; removing or unmounting the editable draft cannot revoke them.
      const sent = snapshot.attachments.map((a) => ({ ...a, url: a.url ? URL.createObjectURL(a.file) : undefined }));
      rememberSetup({ ...setup, at: setupTime() });
      const id = launchAgent({ machineId: mid, cwd: dir, workspaceId: desktopStart ? undefined : workspaceId, desktopProjectId: projectForDirectory(dir), ...target, ...launchDisplay, text: snapshot.text, attachments: sent });
      if (onStarted) whenStarted(id, onStarted);
      const ids = new Set(snapshot.attachments.map((a) => a.id));
      releaseAttachments(attachmentsRef.current.filter((a) => ids.has(a.id)));
      attachmentsRef.current = attachmentsRef.current.filter((a) => !ids.has(a.id));
      setAttachments(attachmentsRef.current);
      if (promptRevision.current === revision) setPrompt("");
      setForwards([]);
      dismiss();
    };
    // in a worktree of its own (#6115): made first, by the engine it opens in; a failure says why here, and nothing starts.
    // From a picked Paseo or Orca workspace (#8856, #17050) the start goes into the workspace that engine made for the
    // worktree, not the one picked.
    if ((!workspace || enginePicked) && inWorktree && worktreeOffered && !desktopScratch) {
      // Compare runs (#8916): the sheet closes at once on the runs' page, which shows each worktree and start as it goes
      // (lib/compare-runner.ts); the task stays in this tab for a run's retry, so the draft goes now
      if (runCount > 1) {
        const hint = agentNameHint(prompt, cwd);
        const id = startCompare({ mid, cwd: cwd.trim(), nameHint: hint, branch: wtBranch.trim() || agentBranch(hint), count: runCount, spec: target,
          intoWorkspace: enginePicked, text: snapshot.text, attachments: snapshot.attachments });
        rememberSetup({ ...setup, at: setupTime() });
        releaseAttachments(attachmentsRef.current);
        attachmentsRef.current = [];
        setAttachments([]);
        if (promptRevision.current === revision) setPrompt("");
        setForwards([]);
        dismiss();
        navigate({ name: "compare", id });
        return;
      }
      setWtBusy(true);
      setWtError("");
      void addAgentWorktree(mid, cwd.trim(), agentNameHint(prompt, cwd), wtBranch, { signal: controller.signal, token, tokenOwner: owner }, opensIn)
        .then(
          (wt) => launch(wt.path, enginePicked ? wt.workspace_id || undefined : undefined),
          (e) => {
            if (!current()) return;
            submitted.current = false;
            setWtError(errorText(e));
          },
        )
        .finally(() => {
          if (!current()) return;
          preparation.current = null;
          documentPreparation.current = null;
          setWtBusy(false);
        });
      return;
    }
    launch(workspace ? workspace.cwd : dir, workspace?.workspaceId);
  };

  if (none)
    return (
      <>
        <Dialog
          open={open}
          onClose={dismiss}
          title={t("newagent.title")}
          width={520}
          footer={
            <Button tone="quiet" onClick={dismiss}>
              {t("common.cancel")}
            </Button>
          }
        >
          <Field label={t("newagent.computer")}>
            <div data-testid="new-agent-no-computer" className="flex flex-col items-start gap-3 rounded-control bg-inset px-3.5 py-3 shadow-hairline">
              <div className="flex items-start gap-2.5">
                <Icon name="machine" size={15} className="mt-0.5 shrink-0 text-ink-3" />
                <div className="flex min-w-0 flex-col gap-0.5">
                  <span className="text-[13px] font-medium text-ink">{t("newagent.no_computer_title")}</span>
                  <span className="text-[12.5px] leading-relaxed text-ink-2">{t("newagent.no_computer_body")}</span>
                </div>
              </div>
              <Button
                tone="primary"
                icon="plus"
                onClick={() => {
                  dismiss(); // the add flow is a dialog of its own, not one over this
                  adding.open();
                }}
              >
                {t("machines.add")}
              </Button>
            </div>
          </Field>
        </Dialog>
        {adding.dialog}
      </>
    );

  return (
    <ProjectDocumentContext value={{
      open, scope: documentScope, busy: wtBusy, attached: projectDocument?.snapshot ?? null, blocked: documentBlocked,
      onAttach: (snapshot) => {
        if (!open || !documentScope || submitted.current || credential !== credentialOwner()) return;
        setProjectDocument((current) => current ?? { snapshot, owner: credential, manager });
        firstMessage.current?.focus({ preventScroll: true });
      },
      onRemove: () => {
        if (submitted.current) return;
        setProjectDocument(null);
        firstMessage.current?.focus({ preventScroll: true });
      },
    }}>
      <Dialog
        open={open}
        onClose={dismiss}
        title={t("newagent.title")}
        width={520}
        footer={
          <>
            <span className="mr-auto min-w-0 truncate text-[11.5px] text-ink-3">{desktopScratch ? t("newagent.temporary_session") : cwd ? shortPath(cwd) : t("newagent.pick_folder")}</span>
            <Button tone="quiet" onClick={dismiss}>
              {t("common.cancel")}
            </Button>
            <Button tone="primary" busy={wtBusy} disabled={loadingModels || presetUnavailable || execBlocked || !workspaceCurrent || !targetOnline || !(cwd.trim() || desktopScratch) || Boolean(documentBlocked) || (needsFirst && !prompt.trim() && !projectDocument)} onClick={start}>
              {runCount > 1 ? t("newagent.start_runs", { count: runCount }) : t("newagent.start")}
            </Button>
          </>
        }
      >
        <div ref={formTop} className="flex flex-col gap-4">
          {presetUnavailable && <p role="alert" className="text-xs text-red">{t("newagent.desktop_unavailable", { app: presetSource === "codex_app" ? "Codex" : "Claude" })}</p>}
          {presetProject && !desktopProjectId && <p role="status" className="text-xs text-ink-3">{t("newagent.desktop_project_detached")}</p>}
          {cards.length > 0 && !presetFolder && (
            <RecentSetups
              names={names}
              cards={cards}
              machines={machines}
              picked={pickedShown}
              onPick={pickSetup}
              onForget={(setup) => setRecents(forgetSetup(setup))}
              onClear={() => {
                setRecents(clearRecentSetups());
                setPickedSetup("");
              }}
            />
          )}
          <Field label={t("newagent.computer")}>
            <div className="flex flex-wrap gap-1.5">
              {machines.map((m) => (
                <button
                  key={m.id}
                  type="button"
                  disabled={(Boolean(workspace) && !workspacePicked) || !m.online}
                  onClick={() => {
                    if (m.id === mid) return;
                    if (workspacePicked) clearWorkspace();
                    setMid(m.id);
                  }}
                  className={cx(
                    "flex h-8 items-center gap-1.5 rounded-control px-2.5 text-[12.5px] whitespace-nowrap transition-[background-color,box-shadow,color]",
                    workspace && !workspacePicked && mid === m.id ? "disabled:opacity-100" : "disabled:opacity-40",
                    mid === m.id ? "bg-ink font-medium text-surface" : "bg-surface text-ink-2 shadow-btn enabled:hover:bg-hover enabled:hover:text-ink",
                  )}
                >
                  <span className={cx("size-1.5 rounded-full", m.online ? "bg-green" : "bg-ink-3")} />
                  {m.name}
                  {hubLabel(m, machines) && <span className={mid === m.id ? "opacity-70" : "text-ink-3"}>{hubLabel(m, machines)}</span>}
                </button>
              ))}
            </div>
          </Field>

          {workspace ? <Field label={t("newagent-workspace.destination")}>
            <div data-testid="new-agent-workspace" className="flex min-w-0 flex-col gap-1.5 rounded-control bg-inset px-3 py-2.5 text-[12.5px] text-ink shadow-hairline">
              <span className="font-medium [overflow-wrap:anywhere]">{workspace.label || baseName(workspace.cwd) || t("newagent-workspace.destination")}</span>
              <span className="font-mono text-ink-2 [overflow-wrap:anywhere]">{workspace.cwd}</span>
              <span className="text-ink-2 [overflow-wrap:anywhere]">{engineName(workspace.host, picked)}</span>
              <p className="text-[11.5px] text-ink-3">{t("newagent-workspace.existing_hint")}</p>
              {!workspaceCurrent && <p role="alert" className="text-red">{t("newagent-workspace.owner_changed")}</p>}
              <Button tone="quiet" onClick={clearWorkspace}>{t("newagent-workspace.choose_folder")}</Button>
            </div>
          </Field> : <>
          {workspaceGroups.length > 0 && <Field label={t("newagent-workspace.destination")}>
            <WorkspaceChoices groups={workspaceGroups} onPick={pickWorkspace} />
          </Field>}
          <Field label={t("newagent.folder")}>
            {desktopScratch && <Suspense fallback={null}><DesktopTemporaryRow codex={codexApp} /></Suspense>}
            <div className="relative">
              <input
                value={desktopScratch ? "" : cwd}
                onChange={(e) => typeCwd(e.target.value)}
                placeholder={t("newagent.folder_placeholder")}
                spellCheck={false}
                className={cx(
                  "h-9 w-full rounded-control bg-inset px-3 font-mono text-[12.5px] text-ink shadow-hairline outline-none placeholder:font-sans placeholder:text-ink-3 focus:shadow-[0_0_0_1px_var(--line-strong)]",
                  !desktopScratch && cwd && "pr-8",
                )}
              />
              {!desktopScratch && cwd && (
                <button
                  type="button"
                  aria-label={t("newagent.clear_folder")}
                  onClick={clearCwd}
                  className="touch-hit absolute top-1/2 right-1.5 flex size-6 -translate-y-1/2 items-center justify-center rounded-[4px] text-ink-3 hover:bg-hover hover:text-ink"
                >
                  <Icon name="x" size={12} stroke={2.2} />
                </button>
              )}
            </div>
            <div className="mt-1.5 flex max-h-52 flex-col overflow-hidden rounded-control bg-surface shadow-hairline">
              <div className="flex shrink-0 items-center gap-1.5 border-b border-line px-2 py-1">
                {showRecent ? (
                  <span className="min-w-0 flex-1 truncate text-submeta font-medium text-ink-3">{t("newagent.recent_folders")}</span>
                ) : (
                  <div className="flex min-w-0 flex-1 items-center gap-1">
                    {dirs?.parent && dirs.parent !== dirs.path && (
                      <button
                        type="button"
                        aria-label={t("newagent.parent_folder")}
                        onClick={() => navigateFolder(dirs.parent!)}
                        className="touch-hit -ml-1 flex size-5 shrink-0 items-center justify-center rounded-[4px] text-ink-3 hover:bg-hover hover:text-ink"
                      >
                        <Icon name="chevronLeft" size={12} stroke={2.4} />
                      </button>
                    )}
                    {breadcrumbs.length > 0 ? (
                      <nav ref={breadcrumbRef} aria-label={t("newagent.folder_path")} className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto text-label">
                        {breadcrumbs.map((seg, i) => {
                          const last = i === breadcrumbs.length - 1;
                          return (
                            <span key={seg.path} className="flex shrink-0 items-center gap-0.5">
                              {i > 0 && <span aria-hidden className="px-0.5 text-ink-3">/</span>}
                              <button
                                type="button"
                                data-breadcrumb-path={seg.path}
                                aria-current={last ? "location" : undefined}
                                title={seg.path}
                                onClick={() => navigateFolder(seg.path)}
                                className={cx(
                                  "touch-hit rounded-[4px] px-1 py-0.5 whitespace-nowrap transition-colors hover:bg-hover hover:text-ink",
                                  last ? "font-medium text-ink" : "text-ink-2",
                                )}
                              >
                                {seg.label}
                              </button>
                            </span>
                          );
                        })}
                      </nav>
                    ) : (
                      <span className="min-w-0 flex-1 truncate text-label text-ink-3">{t("newagent.home_folder")}</span>
                    )}
                  </div>
                )}
                {loadingDirs && <Spinner size={10} />}
                {!showRecent && desktopStart && dirs?.path && !naming && <Suspense fallback={null}><NewFolderButton onClick={() => setNaming(true)} /></Suspense>}
                {recentFolders.length > 0 && (
                  <SegmentedTrack
                    role="tablist"
                    label={t("newagent.folder_view")}
                    data-testid="folder-view-toggle"
                    className="flex h-6 shrink-0 items-center rounded-[6px] p-0.5"
                    thumbClassName="rounded-[4px]"
                  >
                    {(["browse", "recent"] as const).map((view) => {
                      const selected = (showRecent ? "recent" : "browse") === view;
                      return (
                        <button
                          key={view}
                          type="button"
                          role="tab"
                          aria-selected={selected}
                          data-selected={selected}
                          onClick={() => setFolderView(view)}
                          className={cx(
                            "touch-hit relative z-[1] h-5 whitespace-nowrap rounded-[4px] px-1.5 text-submeta font-medium transition-colors duration-150",
                            selected ? "text-ink" : "text-ink-3 hover:text-ink",
                          )}
                        >
                          {t(view === "browse" ? "newagent.browse" : "newagent.recent")}
                        </button>
                      );
                    })}
                  </SegmentedTrack>
                )}
              </div>
              <div className="min-h-0 flex-1 overflow-y-auto p-1">
                {showRecent ? (
                  recentFolders.map((d) => (
                    <DirRow
                      key={d.path}
                      name={d.name || baseName(d.path)}
                      path={d.path}
                      sub={shortPath(d.path)}
                      selected={!desktopScratch && cwd === d.path}
                      onPick={() => navigateFolder(d.path)}
                    />
                  ))
                ) : (
                  <>
                    {naming && dirs?.path && (
                      <Suspense fallback={null}>
                        <NewFolderNaming key={dirs.path} folder={dirs.path} onCancel={() => setNaming(false)} onUse={(made) => { navigateFolder(made); setNaming(false); }} />
                      </Suspense>
                    )}
                    {visibleEntries.length > 0 ? (
                      visibleEntries.map((d) => (
                        <DirRow key={d.path} name={d.name} path={d.path} git={d.is_git} selected={!desktopScratch && cwd === d.path} drillable onPick={() => navigateFolder(d.path)} />
                      ))
                    ) : (
                      !loadingDirs && dirs && !naming && (
                        <div className="px-2 py-2 text-center text-label text-ink-3">{t("newagent.no_subfolders")}</div>
                      )
                    )}
                  </>
                )}
              </div>
            </div>
            {desktopStart && <Suspense fallback={null}><DesktopFolderNote codex={codexApp} scratch={desktopScratch} onUseTemporary={() => setCwdPicked(false)} /></Suspense>}
          </Field></>}
          {worktreeOffered && !desktopScratch && (
            <div data-testid="new-agent-worktree" className="-mt-2 rounded-control bg-surface px-3 py-2 shadow-hairline">
              <div className="flex items-center gap-2">
                <Icon name="branch" size={13} stroke={2.2} className="shrink-0 text-ink-2" />
                <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink">{t("newagent.worktree")}</span>
                <Switch on={inWorktree} onChange={setInWorktree} label={t("newagent.worktree")} />
              </div>
              <p className="mt-1 pl-[21px] text-[11.5px] leading-snug text-ink-3">{t("newagent.worktree_hint")}</p>
              {inWorktree && (
                <label className="mt-1.5 flex items-center gap-2 pl-[21px]">
                  <span className="shrink-0 text-[11.5px] text-ink-3">{t("newagent.worktree_branch")}</span>
                  <input
                    value={wtBranch}
                    onChange={(e) => {
                      setWtBranch(e.target.value);
                      setWtError("");
                    }}
                    placeholder={agentBranch(agentNameHint(prompt, cwd))}
                    spellCheck={false}
                    data-worktree-branch
                    className="h-7 min-w-0 flex-1 rounded-control bg-inset px-2 font-mono text-[12px] text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-[0_0_0_1px_var(--line-strong)]"
                  />
                </label>
              )}
              {inWorktree && repo?.checkouts && (
                <p data-worktree-path className="mt-1 truncate pl-[21px] font-mono text-[11px] text-ink-3">
                  {t("newagent.worktree_path", { path: shortPath(suggestedPath(repo.checkouts, wtBranch.trim() || agentBranch(agentNameHint(prompt, cwd)))) })}
                </p>
              )}
              {runsOffered && (
                <div data-new-agent-runs className="mt-1.5 pl-[21px]">
                  <div className="flex items-center gap-2">
                    <span id="new-agent-runs" className="shrink-0 text-submeta text-ink-3">{t("newagent.runs")}</span>
                    <div role="radiogroup" aria-labelledby="new-agent-runs" className="flex gap-1">
                      {Array.from({ length: MAX_RUNS }, (_, i) => i + 1).map((n) => (
                        <Choice key={n} radio selected={runTimes === n} onClick={() => setRunTimes(n)}>{n}</Choice>
                      ))}
                    </div>
                  </div>
                  {runTimes > 1 && <p className="mt-1 text-submeta leading-snug text-ink-3">{t("newagent.runs_hint", { count: runTimes })}</p>}
                </div>
              )}
              {wtError && <p role="alert" className="mt-1 pl-[21px] text-[11.5px] leading-snug text-red">{t("newagent.worktree_failed", { error: wtError })}</p>}
            </div>
          )}

          {!desktopStart && !workspace && hosts.length > 1 && (
            <Field label={t("lib-engines.start_in")}>
              <div role="radiogroup" aria-label={t("lib-engines.start_in")} data-testid="start-host" className="flex flex-wrap gap-1.5">
                {hosts.map((h) => (
                  <Choice key={h} radio selected={(host || hostDefault) === h} onClick={() => setHost(h)}>
                    {engineName(h, picked)}
                    {h === hostDefault ? ` · ${t("lib-engines.default")}` : ""}
                  </Choice>
                ))}
              </div>
            </Field>
          )}

          {claudeModes && (
            <Field label={t("newagent.permission_mode")}>
              <div role="radiogroup" aria-label={t("newagent.permission_mode")} className="flex flex-wrap gap-1.5">
                <Choice radio selected={effectiveClaudeMode === ""} onClick={() => chooseMode("")}>{t("newagent.follow_launcher")}</Choice>
                {claudeModeOptions.map((m) => (
                  <Choice key={m.id} radio selected={effectiveClaudeMode === m.id} onClick={() => chooseMode(m.id)} title={m.detail}>
                    {m.title}
                  </Choice>
                ))}
              </div>
              <p className={cx("mt-1.5 text-[11.5px]", effectiveClaudeMode === BYPASS_MODE ? "text-red" : "text-ink-3")}>
                {effectiveClaudeMode === BYPASS_MODE ? BYPASS_WARNING : claudeModeOptions.find((m) => m.id === effectiveClaudeMode)?.detail ?? t("newagent.permission_mode_hint")}
              </p>
            </Field>
          )}

          {/* a launcher that lists its own modes (Paseo's other providers, #8261): its own, or what else it lists (Full
              access), the two its conversation switches between; one with nothing more than its own shows no field */}
          {providerModes && providerModes.some((m) => m !== "default") && (
            <Field label={t("newagent.permission_mode")}>
              <div role="radiogroup" aria-label={t("newagent.permission_mode")} data-testid="new-agent-provider-modes" className="flex flex-wrap gap-1.5">
                <Choice radio selected={providerMode === "" || providerMode === "default" || !providerModes.includes(providerMode)} onClick={() => chooseProviderMode("")}>{t("newagent.follow_launcher")}</Choice>
                {providerModes.filter((id) => id !== "default").map(listedModeOption).map((m) => (
                  <Choice key={m.id} radio selected={providerMode === m.id} onClick={() => chooseProviderMode(m.id)} title={m.detail}>
                    {m.title}
                  </Choice>
                ))}
              </div>
              <p className={cx("mt-1.5 text-submeta", providerMode === BYPASS_MODE ? "text-red" : "text-ink-3")}>
                {providerMode === BYPASS_MODE ? BYPASS_WARNING : t("newagent.provider_mode_hint")}
              </p>
            </Field>
          )}

          <Field label={t(needsFirst ? "newagent.first_message" : "newagent.first_message_optional")}>
            <div className={cx("flex flex-col gap-2 rounded-control bg-inset shadow-hairline focus-within:shadow-[0_0_0_1px_var(--line-strong)]", dragging && "outline-2 outline-accent")}
              onDragOver={(e) => { if (e.dataTransfer.types.includes("Files")) { e.preventDefault(); setDragging(true); } }}
              onDragLeave={(e) => { if (!e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false); }}
              onDrop={(e) => { e.preventDefault(); setDragging(false); addFiles(e.dataTransfer.files); }}>
              {/* an Inbox item handed to a new Agent (#5933) shows as forwarded from where it came ("Lark · <title>"); shown
                  only: it goes as the <persona-context> an Agent target gets */}
              {forwards.length > 0 && <div className="px-2 pt-2" data-new-agent-forward>{forwards.length > 1 ? <Suspense fallback={null}><ForwardGroupBar forwards={forwards} onRemove={() => setForwards([])} /></Suspense> : <ForwardBar forward={forwards[0].kind === "context" ? { ...forwards[0], attrs: { author: forwards[0].title } } : forwards[0]} onRemove={() => setForwards([])} />}</div>}
              {attachments.length > 0 && <div className="px-3 pt-2"><AttachmentTray attachments={attachments} onRemove={(id) => {
                releaseAttachments(attachments.filter((a) => a.id === id));
                setAttachments((current) => current.filter((a) => a.id !== id));
              }} /></div>}
              <ProjectDocumentSource />
              <textarea
                ref={firstMessage}
                value={prompt}
                onChange={(e) => { promptRevision.current++; setPrompt(e.target.value); }}
                onPaste={(e) => { if (e.clipboardData.files.length) { e.preventDefault(); addFiles(e.clipboardData.files); } }}
                onKeyDown={(e) => {
                  if (e.nativeEvent.isComposing || e.keyCode === 229) return;
                  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); start(); }
                }}
                rows={3}
                aria-label={t("newagent.first_message")}
                placeholder={keys ? t("newagent.prompt_placeholder_keys", { keys }) : t("newagent.prompt_placeholder")}
                className="w-full resize-none bg-transparent px-3 py-2 text-[13px] leading-relaxed text-ink outline-none placeholder:text-ink-3"
              />
              <div className="flex items-center gap-2 px-2 pb-2">
                <input ref={fileInput} type="file" multiple hidden onChange={(e) => { if (e.currentTarget.files) addFiles(e.currentTarget.files); e.currentTarget.value = ""; }} />
                <button type="button" aria-label={t("newagent.add_files")} title={t("newagent.add_files")} onClick={() => fileInput.current?.click()} className="flex size-7 shrink-0 items-center justify-center rounded-control text-ink-3 hover:bg-hover hover:text-ink"><Icon name="plus" size={16} stroke={2} /></button>
                {/* the hint gives way to the model pill (on a phone it is only the + button's tooltip) */}
                <span title={t("newagent.attach_hint")} className="min-w-0 flex-1 truncate text-[11.5px] text-ink-3 max-sm:hidden">{t("newagent.attach_hint")}</span>
                <span className="ml-auto flex min-w-0 shrink justify-end" data-testid="new-agent-model">
                  <NewAgentModel
                    mid={mid}
                    host={opensIn}
                    startHost={startHost}
                    launchers={launchers}
                    ready={settled}
                    onRetry={launchersFailed ? retryLaunchers : undefined}
                    choice={{ launcher, model, effort }}
                    runs={runs}
                    configured={unified ? configured : null}
                    tiers={tiers}
                    unavailable={skipped}
                    follow={unified ? { label: t("newagent.follow_row"), active: launcher === "", detail: followDetail, warning: defaultBlock?.reason } : undefined}
                    fallback={modelDefault || t("chat-modelpill.default_model")}
                    speed={speedOptions && speedKey ? { options: speedOptions, value: speed, onChange: (value) => setSpeedChoice({ key: speedKey, value }) } : undefined}
                    ultracode={ultracodeShown ? <div data-testid="new-agent-ultracode"><UltracodeStartRow on={ultracode} onChange={setUltracode} blocked={ultracodeBlocked} /></div> : undefined}
                    ultracodeOn={ultracodeShown && !ultracodeBlocked && ultracode}
                    onChoice={pickModel}
                    onPreset={(name) => {
                      const x = tiers.find((v) => v.name === name);
                      if (x) pickTier(x);
                    }}
                    onFollow={followDefault}
                    onHelp={(name) => {
                      // a provider left out here: its page in Settings, where "Computers" turns its own-service rule off
                      if (!skipped.some((u) => u.name === name)) return setHelpFor(name);
                      dismiss();
                      navigate(providersPath(name));
                    }}
                    onConfigure={() => {
                      dismiss();
                      navigate(settingsHref("models", null, false, null, "providers"));
                    }}
                  />
                </span>
              </div>
            </div>
            <ModelNotes>
              {orcaChat && (
                <Note data-testid="new-agent-orca-chat" icon="info">
                  {t(orcaChat.dropped ? "newagent.opens_orca_chat_own" : "newagent.opens_orca_chat", { name: orcaChat.display_name || orcaChat.launcher })}
                </Note>
              )}
              {dropped && (
                <Note data-testid="new-agent-dropped" icon="info">{t("newagent.carry_dropped", { name: dropped.name, computer: dropped.computer })}</Note>
              )}
              {launchersFailed && (
                // this computer's launchers couldn't be read (#10952): why, and Retry; Start still runs its default
                <Note data-testid="new-agent-launchers-failed" icon="warning" tone="orange">
                  {t("newagent.launchers_failed")} <span className="text-ink-3">{launchersFailed}</span>{" "}
                  <button type="button" onClick={retryLaunchers} className="touch-hit whitespace-nowrap text-accent-ink hover:underline">
                    {t("newagent.retry")}
                  </button>
                </Note>
              )}
              {runs && models.list === undefined && models.error && !models.loading && (
                <Note data-testid="new-agent-models-failed" icon="warning" tone="orange">{t("newagent.models_failed")}</Note>
              )}
              {runsBlock && (
                // what runs this time can't start here (#1643): one flow of words that wraps (a phone has no hover), the link a unit
                <Note data-testid="new-agent-blocked" icon="warning" tone="orange">
                  <Pill tone="orange">{t("newagent.runs_this_time")}</Pill>{" "}
                  <ProviderName of={namedLauncher(runs, names)} className="text-ink-2" idClassName="text-[11px]" /> <span className="text-ink-3">{runsBlock.reason}</span>{" "}
                  <button
                    type="button"
                    aria-expanded={helpFor === runs}
                    aria-controls={helpFor === runs ? "new-agent-blocked-help" : undefined}
                    aria-label={t("newagent.how_to_fix_named", { name: launcherTitle(runs, names) })}
                    onClick={() => setHelpFor((open) => (open === runs ? "" : runs))}
                    className="whitespace-nowrap text-accent-ink hover:underline"
                  >
                    {t("newagent.how_to_fix")}
                  </button>
                </Note>
              )}
              {tier && (
                <Note data-testid="new-agent-preset-hint" icon={tierMissed.length ? "warning" : "layers"} tone={tierMissed.length ? "orange" : undefined}>
                  {tierMissed.length
                    ? t("newagent.preset_left_out", { what: tierMissed.map((v) => (v === tier.effort ? effortTitle(v) : v)).join(" · ") })
                    : t("newagent.preset_picked", { name: tier.name, what: tier.description || tierRuns(tier, names) })}
                </Note>
              )}
            </ModelNotes>
            <div data-testid="new-agent-execution" className="mt-1.5 empty:hidden">
              <ExecutionCheck
                machine={mid}
                launcher={runs}
                model={model}
                effort={effort}
                onBlocked={setExecBlocked}
                onPick={(p) => pickModel({ launcher: p.launcher, model: p.model ?? "", effort: p.effort ?? "" })}
              />
            </div>
            {helpLauncher && helpBlock && (
              <LauncherBlockedHelp
                launcher={helpLauncher}
                block={helpBlock}
                onTryAnyway={() => pickModel({ launcher: helpLauncher.name, model: "", effort: "" })}
                onAddProvider={() => {
                  dismiss();
                  navigate("/settings?tab=models&add=1"); // the provider wizard, as readiness's connect_model
                }}
                onClose={() => setHelpFor("")}
              />
            )}
          </Field>
        </div>
      </Dialog>
      {adding.dialog}
    </ProjectDocumentContext>
  );
}

/** The lines under the first message box about the model choice (what was dropped, what can't start here, the preset);
 *  nothing when there are none. */
function ModelNotes({ children }: { children: React.ReactNode }) {
  const lines = (Array.isArray(children) ? children : [children]).filter(Boolean);
  if (!lines.length) return null;
  return <div aria-live="polite" className="mt-1.5 flex flex-col gap-1">{lines}</div>;
}

function Note({ icon, tone, children, ...rest }: { icon: "info" | "warning" | "layers"; tone?: "orange"; children: React.ReactNode; "data-testid"?: string }) {
  return (
    <p {...rest} className={cx("flex min-w-0 items-start gap-1.5 text-[11.5px] leading-snug", tone === "orange" ? "text-orange" : "text-ink-3")}>
      <Icon name={icon} size={12} className={cx("mt-px shrink-0", tone === "orange" && "text-orange")} />
      <span className="min-w-0 [overflow-wrap:anywhere]">{children}</span>
    </p>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1.5 text-[12px] font-medium text-ink-2">{label}</div>
      {children}
    </div>
  );
}

// The recent setups (#2134): up to three cards in a row, each a setup that started an agent (folder · computer, then launcher
// · model · mode · effort); an offline computer's is dimmed and can't be picked. The ⋯ removes one or clears them all.
function RecentSetups({ cards, machines, names, picked, onPick, onForget, onClear }: { cards: RecentCard[]; machines: Machine[]; /** What a launcher ID is called (#5029). */ names: Named[]; /** The card the form holds (setupKey); "": none. */ picked: string; onPick: (card: RecentCard) => void; onForget: (setup: RecentSetup) => void; onClear: () => void }) {
  const menu = useMenu();
  // a card of a temporary session (a start in a desktop app with no folder, #5480) says so where the folder goes
  const folder = (s: RecentSetup) => (s.cwd ? baseName(shortPath(s.cwd)) : t("newagent.temporary_session"));
  const title = (s: RecentSetup) => `${folder(s)} · ${machines.find((m) => m.id === s.machine)?.name ?? s.machine}`;
  return (
    <div data-testid="recent-setups">
      <div className="mb-1.5 flex h-[18px] items-center justify-between gap-2">
        <span className="text-[12px] font-medium text-ink-2">{t("newagent.recent_setups")}</span>
        <IconButton icon="more" size={22} iconSize={14} label={t("newagent.recent_options")} onClick={menu.open} active={Boolean(menu.anchor)} />
        {menu.anchor && (
          <Menu
            anchor={menu.anchor}
            onClose={menu.close}
            align="right"
            width={232}
            items={[
              { key: "remove", label: t("newagent.recent_remove"), icon: "x", onSelect: () => {}, children: cards.map((c) => ({ key: setupKey(c.setup), label: title(c.setup), onSelect: () => onForget(c.setup) })) },
              { key: "clear", label: t("newagent.recent_clear"), icon: "trash", danger: true, divider: true, onSelect: onClear },
            ]}
          />
        )}
      </div>
      <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-3">
        {cards.map((c) => {
          const s = c.setup;
          const on = picked === setupKey(s);
          const claude = s.agent_type === "claude";
          // the launcher by its display name (#5029), its startup ID in the card's tooltip; an rc alias keeps its ID in mono
          const runsOn = s.launcher ? namedLauncher(s.launcher, names) : null;
          const runsId = runsOn ? providerIdNote(runsOn) : "";
          const parts: React.ReactNode[] = [
            runsOn ? <span key="l" className={cx(!runsId && "font-mono")}>{providerTitle(runsOn)}</span> : t("newagent.follow_model_settings"),
            s.model && <span key="m" className="font-mono">{s.model}</span>,
            // "Allow everything" said with the warning it has in the form
            claude && s.mode && (s.mode === BYPASS_MODE
              ? <span key="p" className="inline-flex items-center gap-0.5 font-medium text-orange"><Icon name="warning" size={11} className="shrink-0" />{modeTitle(s.mode)}</span>
              : modeTitle(s.mode)),
            claude && s.effort && effortTitle(s.effort),
            !c.online && t("machines.offline"),
          ].filter(Boolean);
          return (
            <button
              key={setupKey(s)}
              type="button"
              data-recent-setup
              data-bypass={(claude && s.mode === BYPASS_MODE) || undefined}
              aria-pressed={on}
              disabled={!c.online}
              title={[shortPath(s.cwd), runsId].filter(Boolean).join("\n")}
              onClick={() => onPick(c)}
              className={cx(
                "flex min-w-0 flex-col gap-0.5 rounded-control px-2.5 py-2 text-left transition-[background-color,box-shadow,opacity] disabled:opacity-45",
                on ? "bg-accent-tint shadow-[0_0_0_1.5px_var(--accent)]" : "bg-surface shadow-btn enabled:hover:bg-hover",
              )}
            >
              <span className="flex min-w-0 items-baseline gap-1 text-[12.5px] leading-snug">
                <span className={cx("min-w-0 shrink truncate font-medium", on ? "text-accent-ink" : "text-ink")}>{folder(s)}</span>
                <span className="shrink-0 text-ink-3">·</span>
                <span className="min-w-0 shrink-[2] truncate text-ink-2">{machines.find((m) => m.id === s.machine)?.name ?? s.machine}</span>
              </span>
              <span className="line-clamp-2 text-[11.5px] leading-snug text-ink-3">
                {parts.map((p, i) => (
                  <span key={i}>
                    {i > 0 && " · "}
                    {p}
                  </span>
                ))}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

/** What a preset runs on, in a line: its launcher ("Title (id)", #5029), model and effort (its name). */
function tierRuns(x: PickerTier, names: Named[]): string {
  return [providerLabel(namedLauncher(x.launcher, names)), x.model, x.effort && effortTitle(x.effort), x.permission_mode && modeTitle(x.permission_mode)].filter(Boolean).join(" · ");
}

function Choice({ selected, onClick, title, radio, children }: { selected: boolean; onClick: () => void; title?: string; /** One of a radiogroup. */ radio?: boolean; children: React.ReactNode }) {
  return (
    <button
      type="button"
      role={radio ? "radio" : undefined}
      aria-checked={radio ? selected : undefined}
      aria-pressed={radio ? undefined : selected}
      onClick={onClick}
      title={title}
      className={cx(
        "h-7 rounded-control px-2.5 text-[12px] whitespace-nowrap transition-colors",
        selected ? "bg-ink text-surface" : "bg-surface text-ink-2 shadow-btn hover:bg-hover hover:text-ink",
      )}
    >
      {children}
    </button>
  );
}

// A folder to start in: single-click navigates into it and updates cwd in lockstep (#14595).
function DirRow({ name, path, sub, git, selected, drillable, onPick }: { name: string; path: string; sub?: string; git?: boolean; selected: boolean; drillable?: boolean; onPick: () => void }) {
  return (
    <button
      type="button"
      data-dir-row
      onClick={onPick}
      title={path}
      className={cx(
        "group/dir flex h-8 w-full items-center gap-2 rounded-[6px] px-2 text-left transition-colors pointer-coarse:min-h-11",
        selected ? "bg-accent-tint" : "hover:bg-hover",
      )}
    >
      <Icon name={git ? "git" : "folder"} size={13} className={cx("shrink-0", selected ? "text-accent-ink" : "text-ink-3")} />
      <span className={cx("shrink-0 truncate text-[12.5px]", selected ? "font-medium text-accent-ink" : "text-ink")}>{name}</span>
      {sub && <span className="min-w-0 truncate font-mono text-[11px] text-ink-3">{sub}</span>}
      {drillable && <Icon name="chevronRight" size={12} stroke={2.4} className="ml-auto shrink-0 text-ink-3 transition-colors group-hover/dir:text-ink" />}
    </button>
  );
}
