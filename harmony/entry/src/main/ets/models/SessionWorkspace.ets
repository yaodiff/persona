import { FolderCrumb, folderCrumbs, parentFolder, joinedFilePath } from './WorkspaceBrowser';
import { ClientModel } from './ClientModel';
import { ResourceRow, Wire } from './Wire';
import { segment } from './HubRepository';
import { HubTransport, HttpFailure } from '../platform/HubTransport';
import { FileTransfer, transferMachineTrustedOnline } from './FileTransfer';

const TRANSFER_LIMIT: number = 32 * 1024 * 1024;
const TERMINAL_COLS_MIN: number = 10;
const TERMINAL_COLS_MAX: number = 240;
const TERMINAL_ROWS_MIN: number = 4;
const TERMINAL_ROWS_MAX: number = 100;

export class WorkspaceEntry {
  name: string = '';
  path: string = '';
  directory: boolean = false;
  symlink: boolean = false;
  linkTarget: string = '';
  linkBroken: boolean = false;
  size: number = 0;
  mtime: string = '';
  ignored: boolean = false;
}

export class WorkspaceGitFile {
  path: string = '';
  originalPath: string = '';
  index: string = '.';
  worktree: string = '.';
  added: number = 0;
  deleted: number = 0;
  binary: boolean = false;
  get staged(): boolean { return this.index !== '.' && this.index !== '?'; }
  get unstaged(): boolean { return this.worktree !== '.' && this.index !== '?'; }
  get untracked(): boolean { return this.index === '?'; }
  get mark(): string {
    if (this.untracked) return 'U';
    if (this.index === 'U' || this.worktree === 'U') return '!';
    return this.index !== '.' ? this.index : this.worktree;
  }
}

export class WorkspaceBranch {
  name: string = '';
  remote: boolean = false;
  current: boolean = false;
  upstream: string = '';
  hash: string = '';
  subject: string = '';
  date: string = '';
}

export class WorkspaceTrashItem {
  path: string = '';
  trashId: string = '';
}

export class WorkspacePreviewServer {
  name: string = '';
  port: number = 0;
  url: string = '';
  command: string[] = [];
  env: Record<string, Object> = {};
  cwd: string = '';
  listening: boolean = false;
  title: string = '';
  problem: string = '';
}

export class WorkspacePreviewFrame {
  seq: number = 0;
  width: number = 0;
  height: number = 0;
  data: ArrayBuffer = new ArrayBuffer(0);
}

interface DecodedCodePoint {
  text: string;
  count: number;
}

export interface DownloadedWorkspaceFile {
  data: ArrayBuffer;
  name: string;
  contentType: string;
}

function query(parts: string[]): string { return parts.join('&'); }

function blankGrid(cols: number, rows: number): string[][] {
  const grid: string[][] = [];
  for (let row = 0; row < rows; row++) {
    const cells: string[] = [];
    for (let col = 0; col < cols; col++) cells.push(' ');
    grid.push(cells);
  }
  return grid;
}

function decodedBase64(value: string): Uint8Array | undefined {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  const clean = value.replace(/\s/g, '');
  if (clean.length % 4 !== 0 || /[^A-Za-z0-9+/=]/.test(clean)) return undefined;
  const out: number[] = [];
  for (let i = 0; i < clean.length; i += 4) {
    const a = alphabet.indexOf(clean[i]);
    const b = alphabet.indexOf(clean[i + 1]);
    const c = clean[i + 2] === '=' ? 0 : alphabet.indexOf(clean[i + 2]);
    const d = clean[i + 3] === '=' ? 0 : alphabet.indexOf(clean[i + 3]);
    if (a < 0 || b < 0 || c < 0 || d < 0) return undefined;
    const word = (a << 18) | (b << 12) | (c << 6) | d;
    out.push((word >> 16) & 0xff);
    if (clean[i + 2] !== '=') out.push((word >> 8) & 0xff);
    if (clean[i + 3] !== '=') out.push(word & 0xff);
  }
  return new Uint8Array(out);
}

function codePointAt(bytes: Uint8Array, index: number): DecodedCodePoint {
  const first = bytes[index];
  if (first < 0x80) return { text: String.fromCharCode(first), count: 1 };
  let count = first >= 0xf0 && first <= 0xf4 ? 4 : first >= 0xe0 && first <= 0xef ? 3 : first >= 0xc2 && first <= 0xdf ? 2 : 1;
  if (count === 1) return { text: '\ufffd', count: 1 };
  if (index + count > bytes.length) return { text: '', count: 0 };
  let point = first & (count === 2 ? 0x1f : count === 3 ? 0x0f : 0x07);
  for (let i = 1; i < count; i++) {
    const next = bytes[index + i];
    if ((next & 0xc0) !== 0x80) return { text: '\ufffd', count: 1 };
    point = (point << 6) | (next & 0x3f);
  }
  if ((count === 2 && point < 0x80) || (count === 3 && point < 0x800) || (count === 4 && point < 0x10000) || point > 0x10ffff)
    return { text: '\ufffd', count: 1 };
  if (point <= 0xffff) return { text: String.fromCharCode(point), count: count };
  const value = point - 0x10000;
  return { text: String.fromCharCode(0xd800 + (value >> 10), 0xdc00 + (value & 0x3ff)), count: count };
}

@ObservedV2
export class SessionWorkspace {
  @Trace fileTransfer: FileTransfer = new FileTransfer();
  @Trace tab: number = 0;
  @Trace cwd: string = '';
  @Trace currentPath: string = '';
  @Trace entries: WorkspaceEntry[] = [];
  @Trace folderBack: string[] = [];
  @Trace folderForward: string[] = [];
  @Trace findNames: string[] = [];
  @Trace findRoot: string = '';
  @Trace findBusy: boolean = false;
  @Trace findError: string = '';
  @Trace findTruncated: boolean = false;
  private findEpoch: number = 0;
  @Trace fileBusy: boolean = false;
  @Trace fileReadBusy: boolean = false;
  @Trace fileError: string = '';
  @Trace fileTruncated: boolean = false;
  @Trace fileSheetOpen: boolean = false;
  @Trace selectedFile: string = '';
  @Trace selectedFileName: string = '';
  @Trace selectedFileSize: number = 0;
  @Trace selectedFileMime: string = '';
  @Trace selectedFileText: string = '';
  @Trace selectedFileIsText: boolean = false;
  @Trace fileOffset: number = 0;
  @Trace fileNext: number = 0;
  @Trace fileHasMore: boolean = false;
  @Trace gitBusy: boolean = false;
  @Trace gitError: string = '';
  @Trace gitStatus: Record<string, Object> = {};
  @Trace gitPrAnswer: Record<string,Object> = {};
  @Trace gitPrError: string = '';
  @Trace gitPrBusy: boolean = false;
  private gitPrKey: string = '';
  private gitPrEpoch: number = 0;
  @Trace gitStashBusy: boolean = false;
  @Trace gitStashError: string = '';
  private gitStashEpoch: number = 0;
  @Trace private fileGitMarks: Map<string,string> = new Map<string,string>();
  private fileGitEpoch: number = 0;
  @Trace gitFiles: WorkspaceGitFile[] = [];
  @Trace gitCommits: Record<string,Object>[] = [];
  @Trace gitMoreCommits: boolean = false;
  @Trace gitHistoryBusy: boolean = false;
  @Trace gitHistoryError: string = '';
  @Trace gitCommitHash: string = '';
  @Trace gitShow: Record<string,Object> = {};
  @Trace gitShowBusy: boolean = false;
  @Trace gitShowError: string = '';
  private gitFirst: string = '';
  private gitSkip: number = 0;
  private gitStatusEpoch: number = 0;
  private gitHistoryEpoch: number = 0;
  private gitShowEpoch: number = 0;
  private gitDiffEpoch: number = 0;
  @Trace gitDiff: string = '';
  @Trace gitDiffError: string = '';
  @Trace gitDiffBusy: boolean = false;
  @Trace gitDiffPath: string = '';
  @Trace gitDiffStaged: boolean = false;
  @Trace gitDiffTruncated: boolean = false;
  @Trace gitWriteBusy: boolean = false;
  @Trace gitWriteState: string = 'idle';
  @Trace gitPullState: string = '';
  @Trace gitWriteError: string = '';
  @Trace gitBranches: WorkspaceBranch[] = [];
  @Trace gitStashes: Record<string, Object>[] = [];
  @Trace gitCommitMessage: string = '';
  @Trace fileWriteBusy: boolean = false;
  @Trace fileWriteState: string = 'idle';
  @Trace fileWriteError: string = '';
  @Trace fileConflictName: string = '';
  @Trace trashUndo: WorkspaceTrashItem[] = [];
  @Trace screenText: string = '';
  @Trace screenError: string = '';
  @Trace screenBusy: boolean = false;
  @Trace screenKeyBusy: boolean = false;
  @Trace screenControlRequested: boolean = false;
  @Trace screenRevision: number = 0;
  @Trace previewServers: WorkspacePreviewServer[] = [];
  @Trace previewError: string = '';
  @Trace previewState: string = 'idle';
  @Trace previewFrame: WorkspacePreviewFrame | undefined = undefined;
  @Trace previewMessage: string = '';
  @Trace previewExternalUrl: string = '';
  @Trace previewPort: number = 0;
  @Trace previewPath: string = '/';
  @Trace previewName: string = '';
  @Trace previewStartingName: string = '';
  @Trace terminalText: string = '';
  @Trace terminalError: string = '';
  @Trace terminalControlState: string = 'off';
  @Trace terminalControlRequested: boolean = false;
  @Trace terminalCols: number = 80;
  @Trace terminalRows: number = 24;
  @Trace terminalGridCols: number = 80;
  @Trace terminalGridRows: number = 24;
  @Trace hasMoreEntries: boolean = false;

  private model: ClientModel | undefined;
  private ownerScope: string = '';
  private ownerRowKey: string = '';
  private ownerTarget: string = '';
  private ownerGeneration: number = 0;
  private ownerFeatures: string[] = [];
  private machineId: string = '';
  private sessionId: string = '';
  private fsWatchPath: string = '';
  private fsWatchGit: boolean = false;
  private fileListEpoch: number = 0;
  private fileReadEpoch: number = 0;
  private visible: boolean = false;
  private connectionReady: boolean = false;
  private terminalSubscribed: boolean = false;
  private terminalEpoch: string = '';
  private terminalSeq: number = 0;
  private terminalGrid: string[][] = blankGrid(80, 24);
  private terminalCursorColumn: number = 0;
  private terminalCursorRow: number = 0;
  private terminalEscape: number = 0;
  private terminalCsi: string = '';
  private terminalOscEscape: boolean = false;
  private terminalPending: Uint8Array = new Uint8Array(0);
  private terminalPollTimer: number = -1;
  private screenPollTimer: number = -1;
  private previewPollTimer: number = -1;
  private fsRefreshTimer: number = -1;
  private revision: number = 0;
  private previewTransport: HubTransport | undefined;
  private previewSeq: number = 0;
  private previewStartedAt: number = 0;

