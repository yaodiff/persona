import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { tr } from '../../design/Strings';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'SendAgentTask',
  domain: 'ProductivityDomain',
  intentVersion: '1.0.0',
  displayName: 'Send Task to Agent',
  displayDescription: 'Send a prompt to an active Agent session on one of your computers',
  llmDescription: 'Dispatch a task prompt to a live Agent session on a chosen or remembered computer, or start a new session with its remembered setup.',
  keywords: ['Send Agent task', 'Ask Claude', '交给Agent', '让Agent去做', 'エージェントに依頼'],
  abilityName: 'EntryAbility',
  executeMode: [insightIntent.ExecuteMode.UI_ABILITY_BACKGROUND],
  parameters: {
    type: 'object',
    properties: {
      prompt: { type: 'string', description: 'Instruction to send to the Agent' },
      machineName: { type: 'string', description: 'Optional computer name or ID' },
      sessionName: { type: 'string', description: 'Optional active session title or ID' },
      newSession: { type: 'boolean', description: 'Start a new session using the remembered folder and launcher' }
    },
    required: ['prompt']
  },
  result: {
    type: 'object',
    properties: {
      entityId: { type: 'string' },
      summary: { type: 'string' },
      status: { type: 'string' },
      deepLinkUri: { type: 'string' },
      machine: { type: 'string' },
      session: { type: 'string' }
    }
  }
})
export default class SendAgentTaskIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  prompt: string = '';
  machineName: string = '';
  sessionName: string = '';
  newSession: boolean = false;

  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const sent = await hub.sendAgentTask(this.prompt, this.machineName, this.sessionName, this.newSession);
      const label = tr('xiaoyi_task_sent_summary')
        .replace('{session}', sent.sessionTitle)
        .replace('{machine}', sent.machineName);
      return okIntentResult(
        'task:' + sent.sessionId,
        label === 'xiaoyi_task_sent_summary' ? 'Sent to ' + sent.sessionTitle + ' (' + sent.machineName + ').' : label,
        'ok',
        sent.deepLinkUri,
        sent.machineId,
        sent.sessionId
      );
    } catch (error) {
      return errorIntentResult('task:error', error as Error, 'persona://open?dest=agents');
    } finally {
      await hub.close();
    }
  }
}
