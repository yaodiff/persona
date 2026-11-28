import { InsightIntentEntry, InsightIntentEntryExecutor, insightIntent } from '@kit.AbilityKit';
import { tr } from '../../design/Strings';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';
import { PersonaActionResultEntity, errorIntentResult, okIntentResult } from './PersonaIntentEntities';

@InsightIntentEntry({
  intentName: 'AddPersonaTodo',
  domain: 'ProductivityDomain',
  intentVersion: '1.0.0',
  displayName: 'Add Persona To-Do',
  displayDescription: 'Create a new to-do item in Persona',
  llmDescription: 'Add a new to-do task with a title and optional notes to the user\'s Persona To-dos list.',
  keywords: ['Add to-do', 'New to-do', '新建待办', '记个待办', 'ToDoを追加'],
  abilityName: 'EntryAbility',
  executeMode: [insightIntent.ExecuteMode.UI_ABILITY_BACKGROUND],
  parameters: {
    type: 'object',
    properties: {
      title: { type: 'string', description: 'Title of the to-do item' },
      notes: { type: 'string', description: 'Optional extra details' }
    },
    required: ['title']
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
export default class AddPersonaTodoIntent extends InsightIntentEntryExecutor<PersonaActionResultEntity> {
  title: string = '';
  notes: string = '';

  async onExecute(): Promise<insightIntent.IntentResult<PersonaActionResultEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const created = await hub.addTodo(this.title, this.notes);
      const label = tr('xiaoyi_todo_saved_summary').replace('{title}', created.title);
      return okIntentResult(
        'todo:' + (created.id || 'created'),
        label === 'xiaoyi_todo_saved_summary' ? 'To-do saved: ' + created.title : label,
        'ok',
        created.deepLinkUri
      );
    } catch (error) {
      return errorIntentResult('todo:error', error as Error, 'persona://open?dest=todos');
    } finally {
      await hub.close();
    }
  }
}
