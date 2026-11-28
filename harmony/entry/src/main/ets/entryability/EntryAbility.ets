import { phoneTextSize } from '../platform/PhoneTextSizePreference';
import { nativeMeetingServices } from '../platform/NativeMeetingServices';
import { phonePasswordLogin } from '../platform/PhonePasswordLogin';
import { isPairingLink } from '../core/WireCompatibility';
import { parseXiaoYiWant } from '../core/XiaoYiRoutes';
import { deviceFeatures } from '../models/DeviceFeatures';
import { XiaoYiRuntimeContext } from '../xiaoyi/XiaoYiRuntimeContext';
import { IntentSuggestionsService } from '../xiaoyi/IntentSuggestionsService';
import { ShareReceiver } from '../xiaoyi/ShareReceiver';
import { AbilityConstant, ConfigurationConstant, UIAbility, Want, common } from '@kit.AbilityKit';
import type { Configuration } from '@kit.AbilityKit';
import { window } from '@kit.ArkUI';
import { Strings } from '../design/Strings';
import { ClientModel, client } from '../models/ClientModel';
import { nativeNotifications, NOTIFICATION_ACTION } from '../platform/NativeNotifications';
import { LIVE_VIEW_ACTION, nativeLiveView } from '../platform/NativeLiveView';
import { Wire } from '../models/Wire';
import { KnowledgeRecording } from '../models/KnowledgeRecording';
import { deviceLook } from '../models/DeviceLook';
import { NativeDictation } from '../platform/NativeDictation';
import { nativeEnvironment } from '../platform/NativeEnvironment';

function clearXiaoYiSuggestionsOnForget(_hubId: string, context: common.Context): Promise<void> {
  return IntentSuggestionsService.clearAll(context);
}

function syncXiaoYiSuggestionsOnStatus(pendingCount: number, workingCount: number, context: common.Context): void {
  IntentSuggestionsService.scheduleStatusShare(context, pendingCount, workingCount);
}

