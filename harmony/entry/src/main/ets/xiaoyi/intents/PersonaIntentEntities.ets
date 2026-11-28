import { InsightIntentEntity, insightIntent } from '@kit.AbilityKit';
import { XiaoYiMachine, XiaoYiSession } from '../../core/XiaoYiActions';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';

@InsightIntentEntity({
  entityCategory: 'PersonaActionResult',
  parameters: {
    type: 'object',
    properties: {
      entityId: { type: 'string', description: 'Unique result identifier' },
      summary: { type: 'string', description: 'Human-readable result summary' },
      status: { type: 'string', description: 'Outcome status: ok, pending, needs_screen, queued, auth_required, or error' },
      deepLinkUri: { type: 'string', description: 'Persona deep link URI to continue in the app' },
      machine: { type: 'string', description: 'Target computer ID or name when applicable' },
      session: { type: 'string', description: 'Target session or conversation ID when applicable' }
    },
    required: ['entityId', 'summary', 'status']
  }
})
export class PersonaActionResultEntity implements insightIntent.IntentEntity {
  entityId: string = 'persona_action_result';
  summary: string = 'ok';
  status: string = 'ok';
  deepLinkUri: string = '';
  machine: string = '';
  session: string = '';

  constructor(
    entityId: string = '',
    summary: string = '',
    status: string = 'ok',
    deepLinkUri: string = '',
    machine: string = '',
    session: string = ''
  ) {
    this.entityId = entityId;
    this.summary = summary;
    this.status = status;
    this.deepLinkUri = deepLinkUri;
    this.machine = machine;
    this.session = session;
  }
}

@InsightIntentEntity({
  entityCategory: 'PersonaMachine',
  supportedQueryProperties: ['name', 'online'],
  parameters: {
    type: 'object',
    properties: {
      entityId: { type: 'string', description: 'Computer identifier' },
      displayName: { type: 'string', description: 'Friendly computer name' },
      name: { type: 'string', description: 'Computer name' },
      online: { type: 'boolean', description: 'Whether the computer is currently online' }
    },
    required: ['entityId', 'displayName']
  }
})
export class PersonaMachineEntity extends insightIntent.AppIntentEntity<PersonaMachineEntity> {
  entityId: string = 'persona_machine';
  displayName: string = 'Computer';
  name: string = '';
  online: boolean = false;

  constructor(entityId: string = '', name: string = '', online: boolean = false) {
    super();
    this.entityId = entityId;
    this.displayName = name || entityId;
    this.name = name || entityId;
    this.online = online;
  }

  async onQueryEntity(params: insightIntent.QueryEntityParam): Promise<Array<PersonaMachineEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const machines: XiaoYiMachine[] = await hub.listMachines();
      const trusted = machines.filter((m: XiaoYiMachine): boolean => m.trusted && !!m.id);
      let filtered = trusted;
      if (params.queryType === insightIntent.QueryType.BY_PROPERTY && params.parameters) {
        const nameParam = params.parameters['name'];
        const onlineParam = params.parameters['online'];
        if (typeof nameParam === 'string' && nameParam.trim()) {
          const needle = nameParam.trim().toLowerCase();
          filtered = filtered.filter((m: XiaoYiMachine): boolean =>
            m.name.toLowerCase().includes(needle) || m.id.toLowerCase().includes(needle)
          );
        }
        if (typeof onlineParam === 'boolean') {
          filtered = filtered.filter((m: XiaoYiMachine): boolean => m.online === onlineParam);
        }
      }
      filtered.sort((a: XiaoYiMachine, b: XiaoYiMachine): number => (a.online === b.online ? 0 : a.online ? -1 : 1));
      return filtered.slice(0, 20).map((m: XiaoYiMachine): PersonaMachineEntity =>
        new PersonaMachineEntity(m.id, m.name, m.online)
      );
    } catch (_) {
      return [];
    } finally {
      await hub.close();
    }
  }
}

@InsightIntentEntity({
  entityCategory: 'PersonaSession',
  supportedQueryProperties: ['title', 'machine', 'status'],
  parameters: {
    type: 'object',
    properties: {
      entityId: { type: 'string', description: 'Session identifier (machine/session)' },
      displayName: { type: 'string', description: 'Friendly session title' },
      title: { type: 'string', description: 'Session title' },
      machine: { type: 'string', description: 'Computer identifier' },
      status: { type: 'string', description: 'Session state' }
    },
    required: ['entityId', 'displayName']
  }
})
export class PersonaSessionEntity extends insightIntent.AppIntentEntity<PersonaSessionEntity> {
  entityId: string = 'persona_session';
  displayName: string = 'Session';
  title: string = '';
  machine: string = '';
  status: string = '';

  constructor(entityId: string = '', title: string = '', machine: string = '', status: string = '') {
    super();
    this.entityId = entityId;
    this.displayName = title || entityId;
    this.title = title || entityId;
    this.machine = machine;
    this.status = status;
  }

  async onQueryEntity(params: insightIntent.QueryEntityParam): Promise<Array<PersonaSessionEntity>> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      const sessions: XiaoYiSession[] = await hub.listActiveSessions();
      let filtered = sessions;
      if (params.queryType === insightIntent.QueryType.BY_PROPERTY && params.parameters) {
        const titleParam = params.parameters['title'];
        const machineParam = params.parameters['machine'];
        const statusParam = params.parameters['status'];
        if (typeof titleParam === 'string' && titleParam.trim()) {
          const needle = titleParam.trim().toLowerCase();
          filtered = filtered.filter((s: XiaoYiSession): boolean =>
            s.title.toLowerCase().includes(needle) || s.id.toLowerCase().includes(needle)
          );
        }
        if (typeof machineParam === 'string' && machineParam.trim()) {
          const m = machineParam.trim().toLowerCase();
          filtered = filtered.filter((s: XiaoYiSession): boolean => s.machineId.toLowerCase() === m);
        }
        if (typeof statusParam === 'string' && statusParam.trim()) {
          const st = statusParam.trim().toLowerCase();
          filtered = filtered.filter((s: XiaoYiSession): boolean =>
            s.state.toLowerCase() === st || s.status.toLowerCase() === st
          );
        }
      }
      return filtered.slice(0, 20).map((s: XiaoYiSession): PersonaSessionEntity =>
        new PersonaSessionEntity(s.machineId + '/' + s.id, s.title || s.id, s.machineId, s.state || s.status)
      );
    } catch (_) {
      return [];
    } finally {
      await hub.close();
    }
  }
}

export function okIntentResult(
  entityId: string,
  summary: string,
  status: string = 'ok',
  deepLinkUri: string = '',
  machine: string = '',
  session: string = ''
): insightIntent.IntentResult<PersonaActionResultEntity> {
  return {
    code: 0,
    result: new PersonaActionResultEntity(entityId, summary, status, deepLinkUri, machine, session)
  };
}

export function errorIntentResult(
  entityId: string,
  error: Error,
  deepLinkUri: string = 'persona://open?dest=assistant'
): insightIntent.IntentResult<PersonaActionResultEntity> {
  const summary = XiaoYiRuntimeContext.formatError(error);
  const status = error.message === 'xiaoyi_not_paired' || error.message === 'xiaoyi_auth_expired'
    ? 'auth_required'
    : error.message === 'xiaoyi_unlock_required' || error.message === 'xiaoyi_needs_screen'
      ? 'needs_screen'
      : 'error';
  return {
    code: status === 'auth_required' ? 401 : 1,
    result: new PersonaActionResultEntity(entityId, summary, status, deepLinkUri)
  };
}