  bind(model: ClientModel): void {
    const row = model.selected;
    const cwd = row.path || Wire.text(row.data(), 'cwd');
    const machine = row.machine || Wire.text(row.data(), 'machine_id');
    const session = row.session || row.id;
    const scope = model.scope(), target = model.target(row), generation = model.connectionGeneration();
    const key = JSON.stringify([scope, row.key(), target, generation, cwd, machine, session]);
    if (this.model && key === JSON.stringify([this.ownerScope, this.ownerRowKey, this.ownerTarget, this.ownerGeneration, this.cwd, this.machineId, this.sessionId])) return;
    this.hide();
    this.revision++;
    this.model = model;
    this.ownerScope = scope;
    this.ownerRowKey = row.key();
    this.ownerTarget = target;
    this.ownerGeneration = generation;
    this.ownerFeatures = model.features.slice();
    this.machineId = machine;
    this.sessionId = session;
    const manager = row.manager || model.profile.manager;
    this.fileTransfer.bind(model, machine, manager);
    this.cwd = cwd;
    this.currentPath = cwd; this.folderBack = []; this.folderForward = []; this.resetFileFind();
    this.entries = [];
    this.fileBusy = false; this.fileReadBusy = false;
    this.gitPrKey=''; this.gitPrEpoch++; this.gitPrAnswer={}; this.gitPrError=''; this.gitPrBusy=false;
    this.gitStashEpoch++; this.gitStashError=''; this.gitStashBusy=false; this.fileGitEpoch++; this.fileGitMarks=new Map<string,string>();
    this.gitStatus = {}; this.gitFirst = ''; this.gitSkip = 0; this.gitCommits = []; this.gitMoreCommits = false;
    this.gitHistoryError = ''; this.gitHistoryBusy = false; this.gitHistoryEpoch++; this.closeGitShow(); this.closeGitDiff();
    this.gitFiles = [];
    this.gitBusy = false; this.gitDiffBusy = false;
    this.gitBranches = [];
    this.gitStashes = [];
    this.gitWriteBusy = false; this.gitWriteState = 'idle'; this.gitWriteError = ''; this.gitPullState = '';
    this.fileWriteBusy = false; this.fileWriteState = 'idle'; this.fileWriteError = ''; this.fileConflictName = ''; this.trashUndo = [];
    this.fileError = '';
    this.gitError = '';
    this.screenText = ''; this.screenError = ''; this.screenRevision = 0; this.screenBusy = false; this.screenControlRequested = false;
    this.previewServers = []; this.previewError = ''; this.previewState = 'idle'; this.previewFrame = undefined;
    this.previewMessage = ''; this.previewPort = 0; this.previewPath = '/'; this.previewName = '';
    this.previewExternalUrl = '';
    this.terminalText = '';
    this.terminalError = '';
    this.terminalControlState = 'off';
    this.terminalControlRequested = false;
    this.terminalEpoch = '';
    this.terminalSeq = 0;
    this.terminalCols = 80; this.terminalRows = 24;
    this.resetTerminalGrid(80, 24);
  }

  private current(): boolean {
    const model = this.model;
    if (!model || !this.machineId || !this.sessionId || !this.cwd) return false;
    return model.owns(this.ownerGeneration, this.ownerScope) && model.target() === this.ownerTarget && model.selected.key() === this.ownerRowKey &&
      this.machineId === (model.selected.machine || Wire.text(model.selected.data(), 'machine_id')) &&
      this.sessionId === (model.selected.session || model.selected.id);
  }

  private activeRevision(revision: number, target: string, tab: number): boolean {
    return revision === this.revision && target === this.ownerTarget && this.current() && this.visible && this.tab === tab;
  }

  private sameOwner(target: string, generation: number): boolean {
    return target === this.ownerTarget && generation === this.ownerGeneration && this.current();
  }

  currentTarget(): string { return this.ownerTarget; }
  currentRevision(): number { return this.revision; }
  isCurrent(): boolean { return this.current(); }
  currentMachineId(): string { return this.machineId; }

  private feature(name: string): boolean {
    return this.ownerFeatures.includes(name) && (this.model?.features.includes(name) === true);
  }

  private machineTrustedOnline(): boolean {
    const model = this.model;
    if (!model || !this.current()) return false;
    const manager = model.selected.manager || model.profile.manager;
    return transferMachineTrustedOnline(model, this.machineId, manager);
  }

  canSendFile(entry: WorkspaceEntry): boolean {
    return this.current() && this.visible && this.tab === 0 && this.model?.features.includes('file_transfer') === true &&
      !entry.directory && !entry.symlink && !!entry.path && !!entry.name && this.machineTrustedOnline();
  }

  async openFileTransfer(entry: WorkspaceEntry): Promise<void> {
    if (!this.canSendFile(entry)) throw new Error('transfer_unavailable');
    await this.fileTransfer.openSource(entry.path, entry.name, this.cwd);
  }

  private sessionWritable(): boolean {
    const model = this.model;
    const data = model?.selected.data();
    if (!data) return false;
    const sourceKind = Wire.text(data, 'source_kind');
    const capabilities = Wire.object(data, 'capabilities');
    if (model?.demo || Wire.bool(data, 'read_only', Wire.bool(data, 'readonly')) || Wire.text(data, 'read_only_reason') ||
      (sourceKind !== '' && Wire.has(capabilities, 'send') && !Wire.bool(capabilities, 'send')) ||
      ['identity_conflict', 'codex_history'].includes(sourceKind) ||
      (model?.selected.manager && model.profile.manager && model.selected.manager !== model.profile.manager)) return false;
    return true;
  }

  canUploadFiles(): boolean {
    return this.current() && this.visible && this.tab === 0 && this.feature('upload_dir') &&
      this.machineTrustedOnline() && this.sessionWritable() && !this.hasPendingUpload();
  }

  canEditFiles(): boolean {
    return this.current() && this.visible && this.tab === 0 && this.feature('file_edit') &&
      this.machineTrustedOnline() && this.sessionWritable() && !this.fileWriteBusy && !this.hasPendingFilesEdit();
  }

  private writableGitTarget(): boolean {
    return this.current() && this.visible && this.tab===1 && this.machineTrustedOnline() && this.sessionWritable() &&
      Wire.bool(this.gitStatus,'is_repo') && !this.gitWriteBusy && !this.hasPendingGitWrite();
  }

  canWriteGit(): boolean { return this.writableGitTarget() && this.feature('git_write'); }

  canPullGit(): boolean {
    return this.writableGitTarget() && !this.gitHistoryBusy && !!Wire.text(this.gitStatus,'branch') &&
      !!Wire.text(this.gitStatus,'upstream') && !Wire.bool(this.gitStatus,'gone');
  }

  hasGitPullIssue(): boolean {
    return this.gitPullState==='failed' || this.gitPullState==='pending' || this.gitPullState==='unknown';
  }

  canUsePreview(): boolean {
    return this.current() && this.visible && this.tab === 4 && this.feature('preview') && this.machineTrustedOnline();
  }

  canStartPreview(): boolean {
    return this.canUsePreview() && this.sessionWritable() && !this.hasPending(this.requestPath('/preview/start'));
  }

  canControlScreen(): boolean {
    if (!this.current() || !this.visible || this.tab !== 3 || this.screenKeyBusy || this.hasPending(this.screenKeysPath()) ||
      !this.machineTrustedOnline() || !this.sessionWritable()) return false;
    const data = this.model?.selected.data();
    const caps = data ? Wire.object(data, 'capabilities') : Wire.payload([], []);
    const sourced = !!data && !!Wire.text(data, 'source_kind');
    return !sourced || Wire.bool(caps, 'terminal');
  }

  private hasPending(path: string): boolean {
    const records = this.model?.repo().mutations?.blocked() || [];
    return records.some((record) => record.path === path && record.method === 'POST');
  }

  private screenKeysPath(): string {
    return '/api/v1/sessions/' + segment(this.machineId) + '/' + segment(this.sessionId) + '/keys';
  }

  private hasPendingFilesEdit(): boolean {
    const prefix = this.requestPath('/files/');
    const records = this.model?.repo().mutations?.blocked() || [];
    return records.some((record) => record.method === 'POST' && record.path.startsWith(prefix));
  }

  private hasPendingUpload(): boolean {
    const prefix = this.requestPath('/files?');
    const records = this.model?.repo().mutations?.blocked() || [];
    return records.some((record) => record.method === 'POST' && record.path.startsWith(prefix));
  }

  private hasPendingGitWrite(): boolean {
    const prefix = this.requestPath('/git/');
    const records = this.model?.repo().mutations?.blocked() || [];
    return records.some((record) => record.method === 'POST' && record.path.startsWith(prefix));
  }

  private async request(path: string, method: string = 'GET', body: Record<string, Object> = {},
    extraAdmission?: () => boolean, acknowledged?: (value:Record<string,Object>) => boolean): Promise<Record<string, Object>> {
    if (!this.current()) throw new Error('scope_changed');
    const model = this.model;
    if (!model || !model.connected || !this.connectionReady) throw new Error('offline');
    const revision = this.revision, target = this.ownerTarget, tab = this.tab;
    const admit = (): boolean => revision === this.revision && target === this.ownerTarget && tab === this.tab &&
      this.visible && this.connectionReady && this.current() && model.connected && (!extraAdmission || extraAdmission());
    return model.repo().call(path,method,body,'',admit,false,acknowledged);
  }

  private requestPath(suffix: string): string { return '/api/v1/machines/' + segment(this.machineId) + suffix; }

  show(): void {
    this.visible = true;
    this.connectionReady = this.model?.connected === true;
    this.ownerFeatures = this.model?.features.slice() || [];
    if (!this.current()) return;
    this.fileTransfer.show().catch(() => {});
    this.activateTab(this.tab);
  }

  selectTab(value: number): void {
    const next = Math.max(0, Math.min(4, value));
    if (next === this.tab && this.visible) return;
    if (next !== this.tab) {
      this.revision++;
      this.gitStashEpoch++; this.gitStashBusy=false; this.gitPrEpoch++; if (this.gitPrBusy) this.gitPrKey=''; this.gitPrBusy=false;
      this.fileListEpoch++; if (this.findBusy) this.resetFileFind();
      this.gitStatusEpoch++; this.gitHistoryEpoch++; this.gitHistoryBusy=false; this.closeGitShow(); this.closeGitDiff();
      this.fileBusy = false; this.fileReadBusy = false; this.gitBusy = false; this.gitDiffBusy = false; this.screenBusy = false;
      this.screenKeyBusy = false;
      this.fileWriteBusy = false; this.gitWriteBusy = false;
    }
    if (this.tab === 2 && next !== 2) this.releaseTerminal(true);
    if (this.tab === 3 && next !== 3) this.stopScreen(true);
    if (this.tab === 4 && next !== 4) this.closePreview();
    this.tab = next;
    if (this.visible) this.activateTab(next);
  }

