import { liveViewManager } from '@kit.LiveViewKit';
import { bundleManager, common, wantAgent } from '@kit.AbilityKit';
import type { WantAgent } from '@ohos.wantAgent';
import type { ClientModel } from '../models/ClientModel';
import { ResourceRow, Wire } from '../models/Wire';
import { tr } from '../design/Strings';
import { shouldEndLiveViewOnBackground } from '../core/HubModules';
import {
  FLEET_END_DELAY_MS,
  FLEET_LIVE_VIEW_ID,
  buildFleetLiveViewSpec,
  decideFleetStep,
  decodeFleetState,
  deriveFleetFromSessions,
  emptyFleetState,
  isFleetWorthUpdating,
  sampleFleetState,
  shouldClearFleetNow
} from '../core/FleetLiveState';
import type {
  FleetLiveState,
  FleetLiveViewSpec,
  FleetSessionSummary,
  FleetStrings
} from '../core/FleetLiveState';

export const LIVE_VIEW_ACTION: string = 'persona.liveview.OPEN';

export interface LiveViewPrefsStorage {
  get(key: string): Promise<string | undefined>;
  put(key: string, value: string): Promise<void>;
}

const LIVE_VIEW_PREFS_KEY: string = 'live_view_prefs';
const LIVE_VIEW_ICON: string = 'live_view_icon.png';

function fleetStrings(): FleetStrings {
  return {
    appTitle: tr('persona'),
    needsYouCount: tr('live_view_needs_count'),
    workingCount: tr('live_view_working_count'),
    unreadCount: tr('live_view_unread_count'),
    allCaughtUp: tr('live_view_all_caught_up'),
    assistantReplying: tr('live_view_assistant_replying'),
    backgroundTag: tr('live_view_background_tag'),
    anonPermission: tr('live_view_anon_permission'),
    anonPlan: tr('live_view_anon_plan'),
    anonQuestion: tr('live_view_anon_question'),
    anonScreen: tr('live_view_anon_screen'),
    anonWorking: tr('live_view_anon_working'),
    anonUnread: tr('live_view_anon_unread'),
    capsuleNeeds: tr('live_view_capsule_needs'),
    capsuleWorking: tr('live_view_capsule_working'),
    capsuleUnread: tr('live_view_capsule_unread'),
    capsuleCaughtUp: tr('live_view_capsule_done'),
    moreSuffix: tr('live_view_more_count')
  };
}

function summarizeSession(model: ClientModel, row: ResourceRow): FleetSessionSummary {
  const data = row.data();
  const machineId = row.machine || Wire.text(data, 'machine_id');
  const sessionId = row.session || Wire.text(data, 'session_id') || row.id;
  const work = Wire.object(data, 'work');
  const workflows = Wire.rows(data, 'workflows');
  const runningWorkflow = workflows.find((wf: Record<string, Object>) => Wire.text(wf, 'status') === 'running' || Wire.bool(wf, 'running'));
  const bgWorking = Wire.bool(data, 'background_working') || Wire.bool(work, 'background_working');
  const updatedMs = Date.parse(Wire.text(data, 'updated_at'));
  const startedMs = Date.parse(Wire.text(work, 'started_at') || Wire.text(data, 'work_started_at'));
  const wfStartedMs = runningWorkflow ? Date.parse(Wire.text(runningWorkflow, 'started_at')) : Number.NaN;
  const effectiveStartMs = bgWorking && Number.isFinite(wfStartedMs) ? wfStartedMs : startedMs;
  const machineName = machineId ? model.machineName(machineId) || machineId : '';
  const spaceName = Wire.text(data, 'space_name');
  const place = [machineName, spaceName].filter((part: string) => !!part).join(' · ');
  const interaction = Wire.object(data, 'pending_interaction');
  const bgDetail = runningWorkflow
    ? [Wire.text(runningWorkflow, 'name'), Wire.text(runningWorkflow, 'phase')].filter((part: string) => !!part).join(' · ')
    : Wire.text(work, 'background_detail') || Wire.text(data, 'last_text');
  return {
    machine: machineId,
    session: sessionId,
    title: row.title || Wire.text(data, 'title') || tr('agent_untitled'),
    place: place,
    state: row.state || Wire.text(data, 'status'),
    detail: Wire.text(data, 'last_text') || row.detail,
    updatedAtSec: Number.isFinite(updatedMs) ? Math.floor(updatedMs / 1000) : 0,
    startedAtSec: Number.isFinite(effectiveStartMs) ? Math.floor(effectiveStartMs / 1000) : 0,
    muted: Wire.bool(data, 'muted') || Wire.bool(data, 'quiet'),
    backgroundWorking: bgWorking,
    backgroundDetail: bgDetail,
    progressDone: runningWorkflow ? Wire.number(runningWorkflow, 'agents_done', 0) : 0,
    progressStarted: runningWorkflow ? Wire.number(runningWorkflow, 'agents_started', 0) : 0,
    interactionKind: Wire.text(interaction, 'kind') || Wire.text(data, 'needs_you_kind'),
    interactionId: Wire.text(interaction, 'id')
  };
}

