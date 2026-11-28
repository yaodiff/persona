import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { tr } from '../../design/Strings';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'ApproveFirstPending',
  domain: 'ProductivityDomain',
  intentVersion: '1.0.0',
  displayName: 'Approve First Pending',
  displayDescription: 'Approve the oldest pending permission or plan when the phone is unlocked',
  llmDescription: 'Approve the oldest pending tool permission or edit-mode plan request in Persona. Always requires the phone to be unlocked.',
  keywords: ['Approve pending', 'Allow request', '批准待确认', '通过第一个请求', '承認'],
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
export default class ApproveFirstPendingIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const outcome = await hub.approveFirstPending();
      if (outcome.needsScreen) {
        const msg = tr('xiaoyi_needs_screen');
        return okIntentResult(
          'approve:' + outcome.decisionId,
          msg === 'xiaoyi_needs_screen' ? 'Open Persona to answer this request.' : msg,
          'needs_screen',
          outcome.deepLinkUri
        );
      }
      const msg = tr('xiaoyi_approved_summary').replace('{summary}', outcome.summary);
      return okIntentResult(
        'approve:' + outcome.decisionId,
        msg === 'xiaoyi_approved_summary' ? 'Approved: ' + outcome.summary : msg,
        'ok',
        outcome.deepLinkUri
      );
    } catch (error) {
      return errorIntentResult('approve:error', error as Error, 'persona://open?dest=agents');
    } finally {
      await hub.close();
    }
  }
}