  private activateTab(value: number): void {
    if (!this.current()) return;
    if (value === 0) {
      this.releaseTerminal(true);
      this.stopScreen(true); this.closePreview();
      this.sendFsWatch([this.currentPath || this.cwd], false);
      this.loadFiles();
      this.loadFileGitMarks();
    } else if (value === 1) {
      this.releaseTerminal(true);
      this.stopScreen(true); this.closePreview();
      this.sendFsWatch([], true);
      this.loadGit();
    } else if (value === 2) {
      this.stopScreen(true); this.closePreview();
      this.sendFsWatch([], false);
      this.startTerminal(false);
    } else if (value === 3) {
      this.releaseTerminal(true); this.closePreview(); this.sendFsWatch([], false);
      this.startScreenPoll();
    } else {
      this.releaseTerminal(true); this.stopScreen(true); this.sendFsWatch([], false);
      this.loadPreviewServers();
    }
  }

  private resetFileFind():void {
    this.findEpoch++; this.findNames=[]; this.findRoot=''; this.findBusy=false; this.findTruncated=false; this.findError='';
  }

  private async browse(path:string):Promise<void> {
    if (path !== this.currentPath) {
      this.entries=[]; this.hasMoreEntries=false; this.fileError=''; this.resetFileFind(); this.sendFsWatch([],false);
    }
    this.currentPath=path;
    await this.loadFiles();
  }

  async openPath(path: string): Promise<void> {
    const value=path.trim();
    if (!value || !this.current() || !this.visible || this.tab !== 0) return;
    if (value !== this.currentPath) {
      this.folderBack=this.folderBack.concat([this.currentPath]).slice(-50); this.folderForward=[];
    }
    await this.browse(value);
  }

  async historyFolder(forward:boolean):Promise<void> {
    if (!this.current() || !this.visible || this.tab !== 0) return;
    const stack=forward ? this.folderForward : this.folderBack;
    if (!stack.length) return;
    const next=stack[stack.length-1];
    if (forward) { this.folderForward=stack.slice(0,-1); this.folderBack=this.folderBack.concat([this.currentPath]).slice(-50); }
    else { this.folderBack=stack.slice(0,-1); this.folderForward=this.folderForward.concat([this.currentPath]).slice(-50); }
    await this.browse(next);
  }

  crumbs(): FolderCrumb[] {
    const machine=this.model?.machines.find((row:ResourceRow) => row.id === this.machineId);
    return folderCrumbs(this.currentPath,machine ? Wire.text(machine.data(),'home') : '',this.cwd);
  }

  fileGitMark(entry:WorkspaceEntry):string {
    return this.fileGitMarks.get(entry.path.replace(/\\/g,'/')) || '';
  }

  async loadFileGitMarks():Promise<void> {
    if (!this.current() || !this.visible || this.tab!==0 || !this.connectionReady) return;
    const rev=this.revision, target=this.ownerTarget, epoch=++this.fileGitEpoch;
    const live=():boolean => epoch===this.fileGitEpoch && this.activeRevision(rev,target,0);
    try {
      const state=await this.request(this.requestPath('/git/status?cwd='+segment(this.cwd)));
      if (!live()) return;
      const marks=new Map<string,string>(), root=Wire.text(state,'root').replace(/\\/g,'/').replace(/\/+$/,'');
      if (root && Wire.bool(state,'is_repo')) {
        for (const item of Wire.rows(state,'files')) {
          const file=this.gitFile(item);
          const components=file.path.replace(/\\/g,'/').split('/').filter((name:string) => !!name);
          let current=root;
          for (const name of components) {
            current+='/'+name;
            if (!marks.has(current)) marks.set(current,file.mark);
          }
        }
      }
      this.fileGitMarks=marks;
    } catch (_) {
      if (live()) this.fileGitMarks=new Map<string,string>();
    }
  }

  async loadFileIndex(force:boolean=false):Promise<void> {
    if (!this.current() || !this.visible || this.tab !== 0) return;
    const path=this.currentPath || this.cwd, revision=this.revision;
    if (!force && this.findRoot===path) return;
    const epoch=++this.findEpoch; this.findRoot=path; this.findNames=[]; this.findTruncated=false; this.findBusy=true; this.findError='';
    const live=():boolean => epoch===this.findEpoch && this.activeRevision(revision,this.ownerTarget,0) && path===this.currentPath;
    try {
      const reply=await this.request(this.requestPath('/fs/find?cwd='+segment(path)));
      if (!live()) return;
      if (!Array.isArray(reply['files'])) throw new Error('session_find_unavailable');
      this.findNames=Wire.strings(reply,'files'); this.findTruncated=Wire.bool(reply,'truncated');
    } catch (error) { if (live()) this.findError=this.message(error as Error); }
    finally { if (epoch===this.findEpoch) this.findBusy=false; }
  }

  hide(): void {
    this.visible = false;
    this.revision++;
    this.fileTransfer.hide();
    this.resetBusyState();
    if (this.fsRefreshTimer !== -1) clearTimeout(this.fsRefreshTimer);
    if (this.terminalPollTimer !== -1) clearTimeout(this.terminalPollTimer);
    if (this.screenPollTimer !== -1) clearTimeout(this.screenPollTimer);
    if (this.previewPollTimer !== -1) clearTimeout(this.previewPollTimer);
    this.fsRefreshTimer = -1; this.terminalPollTimer = -1; this.screenPollTimer = -1; this.previewPollTimer = -1;
    this.sendFsWatch([], false, true);
    this.releaseTerminal(true);
    this.stopScreen(false); this.closePreview();
    this.connectionReady = false;
  }

  onConnected(): void {
    if (!this.visible) return;
    if (!this.current()) { const model = this.model; if (model) { this.bind(model); this.show(); } return; }
    if (this.connectionReady) { this.fileTransfer.onConnected(); return; }
    this.connectionReady = true;
    this.ownerFeatures = this.model?.features.slice() || [];
    this.fileTransfer.onConnected();
    if (this.tab === 2) {
      this.sendFsWatch([], false);
      this.startTerminal(this.terminalEpoch.length > 0);
    } else this.activateTab(this.tab);
  }

  onDisconnected(): void {
    this.revision++;
    this.fileTransfer.onDisconnected();
    this.resetBusyState();
    this.connectionReady = false;
    this.terminalSubscribed = false;
    this.fsWatchPath = '';
    this.fsWatchGit = false;
    if (this.terminalPollTimer !== -1) clearTimeout(this.terminalPollTimer);
    if (this.fsRefreshTimer !== -1) clearTimeout(this.fsRefreshTimer);
    if (this.screenPollTimer !== -1) clearTimeout(this.screenPollTimer);
    if (this.previewPollTimer !== -1) clearTimeout(this.previewPollTimer);
    this.terminalPollTimer = -1; this.fsRefreshTimer = -1; this.screenPollTimer = -1; this.previewPollTimer = -1;
    this.screenControlRequested = false;
    this.releaseTerminal(false);
    this.stopScreen(false); this.closePreview();
  }

  onBinary(_payload: ArrayBuffer): void {
    // The hub protocol sends terminal VT frames as base64 in `terminal` envelopes. File bytes use HTTP.
  }

  onEvent(raw: string | Record<string, Object>): void {
    if (!this.visible || !this.current()) return;
    let event: Record<string, Object>;
    try { event = typeof raw === 'string' ? Wire.parse(raw) : raw; } catch (_) { return; }
    const type = Wire.text(event, 'type');
    if (type === 'terminal' || type === 'terminal_control') {
      if (Wire.text(event, 'machine_id') !== this.machineId || Wire.text(event, 'session_id') !== this.sessionId || this.tab !== 2) return;
      if (type === 'terminal_control') { this.terminalControlState = Wire.text(event, 'state', 'off'); return; }
      this.receiveTerminal(event);
      return;
    }
    if (type === 'fs_changed' && Wire.text(event, 'machine_id') === this.machineId && Wire.text(event, 'cwd') === this.cwd) {
      const dirs = Wire.strings(event, 'dirs');
      const gitChanged = Wire.bool(event, 'git');
      if ((this.tab === 0 && dirs.includes(this.currentPath)) || (this.tab === 1 && gitChanged)) this.scheduleFsRefresh();
    }
  }

  private sendFsWatch(paths: string[], git: boolean, releaseStale: boolean = false): void {
    const model = this.model;
    if (!model || (!releaseStale && !this.current()) || !model.connected) return;
    const samePaths = paths.length === 1 && this.fsWatchPath === paths[0];
    if ((git === this.fsWatchGit && (paths.length === 0 ? this.fsWatchPath === '' : samePaths))) return;
    this.fsWatchPath = paths.length === 1 ? paths[0] : '';
    this.fsWatchGit = git;
    const body = Wire.payload(['type', 'machine_id', 'cwd', 'paths', 'git'], ['fs_watch', this.machineId, this.cwd, paths as Object, git]);
    try { model.repo().transport.send(JSON.stringify(body)); } catch (_) { }
  }

  private scheduleFsRefresh(): void {
    if (this.fsRefreshTimer !== -1) clearTimeout(this.fsRefreshTimer);
    this.fsRefreshTimer = setTimeout(() => {
      this.fsRefreshTimer = -1;
      if (!this.visible || !this.current()) return;
      if (this.tab === 0) this.loadFiles();
      if (this.tab === 1) this.loadGit();
    }, 200);
  }

  async loadFiles(): Promise<void> {
    if (!this.current() || !this.visible || this.tab !== 0) return;
    const revision = this.revision, path = this.currentPath || this.cwd, epoch = ++this.fileListEpoch;
    this.fileBusy = true;
    this.fileError = '';
    try {
      const result = await this.request(this.requestPath('/fs?' + query(['path=' + segment(path), 'cwd=' + segment(this.cwd)])));
      if (epoch !== this.fileListEpoch || revision !== this.revision || !this.current() || (this.currentPath || this.cwd) !== path) return;
      const serverPath = Wire.text(result, 'path', path);
      this.currentPath = serverPath;
      this.hasMoreEntries = Wire.bool(result, 'truncated');
      this.entries = Wire.rows(result, 'entries').map((item: Record<string, Object>) => {
        const row = new WorkspaceEntry();
        row.name = Wire.text(item, 'name'); row.path = this.joinPath(serverPath, row.name);
        row.directory = Wire.bool(item, 'dir'); row.symlink = Wire.bool(item, 'symlink');
        row.linkTarget = Wire.text(item,'link_target'); row.linkBroken = Wire.bool(item,'link_broken');
        row.size = Wire.number(item, 'size'); row.mtime = Wire.text(item, 'mtime'); row.ignored = Wire.bool(item, 'ignored');
        return row;
      });
      this.sendFsWatch([serverPath], false);
    } catch (error) {
      if (epoch === this.fileListEpoch && revision === this.revision && this.current() && (this.currentPath || this.cwd) === path)
        this.fileError = this.message(error as Error);
    } finally {
      if (epoch === this.fileListEpoch && revision === this.revision) this.fileBusy = false;
    }
  }

  private joinPath(parent: string, name: string): string {
    if (parent.endsWith('/')) return parent + name;
    return joinedFilePath(parent,name);
  }

  async openEntry(entry: WorkspaceEntry): Promise<void> {
    if (entry.directory) await this.openPath(entry.path);
    else await this.openFile(entry.path,entry.name);
  }

  openParent(): void {
    const parent=parentFolder(this.currentPath);
    if (parent) this.openPath(parent);
  }