/**
 * HarmonyOS LiveViewKit Fleet controller aligned to iOS `LiveActivityController`
 * (apple/Persona/App/LiveActivityController.swift & docs/apple/live-activity.md).
 */
@ObservedV2
export class NativeLiveView {
  @Trace enabled: boolean = true;
  @Trace lockDetails: boolean = false;
  @Trace systemEnabled: boolean = false;
  @Trace active: boolean = false;
  @Trace busy: boolean = false;
  @Trace statusCode: string = '';
  @Trace currentState: FleetLiveState = emptyFleetState(false);

  private context: common.UIAbilityContext | undefined;
  private storage: LiveViewPrefsStorage | undefined;
  private shownState: FleetLiveState | undefined;
  private lastUpdatedMs: number = 0;
  private sequence: number = 1;
  private endTimer: number = -1;
  private epoch: number = 0;
  private foreground: boolean = true;
  private ownerProfile: string = '';
  private ownerManager: string = '';
  private rawHubFleet: Record<string, Object> | undefined;

  async initialize(context: common.UIAbilityContext, storage?: LiveViewPrefsStorage): Promise<void> {
    this.context = context;
    if (storage) this.storage = storage;
    await this.loadPrefs();
    await this.refreshSystemState();
  }

  private async loadPrefs(): Promise<void> {
    if (!this.storage) return;
    try {
      const raw = await this.storage.get(LIVE_VIEW_PREFS_KEY);
      if (!raw) return;
      const parsed = Wire.parse(raw);
      if (Wire.has(parsed, 'enabled')) this.enabled = Wire.bool(parsed, 'enabled', true);
      if (Wire.has(parsed, 'lockDetails')) this.lockDetails = Wire.bool(parsed, 'lockDetails', false);
    } catch (_) {
      // Keep defaults when local preference storage is unavailable.
    }
  }

  private async savePrefs(): Promise<void> {
    if (!this.storage) return;
    try {
      await this.storage.put(LIVE_VIEW_PREFS_KEY, JSON.stringify({ enabled: this.enabled, lockDetails: this.lockDetails }));
    } catch (_) {
      this.statusCode = 'cache_unavailable';
    }
  }

  async refreshSystemState(): Promise<boolean> {
    try {
      this.systemEnabled = await liveViewManager.isLiveViewEnabled();
      if (!this.systemEnabled && this.active) {
        this.active = false;
        this.shownState = undefined;
      }
      return this.systemEnabled;
    } catch (_) {
      this.systemEnabled = false;
      return false;
    }
  }