export default class EntryAbility extends UIAbility {
  private lifecycle: number = 0;
  private shareGeneration: number = 0;
  private destroyed: boolean = false;
  private visible: boolean = true;
  private pendingNotification: Record<string, Object> | undefined;
  private pendingXiaoYiRoute: string = '';
  onCreate(want: Want, launchParam: AbilityConstant.LaunchParam): void {
    XiaoYiRuntimeContext.bind(this.context);
    ClientModel.registerForgetHook(clearXiaoYiSuggestionsOnForget);
    ClientModel.registerStatusHook(syncXiaoYiSuggestionsOnStatus);
    phoneTextSize.attach(this.context);
    nativeEnvironment.update(this.context.config);
    Strings.bind(this.context.resourceManager);
    deviceLook.initialize(this.context).catch(() => { deviceLook.error = 'device_look_storage_failed'; });
    this.receiveWant(want);
    if (want.action === NOTIFICATION_ACTION || want.action === LIVE_VIEW_ACTION) this.pendingNotification = Wire.record(want.parameters);
    client.initialize(this.context).then(() => {
      if (!this.visible) return;
      this.context.getApplicationContext().setColorMode(client.appearance === 1 ? ConfigurationConstant.ColorMode.COLOR_MODE_LIGHT :
        client.appearance === 2 ? ConfigurationConstant.ColorMode.COLOR_MODE_DARK : ConfigurationConstant.ColorMode.COLOR_MODE_NOT_SET);
      this.dispatchNotification();
      this.dispatchXiaoYiRoute();
    }).catch(() => { client.error = 'failed'; client.ready = true; });
  }
  private receiveWant(want: Want): void {
    const params = Wire.record(want.parameters);
    nativeMeetingServices.receive(params);
    const uri = want.uri || '';
    if (phonePasswordLogin.receive(uri)) return;
    if (uri.length <= 16384 && isPairingLink(uri)) { client.pairingLink = uri; return; }
    if (want.action !== NOTIFICATION_ACTION) {
      if (ShareReceiver.isShareWant(want)) {
        const gen = ++this.shareGeneration;
        ShareReceiver.parseWant(want).then((shared) => {
          if (gen === this.shareGeneration && !this.destroyed && shared.hasContent && shared.route) {
            this.pendingXiaoYiRoute = JSON.stringify(shared.route);
            this.dispatchXiaoYiRoute();
          }
        }).catch(() => {});
        return;
      }
      this.shareGeneration++;
      const route = parseXiaoYiWant(uri, params);
      if (route) { this.pendingXiaoYiRoute = JSON.stringify(route); return; }
      if (!uri && params['personaPage'] === 'projects') client.projectsRequested = true;
    }
  }
  onNewWant(want: Want, launchParam: AbilityConstant.LaunchParam): void {
    nativeEnvironment.update(this.context.config);
    this.receiveWant(want);
    if (want.action === NOTIFICATION_ACTION || want.action === LIVE_VIEW_ACTION) { this.pendingNotification = Wire.record(want.parameters); this.dispatchNotification(); }
    this.dispatchXiaoYiRoute();
  }
  onConfigurationUpdate(_configuration: Configuration): void {
    phoneTextSize.refresh(_configuration.fontSizeScale || 0);
    nativeEnvironment.update(_configuration);
    if (client.language === 'default') { Strings.refresh(); client.announceLanguage(); }
  }
  private dispatchNotification(): void {
    if (!this.visible || !client.ready || !this.pendingNotification) return;
    const target = this.pendingNotification; this.pendingNotification = undefined;
    client.openNotification(target).catch(() => {});
  }
  private dispatchXiaoYiRoute(): void {
    if (!this.visible || !client.ready || !this.pendingXiaoYiRoute) return;
    const route = this.pendingXiaoYiRoute; this.pendingXiaoYiRoute = '';
    client.openXiaoYiRoute(route);
  }
  onWindowStageCreate(stage: window.WindowStage): void {
    nativeEnvironment.bindWindow(stage.getMainWindowSync());
    stage.loadContent('pages/Index', (error) => { if (error.code) client.error = 'failed'; });
  }
  onWindowStageDestroy(): void { nativeEnvironment.unbindWindow(); }
  onBackground(): void { nativeEnvironment.foreground = false; NativeDictation.cancelActive(); this.visible = false; this.lifecycle++; deviceLook.bind(undefined).catch(() => {}); Promise.all([KnowledgeRecording.backgroundAll().then(() => nativeMeetingServices.setRecordingEvent('')), deviceFeatures.background(), client.background()]).catch(() => {}); }
  onForeground(): void {
    phoneTextSize.refresh(this.context.config.fontSizeScale || 0);
    nativeEnvironment.foreground = true;
    nativeMeetingServices.initialize(this.context).catch(() => {});
    nativeEnvironment.update(this.context.config);
    const epoch = ++this.lifecycle; this.visible = true; client.activateForeground();
    if (!client.ready) return;
    nativeNotifications.refresh().then(async (): Promise<void> => {
      if (!this.visible || epoch !== this.lifecycle) return;
      await client.foregrounded();
      if (this.visible && epoch === this.lifecycle) { this.dispatchNotification(); this.dispatchXiaoYiRoute(); }
    }).catch(() => { if (this.visible && epoch === this.lifecycle) client.error = 'failed'; });
    deviceFeatures.foreground().catch(() => {});
  }
  onDestroy(): void { this.destroyed = true; this.shareGeneration++; IntentSuggestionsService.resetSchedule(); phoneTextSize.detach(); nativeEnvironment.foreground = false; nativeLiveView.stopNow().catch(() => {}); NativeDictation.cancelActive(); nativeEnvironment.unbindWindow(); this.visible = false; this.lifecycle++; this.pendingNotification = undefined; this.pendingXiaoYiRoute = ''; deviceLook.bind(undefined).catch(() => {}); KnowledgeRecording.backgroundAll().catch(() => {}); deviceFeatures.background().catch(() => {}); client.shutdown().catch(() => {}); }
}