  async openFile(path: string, name: string): Promise<void> {
    if (!this.current() || !this.visible) return;
    const revision = this.revision, selected = path;
    this.fileError = '';
    this.selectedFile = path;
    this.selectedFileName = name;
    this.selectedFileSize = 0; this.selectedFileMime = '';
    this.selectedFileText = ''; this.selectedFileIsText = false;
    this.fileOffset = 0; this.fileNext = 0; this.fileHasMore = false;
    this.fileSheetOpen = true;
    await this.readFilePage(selected, 0, revision, false);
  }

  private async readFilePage(path: string, offset: number, revision: number, append: boolean): Promise<void> {
    const epoch = ++this.fileReadEpoch;
    this.fileReadBusy = true;
    try {
      const result = await this.request(this.requestPath('/file?' + query([
        'path=' + segment(path), 'cwd=' + segment(this.cwd), 'offset=' + String(offset),
        'stream=' + (this.model?.features.includes('file_stream') ? '1' : '')
      ])));
      if (epoch !== this.fileReadEpoch || revision !== this.revision || !this.current() || !this.fileSheetOpen || this.selectedFile !== path) return;
      this.selectedFileName = Wire.text(result, 'name', this.selectedFileName);
      this.selectedFileSize = Wire.number(result, 'size'); this.selectedFileMime = Wire.text(result, 'mime');
      const textValue = result['text'];
      this.selectedFileIsText = typeof textValue === 'string';
      if (typeof textValue === 'string') this.selectedFileText = append ? this.selectedFileText + textValue : textValue;
      this.fileOffset = Wire.number(result, 'offset', offset);
      this.fileNext = Wire.number(result, 'next');
      this.fileHasMore = this.fileNext > 0 && Wire.bool(result, 'truncated', true);
      this.fileTruncated = Wire.bool(result, 'truncated');
      if (!this.selectedFileIsText && !Wire.has(result, 'stream') && Wire.has(result, 'data')) this.selectedFileIsText = false;
    } catch (error) {
      if (epoch === this.fileReadEpoch && revision === this.revision && this.current() && this.fileSheetOpen) this.fileError = this.message(error as Error);
    } finally {
      if (epoch === this.fileReadEpoch && revision === this.revision) this.fileReadBusy = false;
    }
  }

  loadMoreText(): void {
    if (!this.selectedFile || !this.fileHasMore || this.fileNext <= 0) return;
    const path = this.selectedFile, offset = this.fileNext, revision = this.revision;
    this.fileHasMore = false;
    this.readFilePage(path, offset, revision, true);
  }

  closeFile(): void { this.fileSheetOpen = false; this.fileReadEpoch++; this.fileReadBusy = false; }

  async uploadFile(name: string, bytes: ArrayBuffer): Promise<void> {
    if (!this.canUploadFiles()) throw new Error('write_unavailable');
    if (bytes.byteLength <= 0 || bytes.byteLength > TRANSFER_LIMIT) throw new Error('transfer_limit');
    const revision = this.revision, path = this.currentPath || this.cwd;
    const url = this.requestPath('/files?' + query([
      'name=' + segment(name), 'dest=dir', 'dir=' + segment(path), 'cwd=' + segment(this.cwd)
    ]));
    const model = this.model;
    if (!model) throw new Error('offline');
    const target = this.ownerTarget, tab = this.tab;
    const admit = (): boolean => revision === this.revision && target === this.ownerTarget && tab === this.tab &&
      this.visible && this.connectionReady && this.current() && model.connected;
    await model.repo().upload(url, bytes, admit);
    if (revision !== this.revision || !this.current()) return;
    await this.loadFiles();
  }

  async renameFile(path: string, name: string): Promise<boolean> {
    const nextName = name.trim();
    if (!nextName || nextName === '.' || nextName === '..' || /[\/\u0000]/.test(nextName)) throw new Error('invalid_name');
    if (!this.canEditFiles() || !path) throw new Error('write_unavailable');
    const revision = this.revision, target = this.ownerTarget, generation = this.ownerGeneration, url = this.requestPath('/files/rename');
    this.fileWriteBusy = true; this.fileWriteState = 'busy'; this.fileWriteError = '';
    try {
      await this.request(url, 'POST', Wire.payload(['path', 'cwd', 'name'], [path, this.cwd, nextName]));
      if (this.sameOwner(target, generation)) { this.fileWriteState = 'acknowledged'; this.fileWriteError = ''; }
      this.loadFiles().catch(() => {});
      return true;
    } catch (error) {
      if (this.sameOwner(target, generation)) {
        const failure = error as HttpFailure;
        this.fileWriteState = failure instanceof HttpFailure && failure.resultUnknown ? (failure.resultPending ? 'pending' : 'unknown') : 'failed';
        this.fileWriteError = this.message(error as Error);
      }
      return false;
    } finally { if (revision === this.revision) this.fileWriteBusy = false; }
  }

  async moveFiles(paths: string[], directory: string, conflict: string = ''): Promise<boolean> {
    const target = directory.trim();
    if (!this.canEditFiles() || !paths.length || !target) throw new Error('write_unavailable');
    if (conflict && !['keep', 'replace'].includes(conflict)) throw new Error('invalid_conflict');
    const revision = this.revision, targetSnapshot = this.ownerTarget, generation = this.ownerGeneration, url = this.requestPath('/files/move');
    const fields = ['paths', 'cwd', 'dir'];
    const values: Object[] = [paths as Object, this.cwd, target];
    if (conflict) { fields.push('conflict'); values.push(conflict); }
    this.fileWriteBusy = true; this.fileWriteState = 'busy'; this.fileWriteError = ''; this.fileConflictName = '';
    try {
      await this.request(url, 'POST', Wire.payload(fields, values));
      if (this.sameOwner(targetSnapshot, generation)) { this.fileWriteState = 'acknowledged'; this.fileWriteError = ''; }
      this.loadFiles().catch(() => {});
      return true;
    } catch (error) {
      if (this.sameOwner(targetSnapshot, generation)) {
        const failure = error as HttpFailure;
        if (failure instanceof HttpFailure && failure.status === 409 && failure.code === 'exists' && !conflict) {
          this.fileConflictName = paths[0].split('/').pop() || paths[0];
          this.fileWriteState = 'conflict';
        } else {
          this.fileWriteState = failure instanceof HttpFailure && failure.resultUnknown ? (failure.resultPending ? 'pending' : 'unknown') : 'failed';
          this.fileWriteError = this.message(error as Error);
        }
      }
      return false;
    } finally { if (revision === this.revision) this.fileWriteBusy = false; }
  }

  async trashFiles(paths: string[]): Promise<boolean> {
    if (!this.canEditFiles() || !paths.length) throw new Error('write_unavailable');
    const revision = this.revision, target = this.ownerTarget, generation = this.ownerGeneration, url = this.requestPath('/files/trash');
    this.fileWriteBusy = true; this.fileWriteState = 'busy'; this.fileWriteError = ''; this.fileConflictName = '';
    try {
      const result = await this.request(url, 'POST', Wire.payload(['paths', 'cwd'], [paths as Object, this.cwd]));
      if (this.sameOwner(target, generation)) {
        this.trashUndo = Wire.rows(result, 'trashed').map((item: Record<string, Object>) => {
          const row = new WorkspaceTrashItem(); row.path = Wire.text(item, 'path'); row.trashId = Wire.text(item, 'trash_id'); return row;
        });
        this.fileWriteState = 'acknowledged'; this.fileWriteError = '';
      }
      this.loadFiles().catch(() => {});
      return true;
    } catch (error) {
      if (this.sameOwner(target, generation)) {
        const failure = error as HttpFailure;
        this.fileWriteState = failure instanceof HttpFailure && failure.resultUnknown ? (failure.resultPending ? 'pending' : 'unknown') : 'failed';
        this.fileWriteError = this.message(error as Error);
      }
      return false;
    } finally { if (revision === this.revision) this.fileWriteBusy = false; }
  }

  async restoreTrash(): Promise<boolean> {
    const ids = this.trashUndo.map((item: WorkspaceTrashItem) => item.trashId).filter((value: string) => !!value);
    if (!this.canEditFiles() || !ids.length) return false;
    const revision = this.revision, target = this.ownerTarget, generation = this.ownerGeneration, url = this.requestPath('/files/restore');
    this.fileWriteBusy = true; this.fileWriteState = 'busy'; this.fileWriteError = '';
    try {
      await this.request(url, 'POST', Wire.payload(['trash_ids'], [ids as Object]));
      if (this.sameOwner(target, generation)) {
        this.trashUndo = []; this.fileWriteState = 'acknowledged'; this.fileWriteError = '';
      }
      this.loadFiles().catch(() => {});
      return true;
    } catch (error) {
      if (this.sameOwner(target, generation)) {
        const failure = error as HttpFailure;
        this.fileWriteState = failure instanceof HttpFailure && failure.resultUnknown ? (failure.resultPending ? 'pending' : 'unknown') : 'failed';
        this.fileWriteError = this.message(error as Error);
      }
      return false;
    } finally { if (revision === this.revision) this.fileWriteBusy = false; }
  }

  async downloadFile(): Promise<DownloadedWorkspaceFile> {
    if (!this.current() || !this.selectedFile) throw new Error('file_not_selected');
    if (!this.feature('file_download')) throw new Error('preview_unavailable');
    const path = this.selectedFile, revision = this.revision;
    const model = this.model;
    if (!model) throw new Error('offline');
    const result = await model.repo().transport.requestBytes(this.requestPath('/file/download?' + query([
      'path=' + segment(path), 'cwd=' + segment(this.cwd)
    ])), 'GET');
    if (revision !== this.revision || !this.current() || this.selectedFile !== path) throw new Error('scope_changed');
    if (result.data.byteLength > TRANSFER_LIMIT) throw new Error('transfer_limit');
    return { data: result.data, name: this.selectedFileName || path.split('/').pop() || 'download', contentType: result.contentType };
  }

  private applyGitStatus(result:Record<string,Object>, forcePr:boolean):void {
    if (Wire.text(this.gitStatus,'branch')!==Wire.text(result,'branch') ||
        Wire.text(this.gitStatus,'root')!==Wire.text(result,'root') ||
        Wire.text(this.gitStatus,'upstream')!==Wire.text(result,'upstream') || Wire.bool(result,'gone')) {
      if (this.gitPullState==='failed') this.gitWriteError='';
      this.gitPullState='';
    }
    const first=Wire.rows(result,'commits'), head=Wire.text(Wire.object(result,'head'),'hash') || (first.length ? Wire.text(first[0],'hash') : '');
    const identity=JSON.stringify([Wire.text(result,'branch'),head,first.map((c:Record<string,Object>) => Wire.text(c,'hash'))]);
    if (identity !== this.gitFirst) {
      this.gitFirst=identity; this.gitHistoryEpoch++; this.gitHistoryBusy=false; this.gitHistoryError='';
      this.gitCommits=first; this.gitSkip=first.length; this.gitMoreCommits=first.length>=30;
    } else this.gitCommits=first.concat(this.gitCommits.slice(first.length));
    this.gitStatus = result;
    this.gitFiles=Wire.rows(result,'files').map((item:Record<string,Object>) => this.gitFile(item));
    if (Array.isArray(result['stashes'])) this.gitStashes=Wire.rows(result,'stashes');
    else if (!Wire.number(result,'stashes')) this.gitStashes=[];
    this.loadGitPullRequest(forcePr);
    this.sendFsWatch([], true);
  }

