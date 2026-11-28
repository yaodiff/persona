import type { ClientModel } from './ClientModel';
import { Decision, ResourceRow, Wire } from './Wire';
import { decodeInteractionDecision, interactionCanSubmit, interactionIsOpen, interactionSupervisorHeld } from './InteractionDecision';

// A runtime can host several providers. Its own name alone does not identify which one is asking.
function previewAgent(data:Record<string,Object>):string {
  const agent=(Wire.text(data,'agent') || Wire.text(data,'agent_type')).trim();
  const label=Wire.text(data,'agent_label').trim(),launcher=Wire.text(data,'launcher').trim();
  const id=(agent || label || launcher).toLowerCase();
  if (id==='codex' || id==='openai') return 'Codex';
  if (id==='traex') return 'TRAE CLI';
  if (['claude','claude-code','claude code'].includes(id)) return 'Claude';
  if (id==='paseo' && (!label || label.toLowerCase()===id)) return launcher.toLowerCase()===id ? '' : launcher;
  return label || agent || launcher;
}

export class AgentDecisionCard {
  readonly request: Decision;
  readonly supervisorHeld: boolean;
  readonly key: string;
  readonly preview: string;
  readonly namedPermissionAgent: boolean;
  readonly since: number;
  private readonly requestAgent:string;
  readonly owner: ClientModel;
  readonly manager: string;
  readonly machine: string;
  readonly session: string;
  readonly parent: string;
  constructor(owner: ClientModel, manager: string, machine: string, session: string, data: Record<string, Object>) {
    this.owner=owner; this.manager=manager; this.machine=machine; this.session=session;
    this.request=decodeInteractionDecision(data);
    this.supervisorHeld=interactionSupervisorHeld(this.request);
    this.request.machine=machine; this.request.session=session;
    this.key=JSON.stringify([owner.scope(),manager,machine,session,this.request.id]);
    const nested=Wire.object(data,'interaction'), source=Object.keys(nested).length ? nested : data;
    const requestingAgent=Wire.text(source,'agent') || Wire.text(data,'agent');
    this.namedPermissionAgent=!!requestingAgent && requestingAgent!=='claude';
    this.parent=Wire.text(source,'folded_under') || Wire.text(data,'folded_under');
    this.since = Date.parse(Wire.text(source,'created_at') || Wire.text(data,'created_at')) || 0;
    const tool=Wire.text(source,'tool_name'), input=Wire.object(source,'tool_input');
    this.requestAgent=tool==='ask_user' ? 'Persona' : previewAgent(source) || (source===data ? '' : previewAgent(data));
    if (this.request.kind==='question') this.preview=this.request.fields[0]?.title || this.request.prompt;
    else if (this.request.kind==='permission') this.preview=tool==='Bash' && Wire.text(input,'command') ?
      tool+': '+Wire.text(input,'command') : Wire.text(source,'summary') || Wire.text(input,'description') || tool;
    else this.preview=this.request.prompt.replace(/^#+\s*/,'').split('\n')[0];
  }

  questionAgent(row:ResourceRow):string { return this.requestAgent || previewAgent(row.data()); }

  quickOptions(): string[] {
    if (!interactionCanSubmit(this.request)) return [];
    if (this.request.kind==='permission' && !this.request.fields.length) return ['allow','deny'];
    if (this.request.kind==='question' && this.request.fields.length===1 && this.request.fields[0].kind==='choice' &&
        this.request.fields[0].choices.length<=4) return this.request.fields[0].choices;
    return [];
  }
}

export interface AgentDecisionSnapshot {
  owner: ClientModel;
  generation: number;
  scope: string;
  records: Record<string,Object>[];
}

@ObservedV2
export class AgentDecisionQueue {
  @Trace cards: AgentDecisionCard[]=[];
  @Trace private supervisorCards: AgentDecisionCard[]=[];
  @Trace snapshotComplete: boolean = false;
  @Trace submitted: string[]=[];
  @Trace error:string='';
  private acknowledged:Map<string,string>=new Map();
  private generation:number=0;
  private sequence:number=0;
  private current:ClientModel|undefined;
  private singleOwner:ClientModel|undefined;
  private reportDirectory:((pending:AgentDecisionSnapshot[]) => void)|undefined;

