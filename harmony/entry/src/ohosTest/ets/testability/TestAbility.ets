import { ConfigurationConstant, UIAbility } from '@kit.AbilityKit';
import { abilityDelegatorRegistry } from '@kit.TestKit';
import { Hypium } from '@ohos/hypium';
import nativeTransportTests, { fixtureArgument, nativeIPv6TransportTests } from '../test/NativeTransport.test';
import nativeStorageTests from '../test/NativeStorage.test';
import nativeRFBTests from '../test/NativeRFB.test';
import nativePreviewTests from '../test/NativePreview.test';
import nativeFontTests from '../test/NativeFont.test';
import nativeSshTests from '../test/NativeSsh.test';
import nativeRecordingTests from '../test/NativeRecording.test';
import nativeMeetingAudioTests from '../test/NativeMeetingAudio.test';
import nativeAlarmsTests from '../test/NativeAlarms.test';
import nativeMeetingServicesTests from '../test/NativeMeetingServices.test';
import nativePhoneTests from '../test/NativePhone.test';
import nativeCoreTests from '../test/NativeCore.test';
import nativeXiaoYiTests, { nativeXiaoYiHubE2ETests, nativeXiaoYiPreviewHold } from '../test/NativeXiaoYi.test';
import { bindNativeTestContext } from '../test/NativeContext';

export default class TestAbility extends UIAbility {
  private originalFontScale: number = 1;
  private appliedPreviewScale: boolean = false;

  onCreate(): void {
    bindNativeTestContext(this.context);
    const testArguments = abilityDelegatorRegistry.getArguments();
    if (fixtureArgument('settingsPreview') === '1') {
      const appCtx = this.context.getApplicationContext();
      const dark = fixtureArgument('cardDark') === '1';
      appCtx.setColorMode(dark ? ConfigurationConstant.ColorMode.COLOR_MODE_DARK : ConfigurationConstant.ColorMode.COLOR_MODE_LIGHT);
      const scaleArg = Number(fixtureArgument('cardFontScale') || '1');
      if (scaleArg === 1.75 || scaleArg === 2 || scaleArg === 3.2) {
        this.originalFontScale = this.context.config.fontSizeScale || 1;
        this.appliedPreviewScale = true;
        appCtx.setFontSizeScale(scaleArg);
      }
      abilityDelegatorRegistry.getAbilityDelegator().startAbility({
        bundleName: testArguments.bundleName,
        moduleName: 'entry',
        abilityName: 'EntryAbility',
        action: 'ohos.want.action.viewData',
        uri: 'persona://open?dest=settings_xiaoyi'
      }).catch(() => {});
      testArguments.parameters['-s timeout'] = '60000';
      Hypium.hypiumTest(abilityDelegatorRegistry.getAbilityDelegator(), testArguments, (): void => {
        nativeXiaoYiPreviewHold();
      });
      return;
    }
    if (fixtureArgument('fontScale')) testArguments.parameters['-s timeout'] = '150000';
    if (fixtureArgument('sshFixtureFile')) testArguments.parameters['-s timeout'] = '60000';
    if (fixtureArgument('sshRecheck') === '1') testArguments.parameters['-s timeout'] = '175000';
    if (fixtureArgument('recordingFixtureId')) testArguments.parameters['-s timeout'] = '120000';
    if (fixtureArgument('phoneCheck')) testArguments.parameters['-s timeout'] = '90000';
    if (fixtureArgument('xiaoYiHubUrl')) testArguments.parameters['-s timeout'] = '90000';
    Hypium.hypiumTest(abilityDelegatorRegistry.getAbilityDelegator(), testArguments, (): void => {
      if (fixtureArgument('coreReplay')) nativeCoreTests();
      else if (fixtureArgument('ipv6FixturePort')) nativeIPv6TransportTests();
      else if (fixtureArgument('fontScale')) nativeFontTests();
      else if (fixtureArgument('sshFixtureFile')) nativeSshTests();
      else if (fixtureArgument('meetingAudioFixture')==='synthetic') nativeMeetingAudioTests();
      else if (fixtureArgument('alarmFixture')==='synthetic') nativeAlarmsTests();
      else if (fixtureArgument('meetingReminderFixture')==='synthetic') nativeMeetingServicesTests();
      else if (fixtureArgument('recordingFixtureId')) nativeRecordingTests();
      else if (fixtureArgument('phoneCheck')) nativePhoneTests();
      else if (fixtureArgument('xiaoYiHubUrl')) nativeXiaoYiHubE2ETests();
      else if (fixtureArgument('xiaoYiCheck') === '1') nativeXiaoYiTests();
      else { nativeStorageTests(); nativeXiaoYiTests(); nativeRFBTests(); if (fixtureArgument('fixtureAddress')) nativeTransportTests(); if (fixtureArgument('previewCheck')) nativePreviewTests(); }
    });
  }

  onDestroy(): void {
    if (this.appliedPreviewScale) {
      try { this.context.getApplicationContext().setFontSizeScale(this.originalFontScale); } catch (_) {}
    }
  }
}
