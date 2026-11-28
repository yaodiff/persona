import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { tr } from '../../design/Strings';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'LogPersonaWater',
  domain: 'HealthDomain',
  intentVersion: '1.0.0',
  displayName: 'Log Water in Persona',
  displayDescription: 'Record a glass or specific millilitres of water in Persona Health',
  llmDescription: 'Log water intake in millilitres (or default glass size if omitted) in the user\'s Persona Health space.',
  keywords: ['Log water', 'Drink water', '记喝水', '喝了一杯水', '水分を記録'],
  abilityName: 'EntryAbility',
  executeMode: [insightIntent.ExecuteMode.UI_ABILITY_BACKGROUND],
  parameters: {
    type: 'object',
    properties: {
      amountMl: { type: 'number', description: 'Optional water amount in millilitres (1 to 3000)' }
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
export default class LogPersonaWaterIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  amountMl: number = 0;

  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const requested = this.amountMl > 0 ? this.amountMl : undefined;
      const logged = await hub.logWater(requested);
      const template = tr('xiaoyi_water_logged_summary');
      const summary = template === 'xiaoyi_water_logged_summary'
        ? `Logged ${logged.recordedMl} ml. Today: ${logged.todayMl} / ${logged.goalMl} ml.`
        : template
          .replace('{amount}', String(logged.recordedMl))
          .replace('{today}', String(logged.todayMl))
          .replace('{goal}', String(logged.goalMl));
      return okIntentResult('water:' + String(logged.todayMl), summary, 'ok', logged.deepLinkUri);
    } catch (error) {
      return errorIntentResult('water:error', error as Error, 'persona://open?dest=health');
    } finally {
      await hub.close();
    }
  }
}