  clear():void {
    this.snapshotComplete = false; this.reportDirectory = undefined;
    this.generation++; this.sequence++; this.current=undefined; this.singleOwner=undefined; this.cards=[]; this.supervisorCards=[]; this.submitted=[]; this.acknowledged.clear(); this.error='';
  }
  private read(owner:ClientModel,rows:Record<string,Object>[]):AgentDecisionCard[] {
    const found:AgentDecisionCard[]=[], ids=new Set<string>();
    for (const data of rows) {
      const nested=Wire.object(data,'interaction');
      const manager=Wire.text(data,'manager') || Wire.text(data,'manager_id') || Wire.text(nested,'manager') || owner.profile.manager;
      const machine=Wire.text(data,'machine_id') || Wire.text(nested,'machine_id');
      const session=Wire.text(data,'session_id') || Wire.text(nested,'session_id');
      if (manager!==owner.profile.manager || !machine || !session || Wire.bool(data,'muted') || Wire.bool(nested,'muted')) continue;
      const card=new AgentDecisionCard(owner,manager,machine,session,data);
      if (!interactionIsOpen(card.request) || ids.has(card.key)) continue;
      ids.add(card.key); found.push(card);
    }
    return found.sort((a:AgentDecisionCard,b:AgentDecisionCard) => a.since-b.since);
  }
  async reload(root:ClientModel, singleOwner?:ClientModel,
    readDirectory?: (pending:AgentDecisionSnapshot[]) => void):Promise<void> {
    if (this.current!==root || this.singleOwner!==singleOwner) { this.clear(); this.current=root; this.singleOwner=singleOwner; }
    // A quick answer reloads this same queue. Publish its directory counts along with the refreshed cards.
    if (readDirectory) this.reportDirectory=readDirectory;
    const report=this.reportDirectory;
    const n=++this.sequence, generation=root.connectionGeneration(), scope=root.scope();
    const owners=singleOwner ? root.directoryOwners().filter((owner:ClientModel) => singleOwner===owner) : root.directoryOwners();
    const pending:AgentDecisionSnapshot[] = [];
    let complete = owners.length > 0;
    const groups=await Promise.all(owners.map(async (owner:ClientModel):Promise<AgentDecisionCard[]> => {
      const ownerGeneration=owner.connectionGeneration(), ownerScope=owner.scope();
      if (!owner.connected) { complete = false; return []; }
      try {
        const data=await owner.repo().decisions();
        if (!owner.owns(ownerGeneration,ownerScope)) { complete = false; return []; }
        if (report) pending.push({ owner, generation:ownerGeneration, scope:ownerScope, records:data });
        const cards=this.read(owner,data);
        return cards.filter((card:AgentDecisionCard) => this.acknowledged.get(card.key)!==card.request.raw);
      } catch (_) { complete = false; return []; }
    }));
    if (this.current===root && this.sequence===n && root.owns(generation,scope)) {
      this.cards=groups.flat();
      this.supervisorCards=this.cards.filter((card:AgentDecisionCard) => card.supervisorHeld);
      this.snapshotComplete=complete;
      if (report) report(pending);
    }
  }
  forSession(root:ClientModel,row:ResourceRow):AgentDecisionCard[] {
    const owner=root.directoryOwnerFor(row);
    return this.cards.filter((card:AgentDecisionCard) => !card.supervisorHeld && card.owner===owner && card.manager===row.manager &&
      card.machine===row.machine && card.session===(row.session || row.id));
  }
  get hasSupervisorCards():boolean { return this.supervisorCards.length > 0; }

