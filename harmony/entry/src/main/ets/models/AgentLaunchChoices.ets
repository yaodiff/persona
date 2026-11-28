import type { ClientModel } from './ClientModel';
import type { HttpFailure } from '../platform/HubTransport';
import { Wire } from './Wire';

const START_EFFORTS: string[] = ['low','medium','high','xhigh','max'];

export function typedAgentDirectory(input:string,current:string):string {
  const path = input.trim();
  if (!path || path.startsWith('/') || path === '~' || path.startsWith('~/') || /^[A-Za-z]:[\\/]/.test(path) || path.startsWith('\\\\')) return path;
  if (!current) return '~/' + path;
  if (current.includes('\\') && !current.includes('/')) return current.replace(/\\+$/,'') + '\\' + path;
  return current.replace(/\/+$/,'') + '/' + path;
}

function startsNativeOmp(launcher: Record<string, Object>): boolean {
  const runtime = Wire.text(launcher,'agent_type') || Wire.text(launcher,'agent');
  const source = Wire.text(launcher,'source');
  return Wire.text(launcher,'name') === 'omp' && ['', 'zshrc', 'path', 'builtin'].includes(source) && (!runtime || runtime === 'omp');
}

// Providers with their own start modes can only offer efforts from the selected model.
export function providerStartModes(launcher: Record<string, Object>): boolean {
  const agent = Wire.text(launcher,'agent');
  return startsNativeOmp(launcher) || Wire.strings(launcher,'permission_modes').length > 0 ||
    Wire.text(launcher,'source') === 'paseo' && !!agent && !['claude','codex'].includes(agent);
}

export class LaunchChoice {
  aliases: string[] = [];
  id: string;
  title: string;
  detail: string;
  disabled: boolean;
  description: string;
  constructor(id: string, title: string, detail: string = '', disabled: boolean = false, description: string = '') {
    this.id = id; this.title = title; this.detail = detail; this.disabled = disabled; this.description = description;
  }
}
export function builtinLauncherTitle(name: string, displayName: string = ''): string {
  const custom = displayName.trim();
  const key = name.trim().toLowerCase();
  if (custom && custom.toLowerCase() !== key) {
    if (!(key === 'traex' || key === 'traecli' || key === 'trae-cli') ||
      !['traex', 'traecli', 'trae cli', 'trae-cli'].includes(custom.toLowerCase())) {
      return custom;
    }
  }
  switch (key) {
    case 'claude': return 'Claude Code';
    case 'codex': return 'Codex';
    case 'claude-desktop': return 'Claude Desktop';
    case 'codex-app': return 'Codex App';
    case 'traex':
    case 'traecli':
    case 'trae-cli':
      return 'TRAE CLI';
    case 'agy': return 'Antigravity CLI';
    case 'omp': return 'Oh My Pi';
    case 'cursor': return 'Cursor';
    case 'gemini': return 'Gemini CLI';
    default: return custom || name;
  }
}

export function launchModelName(id: string, name: string): string {
  const raw = name.trim(), bare = id.split('/').pop() || id;
  if (raw && raw.toLowerCase() !== id.toLowerCase() && raw.toLowerCase() !== bare.toLowerCase()) return raw;
  if (!/^gpt-[a-z0-9.]+(?:-[a-z0-9.]+)*$/i.test(bare)) return raw || bare;
  return 'GPT-' + bare.slice(4).split('-').map((part:string) => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase()).join(' ');
}

export function recentLaunchDirectories(value: Record<string,Object>): LaunchChoice[] {
  return Wire.rows(value,'recent').filter((item:Record<string,Object>) => !!Wire.text(item,'path'))
    .map((item:Record<string,Object>) => new LaunchChoice(Wire.text(item,'path'),
      Wire.text(item,'name') || Wire.text(item,'path').split('/').pop() || Wire.text(item,'path'),
      Wire.text(item,'path'),false,Wire.text(item,'last_used')));
}

export class RecentAgentSetup {
  machine: string = '';
  cwd: string = '';
  tree: string = '';
  launcher: string = '';
  model: string = '';
  effort: string = '';
  preset: string = '';
  get key(): string { return JSON.stringify([this.machine,this.cwd,this.tree,this.launcher,this.model,this.effort]); }
}
export function loadAgentSetups(raw: string): RecentAgentSetup[] {
  return Wire.rows(Wire.parse(raw),'entries').slice(0,8).map((v:Record<string,Object>) => {
    const s = new RecentAgentSetup();
    s.machine = Wire.text(v,'machine'); s.cwd = Wire.text(v,'cwd'); s.tree = Wire.text(v,'tree');
    s.launcher = Wire.text(v,'launcher'); s.model = Wire.text(v,'model'); s.effort = Wire.text(v,'effort'); s.preset = Wire.text(v,'preset');
    return s;
  }).filter((s:RecentAgentSetup) => !!s.machine);
}
export function rememberAgentSetup(previous: RecentAgentSetup[], item: RecentAgentSetup): RecentAgentSetup[] {
  return [item].concat(previous.filter((v:RecentAgentSetup) => v.key !== item.key)).slice(0,8);
}

