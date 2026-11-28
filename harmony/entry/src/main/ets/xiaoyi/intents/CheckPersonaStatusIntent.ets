import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'CheckPersonaStatus',
  domain: 'ProductivityDomain',
  intentVersion: '1.0.0',
  displayName: 'Check Persona Status',
  displayDescription: 'Check running tasks, pending approvals, and unread assistant updates',
  llmDescription: 'Check how many tasks are running in Persona, how many requests need confirmation, and summarize the oldest pending item.',
  keywords: ['Persona status', 'check Persona', 'Persona状态', '查看待确认', 'Personaの状況'],
  abilityName: 'EntryAbility',
  executeMode: [insightIntent.ExecuteMode.UI_ABILITY_BACKGROUND],
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
export default class CheckPersonaStatusIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const status = await hub.checkStatus();
      const top = status.topPending;
      const uri = top && top.machineId && top.sessionId
        ? 'persona://open?dest=session&machine=' + encodeURIComponent(top.machineId) + '&session=' + encodeURIComponent(top.sessionId)
        : 'persona://open?dest=agents';
      return okIntentResult(
        'status:' + String(status.workingCount) + ':' + String(status.pendingCount),
        status.spokenText,
        status.pendingCount > 0 ? 'needs_screen' : 'ok',
        uri,
        top ? top.machineId : '',
        top ? top.sessionId : ''
      );
    } catch (error) {
      return errorIntentResult('status:error', error as Error, 'persona://open?dest=agents');
    } finally {
      await hub.close();
    }
  }
}
