import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { tr } from '../../design/Strings';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'PushPersonaItem',
  domain: 'ProductivityDomain',
  intentVersion: '1.0.0',
  displayName: 'Save Item to Persona',
  displayDescription: 'Push a note, clip, or notification into Persona\'s device push source',
  llmDescription: 'Send a title, body, source app name, and optional URL into Persona\'s subscription push source with offline queue fallback.',
  keywords: ['Save to Persona', 'Push item', '发送到Persona', '保存到Persona', 'Personaに送る'],
  abilityName: 'EntryAbility',
  executeMode: [insightIntent.ExecuteMode.UI_ABILITY_BACKGROUND],
  parameters: {
    type: 'object',
    properties: {
      title: { type: 'string', description: 'Optional item title' },
      body: { type: 'string', description: 'Item text body' },
      app: { type: 'string', description: 'Optional source app name' },
      link: { type: 'string', description: 'Optional http/https URL' }
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
export default class PushPersonaItemIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  title: string = '';
  body: string = '';
  app: string = '';
  link: string = '';

  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const pushed = await hub.pushSubscriptionItem(this.title, this.body, this.app, this.link);
      const key = pushed.status === 'queued'
        ? 'xiaoyi_push_queued_summary'
        : pushed.hasDestination ? 'xiaoyi_push_sent_summary' : 'xiaoyi_push_sent_no_dest_summary';
      const fallback = pushed.status === 'queued'
        ? 'Queued to send when your Hub is reachable.'
        : pushed.hasDestination
          ? 'Sent to Persona.'
          : 'Sent to Persona. Turn on Inbox or Knowledge destination in Settings > Subscriptions.';
      const msg = tr(key);
      return okIntentResult(
        'push:' + pushed.status,
        msg === key ? fallback : msg,
        pushed.status === 'queued' ? 'queued' : 'ok',
        'persona://open?dest=inbox'
      );
    } catch (error) {
      return errorIntentResult('push:error', error as Error, 'persona://open?dest=inbox');
    } finally {
      await hub.close();
    }
  }
}