  /** Resolve a folded child's cards under its waiting parent, without ever changing their owner or write target. */
  heldOnlyRows(root:ClientModel, waiting:ResourceRow[]):Set<ResourceRow> {
    const held = new Set<ResourceRow>();
    if (this.current !== root || !this.supervisorCards.length) return held;
    const homes = new Map<ClientModel,Map<string,ResourceRow>>();
    for (const row of waiting) {
      if (!['needs_you','blocked','waiting_for_user'].includes(row.state) && !row.badgeFields().childrenWaiting) continue;
      const owner = root.directoryOwnerFor(row);
      if (!owner) continue;
      const rows = homes.get(owner) || new Map<string,ResourceRow>();
      rows.set(JSON.stringify([row.manager,row.machine,row.session || row.id]),row);
      homes.set(owner,rows);
    }
    const home = (card:AgentDecisionCard):ResourceRow|undefined => {
      const rows = homes.get(card.owner);
      return rows?.get(JSON.stringify([card.manager,card.machine,card.parent])) ||
        rows?.get(JSON.stringify([card.manager,card.machine,card.session]));
    };
    for (const card of this.supervisorCards) {
      const row = home(card);
      if (row) held.add(row);
    }
    for (const card of this.cards) {
      if (card.supervisorHeld) continue;
      const row = home(card);
      if (row) held.delete(row);
    }
    return held;
  }
  waitingSince(root:ClientModel,row:ResourceRow):number {
    const cards=this.forSession(root,row);
    if (cards.length) return cards[0].since;
    const data=row.data();
    return Date.parse(Wire.text(data,'state_since') || Wire.text(data,'updated_at')) || 0;
  }
  enabled(root:ClientModel,row:ResourceRow,card:AgentDecisionCard):boolean {
    return this.current===root && this.forSession(root,row).some((value:AgentDecisionCard) => value.key===card.key && value.request.raw===card.request.raw) &&
      !this.submitted.includes(card.key) && card.owner.canApproveSession(row) && !card.owner.operationBlocked(this.path(card));
  }
  private path(card:AgentDecisionCard):string { return '/api/v1/interactions/'+encodeURIComponent(card.request.id)+'/resolve'; }
  async answer(root:ClientModel,row:ResourceRow,card:AgentDecisionCard,choice:string,op:string,visible:()=>boolean):Promise<void> {
    if (!card.quickOptions().includes(choice)) return;
    const values:Record<string,Object>={};
    if (card.request.kind==='question') values[card.request.fields[0].key]=choice;
    await this.answerForm(root,row,card,card.request.kind==='question' ? 'answer' : choice,values,op,visible);
  }
  async answerForm(root:ClientModel,row:ResourceRow,card:AgentDecisionCard,option:string,values:Record<string,Object>,op:string,visible:()=>boolean):Promise<void> {
    if (!visible() || !this.enabled(root,row,card)) return;
    const generation=this.generation, owner=card.owner, ownerGeneration=owner.connectionGeneration(), ownerScope=owner.scope(), repo=owner.repo();
    const rootGeneration=root.connectionGeneration(), rootScope=root.scope(), rootManager=root.profile?.manager;
    const current=():boolean => root.owns(rootGeneration,rootScope) && root.profile?.manager===rootManager && this.generation===generation && this.current===root && visible() && root.directoryOwnerFor(row)===owner &&
      owner.owns(ownerGeneration,ownerScope) && owner.repo()===repo && owner.canApproveSession(row);
    this.submitted=this.submitted.concat([card.key]); this.error='';
    try {
      const actual=this.read(owner,await repo.decisions()).find((fresh:AgentDecisionCard) => fresh.key===card.key);
      if (!current()) return;
      if (!actual || !interactionCanSubmit(actual.request) || actual.request.raw!==card.request.raw || owner.operationBlocked(this.path(card))) {
        this.error='request_updated'; return;
      }
      const dispatch=():boolean => current() && this.cards.some((pending:AgentDecisionCard) =>
        pending.key===card.key && pending.request.raw===actual.request.raw && interactionCanSubmit(pending.request));
      await repo.resolve(actual.request,option,values,op,dispatch);
      if (current()) {
        this.acknowledged.set(card.key,card.request.raw);
        card.request.status='resolved';
        this.cards=this.cards.filter((value:AgentDecisionCard) => value.key!==card.key);
      }
    } catch (failure) {
      if (current()) {
        const data=failure as Object as Record<string,Object>;
        this.error=Wire.bool(data,'resultUnknown') ? 'result_unknown' : Wire.bool(data,'resultPending') ? 'pending' : 'failed';
      }
    } finally {
      if (this.generation===generation) {
        if (current()) await this.reload(root,this.singleOwner);
        this.submitted=this.submitted.filter((key:string) => key!==card.key);
      }
    }
  }
}