  async loadGit(forcePr:boolean=false): Promise<void> {
    if (!this.current() || !this.visible || this.tab !== 1) return;
    const revision = this.revision, epoch=++this.gitStatusEpoch;
    const current=():boolean => this.activeRevision(revision,this.ownerTarget,1) && epoch===this.gitStatusEpoch;
    this.gitBusy = true;
    this.gitError = '';
    try {
      const result = await this.request(this.requestPath('/git/status?cwd=' + segment(this.cwd)));
      if (!current()) return;
      this.applyGitStatus(result,forcePr);
    } catch (error) {
      if (current()) this.gitError = this.message(error as Error);
    } finally { if (current()) this.gitBusy = false; }
  }

  private gitFile(item:Record<string,Object>):WorkspaceGitFile {
    const file=new WorkspaceGitFile();
    file.path=Wire.text(item,'path'); file.originalPath=Wire.text(item,'orig_path');
    file.index=Wire.text(item,'index','.'); file.worktree=Wire.text(item,'worktree','.');
    file.added=Wire.number(item,'added'); file.deleted=Wire.number(item,'deleted'); file.binary=Wire.bool(item,'binary');
    return file;
  }

  gitStashCount():number {
    return typeof this.gitStatus['stashes']==='number' ? Wire.number(this.gitStatus,'stashes') : this.gitStashes.length;
  }

  async loadGitStashes():Promise<void> {
    const model=this.model;
    if (!model || !this.canWriteGit()) return;
    const rev=this.revision, target=this.ownerTarget, epoch=++this.gitStashEpoch;
    const live=():boolean => epoch===this.gitStashEpoch && this.activeRevision(rev,target,1);
    this.gitStashBusy=true; this.gitStashError='';
    try {
      // Stash enumeration is a read-only POST. A read timeout must never block later writes in the journal.
      const data=Wire.parse(await model.repo().transport.request(this.requestPath('/git/stash'),'POST',
        JSON.stringify(Wire.payload(['cwd','op'],[this.cwd,'list']))));
      if (live()) this.gitStashes=Wire.rows(data,'stashes');
    } catch (error) { if (live()) this.gitStashError=this.message(error as Error); }
    finally { if (live()) this.gitStashBusy=false; }
  }

  branchPushedForPr():boolean {
    return !!Wire.text(this.gitStatus,'branch') && !!Wire.text(this.gitStatus,'upstream') &&
      !Wire.bool(this.gitStatus,'gone') && Wire.number(this.gitStatus,'ahead')===0;
  }

  async loadGitPullRequest(force:boolean=false):Promise<void> {
    const branch=Wire.text(this.gitStatus,'branch');
    if (!this.current() || !this.visible || this.tab!==1 || !this.feature('git_write') || !Wire.bool(this.gitStatus,'is_repo') || !branch) {
      this.gitPrEpoch++; this.gitPrKey=''; this.gitPrAnswer={}; this.gitPrError=''; this.gitPrBusy=false; return;
    }
    const key=JSON.stringify([branch,Wire.text(this.gitStatus,'upstream'),Wire.bool(this.gitStatus,'gone'),
      Wire.number(this.gitStatus,'ahead'),Wire.text(Wire.object(this.gitStatus,'head'),'hash')]);
    if (this.gitPrKey===key && !force) return;
    if (this.gitPrKey!==key) this.gitPrAnswer={};
    const rev=this.revision, target=this.ownerTarget, epoch=++this.gitPrEpoch;
    const live=():boolean => this.activeRevision(rev,target,1) && epoch===this.gitPrEpoch && this.gitPrKey===key;
    this.gitPrKey=key; this.gitPrBusy=true; this.gitPrError='';
    try {
      const answer=await this.request(this.requestPath('/git/pr?cwd='+segment(this.cwd)));
      if (!live()) return;
      if (Wire.text(answer,'branch')!==branch) { this.gitPrKey=''; this.gitPrError='session_git_pr_read_failed'; }
      else this.gitPrAnswer=answer;
    } catch (error) { if (live()) this.gitPrError=this.message(error as Error); }
    finally { if (live()) this.gitPrBusy=false; }
  }

  createPullRequest(branch:string,title:string,description:string,base:string,draft:boolean):Promise<boolean> {
    if (!title.trim() || branch!==Wire.text(this.gitStatus,'branch') || !this.branchPushedForPr() ||
      Wire.text(this.gitPrAnswer,'branch')!==branch || Wire.number(Wire.object(this.gitPrAnswer,'pr'),'number')>0) return Promise.resolve(false);
    const body=Wire.payload(['cwd','title','body','draft'],[this.cwd,title.trim(),description,draft]);
    if (base.trim()) body['base']=base.trim();
    return this.gitMutation('pr',body);
  }

  async olderGitCommits():Promise<void> {
    if (!this.current() || !this.visible || this.tab!==1 || !this.gitMoreCommits || this.gitHistoryBusy) return;
    const revision=this.revision, target=this.ownerTarget, first=this.gitFirst, skip=this.gitSkip, epoch=++this.gitHistoryEpoch;
    const live=():boolean => this.activeRevision(revision,target,1) && first===this.gitFirst && epoch===this.gitHistoryEpoch;
    this.gitHistoryBusy=true; this.gitHistoryError='';
    try {
      const result=await this.request(this.requestPath('/git/log?cwd='+segment(this.cwd)+'&skip='+skip+'&limit=50'));
      if (!live()) return;
      if (!Array.isArray(result['commits'])) throw new Error('session_git_history_error');
      const page=Wire.rows(result,'commits'), seen=new Set<string>(this.gitCommits.map((c:Record<string,Object>) => Wire.text(c,'hash')));
      const distinct=page.filter((c:Record<string,Object>) => { const hash=Wire.text(c,'hash'); if (!hash || seen.has(hash)) return false; seen.add(hash); return true; });
      this.gitCommits=this.gitCommits.concat(distinct); this.gitSkip=skip+page.length;
      this.gitMoreCommits=page.length>0 && Wire.bool(result,'has_more',page.length===50);
    } catch (error) { if (live()) this.gitHistoryError=this.message(error as Error); }
    finally { if (live()) this.gitHistoryBusy=false; }
  }

  async showGitCommit(hash:string):Promise<void> {
    if (!this.current() || !this.visible || this.tab!==1 || !/^[0-9a-f]{7,64}$/i.test(hash) ||
      !this.gitCommits.some((c:Record<string,Object>) => Wire.text(c,'hash')===hash)) return;
    const rev=this.revision, target=this.ownerTarget, epoch=++this.gitShowEpoch;
    const live=():boolean => this.activeRevision(rev,target,1) && this.gitCommitHash===hash && epoch===this.gitShowEpoch;
    this.gitCommitHash=hash; this.gitShow={}; this.gitShowError=''; this.gitShowBusy=true;
    try {
      const result=await this.request(this.requestPath('/git/show?cwd='+segment(this.cwd)+'&hash='+segment(hash)));
      if (!live()) return;
      if (Wire.text(Wire.object(result,'commit'),'hash')!==hash) throw new Error('session_git_history_error');
      this.gitShow=result;
    } catch (error) { if (live()) this.gitShowError=this.message(error as Error); }
    finally { if (live()) this.gitShowBusy=false; }
  }
  closeGitShow():void {
    this.gitShowEpoch++; this.gitCommitHash=''; this.gitShow={}; this.gitShowBusy=false; this.gitShowError='';
  }

  async loadGitBranches(): Promise<void> {
    if (!this.current() || !this.visible || this.tab !== 1 || !this.feature('git_write')) return;
    const revision = this.revision;
    try {
      const result = await this.request(this.requestPath('/git/branches?cwd=' + segment(this.cwd)));
      if (revision !== this.revision || !this.current()) return;
      this.gitBranches = Wire.rows(result, 'branches').map((item: Record<string, Object>) => {
        const branch = new WorkspaceBranch(); branch.name = Wire.text(item, 'name'); branch.remote = Wire.bool(item, 'remote');
        branch.current = Wire.bool(item, 'current'); branch.upstream = Wire.text(item, 'upstream');
        branch.hash = Wire.text(item, 'hash'); branch.subject = Wire.text(item, 'subject'); branch.date = Wire.text(item, 'date');
        return branch;
      });
    } catch (error) {
      if (revision === this.revision && this.current()) {
        if (this.hasGitPullIssue()) this.gitPullState='';
        this.gitWriteError = this.message(error as Error);
      }
    }
  }

  private async gitMutation(operation: string, body: Record<string, Object>): Promise<boolean> {
    if (!['stage','commit','push','switch','stash','pr','pull'].includes(operation)) return false;
    if (!(operation==='pull' ? this.canPullGit() : this.canWriteGit())) return false;
    const revision=this.revision, target=this.ownerTarget, generation=this.ownerGeneration;
    const path=this.requestPath('/git/'+operation), branch=Wire.text(this.gitStatus,'branch');
    const upstream=Wire.text(this.gitStatus,'upstream'), root=Wire.text(this.gitStatus,'root');
    const samePullTarget=():boolean => branch===Wire.text(this.gitStatus,'branch') && root===Wire.text(this.gitStatus,'root') &&
      upstream===Wire.text(this.gitStatus,'upstream') && !Wire.bool(this.gitStatus,'gone');
    this.gitWriteBusy=true; this.gitWriteState='busy'; this.gitWriteError='';
    this.gitPullState=operation==='pull' ? 'pulling' : '';
    try {
      let result:Record<string,Object>;
      if (operation==='pull') {
        // A dirty snapshot may already be stale after the user cleans the checkout on the host.
        // Re-read it before sending the write, preserving the current branch and session.
        if (this.gitFiles.length>0) {
          const status=await this.request(this.requestPath('/git/status?cwd='+segment(this.cwd)));
          if (!this.activeRevision(revision,target,1) || !samePullTarget()) throw new Error('request_updated');
          this.gitStatusEpoch++; this.gitBusy=false;
          this.applyGitStatus(status,false);
          if (!samePullTarget()) throw new Error('request_updated');
          if (this.gitFiles.length>0) {
            this.gitPullState='failed'; this.gitWriteState='failed'; this.gitWriteError='session_git_pull_uncommitted_changes';
            return false;
          }
        }
        result=await this.request(path,'POST',body,() => samePullTarget() && this.machineTrustedOnline() && this.sessionWritable(),
          (value:Record<string,Object>) => {
            const status=Wire.object(value,'status');
            return typeof value['updated']==='boolean' && Wire.bool(status,'is_repo') && Wire.text(status,'branch')===branch &&
              (!root || Wire.text(status,'root')===root);
          });
      } else if (operation==='pr') {
        result=await this.request(path,'POST',body,() => this.branchPushedForPr() && Wire.text(this.gitStatus,'branch')===branch,
          (value:Record<string,Object>) => Wire.text(value,'branch')===branch && Wire.number(Wire.object(value,'pr'),'number')>0);
      } else {
        result=await this.request(path,'POST',body);
      }
      const returned=Wire.object(result,'status');
      if (operation==='pull' && this.activeRevision(revision,target,1) && samePullTarget()) {
        this.gitStatusEpoch++; this.gitBusy=false; this.gitError='';
        this.applyGitStatus(returned,true);
        this.gitPullState=Wire.bool(result,'updated') ? 'updated' : 'current';
      }
      if (operation==='stage' && this.activeRevision(revision,target,1) && Wire.bool(returned,'is_repo') && Array.isArray(returned['files'])) {
        this.gitFiles=Wire.rows(returned,'files').map((file:Record<string,Object>) => this.gitFile(file));
      }
      if (operation==='pr' && this.sameOwner(target,generation) && Wire.text(result,'branch')===Wire.text(this.gitStatus,'branch')) {
        this.gitPrEpoch++; this.gitPrKey=''; this.gitPrBusy=false; this.gitPrAnswer=result;
      }
      if (this.sameOwner(target, generation)) { this.gitWriteState = 'acknowledged'; this.gitWriteError = ''; }
      // The POST is durably acknowledged before this best-effort read; a read failure cannot make the write retryable.
      if (operation!=='pull') this.loadGit().catch(() => {});
      return true;
    } catch (error) {
      if (this.sameOwner(target, generation)) {
        this.gitPullState='';
        const failure = error as HttpFailure;
        this.gitWriteState = failure instanceof HttpFailure && failure.resultUnknown ? (failure.resultPending ? 'pending' : 'unknown') : 'failed';
        const code=failure instanceof HttpFailure ? failure.code : '';
        const refusal=operation==='pull' && ['no_directory','not_a_repository','detached_head','no_upstream','uncommitted_changes','not_fast_forward'].includes(code);
        this.gitWriteError=refusal ? 'session_git_pull_'+code : this.message(error as Error);
        if (operation==='pull') {
          if (this.activeRevision(revision,target,1) && samePullTarget()) this.gitPullState=this.gitWriteState;
          else if (this.gitWriteState==='failed') this.gitWriteError='';
        }
      }
      return false;
    } finally {
      if (revision===this.revision) {
        this.gitWriteBusy=false;
        if (this.gitPullState==='pulling') this.gitPullState='';
      }
    }
  }

