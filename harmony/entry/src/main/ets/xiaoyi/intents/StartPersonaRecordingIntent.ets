import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { buildXiaoYiRouteUri, createXiaoYiRoute } from '../../core/XiaoYiRoutes';
import { Strings, tr } from '../../design/Strings';
import { HeadlessHubFailure } from '../HeadlessHubService';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'StartPersonaRecording',
  domain: 'ProductivityDomain',
  intentVersion: '1.0.0',
  displayName: 'Start Meeting Recording',
  displayDescription: 'Open Persona directly to Meeting Recording',
  llmDescription: 'Open Persona in the foreground on the Meeting Recording screen so the user can start recording a meeting. Refuses cleanly when the Hub recording module is off.',
  keywords: ['Record meeting', 'Start meeting', '开始会议录音', '用Persona录音', '会議を録音'],
  abilityName: 'EntryAbility',
  executeMode: [insightIntent.ExecuteMode.UI_ABILITY_FOREGROUND],
  parameters: {
    type: 'object',
    properties: {}
  },
  result: {
    type: 'object',
    properties: {
      entityId: { type: 'string' },
      summary: { type: 'string' },
      status: { type: 'string' },
      deepLinkUri: { type: 'string' }
    }
  }
})
export default class StartPersonaRecordingIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const appCtx = XiaoYiRuntimeContext.get();
    if (appCtx) {
      try { Strings.bind(appCtx.resourceManager); } catch (_) {}
      const hub = XiaoYiRuntimeContext.openHub(appCtx);
      try {
        await hub.assertRecordingModuleAllowed();
      } catch (error) {
        const failure = error as Error;
        if (failure instanceof HeadlessHubFailure && failure.code === 'xiaoyi_recording_module_off') {
          const readOnlyUri = buildXiaoYiRouteUri(createXiaoYiRoute('recording'));
          return errorIntentResult('recording:module_off', error as Error, readOnlyUri);
        }
        // Unpaired or transient offline without a remembered off-flag still allows opening the app.
      } finally {
        await hub.close();
      }
    }

    const route = createXiaoYiRoute('recording');
    route.autoStart = true;
    const uri = buildXiaoYiRouteUri(route);
    try {
      const bundleName = appCtx?.applicationInfo?.name;
      await this.context.startAbility({
        bundleName: bundleName || undefined,
        abilityName: 'EntryAbility',
        action: 'ohos.want.action.viewData',
        uri: uri,
        parameters: {
          personaDest: 'recording',
          autoStart: true
        }
      });
      const msg = tr('xiaoyi_opening_recording');
      return okIntentResult(
        'recording:open',
        msg === 'xiaoyi_opening_recording' ? 'Opening Persona Meeting Recording.' : msg,
        'ok',
        uri
      );
    } catch (error) {
      return errorIntentResult('recording:error', error as Error, uri);
    }
  }
}