// The picker can outlive a machine change while the node is answering. Keep reads bound to
// their original machine, owner and connection generation; a delayed reply cannot repoint Start.
@ObservedV2
export class AgentLaunchChoices {
  @Trace loading: boolean = false;
  @Trace error: string = '';
  @Trace path: string = '';
  @Trace parent: string = '';
  @Trace directories: LaunchChoice[] = [];
  @Trace recentDirectories: LaunchChoice[] = [];
  @Trace attemptedFolder: string = '';
  private folderOwner: ClientModel | undefined = undefined;
  private folderContext: string = '';
  @Trace models: LaunchChoice[] = [];
  @Trace locked: string = '';
  @Trace acceptsTypedModel: boolean = false;
  @Trace effortNames: string[] = [];
  @Trace private effortListKnown: boolean = false;
  @Trace private modelData: Record<string,Object>[] = [];
  private revision: number = 0;

  clear(): void {
    this.folderOwner = undefined; this.folderContext = ''; this.attemptedFolder = '';
    this.revision++; this.loading = false; this.error = ''; this.path = ''; this.parent = '';
    this.directories = []; this.recentDirectories = []; this.models = []; this.locked = ''; this.effortNames = []; this.effortListKnown = false; this.modelData = []; this.acceptsTypedModel = false;
  }
  cancelRead(): void { this.revision++; this.loading = false; }
  private resetFolder():void { this.path = ''; this.parent = ''; this.directories = []; this.recentDirectories = []; }
  private folderAccessDenied(error:HttpFailure):boolean {
    return error.status === 401 || error.status === 403 || ['permission_denied','unauthorized','forbidden'].includes(error.code || error.message);
  }
  async folder(owner:ClientModel,machine:string,path:string): Promise<void> {
    const rev = ++this.revision, epoch = owner.connectionGeneration(), scope = owner.scope();
    const context = JSON.stringify([scope,epoch,machine]);
    const valid = (): boolean => this.revision === rev && owner.owns(epoch,scope);
    if (owner !== this.folderOwner || context !== this.folderContext) this.resetFolder();
    const requested = typedAgentDirectory(path,this.path);
    this.folderOwner = owner; this.folderContext = context; this.attemptedFolder = requested;
    this.loading = true; this.error = '';
    const read = (target:string):Promise<Record<string,Object>> =>
      owner.repo().call('/api/v1/machines/' + encodeURIComponent(machine) + '/dirs?path=' + encodeURIComponent(target.trim()));
    try {
      let value:Record<string,Object>;
      try {
        value = await read(requested);
      } catch (error) {
        if (!valid()) return;
        const status = (error as HttpFailure).status;
        this.error = 'phone_directory_read_failed';
        if (this.folderAccessDenied(error as HttpFailure)) { this.resetFolder(); return; }
        // If the initial saved folder is gone, ask the same machine for its home listing.
        if (this.path || !requested || ![400,404,422,500,502].includes(status)) return;
        value = await read('');
      }
      if (!valid()) return;
      const resolved = Wire.text(value,'path');
      if (!resolved) throw new Error('phone_directory_read_failed');
      this.path = resolved; this.parent = Wire.text(value,'parent');
      this.directories = Wire.rows(value,'entries').filter((v:Record<string,Object>) => !!Wire.text(v,'path'))
        .map((v:Record<string,Object>) => new LaunchChoice(Wire.text(v,'path'),Wire.text(v,'name') || Wire.text(v,'path'),Wire.bool(v,'is_git') ? 'Git' : ''));
      this.recentDirectories = recentLaunchDirectories(value);
    } catch (error) {
      if (valid()) {
        this.error = 'phone_directory_read_failed';
        if (this.folderAccessDenied(error as HttpFailure)) this.resetFolder();
      }
    } finally {
      if (this.revision === rev) {
        if (!owner.owns(epoch,scope)) this.resetFolder();
        this.loading = false;
      }
    }
  }
  async loadModels(owner:ClientModel,machine:string,launcher:string): Promise<void> {
    this.clear();
    if (!launcher) return;
    const rev = this.revision, scope = owner.scope(), epoch = owner.connectionGeneration();
    const valid = (): boolean => rev === this.revision && owner.owns(epoch,scope);
    this.loading = true;
    try {
      const value = await owner.repo().call('/api/v1/machines/' + encodeURIComponent(machine) + '/launchers/' +
        encodeURIComponent(launcher) + '/models?purpose=agent.new');
      if (!valid()) return;
      this.locked = Wire.text(value,'locked');
      this.acceptsTypedModel = !this.locked && ['claude','codex','pi','cursor','agy'].includes(Wire.text(value,'agent'));
      this.modelData = Wire.rows(value,'models').filter((v:Record<string,Object>) => !!Wire.text(v,'id'));
      // The host default is already represented by the first, empty-ID choice in the phone form.
      // Choosing the default keeps that semantic even after a launcher changes its default model.
      this.models = this.modelData.filter((v:Record<string,Object>) => !Wire.bool(v,'default') && !Wire.bool(v,'is_default'))
        .map((v:Record<string,Object>) => {
          const id = Wire.text(v,'id'), title = launchModelName(id,Wire.text(v,'name')), resolved = Wire.text(v,'resolved');
          const detail = [launcher, title !== id ? id : '', resolved && resolved !== id && resolved !== title ? '→ '+resolved : '']
            .filter((part:string) => !!part).join(' · ');
          const choice = new LaunchChoice(id,title,detail,false,Wire.text(v,'description'));
          choice.aliases = Wire.strings(v,'aliases');
          return choice;
        });
      this.updateEfforts('');
    } catch (_) { if (valid()) this.error = 'flow_models_offline'; }
    finally { if (valid()) this.loading = false; }
  }
  selectedModelId(id:string): string {
    const selected = this.modelData.find((m:Record<string,Object>) => Wire.text(m,'id') === id);
    return selected && (Wire.bool(selected,'is_default') || Wire.bool(selected,'default')) ? '' : id;
  }
  listsModel(id:string):boolean {
    return this.modelData.some((m:Record<string,Object>) => Wire.text(m,'id') === id || Wire.strings(m,'aliases').includes(id));
  }
  hasCatalogModel():boolean {
    return this.modelData.some((m:Record<string,Object>) => !Wire.bool(m,'is_default') && !Wire.bool(m,'default') && !Wire.bool(m,'custom'));
  }
  modelPresentation(picked: string, configured: string, launcher: Record<string,Object>, explicitlyChosen: boolean,
    word: (key:string) => string): LaunchChoice {
    const title = word('phone_start_default_model');
    if (this.locked) return new LaunchChoice('',title,this.locked);
    if (picked) {
      const model = this.modelData.find((item:Record<string,Object>) => Wire.text(item,'id') === picked);
      const display = launchModelName(picked,model ? Wire.text(model,'name') : picked);
      const resolved = model ? Wire.text(model,'resolved') : '';
      let detail = '';
      if (!model || Wire.bool(model,'custom')) {
        detail = !model && display !== picked ? word('phone_start_typed_model_id').replace('{id}',picked) : word('phone_start_typed_model');
      } else {
        detail = [display !== picked ? picked : '',resolved && resolved !== picked && resolved !== display ? '→ '+resolved : '']
          .filter((part:string) => !!part).join(' ');
      }
      return new LaunchChoice(picked,display,detail);
    }
    if (configured && (!explicitlyChosen || !startsNativeOmp(launcher))) {
      return new LaunchChoice('',title,word('phone_start_configured_model').replace('{model}',launchModelName(configured,configured)));
    }
    const def = this.modelData.find((item:Record<string,Object>) => Wire.bool(item,'is_default') || Wire.bool(item,'default'));
    if (!def) return new LaunchChoice('',title,word('flow_use_default'));
    const resolved = Wire.text(def,'resolved');
    return new LaunchChoice('',title,resolved ? launchModelName(resolved,resolved) : launchModelName(Wire.text(def,'id'),Wire.text(def,'name')));
  }
  defaultModelName():string {
    const model = this.modelData.find((m:Record<string,Object>) => Wire.bool(m,'is_default') || Wire.bool(m,'default'));
    return model ? launchModelName(Wire.text(model,'id'),Wire.text(model,'name')) : '';
  }
  updateEfforts(id:string): void {
    const model = id ? this.modelData.find((m:Record<string,Object>) => Wire.text(m,'id') === id) :
      this.modelData.find((m:Record<string,Object>) => Wire.bool(m,'is_default') || Wire.bool(m,'default'));
    this.effortListKnown = !!model && !Wire.bool(model,'custom');
    const reported = model ? Wire.strings(model,'efforts') : [];
    this.effortNames = START_EFFORTS.filter((level:string) => reported.includes(level));
  }
  startEfforts(launcher: Record<string,Object>): string[] {
    const runtime = Wire.text(launcher,'agent_type') || Wire.text(launcher,'agent');
    if (startsNativeOmp(launcher) || runtime === 'traex' && ['', 'zshrc'].includes(Wire.text(launcher,'source'))) return [];
    if (Wire.text(launcher,'agent') === 'codex' && Wire.strings(launcher,'permission_modes').length === 0) return [];
    if (this.effortListKnown) return this.effortNames;
    return providerStartModes(launcher) ? [] : START_EFFORTS;
  }
}