  pullGit():Promise<boolean> { return this.gitMutation('pull',Wire.payload(['cwd'],[this.cwd])); }

  stageGitFile(file: WorkspaceGitFile, unstage: boolean = false): Promise<boolean> {
    if (!file.path) return Promise.resolve(false);
    const body = Wire.payload(['cwd', 'paths'], [this.cwd, [file.path] as Object]);
    if (unstage) body['unstage'] = true;
    return this.gitMutation('stage', body);
  }

  allFilesStaged():boolean {
    return this.gitFiles.length>0 && this.gitFiles.every((file:WorkspaceGitFile) => file.staged && !file.unstaged);
  }

  toggleAllGitFiles():Promise<boolean> {
    const paths=this.gitFiles.map((file:WorkspaceGitFile) => file.path).filter((path:string) => !!path);
    if (!paths.length || paths.length!==this.gitFiles.length) return Promise.resolve(false);
    const body=Wire.payload(['cwd','paths'],[this.cwd,paths as Object]);
    if (this.allFilesStaged()) body['unstage']=true;
    return this.gitMutation('stage',body);
  }

  commitGit(message: string): Promise<boolean> {
    const value = message.trim(); if (!value) return Promise.resolve(false);
    return this.gitMutation('commit', Wire.payload(['cwd', 'message'], [this.cwd, value]));
  }

  pushGit(setUpstream: boolean = false): Promise<boolean> {
    const body = Wire.payload(['cwd'], [this.cwd]); if (setUpstream) body['set_upstream'] = true;
    return this.gitMutation('push', body);
  }

  switchGit(branch: string, create: boolean = false, from: string = ''): Promise<boolean> {
    if (!branch.trim()) return Promise.resolve(false);
    const fields = ['cwd', 'branch'], values: Object[] = [this.cwd, branch.trim()];
    if (create) { fields.push('create'); values.push(true); }
    if (from) { fields.push('from'); values.push(from); }
    return this.gitMutation('switch', Wire.payload(fields, values));
  }

  stashGit(operation: string, oid: string = '', message: string = '', untracked: boolean = false): Promise<boolean> {
    if (!['push', 'list', 'apply', 'pop', 'drop'].includes(operation)) return Promise.resolve(false);
    if (['apply', 'pop', 'drop'].includes(operation) && !/^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(oid)) return Promise.resolve(false);
    const fields = ['cwd', 'op'], values: Object[] = [this.cwd, operation];
    if (oid) { fields.push('oid'); values.push(oid); }
    if (message) { fields.push('message'); values.push(message); }
    if (untracked) { fields.push('untracked'); values.push(true); }
    return this.gitMutation('stash', Wire.payload(fields, values));
  }

  async openGitDiff(file:WorkspaceGitFile, staged?:boolean):Promise<void> {
    this.gitDiffPath=file.path;
    this.gitDiffStaged=staged === undefined ? file.staged && !file.unstaged : staged && file.staged;
    await this.loadGitDiff();
  }

  async loadGitDiff():Promise<void> {
    if (!this.gitDiffPath || !this.current() || !this.visible || this.tab!==1) return;
    const revision=this.revision, target=this.ownerTarget, path=this.gitDiffPath, staged=this.gitDiffStaged, epoch=++this.gitDiffEpoch;
    const live=():boolean => this.activeRevision(revision,target,1) && this.gitDiffPath===path && this.gitDiffStaged===staged && this.gitDiffEpoch===epoch;
    this.gitDiffError=''; this.gitDiff=''; this.gitDiffTruncated=false; this.gitDiffBusy=true;
    try {
      const result=await this.request(this.requestPath('/git/diff?'+query(['cwd='+segment(this.cwd),'path='+segment(path),'staged='+(staged?'1':'0')])));
      if (!live()) return;
      this.gitDiff=Wire.text(result,'diff'); this.gitDiffTruncated=Wire.bool(result,'truncated');
    } catch (error) { if (live()) this.gitDiffError=this.message(error as Error); }
    finally { if (live()) this.gitDiffBusy=false; }
  }

  closeGitDiff():void { this.gitDiffEpoch++; this.gitDiffPath=''; this.gitDiff=''; this.gitDiffTruncated=false; this.gitDiffError=''; this.gitDiffBusy=false; }

  private canReadScreen(): boolean {
    if (!this.current() || !this.visible || this.tab !== 3 || !this.machineTrustedOnline()) return false;
    const data = this.model?.selected.data();
    const caps = data ? Wire.object(data, 'capabilities') : Wire.payload([], []);
    const sourced = !!data && !!Wire.text(data, 'source_kind');
    return !sourced || Wire.bool(caps, 'terminal');
  }

  private startScreenPoll(): void {
    this.screenControlRequested = false;
    this.screenError = '';
    if (!this.canReadScreen()) { this.screenError = 'screen_unavailable'; return; }
    this.pollScreen();
  }

  private pollScreen(): void {
    if (!this.canReadScreen() || this.screenBusy) return;
    const revision = this.revision, target = this.ownerTarget;
    this.screenBusy = true;
    this.request('/api/v1/sessions/' + segment(this.machineId) + '/' + segment(this.sessionId) + '/screen?lines=60&source=visible')
      .then((result: Record<string, Object>) => {
        if (!this.activeRevision(revision, target, 3)) return;
        this.screenText = Wire.text(result, 'text'); this.screenRevision++; this.screenError = '';
      }).catch((error: Error) => {
        if (this.activeRevision(revision, target, 3)) this.screenError = this.message(error);
      }).finally(() => {
        if (revision !== this.revision) return;
        this.screenBusy = false;
        if (this.visible && this.tab === 3 && this.current()) this.screenPollTimer = setTimeout(() => this.pollScreen(), 1200);
      });
  }

  refreshScreen(): void {
    if (!this.screenBusy) { this.screenError = ''; this.pollScreen(); }
  }

  private stopScreen(clear: boolean): void {
    if (this.screenPollTimer !== -1) clearTimeout(this.screenPollTimer);
    this.screenPollTimer = -1; this.screenBusy = false; this.screenKeyBusy = false; this.screenControlRequested = false;
    if (clear) this.screenText = '';
  }

  enableScreenControl(): boolean {
    if (!this.canControlScreen()) return false;
    this.screenControlRequested = true;
    return true;
  }

  revokeScreenControl(): void { this.screenControlRequested = false; }

  async sendScreenKeys(keys: string[], text: string, expectedRevision: number): Promise<boolean> {
    if (!this.screenControlRequested || this.screenKeyBusy || !this.canControlScreen() || expectedRevision !== this.screenRevision || (!keys.length && !text)) return false;
    const safeKeys = keys.filter((key: string) => ['enter', 'esc', 'tab', 'backspace', 'up', 'down', 'left', 'right', 'ctrl+c', 'ctrl+d'].includes(key));
    if (keys.length && safeKeys.length !== keys.length) return false;
    const revision = this.revision, screenRevision = this.screenRevision;
    this.screenKeyBusy = true;
    try {
      await this.request(this.screenKeysPath(), 'POST', Wire.payload(['keys', 'text'], [safeKeys as Object, text]));
      if (revision === this.revision && this.current() && screenRevision === this.screenRevision) this.pollScreen();
      return true;
    } catch (error) {
      if (revision === this.revision && this.current()) {
        this.screenError = this.message(error as Error);
        if (this.hasPending(this.screenKeysPath())) this.screenControlRequested = false;
      }
      return false;
    } finally { if (revision === this.revision) this.screenKeyBusy = false; }
  }

  async loadPreviewServers(): Promise<void> {
    if (!this.current() || !this.visible || this.tab !== 4) return;
    if (!this.feature('preview')) { this.previewState = 'unsupported'; this.previewError = 'preview_unavailable'; return; }
    const revision = this.revision, target = this.ownerTarget;
    try {
      const result = await this.request(this.requestPath('/preview/servers?cwd=' + segment(this.cwd)));
      if (!this.activeRevision(revision, target, 4)) return;
      this.previewServers = Wire.rows(result, 'servers').map((item: Record<string, Object>) => {
        const server = new WorkspacePreviewServer(); server.name = Wire.text(item, 'name'); server.port = Wire.number(item, 'port');
        server.url = Wire.text(item, 'url'); server.command = Wire.strings(item, 'command'); server.env = Wire.object(item, 'env');
        server.cwd = Wire.text(item, 'cwd'); server.listening = Wire.bool(item, 'listening');
        server.title = Wire.text(item, 'title'); server.problem = Wire.text(item, 'problem'); return server;
      });
      this.previewError = Wire.text(result, 'error');
      if (this.previewState === 'starting') {
        const started = this.previewServers.find((server: WorkspacePreviewServer) => server.name === this.previewStartingName);
        if (started?.listening && started.port > 0) {
          this.previewStartingName = '';
          if (this.previewPollTimer !== -1) clearTimeout(this.previewPollTimer);
          this.previewPollTimer = -1;
          await this.openPreview(started.port, this.pathInPreviewUrl(started.url), started.name);
          return;
        }
        if (Date.now() - this.previewStartedAt >= 90000) {
          this.previewState = 'failed'; this.previewError = 'preview_start_slow'; this.previewStartingName = '';
          return;
        }
        this.schedulePreviewPoll(revision, target);
      }
    } catch (error) {
      if (this.activeRevision(revision, target, 4)) this.previewError = this.message(error as Error);
      if (this.previewState === 'starting' && Date.now() - this.previewStartedAt < 90000) this.schedulePreviewPoll(revision, target);
    }
  }

