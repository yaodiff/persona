import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { XiaoYiDestination, buildXiaoYiRouteUri, createXiaoYiRoute, parseXiaoYiWant } from '../../core/XiaoYiRoutes';
import { Strings, tr } from '../../design/Strings';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'OpenPersonaDestination',
  domain: 'ProductivityDomain',
  intentVersion: '1.0.0',
  displayName: 'Open Persona',
  displayDescription: 'Open Assistant, Agents, To-dos, Knowledge, Health, Inbox, Dictation, or a specific session',
  llmDescription: 'Open Persona in the foreground to a requested workspace destination or a specific machine/session conversation.',
  keywords: ['Open Persona', 'Open Agents', '打开Persona', '打开待办', 'Personaを開く'],
  abilityName: 'EntryAbility',
  executeMode: [insightIntent.ExecuteMode.UI_ABILITY_FOREGROUND],
  parameters: {
    type: 'object',
    properties: {
      destination: {
        type: 'string',
        description: 'Destination: assistant, agents, session, todos, knowledge, health, inbox, recording, dictate, or settings_xiaoyi'
      },
      machine: { type: 'string', description: 'Optional computer ID when destination is session' },
      session: { type: 'string', description: 'Optional session ID when destination is session' }
    }
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
export default class OpenPersonaDestinationIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  destination: string = 'assistant';
  machine: string = '';
  session: string = '';

  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const appCtx = XiaoYiRuntimeContext.get();
    if (appCtx) {
      try { Strings.bind(appCtx.resourceManager); } catch (_) {}
    }
    const params: Record<string, Object> = {};
    params['personaDest'] = this.destination || 'assistant';
    if (this.machine) params['machine'] = this.machine;
    if (this.session) params['session'] = this.session;
    const parsed = parseXiaoYiWant('', params) || createXiaoYiRoute('assistant' as XiaoYiDestination);
    const uri = buildXiaoYiRouteUri(parsed);
    try {
      const bundleName = appCtx?.applicationInfo?.name;
      await this.context.startAbility({
        bundleName: bundleName || undefined,
        abilityName: 'EntryAbility',
        action: 'ohos.want.action.viewData',
        uri: uri,
        parameters: params
      });
      const msg = tr('xiaoyi_opening_persona');
      return okIntentResult(
        'open:' + parsed.dest,
        msg === 'xiaoyi_opening_persona' ? 'Opening Persona.' : msg,
        'ok',
        uri,
        parsed.machine,
        parsed.session
      );
    } catch (error) {
      return errorIntentResult('open:error', error as Error, uri);
    }
  }
}
