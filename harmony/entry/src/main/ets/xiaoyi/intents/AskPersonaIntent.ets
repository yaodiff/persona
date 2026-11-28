import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { tr } from '../../design/Strings';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'AskPersona',
  domain: 'ProductivityDomain',
  intentVersion: '1.0.0',
  displayName: 'Ask Persona',
  displayDescription: 'Ask your personal Persona assistant a question',
  llmDescription: 'Send a natural-language question to the user\'s personal Persona assistant and return the answer.',
  keywords: ['Ask Persona', 'Persona', '问Persona', '问一下Persona', 'Personaに聞く'],
  abilityName: 'EntryAbility',
  executeMode: [insightIntent.ExecuteMode.UI_ABILITY_BACKGROUND, insightIntent.ExecuteMode.UI_ABILITY_FOREGROUND],
  parameters: {
    type: 'object',
    properties: {
      question: { type: 'string', description: 'The question to ask Persona' }
    },
    required: ['question']
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
export default class AskPersonaIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  question: string = '';

  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const result = await hub.askPersona(this.question);
      const summary = result.answer ||
        (result.askUserPrompt ? result.askUserPrompt : tr('xiaoyi_sent_still_thinking'));
      const status = result.askUserPrompt ? 'needs_screen' : result.pending ? 'pending' : 'ok';
      return okIntentResult(
        'ask:' + result.sessionId,
        summary,
        status,
        result.deepLinkUri,
        result.machineId,
        result.sessionId
      );
    } catch (error) {
      return errorIntentResult('ask:error', error as Error, 'persona://open?dest=assistant');
    } finally {
      await hub.close();
    }
  }
}