  private schedulePreviewPoll(revision: number, target: string): void {
    if (this.previewPollTimer !== -1) clearTimeout(this.previewPollTimer);
    this.previewPollTimer = setTimeout(() => {
      this.previewPollTimer = -1;
      if (this.activeRevision(revision, target, 4)) this.loadPreviewServers();
    }, 1500);
  }

  private pathInPreviewUrl(value: string): string {
    if (!value) return '/';
    const marker = value.indexOf('://');
    const slash = marker >= 0 ? value.indexOf('/', marker + 3) : value.indexOf('/');
    const path = slash >= 0 ? value.slice(slash) : '/';
    return path.startsWith('/') && !/[\\\r\n]/.test(path) ? path : '/';
  }

  async startPreview(name: string): Promise<boolean> {
    if (!this.canStartPreview() || !this.feature('preview')) return false;
    const server = this.previewServers.find((item: WorkspacePreviewServer) => item.name === name);
    const data = this.model?.selected.data();
    const workspace = data ? Wire.text(data, 'workspace_id') : '';
    if (!server || !server.command.length || !workspace) return false;
    const revision = this.revision, target = this.ownerTarget;
    this.previewState = 'starting'; this.previewStartingName = name; this.previewStartedAt = Date.now(); this.previewError = '';
    const body = Wire.payload(['cwd', 'name', 'workspace', 'kind'], [this.cwd, name, workspace,
      data ? Wire.text(data, 'space_kind', Wire.text(data, 'host')) : '']);
    try {
      await this.request(this.requestPath('/preview/start'), 'POST', body);
      if (!this.activeRevision(revision, target, 4)) return true;
      this.schedulePreviewPoll(revision, target);
      return true;
    } catch (error) {
      if (this.activeRevision(revision, target, 4)) {
        const failure = error as HttpFailure;
        this.previewState = failure instanceof HttpFailure && failure.resultUnknown ? (failure.resultPending ? 'pending' : 'unknown') : 'failed';
        this.previewError = this.message(error as Error); this.previewStartingName = '';
      }
      return false;
    }
  }

  async openPreview(port: number, path: string, name: string): Promise<boolean> {
    if (!this.canUsePreview() || !Number.isInteger(port) || port < 1 || port > 65535) return false;
    this.closePreviewStream();
    const revision = this.revision, target = this.ownerTarget;
    this.previewPort = port; this.previewPath = path.startsWith('/') && !/[\\\r\n]/.test(path) ? path : '/';
    this.previewName = name; this.previewFrame = undefined; this.previewSeq = 0; this.previewExternalUrl = '';
    this.previewState = 'opening'; this.previewError = ''; this.previewMessage = '';
    try {
      await this.request(this.requestPath('/ports/' + String(port) + '/open'), 'POST');
      if (!this.activeRevision(revision, target, 4)) return false;
      const streamPath = this.requestPath('/preview/connect?port=' + String(port) + '&path=' + segment(this.previewPath) + '&w=390&h=844&dpr=1&mobile=1');
      const model = this.model;
      if (!model) throw new Error('offline');
      const child = model.repo().transport.stream(streamPath);
      this.previewTransport = child;
      this.previewState = 'connecting';
      await child.connect((payload: string) => {
        if (this.activeRevision(revision, target, 4) && this.previewTransport === child) this.receivePreviewMessage(payload);
      }, (state: string) => {
        if (!this.activeRevision(revision, target, 4) || this.previewTransport !== child) return;
        if (state === 'ready') this.previewState = 'live';
        else if (state === 'offline' || state === 'closed') this.previewState = 'lost';
      }, (payload: ArrayBuffer) => {
        if (this.activeRevision(revision, target, 4) && this.previewTransport === child) this.receivePreviewFrame(payload);
      });
      if (this.activeRevision(revision, target, 4)) this.previewState = 'live';
      else child.close();
      return true;
    } catch (error) {
      if (this.activeRevision(revision, target, 4)) { this.previewState = 'failed'; this.previewError = this.message(error as Error); }
      this.closePreviewStream();
      return false;
    }
  }

  private receivePreviewMessage(raw: string): void {
    let event: Record<string, Object>;
    try { event = Wire.parse(raw); } catch (_) { return; }
    const type = Wire.text(event, 't');
    if (type === 'ready') { this.previewState = 'live'; this.previewMessage = Wire.text(event, 'browser'); }
    else if (type === 'page') {
      this.previewMessage = Wire.text(event, 'title', this.previewName);
      this.previewPath = this.pathInPreviewUrl(Wire.text(event, 'url'));
    }
    else if (type === 'external') this.previewExternalUrl = Wire.text(event, 'url');
    else if (type === 'error' || type === 'failed' || type === 'crashed' || type === 'closed') {
      this.previewState = type === 'closed' ? 'closed' : 'failed';
      this.previewError = Wire.text(event, 'error', Wire.text(event, 'code', type));
    }
  }

  private receivePreviewFrame(buffer: ArrayBuffer): void {
    if (buffer.byteLength < 9 || buffer.byteLength > 16 * 1024 * 1024) return;
    const view = new DataView(buffer);
    if (view.getUint8(0) !== 102) return;
    const seq = view.getUint32(1), width = view.getUint16(5), height = view.getUint16(7);
    if (!seq || seq <= this.previewSeq || width <= 0 || height <= 0 || width * height > 40000000) return;
    const frame = new WorkspacePreviewFrame(); frame.seq = seq; frame.width = width; frame.height = height; frame.data = buffer.slice(9);
    this.previewSeq = seq; this.previewFrame = frame; this.previewState = 'live';
  }

  acknowledgePreviewFrame(seq: number): void {
    if (!this.canUsePreview() || this.previewState !== 'live' || seq !== this.previewSeq || !this.previewTransport) return;
    const body = Wire.payload(['t', 'seq'], ['ack', seq]);
    this.previewTransport.send(JSON.stringify(body));
  }

  sendPreviewAction(body: Record<string, Object>, revision: number, target: string): boolean {
    if (!this.activeRevision(revision, target, 4) || this.previewState !== 'live' || !this.previewTransport) return false;
    return this.previewTransport.send(JSON.stringify(body));
  }

  sendPreviewFrameAction(body: Record<string, Object>, seq: number, revision: number, target: string): boolean {
    if (!seq || seq !== this.previewSeq || !this.previewFrame || this.previewFrame.seq !== seq) return false;
    return this.sendPreviewAction(body, revision, target);
  }

  private closePreviewStream(): void {
    const stream = this.previewTransport; this.previewTransport = undefined;
    if (stream) stream.close();
  }

  closePreview(): void {
    if (this.previewPollTimer !== -1) clearTimeout(this.previewPollTimer);
    this.previewPollTimer = -1; this.previewStartingName = '';
    this.closePreviewStream(); this.previewFrame = undefined; this.previewExternalUrl = '';
    if (this.previewState !== 'unsupported') this.previewState = 'idle';
  }

  canControlTerminal(): boolean {
    if (!this.current() || !this.visible || this.tab !== 2 || !this.feature('terminal_control') ||
      !this.machineTrustedOnline() || !this.sessionWritable()) return false;
    const data = this.model?.selected.data();
    const caps = data ? Wire.object(data, 'capabilities') : Wire.payload([], []);
    return !data || !Wire.text(data, 'source_kind') || Wire.bool(caps, 'terminal');
  }

  private startTerminal(continuing: boolean): void {
    if (!this.current() || !this.visible || this.tab !== 2) return;
    this.terminalError = '';
    if (!this.model?.connected) return;
    if (!this.model.features.includes('terminal_stream')) { this.beginTerminalPoll(); return; }
    this.sendTerminalSubscribe(continuing, false);
  }

  setTerminalControl(on: boolean): void {
    if (!this.current() || !this.visible || this.tab !== 2) return;
    if (on && !this.canControlTerminal()) {
      this.terminalControlState = 'unsupported';
      return;
    }
    this.terminalControlRequested = on;
    if (on) {
      this.terminalControlState = 'starting';
      this.sendTerminalSubscribe(false, false);
    }
    else {
      this.terminalControlState = 'off';
      this.sendTerminalSubscribe(false, false);
    }
  }

  takeOverTerminal(): void {
    if (!this.canControlTerminal()) return;
    this.terminalControlRequested = true;
    this.terminalControlState = 'starting';
    this.sendTerminalSubscribe(false, true);
  }

  resizeTerminal(cols: number, rows: number): void {
    if (!Number.isFinite(cols) || !Number.isFinite(rows)) return;
    const nextCols = Math.max(TERMINAL_COLS_MIN, Math.min(TERMINAL_COLS_MAX, Math.floor(cols)));
    const nextRows = Math.max(TERMINAL_ROWS_MIN, Math.min(TERMINAL_ROWS_MAX, Math.floor(rows)));
    if (nextCols === this.terminalCols && nextRows === this.terminalRows) return;
    this.terminalCols = nextCols; this.terminalRows = nextRows;
    if (this.terminalControlRequested && ['active', 'starting'].includes(this.terminalControlState)) {
      const control = Wire.payload(['cols', 'rows'], [nextCols, nextRows]);
      const body = Wire.payload(['type', 'machine_id', 'session_id', 'control'], ['terminal_input', this.machineId, this.sessionId, control]);
      this.sendSocket(body);
    }
  }

  sendTerminalText(text: string): void {
    if (this.terminalControlState !== 'active' || !this.terminalControlRequested || !text ||
      !this.current() || !this.visible || this.tab !== 2 || !this.model?.connected || !this.connectionReady) return;
    let terminalBytes = text;
    if (/\n/.test(text)) terminalBytes = '\u001b[200~' + text.replace(/\r\n/g, '\n').replace(/\n/g, '\r') + '\u001b[201~';
    this.sendTerminalBytes(terminalBytes);
  }

  private sendTerminalBytes(text: string): void {
    if (this.terminalControlState !== 'active' || !this.terminalControlRequested || !text ||
      !this.current() || !this.visible || this.tab !== 2 || !this.model?.connected || !this.connectionReady) return;
    const body = Wire.payload(['type', 'machine_id', 'session_id', 'text'], ['terminal_input', this.machineId, this.sessionId, text]);
    this.sendSocket(body);
  }

  sendTerminalKey(key: string): void {
    let text: string = '';
    switch (key) {
      case 'enter': text = '\r'; break;
      case 'tab': text = '\t'; break;
      case 'escape': text = '\u001b'; break;
      case 'backspace': text = '\u007f'; break;
      case 'up': text = '\u001b[A'; break;
      case 'down': text = '\u001b[B'; break;
      case 'right': text = '\u001b[C'; break;
      case 'left': text = '\u001b[D'; break;
      case 'ctrl_c': text = '\u0003'; break;
      case 'ctrl_d': text = '\u0004'; break;
      default: break;
    }
    if (text) this.sendTerminalBytes(text);
  }