  async setEnabled(value: boolean): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    this.statusCode = '';
    try {
      this.enabled = value;
      await this.savePrefs();
      if (!value) {
        await this.stopNow();
      } else {
        await this.refreshSystemState();
        if (this.currentState.phase !== 'none') await this.applyState(this.currentState, Date.now());
      }
    } finally {
      this.busy = false;
    }
  }

  async setLockDetails(value: boolean): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    this.statusCode = '';
    try {
      this.lockDetails = value;
      await this.savePrefs();
      const rebuilt: FleetLiveState = this.rawHubFleet
        ? decodeFleetState(this.rawHubFleet, this.lockDetails)
        : {
          phase: this.currentState.phase,
          needsYou: this.currentState.needsYou,
          working: this.currentState.working,
          unread: this.currentState.unread,
          items: this.currentState.items,
          workingSince: this.currentState.workingSince,
          assistant: this.currentState.assistant,
          details: this.lockDetails,
          stats: this.currentState.stats
        };
      this.currentState = rebuilt;
      if (this.active) await this.applyState(rebuilt, Date.now(), true);
    } finally {
      this.busy = false;
    }
  }

  setForeground(foreground: boolean, hasPushRoute: boolean = true, isDemo: boolean = false): void {
    this.foreground = foreground;
    if (foreground) {
      this.refreshSystemState().catch(() => {});
      return;
    }
    if (shouldEndLiveViewOnBackground(hasPushRoute, isDemo, this.active)) {
      this.stopNow().catch(() => {});
    }
  }

  /** Called when Hub pushes a `{"type":"fleet","fleet":{...}}` WebSocket event. */
  async receiveFleetEvent(model: ClientModel, data: Record<string, Object>, nowMs: number = Date.now()): Promise<void> {
    this.ownerProfile = model.profile.id;
    this.ownerManager = model.profile.manager;
    this.rawHubFleet = data;
    const state = decodeFleetState(data, this.lockDetails);
    this.currentState = state;
    await this.applyState(state, nowMs);
  }

  /** Local fallback derivation from catalog sessions (used in Demo mode or before Hub sends `fleet`). */
  async syncFromModel(model: ClientModel, nowMs: number = Date.now()): Promise<void> {
    this.ownerProfile = model.profile.id;
    this.ownerManager = model.profile.manager;
    this.rawHubFleet = undefined;
    const rows = model.directoryReady && model.directorySessions.length ? model.directorySessions : model.sessions;
    const summaries: FleetSessionSummary[] = rows
      .filter((row: ResourceRow) => row.kind === 'session' && !Wire.bool(row.data(), 'archived') && !Wire.bool(row.data(), 'assistant'))
      .map((row: ResourceRow) => summarizeSession(model, row));
    const state = deriveFleetFromSessions(summaries, this.lockDetails, model.assistantReplyWorking());
    this.currentState = state;
    await this.applyState(state, nowMs);
  }

  /** Matches iOS `LiveActivityController.showSample`: triggers a live preview directly on the device/emulator. */
  async publishSample(phase: string, nowMs: number = Date.now()): Promise<boolean> {
    this.statusCode = '';
    const sample = sampleFleetState(phase, this.lockDetails, nowMs);
    this.currentState = sample;
    if (phase === 'none') {
      await this.stopNow();
      return true;
    }
    return this.pushSpec(buildFleetLiveViewSpec(sample, fleetStrings(), nowMs), sample, !this.active, nowMs);
  }

  /** Matches iOS `LiveActivityController.endAll()` / Forget Hub cleanup. */
  async stopNow(hubId: string = ''): Promise<void> {
    if (hubId && this.ownerProfile && this.ownerProfile !== hubId) return;
    this.cancelEndTimer();
    this.epoch++;
    if (!this.active) {
      this.shownState = undefined;
      return;
    }
    const spec = buildFleetLiveViewSpec(emptyFleetState(this.lockDetails), fleetStrings(), Date.now());
    this.active = false;
    this.shownState = undefined;
    try {
      const payload = await this.buildNativePayload(spec, false);
      await liveViewManager.stopLiveView(payload);
    } catch (_) {
      // Already dismissed or not running on system side.
    }
  }

  private cancelEndTimer(): void {
    if (this.endTimer !== -1) {
      clearTimeout(this.endTimer);
      this.endTimer = -1;
    }
  }

  private async applyState(state: FleetLiveState, nowMs: number, force: boolean = false): Promise<void> {
    if (!this.enabled) {
      if (this.active) await this.stopNow();
      return;
    }
    const step = decideFleetStep(state, this.active, this.foreground);
    if (step === 'endSoon') {
      if (shouldClearFleetNow(this.shownState, state)) {
        const clearedSpec = buildFleetLiveViewSpec(state, fleetStrings(), nowMs);
        await this.pushSpec(clearedSpec, state, false, nowMs);
      }
      if (this.endTimer === -1) {
        const capturedEpoch = this.epoch;
        this.endTimer = setTimeout(() => {
          this.endTimer = -1;
          if (capturedEpoch === this.epoch) this.stopNow().catch(() => {});
        }, FLEET_END_DELAY_MS);
      }
      return;
    }
    this.cancelEndTimer();
    if (step === 'nothing') return;
    if (step === 'update' && !force && !isFleetWorthUpdating(this.shownState, this.lastUpdatedMs, state, nowMs)) return;
    const spec = buildFleetLiveViewSpec(state, fleetStrings(), nowMs);
    await this.pushSpec(spec, state, step === 'start', nowMs);
  }

  private async pushSpec(spec: FleetLiveViewSpec, state: FleetLiveState, isStart: boolean, nowMs: number): Promise<boolean> {
    const capturedEpoch = this.epoch;
    try {
      const systemOn = await this.refreshSystemState();
      if (!systemOn) {
        this.statusCode = 'live_view_system_disabled';
        return false;
      }
      const payload = await this.buildNativePayload(spec, true);
      if (capturedEpoch !== this.epoch) return false;
      if (isStart) {
        try {
          await liveViewManager.startLiveView(payload);
        } catch (err) {
          const code = Wire.number(Wire.record(err as Object), 'code', 0);
          // 1003500006: LiveView already exists — update in place.
          if (code === 1003500006) await liveViewManager.updateLiveView(payload);
          else throw err as Error;
        }
      } else {
        try {
          await liveViewManager.updateLiveView(payload);
        } catch (err) {
          const code = Wire.number(Wire.record(err as Object), 'code', 0);
          // 1003500009 / 1003500010: missing or ended on system side — recreate if foregrounded.
          if ((code === 1003500009 || code === 1003500010) && this.foreground) {
            await liveViewManager.startLiveView(payload);
          } else {
            throw err as Error;
          }
        }
      }
      if (capturedEpoch !== this.epoch) return false;
      this.active = true;
      this.shownState = state;
      this.lastUpdatedMs = nowMs;
      this.statusCode = '';
      return true;
    } catch (err) {
      const code = Wire.number(Wire.record(err as Object), 'code', 0);
      if (code === 1003500004 || code === 1003500005) this.statusCode = 'live_view_entitlement_required';
      else this.statusCode = 'live_view_unavailable';
      return false;
    }
  }

  private async buildClickAction(spec: FleetLiveViewSpec): Promise<WantAgent | undefined> {
    try {
      const info = bundleManager.getBundleInfoForSelfSync(bundleManager.BundleFlag.GET_BUNDLE_INFO_DEFAULT);
      const params = Wire.payload(
        ['profile', 'manager', 'machine', 'session'],
        [this.ownerProfile, this.ownerManager, spec.targetMachine, spec.targetSession]
      );
      return await wantAgent.getWantAgent({
        wants: [{ bundleName: info.name, abilityName: 'EntryAbility', action: LIVE_VIEW_ACTION, parameters: params }],
        actionType: wantAgent.OperationType.START_ABILITY,
        requestCode: spec.id,
        actionFlags: [wantAgent.WantAgentFlags.UPDATE_PRESENT_FLAG]
      });
    } catch (_) {
      return undefined;
    }
  }

  private async buildNativePayload(spec: FleetLiveViewSpec, alive: boolean): Promise<liveViewManager.LiveView> {
    const click = alive ? await this.buildClickAction(spec) : undefined;
    const seq = ++this.sequence;

    const primary: liveViewManager.PrimaryData = {
      title: spec.title,
      content: spec.content.map((seg): liveViewManager.RichText => ({ text: seg.text })),
      keepTime: alive ? spec.keepTimeSec : 0,
      clickAction: click
    };

    if (spec.extensionText) {
      primary.extensionData = {
        type: liveViewManager.ExtensionType.EXTENSION_TYPE_CAPSULE_TEXT,
        text: spec.extensionText
      };
    }

    if (spec.layoutKind === 'progress') {
      const progressLayout: liveViewManager.ProgressLayout = {
        layoutType: liveViewManager.LayoutType.LAYOUT_TYPE_PROGRESS,
        progress: spec.progressPercent,
        color: spec.progressColor,
        lineType: liveViewManager.LineType.LINE_TYPE_NORMAL_SOLID_LINE,
        indicatorType: liveViewManager.IndicatorType.INDICATOR_TYPE_UNDISPLAYED
      };
      primary.layoutData = progressLayout;
    } else if (spec.layoutKind === 'pickup' && spec.pickupTitle && spec.pickupContent) {
      const pickupLayout: liveViewManager.PickupLayout = {
        layoutType: liveViewManager.LayoutType.LAYOUT_TYPE_PICKUP,
        title: spec.pickupTitle,
        content: spec.pickupContent,
        underlineColor: spec.capsuleBackgroundColor,
        descPic: LIVE_VIEW_ICON
      };
      primary.layoutData = pickupLayout;
    }

    let capsule: liveViewManager.TextCapsule | liveViewManager.TimerCapsule | liveViewManager.ProgressCapsule;
    if (spec.capsuleKind === 'timer') {
      const timerCapsule: liveViewManager.TimerCapsule = {
        type: liveViewManager.CapsuleType.CAPSULE_TYPE_TIMER,
        status: alive ? 1 : -1,
        icon: LIVE_VIEW_ICON,
        backgroundColor: spec.capsuleBackgroundColor,
        content: spec.capsuleTitle,
        time: spec.timerElapsedMs,
        isCountdown: false,
        isPaused: spec.timerPaused
      };
      capsule = timerCapsule;
    } else if (spec.capsuleKind === 'progress') {
      const progressCapsule: liveViewManager.ProgressCapsule = {
        type: liveViewManager.CapsuleType.CAPSULE_TYPE_PROGRESS,
        status: alive ? 1 : -1,
        icon: LIVE_VIEW_ICON,
        backgroundColor: spec.capsuleBackgroundColor,
        progress: spec.progressPercent,
        max: 100,
        indeterminate: true,
        content: spec.capsuleTitle
      };
      capsule = progressCapsule;
    } else {
      const textCapsule: liveViewManager.TextCapsule = {
        type: liveViewManager.CapsuleType.CAPSULE_TYPE_TEXT,
        status: alive ? 1 : -1,
        icon: LIVE_VIEW_ICON,
        backgroundColor: spec.capsuleBackgroundColor,
        title: spec.capsuleTitle,
        content: spec.capsuleContent
      };
      capsule = textCapsule;
    }

    const view: liveViewManager.LiveView = {
      id: FLEET_LIVE_VIEW_ID,
      event: spec.event,
      sequence: seq,
      isMute: spec.isMute,
      lifeCycleMode: liveViewManager.LifeCycleMode.AUTO_STOP_WHEN_APP_TERMINATE,
      liveViewData: {
        primary: primary,
        capsule: capsule
      }
    };
    if (spec.capsuleKind === 'timer' || spec.pickupContent === '${placeholder.timer}') {
      view.timer = {
        time: spec.timerElapsedMs,
        isCountdown: false,
        isPaused: spec.timerPaused
      };
    }
    return view;
  }
}

export const nativeLiveView: NativeLiveView = new NativeLiveView();
