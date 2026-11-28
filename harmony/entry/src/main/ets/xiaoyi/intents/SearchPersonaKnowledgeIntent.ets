import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { tr } from '../../design/Strings';
import { HeadlessKnowledgeHit } from '../HeadlessHubService';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'SearchPersonaKnowledge',
  domain: 'ProductivityDomain',
  intentVersion: '1.0.0',
  displayName: 'Search Persona Knowledge',
  displayDescription: 'Search your personal Persona knowledge base',
  llmDescription: 'Search notes, documents, projects, and meeting minutes stored in the user\'s Persona knowledge base.',
  keywords: ['Search knowledge', 'Find note', '搜索知识库', '查资料', 'ナレッジ検索'],
  abilityName: 'EntryAbility',
  executeMode: [insightIntent.ExecuteMode.UI_ABILITY_BACKGROUND],
  parameters: {
    type: 'object',
    properties: {
      query: { type: 'string', description: 'Search query for the knowledge base' }
    },
    required: ['query']
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
export default class SearchPersonaKnowledgeIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  query: string = '';

  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const hits: HeadlessKnowledgeHit[] = await hub.searchKnowledge(this.query);
      if (hits.length === 0) {
        const emptyMsg = tr('xiaoyi_kb_no_hits').replace('{query}', this.query);
        return okIntentResult(
          'kb:empty',
          emptyMsg === 'xiaoyi_kb_no_hits' ? `No knowledge matches found for "${this.query}".` : emptyMsg,
          'ok',
          'persona://open?dest=knowledge'
        );
      }
      const lines = hits.slice(0, 3).map((hit: HeadlessKnowledgeHit): string =>
        hit.path + (hit.line ? ': ' + hit.line : '')
      );
      return okIntentResult(
        'kb:' + String(hits.length),
        lines.join('\n'),
        'ok',
        'persona://open?dest=knowledge'
      );
    } catch (error) {
      return errorIntentResult('kb:error', error as Error, 'persona://open?dest=knowledge');
    } finally {
      await hub.close();
    }
  }
}