  private sendTerminalSubscribe(continuing: boolean, takeover: boolean): void {
    if (!this.current() || !this.visible || this.tab !== 2 || !this.model?.connected) return;
    const fields: string[] = ['type', 'machine_id', 'session_id'];
    const values: Object[] = ['terminal_subscribe', this.machineId, this.sessionId];
    if (this.terminalControlRequested) {
      const control = Wire.payload(['cols', 'rows'], [this.terminalCols, this.terminalRows]);
      fields.push('control'); values.push(control);
    }
    if (takeover) { fields.push('takeover'); values.push(true); }
    if (continuing && this.terminalEpoch) {
      fields.push('epoch'); values.push(this.terminalEpoch);
      fields.push('after_seq'); values.push(this.terminalSeq);
    }
    this.terminalSubscribed = true;
    this.sendSocket(Wire.payload(fields, values));
  }

  private sendSocket(body: Record<string, Object>): void {
    if (!this.current() || !this.model?.connected || !this.connectionReady) { this.terminalError = 'offline'; return; }
    try { this.model.repo().transport.send(JSON.stringify(body)); }
    catch (error) { this.terminalError = this.message(error as Error); }
  }

  private releaseTerminal(send: boolean): void {
    if (this.terminalPollTimer !== -1) clearTimeout(this.terminalPollTimer);
    this.terminalPollTimer = -1;
    if (send && this.terminalSubscribed) {
      const body = Wire.payload(['type', 'machine_id', 'session_id'], ['terminal_unsubscribe', this.machineId, this.sessionId]);
      this.sendSocket(body);
    }
    this.terminalSubscribed = false;
    this.terminalControlRequested = false;
    this.terminalControlState = 'off';
  }

  private beginTerminalPoll(): void {
    this.terminalControlState = 'unsupported';
    this.pollTerminal();
  }

  private pollTerminal(): void {
    if (!this.visible || this.tab !== 2 || !this.current()) return;
    const revision = this.revision;
    this.request('/api/v1/sessions/' + segment(this.machineId) + '/' + segment(this.sessionId) + '/screen?lines=60&source=visible')
      .then((result: Record<string, Object>) => {
        if (revision !== this.revision || !this.visible || this.tab !== 2 || !this.current()) return;
        this.terminalText = Wire.text(result, 'text'); this.terminalError = '';
      }).catch((error: Error) => { if (revision === this.revision) this.terminalError = this.message(error); })
      .finally(() => {
        if (revision === this.revision && this.visible && this.tab === 2) this.terminalPollTimer = setTimeout(() => this.pollTerminal(), 1000);
      });
  }

  private receiveTerminal(event: Record<string, Object>): void {
    const error = Wire.text(event, 'error');
    if (error) {
      if (Wire.bool(event, 'unsupported')) this.beginTerminalPoll();
      else this.terminalError = error;
      return;
    }
    const epoch = Wire.text(event, 'epoch'), seq = Wire.number(event, 'seq'), data = Wire.text(event, 'data');
    const full = Wire.bool(event, 'full');
    const cols = Wire.number(event, 'cols', this.terminalGridCols), rows = Wire.number(event, 'rows', this.terminalGridRows);
    if (!epoch || seq <= 0 || typeof event['data'] !== 'string') return;
    if (!full && epoch === this.terminalEpoch && seq <= this.terminalSeq) return;
    if (!full && (epoch !== this.terminalEpoch || seq !== this.terminalSeq + 1)) {
      this.terminalEpoch = ''; this.terminalSeq = 0;
      this.sendTerminalSubscribe(false, false);
      return;
    }
    const bytes = decodedBase64(data);
    if (!bytes) { this.terminalError = 'invalid_terminal_frame'; return; }
    if (full || cols !== this.terminalGridCols || rows !== this.terminalGridRows) this.resetTerminalGrid(cols, rows);
    this.applyTerminalBytes(bytes);
    this.terminalEpoch = epoch; this.terminalSeq = seq;
    this.terminalText = this.renderTerminal();
    this.terminalError = '';
  }

  private resetTerminalGrid(cols: number, rows: number): void {
    this.terminalGridCols = Math.max(1, Math.min(TERMINAL_COLS_MAX, cols));
    this.terminalGridRows = Math.max(1, Math.min(TERMINAL_ROWS_MAX, rows));
    this.terminalGrid = blankGrid(this.terminalGridCols, this.terminalGridRows);
    this.terminalCursorColumn = 0; this.terminalCursorRow = 0;
    this.terminalEscape = 0; this.terminalCsi = ''; this.terminalOscEscape = false;
    this.terminalPending = new Uint8Array(0);
  }

  private applyTerminalBytes(bytes: Uint8Array): void {
    let joined = new Uint8Array(this.terminalPending.length + bytes.length);
    joined.set(this.terminalPending); joined.set(bytes, this.terminalPending.length);
    this.terminalPending = new Uint8Array(0);
    let text = '';
    for (let index = 0; index < joined.length;) {
      const point = codePointAt(joined, index);
      if (point.count === 0) { this.terminalPending = joined.slice(index); break; }
      text += point.text; index += point.count;
    }
    for (const char of text) this.consumeTerminalChar(char);
  }

  private consumeTerminalChar(char: string): void {
    const code = char.charCodeAt(0);
    if (this.terminalEscape === 3) {
      if (code === 7 || (this.terminalOscEscape && char === '\\')) { this.terminalEscape = 0; this.terminalOscEscape = false; }
      else this.terminalOscEscape = char === '\u001b';
      return;
    }
    if (this.terminalEscape === 1) {
      if (char === '[') { this.terminalEscape = 2; this.terminalCsi = ''; }
      else if (char === ']') { this.terminalEscape = 3; this.terminalOscEscape = false; }
      else this.terminalEscape = 0;
      return;
    }
    if (this.terminalEscape === 2) {
      this.terminalCsi += char;
      if (code >= 0x40 && code <= 0x7e) { this.applyCsi(this.terminalCsi); this.terminalCsi = ''; this.terminalEscape = 0; }
      return;
    }
    if (code === 0x1b) { this.terminalEscape = 1; return; }
    if (code === 13) { this.terminalCursorColumn = 0; return; }
    if (code === 10) { this.lineFeed(); return; }
    if (code === 8) { this.terminalCursorColumn = Math.max(0, this.terminalCursorColumn - 1); return; }
    if (code === 9) { this.terminalCursorColumn = Math.min(this.terminalGridCols - 1, (Math.floor(this.terminalCursorColumn / 8) + 1) * 8); return; }
    if (code < 32 || code === 127) return;
    if (this.terminalCursorRow < 0 || this.terminalCursorRow >= this.terminalGridRows) this.terminalCursorRow = Math.max(0, Math.min(this.terminalGridRows - 1, this.terminalCursorRow));
    if (this.terminalCursorColumn >= this.terminalGridCols) { this.terminalCursorColumn = 0; this.lineFeed(); }
    const cells = this.terminalGrid[this.terminalCursorRow];
    if (cells) cells[this.terminalCursorColumn] = char;
    this.terminalCursorColumn = Math.min(this.terminalGridCols, this.terminalCursorColumn + 1);
  }

  private applyCsi(value: string): void {
    const final = value.charAt(value.length - 1);
    const nums = value.replace(/^[?=>!]+/, '').replace(/[ -/]+/g, '').split(';');
    const first = nums[0] ? Number(nums[0]) : (final === 'J' || final === 'K' ? 0 : 1);
    const second = Number(nums[1] || '1');
    if (final === 'm' || final === 'h' || final === 'l' || final === 'n' || final === 'c') return;
    if (final === 'J') {
      if (first === 2 || first === 3) this.terminalGrid = blankGrid(this.terminalGridCols, this.terminalGridRows);
      else if (first === 0) for (let row = this.terminalCursorRow; row < this.terminalGridRows; row++) {
        const start = row === this.terminalCursorRow ? this.terminalCursorColumn : 0;
        for (let col = start; col < this.terminalGridCols; col++) this.terminalGrid[row][col] = ' ';
      }
      return;
    }
    if (final === 'K') {
      const cells = this.terminalGrid[this.terminalCursorRow];
      if (!cells) return;
      const start = first === 1 ? 0 : first === 2 ? 0 : this.terminalCursorColumn;
      const end = first === 1 ? this.terminalCursorColumn : this.terminalGridCols - 1;
      for (let col = start; col <= end; col++) cells[col] = ' ';
      return;
    }
    if (final === 'H' || final === 'f') {
      this.terminalCursorRow = Math.max(0, Math.min(this.terminalGridRows - 1, first - 1));
      this.terminalCursorColumn = Math.max(0, Math.min(this.terminalGridCols - 1, second - 1));
      return;
    }
    if (final === 'A') this.terminalCursorRow = Math.max(0, this.terminalCursorRow - first);
    else if (final === 'B') this.terminalCursorRow = Math.min(this.terminalGridRows - 1, this.terminalCursorRow + first);
    else if (final === 'C') this.terminalCursorColumn = Math.min(this.terminalGridCols - 1, this.terminalCursorColumn + first);
    else if (final === 'D') this.terminalCursorColumn = Math.max(0, this.terminalCursorColumn - first);
    else if (final === 'G') this.terminalCursorColumn = Math.max(0, Math.min(this.terminalGridCols - 1, first - 1));
    else if (final === 'd') this.terminalCursorRow = Math.max(0, Math.min(this.terminalGridRows - 1, first - 1));
    else if (final === 's') { this.savedCursorColumn = this.terminalCursorColumn; this.savedCursorRow = this.terminalCursorRow; }
    else if (final === 'u') { this.terminalCursorColumn = this.savedCursorColumn; this.terminalCursorRow = this.savedCursorRow; }
  }

  private savedCursorColumn: number = 0;
  private savedCursorRow: number = 0;

  private lineFeed(): void {
    this.terminalCursorRow++;
    if (this.terminalCursorRow >= this.terminalGridRows) {
      this.terminalGrid.shift();
      const cells: string[] = [];
      for (let col = 0; col < this.terminalGridCols; col++) cells.push(' ');
      this.terminalGrid.push(cells);
      this.terminalCursorRow = this.terminalGridRows - 1;
    }
  }

  private renderTerminal(): string { return this.terminalGrid.map((row: string[]) => row.join('').replace(/ +$/, '')).join('\n'); }

  private message(error: Error): string {
    const text = error.message || 'failed';
    return text === 'scope_changed' ? 'request_updated' : text;
  }

  private resetBusyState(): void {
    this.fileListEpoch++; this.fileReadEpoch++; this.fileGitEpoch++; this.resetFileFind();
    this.gitStashEpoch++; this.gitStashBusy=false; this.gitPrEpoch++; if (this.gitPrBusy) this.gitPrKey=''; this.gitPrBusy=false;
    this.gitStatusEpoch++; this.gitHistoryEpoch++; this.gitHistoryBusy=false; this.closeGitShow(); this.closeGitDiff();
    this.fileBusy = false; this.fileReadBusy = false; this.fileWriteBusy = false;
    this.gitBusy = false; this.gitDiffBusy = false; this.gitWriteBusy = false;
    this.screenBusy = false; this.screenKeyBusy = false;
  }
}
