/** Pure metadata catalog and schema validators for Persona's XiaoYi InsightIntents. */

export interface XiaoYiIntentDescriptor {
  readonly intentName: string;
  readonly domain: string;
  readonly intentVersion: string;
  readonly srcEntry: string;
  readonly executeModes: readonly ('background' | 'foreground')[];
  readonly requiresUnlock: boolean;
}

export const XIAOYI_INTENT_CATALOG: readonly XiaoYiIntentDescriptor[] = [
  {
    intentName: 'AskPersona',
    domain: 'ProductivityDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/AskPersonaIntent.ets',
    executeModes: ['background', 'foreground'],
    requiresUnlock: false
  },
  {
    intentName: 'CheckPersonaStatus',
    domain: 'ProductivityDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/CheckPersonaStatusIntent.ets',
    executeModes: ['background'],
    requiresUnlock: false
  },
  {
    intentName: 'SendAgentTask',
    domain: 'ProductivityDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/SendAgentTaskIntent.ets',
    executeModes: ['background'],
    requiresUnlock: false
  },
  {
    intentName: 'ApproveFirstPending',
    domain: 'ProductivityDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/ApproveFirstPendingIntent.ets',
    executeModes: ['background'],
    requiresUnlock: true
  },
  {
    intentName: 'AddPersonaTodo',
    domain: 'ProductivityDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/AddPersonaTodoIntent.ets',
    executeModes: ['background'],
    requiresUnlock: false
  },
  {
    intentName: 'LogPersonaWater',
    domain: 'HealthDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/LogPersonaWaterIntent.ets',
    executeModes: ['background'],
    requiresUnlock: false
  },
  {
    intentName: 'PushPersonaItem',
    domain: 'ProductivityDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/PushPersonaItemIntent.ets',
    executeModes: ['background'],
    requiresUnlock: false
  },
  {
    intentName: 'StartPersonaRecording',
    domain: 'ProductivityDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/StartPersonaRecordingIntent.ets',
    executeModes: ['foreground'],
    requiresUnlock: false
  },
  {
    intentName: 'OpenPersonaDestination',
    domain: 'ProductivityDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/OpenPersonaDestinationIntent.ets',
    executeModes: ['foreground'],
    requiresUnlock: false
  },
  {
    intentName: 'SearchPersonaKnowledge',
    domain: 'ProductivityDomain',
    intentVersion: '1.0.0',
    srcEntry: './ets/xiaoyi/intents/SearchPersonaKnowledgeIntent.ets',
    executeModes: ['background'],
    requiresUnlock: false
  }
];

export const INTENT_NAME_PATTERN: RegExp = /^[A-Z][a-zA-Z0-9]+$/;
export const INTENT_VERSION_PATTERN: RegExp = /^(\d+\.){2}\d+$/;

export interface SuggestionSeed {
  readonly intentName: string;
  readonly identifier: string;
  readonly entityName: string;
  readonly entityId: string;
  readonly title: string;
  readonly actionMode: string;
}

/** Build privacy-safe, bounded XiaoYi Suggestion seeds (never containing tokens or private message bodies). */
export function buildSafeSuggestionSeeds(pendingCount: number, workingCount: number): SuggestionSeed[] {
  const seeds: SuggestionSeed[] = [];
  if (pendingCount > 0) {
    seeds.push({
      intentName: 'CheckPersonaStatus',
      identifier: 'persona.suggest.pending',
      entityName: 'PersonaActionResult',
      entityId: 'pending-summary',
      title: `Review ${Math.min(pendingCount, 99)} pending items`,
      actionMode: 'status'
    });
  }
  if (workingCount > 0) {
    seeds.push({
      intentName: 'OpenPersonaDestination',
      identifier: 'persona.suggest.agents',
      entityName: 'PersonaActionResult',
      entityId: 'agents-running',
      title: `Check ${Math.min(workingCount, 99)} active tasks`,
      actionMode: 'agents'
    });
  }
  seeds.push({
    intentName: 'AskPersona',
    identifier: 'persona.suggest.ask',
    entityName: 'PersonaActionResult',
    entityId: 'quick-ask',
    title: 'Ask Persona',
    actionMode: 'ask'
  });
  seeds.push({
    intentName: 'AddPersonaTodo',
    identifier: 'persona.suggest.todo',
    entityName: 'PersonaActionResult',
    entityId: 'quick-todo',
    title: 'Add a to-do',
    actionMode: 'todo'
  });
  return seeds;
}

/** Whether an Intents Kit shareIntent error code is an expected graceful non-fatal state. */
export function isGracefulSuggestionError(code: number): boolean {
  return code === 1000101101 || code === 1000101102 || code === 1000101103 ||
    code === 1000101104 || code === 1000101106 || code === 1000101201;
}
