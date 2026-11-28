import { relativeActivityDate } from '../core/PhoneDate';
import { MinutesWaitResult, resolveMinutesWait } from '../core/MinutesWait';
import { isKnownDebateStatus, MinutesDebateItem, MinutesDebatePosition, parseMinutesDebates } from '../core/MinutesDebates';
import {
  buildTranscriptView,
  exportTranscriptText,
  filterTranscriptBookmarks,
  findTranscriptMatches,
  splitTranscriptMatchParts,
  stepTranscriptMatch,
  TranscriptMatchPart,
  TranscriptRow,
  transcriptRowDisplayText,
  TranscriptViewData
} from '../core/TranscriptSearch';
import { pasteboard } from '@kit.BasicServicesKit';
import { toastCenter } from '../models/ToastCenter';
import { KBChatInboxPage, KBChatInboxPiece, KBInboxItem, kbInboxLine, kbInboxTier, parseChatInboxPage } from '../core/KBChatInbox';
import { isHubPageHidden } from '../core/HubModules';
import { PersonaSectionLabel, PersonaStatusPill, PersonaStatusTone } from '../design/NormalizedPrimitives';
import { i18n } from '@kit.LocalizationKit';
import { KnowledgeLandingMenu, knowledgeInk, knowledgePriorityTone, knowledgeRelevanceTone, knowledgeWash } from '../models/KnowledgeListPresentation';
import { IconActions } from '../design/IconActions';
import { Symbols } from '../design/Symbols';
import { PhoneSectionTitle, PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { Haptics } from '../platform/Haptics';
import { NeedsYouPulseDot } from './StatusMotionMarks';
import { workspaceGridColumns, workspaceGridGutter, workspaceHorizontalPadding } from '../core/WorkspaceGrid';
import type { KnowledgeRecordingEntry } from '../models/KnowledgeRecording';
import { Colors } from '../design/Colors';
import { ButtonStyles, SelectStyles } from '../design/Controls';
import { ClientModel } from '../models/ClientModel';
import { KnowledgeDirectory, KnowledgeListEntry, KnowledgeSearchHit, KnowledgeWorkspace, RecordingFactsDraft } from '../models/KnowledgeWorkspace';
import { ResourceRow, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { MarkdownText } from './MarkdownText';
import { Tokens } from '../design/Tokens';
import { DeviceLook, deviceLook } from '../models/DeviceLook';
import { tr } from '../design/Strings';
import { common } from '@kit.AbilityKit';
import { nativeMeetingServices } from '../platform/NativeMeetingServices';
import { MeetingOccurrence } from '../models/MeetingCalendarReminders';
import { MeetingPreferences, meetingRoleLabel } from '../models/MeetingPreferences';
import { KnowledgeRecording } from '../models/KnowledgeRecording';
import { NativeRecording } from '../platform/NativeRecording';
import { KnowledgeRecordingView } from './KnowledgeRecordingView';
import { LengthMetrics, UIUtils } from '@kit.ArkUI';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { PhoneMeetingOptions, meetingCaptureHeight, meetingDefaultTitle, meetingPlaceholderVp } from '../models/PhoneMeetingOptions';
import { PhoneMeetingTranscription } from '../models/PhoneMeetingTranscription';
import { MeetingHubChoice } from '../models/MeetingHubAudio';
import { meetingAudioNetwork } from '../platform/MeetingAudioNetwork';
import type { Binding } from '@kit.ArkUI';

const KB_KINDS: string[] = ['projects', 'topics', 'chats', 'people', 'docs', 'meetings', 'events'];
// The iPhone's caption symbols have different advances and optical bounds; ArkUI's glyphs use a square em.
// Express their native measurements in em, so the same proportions apply to every width and font setting.
class KindSymbolMetrics {
  advance: number;
  size: number;
  horizontalScale: number;
  constructor(advance: number, size: number, horizontalScale: number) {
    this.advance = advance; this.size = size; this.horizontalScale = horizontalScale;
  }
}
const KIND_SYMBOLS: Map<string, KindSymbolMetrics> = new Map<string, KindSymbolMetrics>([
  ['projects', new KindSymbolMetrics(1.28, 1.20, 1.00)], ['topics', new KindSymbolMetrics(1.30, 1.15, 1.22)],
  ['chats', new KindSymbolMetrics(1.70, 1.32, 1.23)], ['people', new KindSymbolMetrics(2.14, 1.18, 1.55)],
  ['docs', new KindSymbolMetrics(1.11, 1.15, 0.95)], ['meetings', new KindSymbolMetrics(1.46, 1.34, 0.96)],
  ['events', new KindSymbolMetrics(1.26, 1.04, 1.11)], ['recordings', new KindSymbolMetrics(1.26, 1.15, 1.00)]
]);

@ComponentV2
struct KnowledgeLibraryRow {
  @Require @Param row: ResourceRow;
  @Require @Param symbol: Resource;
  @Param language:string='default';
  @Param cardSurface: boolean = false;
  @Event open: () => void = () => {};
  @Local badgesWidth:number=0;
  @Local environment:NativeEnvironment=nativeEnvironment;
  @Computed get entry(): KnowledgeListEntry { return new KnowledgeListEntry(this.row); }

  private dateLabel(): string {
    return relativeActivityDate(Date.parse(this.entry.updated),Date.now(),this.language==='default' ? i18n.System.getSystemLanguage() : this.language,
      tr('agent_time_just_now'),tr('kb_minutes_ago'),tr('kb_yesterday'));
  }
  private textWidth(label:string,points:number,bold:boolean):number {
    const ctx=this.getUIContext(), scale=Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(points));
    return ctx.px2vp(ctx.getMeasureUtils().measureText({textContent:label,fontSize:String(points*scale)+'vp',fontWeight:bold ? FontWeight.Bold : FontWeight.Medium}));
  }
  @Computed get fittedTags():string[] {
    const pills:string[]=[];
    if (this.entry.priority) pills.push(this.entry.priority);
    if (this.entry.duration) pills.push(tr(this.entry.duration==='long' ? 'knowledge_project_long' : 'knowledge_project_short'));
    if (this.entry.relevance>0) pills.push(tr('kb_relevance_value').replace('{n}',String(this.entry.relevance)));
    let used=0;
    for (const label of pills) used+=this.textWidth(label,12,label===this.entry.priority)+12+(used ? 6 : 0);
    const tags:string[]=[];
    for (const tag of this.entry.tags.slice(0,2)) {
      const width=this.textWidth('#'+tag,12,false)+(used ? 6 : 0);
      if (used+width>this.badgesWidth) break; used+=width;tags.push(tag);
    }
    return tags;
  }
  private priority():string { return knowledgePriorityTone(this.entry.priority); }
  private rowLineHeight(ordinary:number,largest:number):string {
    return PhoneStyle.settingsExampleLineHeight(ordinary,largest,this.environment.fontScale);
  }
  @Builder badge(value:string,tone:string,bold:boolean=false) {
    Text(value).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontWeight(bold ? FontWeight.Bold : FontWeight.Medium)
      .lineHeight(this.rowLineHeight(14.33,21.67))
      .maxLines(1).fontColor(knowledgeInk(tone,nativeEnvironment.dark)).padding({left:6,right:6,top:2,bottom:2})
      .borderRadius(Tokens.radiusCapsule).backgroundColor(knowledgeWash(tone,nativeEnvironment.dark)).flexShrink(0)
  }
  build() {
    Row({space:12}) {
      Stack() {
        Rect().width(36).height(36).radius(Tokens.radiusControl).fill(knowledgeWash(this.priority(),nativeEnvironment.dark))
        SymbolGlyph(this.symbol).fontSize('16vp').fontWeight(FontWeight.Medium).fontColor([knowledgeInk(this.priority(),nativeEnvironment.dark)])
      }.width(36).height(36).flexShrink(0)
      Column({space:4}) {
        Flex({direction:FlexDirection.Row,alignItems:ItemAlign.Baseline,space:{main:LengthMetrics.vp(6)}}) {
          Text(this.row.title || this.row.id).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeHeading)).lineHeight(this.rowLineHeight(20.33,27.67))
            .fontWeight(600).fontColor(Colors.ink).layoutWeight(1).maxLines(2)
          if (this.dateLabel())
            Text(this.dateLabel()).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).lineHeight(this.rowLineHeight(14.33,21.67))
              .fontColor(Colors.mutedInk).flexShrink(0).maxLines(1)
        }.width('100%')
        if (this.entry.summary) Text(this.entry.summary).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontColor(Colors.secondary).maxLines(2)
          .lineHeight(this.rowLineHeight(18,25.33))
          .lineSpacing(LengthMetrics.vp(PhoneStyle.formSize(2,0.67,this.environment.fontScale)),{onlyBetweenLines:true})
          .textOverflow({overflow:TextOverflow.Ellipsis})
        Row({space:6}) {
          if (this.entry.priority) this.badge(this.entry.priority,this.priority(),true)
          if (this.entry.duration) this.badge(tr(this.entry.duration==='long' ? 'knowledge_project_long' : 'knowledge_project_short'),this.entry.duration==='long' ? 'blue' : 'neutral')
          if (this.entry.relevance>0) this.badge(tr('kb_relevance_value').replace('{n}',String(this.entry.relevance)),knowledgeRelevanceTone(this.entry.relevance))
          ForEach(this.fittedTags,(tag:string)=>{
            Text('#'+tag).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).lineHeight(this.rowLineHeight(14.33,21.67)).fontColor(Colors.secondary).maxLines(1)
          },(tag:string)=>tag)
        }.width('100%').padding({top:1}).onAreaChange((_previous:Area,area:Area)=>{ const width=Number(area.width);if (this.badgesWidth!==width) this.badgesWidth=width; })
      }.layoutWeight(1).alignItems(HorizontalAlign.Start)
      SymbolGlyph(Symbols.expand).fontSize('12vp').width(12).fontColor([Colors.secondary]).alignSelf(ItemAlign.Center)
    }.width('100%').alignItems(VerticalAlign.Top).padding({left:this.cardSurface ? 16 : 20,right:this.cardSurface ? 14 : 18,top:this.cardSurface ? 16 : 19,bottom:this.cardSurface ? 16 : 19}).constraintSize({minHeight:63})
      .backgroundColor(Colors.surface).borderRadius(this.cardSurface ? PhoneStyle.cardRadius : 0)
      .border(this.cardSurface ? {width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius} : {width:0})
      .shadow(this.cardSurface ? PhoneStyle.cardElevation(this.environment.dark) : {radius:0,color:Color.Transparent})
      .accessibilityGroup(true).onClick(() => { Haptics.tap(); this.open(); }).id('knowledge.entry.'+this.row.path+'.'+this.row.id)
  }
}

// Keep the selectable text mounted when only the native appearance changes.
@ComponentV2
struct RecordingProfileCard {
  @BuilderParam content:()=>void;
  @Local environment:NativeEnvironment=nativeEnvironment;
  @Local measuredHeight:number=0;
  build() {
    Stack({alignContent:Alignment.TopStart}) {
      if (this.environment.dark) {
        Rect().width('100%').height(this.measuredHeight).radius(Tokens.radiusPanel).fill(Colors.surface)
          .hitTestBehavior(HitTestMode.None).accessibilityLevel('no')
      } else {
        Rect().width('100%').height(this.measuredHeight).radius(Tokens.radiusPanel).fill(Colors.surface)
          .hitTestBehavior(HitTestMode.None).accessibilityLevel('no')
      }
      Column() { this.content() }.width('100%').alignItems(HorizontalAlign.Start)
        .padding({left:20,right:20,top:21,bottom:21})
        .onAreaChange((_:Area,next:Area)=>{
          const height=Number(next.height);
          if (Number.isFinite(height) && this.measuredHeight!==height) this.measuredHeight=height;
        })
    }.width('100%').borderRadius(Tokens.radiusPanel)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
      .shadow(PhoneStyle.cardElevation(this.environment.dark)).id('knowledge.summary')
  }
}

@ComponentV2
export struct KnowledgeWorkspacePage {
  @Local look: DeviceLook = deviceLook;
  @Local environment:NativeEnvironment=nativeEnvironment;
  @Local private rootWidth: number = 0;
  @Local dispatchExpanded: boolean = false;
  @Local facts: RecordingFactsDraft = new RecordingFactsDraft();
  @Local removedFacts: string = '';
  private removedFactsOwner: string = '';
  private removedFactsNotice: number = 0;
  @Local recordingTools: boolean = false;
  @Local transcriptReviewOpen: boolean = false;
  @Local transcriptSearchQuery: string = '';
  @Local transcriptSelectedId: string = '';
  @Local transcriptBookmarksOnly: boolean = false;
  @Require @Param model: ClientModel;
  @Param row: ResourceRow | undefined = undefined;
  @Param list: ResourceRow[] = [];
  @Param visible: boolean = true;
  @Param rail: boolean = false;
  @Param initialKind: string = '';
  @Param landingMenu:KnowledgeLandingMenu|undefined=undefined;
  @Event onOpenMap:()=>void=()=>{};
  @Param editRevision: number = 0;
  @Param phoneRecordingDetail: boolean = false;
  @Local metadataSheet: boolean = false;
  @Local localMeetings: boolean = false;
  @Param meetingMenu:PhoneMeetingOptions|undefined=undefined;
  @Local fallbackMeetingMenu:PhoneMeetingOptions=new PhoneMeetingOptions();
  private options():PhoneMeetingOptions { return this.meetingMenu || this.fallbackMeetingMenu; }
  private editingMeeting():boolean {
    return this.pageAvailable && this.visible && this.initialKind==='recordings' && !this.row && !this.recordingOpen &&
      this.recording.captureState!=='requesting' && this.recording.captureState!=='recording';
  }
  @Monitor('meetingMenu','meetingKind','privateMeeting','recording.captureState','recordingOpen','model.features','model.connected','visible')
  syncMeetingOptions():void {
    const menu=this.options();
    if (!this.editingMeeting()) { menu.clear(this.recording);return; }
    const key=this.recordingScope(),generation=this.model.connectionGeneration();
    const permitted=():boolean=>this.editingMeeting() && this.recordingScope()===key && this.model.connectionGeneration()===generation;
    const canMark=this.meetingKind==='meeting' && this.model.connected && this.model.features.includes('meeting_conversation');
    menu.bind(this.recording,true,canMark,this.privateMeeting,permitted,(mark:boolean):void=>{
      if (permitted() && this.meetingKind==='meeting' && this.model.features.includes('meeting_conversation')) this.privateMeeting=mark;
    },()=>NativeRecording.meetingLanguages(),()=>NativeRecording.meetingInputs());
  }
  @Local recordingTitleWidth:number=0;
  @Local recordingPageHeight: number = 0;
  private placeholderSize():string {
    const ctx=this.getUIContext(),preferred=17*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(17));
    const width=this.recordingTitleWidth || (nativeEnvironment.windowWidthPx ? ctx.px2vp(nativeEnvironment.windowWidthPx)-72 : 0);
    const label=this.defaultRecordingTitle(this.meetingKind);
    const measured=ctx.px2vp(ctx.getMeasureUtils().measureText({textContent:label,fontSize:String(preferred)+'vp',fontWeight:FontWeight.Medium}));
    return meetingPlaceholderVp(width,measured,preferred);
  }
  private defaultRecordingTitle(kind:string):string {
    return meetingDefaultTitle(kind,new Date(),this.model.language==='default' ? i18n.System.getSystemLanguage() : this.model.language,
      tr('phone_record_meeting'),tr('phone_record_note'));
  }
  @Local meetingKind: string = 'meeting';
  @Local meetingPreferences:MeetingPreferences = new MeetingPreferences();
  @Local meetingSpeech:PhoneMeetingTranscription = new PhoneMeetingTranscription();
  @Local meetingNetworkReady:boolean=false;
  @Local selectedAudioOwner:string='';
  private confirmVersion:number=0;
  private stopMeetingEvents:(()=>void)|undefined;
  @Local invitationOverride:string='';
  @Local privateMeeting:boolean=false;
  private canInvite():boolean { return this.model.connected && this.model.features.includes('meeting_attendance') && this.model.features.includes('meeting_conversation'); }
  private roomRole():string {
    if (this.meetingKind!=='meeting' || !this.canInvite() || this.invitationOverride==='off') return 'off';
    if (!this.invitationOverride) return this.meetingPreferences.role;
    return this.meetingPreferences.role==='participant' ? 'participant' : 'listener';
  }
  @Monitor('model.features') meetingFeaturesChanged():void {
    this.stopMeetingEvents?.();this.stopMeetingEvents=undefined;this.resetMeetingMode();
    if (!this.model.features.includes('meeting_conversation')) this.privateMeeting=false;
    if (!this.pageAvailable || !this.visible || this.initialKind!=='recordings' || this.row) return;
    this.meetingPreferences.open(this.model);
    this.meetingSpeech.open(this.model);
    const model=this.model;
    if (model.connected && !model.demo) this.stopMeetingEvents=model.observeEvents((raw:string):void => {
      if (!this.pageAvailable || !this.visible || this.model!==model || raw.length>2*1024*1024) return;
      const kind=Wire.text(Wire.parse(raw),'type');
      if (['assistant','assistant_config','assistant_settings','plugin','plugins','extensions','features'].includes(kind)) {
        this.resetMeetingMode();this.meetingSpeech.open(model);
      }
    });
    const context=this.getUIContext().getHostContext() as common.UIAbilityContext,owner=this.recordingScope();
    this.meetingNetworkReady=false;
    if (context) meetingAudioNetwork.initialize(context).then(()=>{
      if (this.pageAvailable && owner===this.recordingScope()) this.meetingNetworkReady=true;
    }).catch(()=>{});
  }

  private resetMeetingMode():void { this.confirmVersion++; this.selectedAudioOwner=''; }
  private readyForHub():boolean {
    return this.meetingKind==='meeting' && this.pageAvailable && this.visible && this.model.connected && !this.model.demo &&
      this.meetingNetworkReady && this.meetingSpeech.offered && this.meetingSpeech.ready &&
      ['meeting_conversation','meeting_live_view','meeting_transcription'].every((key:string)=>this.model.features.includes(key));
  }
  private modeOwner():string {
    return JSON.stringify([this.recordingScope(),this.model.connectionGeneration(),this.model.profile.baseUrl,this.model.profile.pin,
      this.meetingSpeech.extensionRef,this.meetingSpeech.serviceName(),meetingAudioNetwork.wifiOnly]);
  }
  private pickedHub():boolean { return this.readyForHub() && this.selectedAudioOwner===this.modeOwner(); }
  private chooseMode(hub:boolean):void { this.confirmVersion++; this.selectedAudioOwner=hub && this.readyForHub() ? this.modeOwner() : ''; }
  private hubService():string { return this.meetingSpeech.serviceName() || tr('persona'); }
  private hubUnavailable():boolean {
    return this.meetingKind==='meeting' && this.meetingSpeech.offered && this.meetingSpeech.mode==='hub' && !this.readyForHub();
  }
  private sourceName(hub:boolean):string {
    return hub ? tr('meeting_cloud_service').replace('{service}',this.hubService()) : tr('meeting_this_device');
  }
  private setMeetingKind(kind:string):void {
    if (kind!==this.meetingKind) { this.resetMeetingMode(); this.meetingKind=kind; this.privateMeeting=false; }
  }
  private startContext():string {
    return JSON.stringify([this.modeOwner(),this.meetingKind,this.meetingTitle,this.roomRole(),this.privateMeeting,this.selectedCalendarEvent,this.options().selectedInput]);
  }
  private modePrivacy(hub:boolean):string {
    const days=this.meetingSpeech.retentionDays;
    const invited=this.roomRole()!=='off';
    if (invited && !hub) return tr(days ? 'meeting_room_privacy_local_days' : 'phone_meeting_live_note').replace('{days}',String(days));
    const message=tr(hub ? (days ? 'meeting_start_privacy_cloud_days' : 'meeting_start_privacy_cloud') : (days ? 'meeting_start_privacy_local_days' : 'phone_record_audio_local'))
      .replace('{service}',this.meetingSpeech.serviceName() || tr('meeting_configured_service')).replace('{days}',String(days));
    return invited ? message+' '+tr('meeting_room_text_shared') : message;
  }

  @Local meetingTitle: string = '';
  @Local calendarEvents:MeetingOccurrence[]=[];
  private selectedCalendarEvent:string='';
  private recordingCalendarEvent:string='';
  @Monitor('recording.captureState') meetingCaptureChanged():void {
    if (this.recordingCalendarEvent && this.recording.captureState!=='recording') { this.recordingCalendarEvent='';nativeMeetingServices.setRecordingEvent(''); }
  }
  @Local calendarState: string = '';
  private calendarRequest: number = 0;
  @Local showIgnored: boolean = false;
  @Local refreshing: boolean = false;
  @Local filterWidth:number=0;
  private separateTagLine():boolean {
    return this.filterWidth>0 && this.filterWidth<215*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(14));
  }
  @Event onAllMeetings: () => void = () => { this.localMeetings = true; this.workspace.setKind('meetings'); };
  private localOwner: string = '';
  private localOpening: Promise<void> | undefined;
  @Event onOpenResource: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onChat: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onLink: (url: string) => void = (_url: string) => {};
  @Local workspace: KnowledgeWorkspace = new KnowledgeWorkspace();
  @Local entryRowBindings: BoundRow<ResourceRow>[] = [];
  @Local resultRowBindings: BoundRow<KnowledgeSearchHit>[] = [];
  @Local agentRowBindings: BoundRow<ResourceRow>[] = [];
  private rowBindingOwner: Object | undefined;
  private rowBindingScope: string = '';
  @Local momentText: string = '';
  @Local selectedActions: string[] = [];
  @Local chatInboxDoneShown: boolean = false;
  @Local recordingOpen: boolean = false;
  @Local recording: KnowledgeRecording = new KnowledgeRecording(new NativeRecording());
  private lastVisible: boolean = true;
  private workspaceOpening: boolean = false;
  private queuedRefresh: boolean = false;
  private openSerial: number = 0;
  private pageAvailable: boolean = false;

  private menuCurrent():boolean { return this.pageAvailable && this.visible && !this.row && (!this.initialKind || this.initialKind === 'meetings'); }
  @Monitor('workspace.tags','workspace.tag','workspace.query','model.profile','model.connected','visible','row','landingMenu')
  syncLandingMenu():void {
    if (this.menuCurrent()) {
      const scope=this.model.scope(),generation=this.model.connectionGeneration();
      this.landingMenu?.bind(this.workspace,()=>this.menuCurrent() && this.model.owns(generation,scope),()=>this.onOpenMap());
    } else this.landingMenu?.clear(this.workspace);
  }
  aboutToAppear(): void {
    this.pageAvailable = true; this.syncMeetingOptions();this.syncLandingMenu(); this.meetingFeaturesChanged(); this.lastVisible = this.visible; this.syncRowBindings(); this.openWorkspace();
    if (this.initialKind === 'recordings' && !this.row) this.loadRecordings().catch(() => undefined);
  }
  aboutToDisappear(): void {
    this.landingMenu?.clear(this.workspace);
    this.pageAvailable=false;this.options().clear(this.recording);this.stopMeetingEvents?.();this.stopMeetingEvents=undefined;this.resetMeetingMode(); this.meetingSpeech.close(); this.meetingPreferences.close(); this.calendarRequest++; this.localOwner=''; this.localOpening=undefined; this.openSerial++; this.workspaceOpening=false; this.queuedRefresh=false;
    this.workspace.hide(); this.recording.dispose().catch(() => undefined);
  }

  @Monitor('editRevision') editTagsRequested(): void {
    if (!this.pageAvailable || !this.visible || this.row?.path!=='recordings' || this.workspace.selected.id!==this.row.id) return;
    if (this.phoneRecordingDetail) this.recordingTools=false;
    this.workspace.beginEdit();
    this.metadataSheet=this.workspace.editOpen;
  }
  @Monitor('workspace.editOpen') metadataChanged():void { if (!this.workspace.editOpen) this.metadataSheet=false; }
  @Monitor('row') rowChanged(): void { this.metadataSheet=false; this.facts.close(); this.removedFacts=''; this.removedFactsOwner=''; this.recordingTools=false; this.transcriptReviewOpen=false; this.transcriptSearchQuery=''; this.transcriptSelectedId=''; this.transcriptBookmarksOnly=false; this.chatInboxDoneShown=false; this.syncRowBindings(); this.openWorkspace(); }

  @Monitor('model.profile') profileChanged(): void {
    this.options().clear(this.recording);
    this.metadataSheet=false; this.facts.close(); this.removedFacts=''; this.removedFactsOwner=''; this.recordingTools=false; this.transcriptReviewOpen=false; this.transcriptSearchQuery=''; this.transcriptSelectedId=''; this.transcriptBookmarksOnly=false; this.chatInboxDoneShown=false; this.showIgnored=false; this.recordingOpen = false;
    this.selectedCalendarEvent=''; this.calendarRequest++; this.calendarEvents=[]; this.calendarState=''; this.meetingTitle=''; this.meetingKind='meeting';
    this.privateMeeting=false; this.invitationOverride='';this.syncMeetingOptions();this.meetingFeaturesChanged(); this.localMeetings = false; this.localOwner = ''; this.localOpening = undefined;
    this.recording.stopForBackground().catch(() => undefined); this.syncRowBindings(); this.openWorkspace();
    if (this.initialKind === 'recordings' && !this.row) this.loadRecordings().catch(() => undefined);
  }
  @Monitor('model.connected') connectionChanged(): void {
    this.meetingFeaturesChanged();
    if (this.model.connected) { this.refreshWhenReady(); if (this.recordingOpen || this.initialKind === 'recordings') this.loadRecordings().catch(() => undefined); }
  }
  @Monitor('visible') visibilityChanged(): void {
    const becameVisible = !this.lastVisible && this.visible;
    this.lastVisible = this.visible;
    if (!this.visible) { this.stopMeetingEvents?.();this.stopMeetingEvents=undefined;this.resetMeetingMode(); }
    else if (becameVisible && this.pageAvailable && this.initialKind==='recordings') this.meetingFeaturesChanged();
    if (becameVisible && this.pageAvailable && !this.row) this.refreshWhenReady();
  }
  @Monitor('workspace.entries', 'workspace.results', 'workspace.agents') rowSourcesChanged(): void { this.syncRowBindings(); }

  private openWorkspace(): void {
    const serial = ++this.openSerial;
    this.workspaceOpening = true;
    this.workspace.open(this.model, this.row, this.list, this.initialKind).catch(() => undefined).finally(() => {
      if (serial !== this.openSerial) return;
      this.workspaceOpening = false;
      const shouldRefresh = this.queuedRefresh; this.queuedRefresh = false;
      if (shouldRefresh && this.pageAvailable) this.workspace.refresh().catch(() => undefined);
    });
  }

  private refreshWhenReady(): void {
    if (!this.pageAvailable) return;
    if (this.workspaceOpening) { this.queuedRefresh = true; return; }
    this.workspace.refresh().catch(() => undefined);
  }

  private syncRowBindings(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager, this.row?.key() || 'list']);
    this.entryRowBindings = reconcileBoundRows(this.entryRowBindings, this.workspace.entries, (row: ResourceRow) => row.key(),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.resultRowBindings = reconcileBoundRows(this.resultRowBindings, this.workspace.results, (hit: KnowledgeSearchHit) => hit.entry.key(),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.agentRowBindings = reconcileBoundRows(this.agentRowBindings, this.workspace.agents, (row: ResourceRow) => row.key(),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.rowBindingOwner = this.model; this.rowBindingScope = scope;
  }

  private recordingScope(): string {
    return JSON.stringify([this.model.scope(),this.model.pairingRevision(),this.row?.manager || this.model.profile.manager]);
  }
  private async loadRecordings(): Promise<boolean> {
    const context = this.getUIContext().getHostContext(), key = this.recordingScope();
    if (!this.pageAvailable || !context || this.model.demo || !this.model.profile.id) return false;
    if (key !== this.localOwner) {
      this.localOwner = key;
      this.localOpening = this.recording.open(this.model,this.row?.manager || this.model.profile.manager,context as common.UIAbilityContext);
    }
    const pending = this.localOpening;
    if (pending) {
      try { await pending; } finally { if (this.localOpening === pending) this.localOpening = undefined; }
    }
    return this.pageAvailable && this.visible && key === this.localOwner && this.recordingScope() === key;
  }
  private requestRecordingStart():void {
    if (!this.model.isRecordingModuleAllowed()) {
      this.getUIContext().getPromptAction().showToast({ message: tr('xiaoyi_recording_module_off') });
      return;
    }
    if (!this.pageAvailable || !this.visible || !this.recording.canStart()) return;
    if (!this.pickedHub()) {
      this.openRecording('',true).catch(() => undefined);
      return;
    }
    const version=++this.confirmVersion, context=this.startContext();
    this.getUIContext().showAlertDialog({title:tr('meeting_start_confirm_title'),message:this.modePrivacy(true),
      primaryButton:{value:tr('cancel'),action:() => { if (version===this.confirmVersion) this.confirmVersion++; }},
      secondaryButton:{value:tr('meeting_start_send'),action:() => {
        if (version!==this.confirmVersion || !this.pickedHub() || !this.recording.canStart() || context!==this.startContext()) return;
        this.confirmVersion++;
        const choice=new MeetingHubChoice(this.model,this.meetingSpeech.extensionRef,this.meetingSpeech.serviceName(),meetingAudioNetwork.wifiOnly);
        this.openRecording('',true,choice,context).catch(() => undefined);
      }}
    });
  }

  private async openRecording(id:string='', start:boolean=false, choice?:MeetingHubChoice, context:string=''):Promise<void> {
    const kind=this.meetingKind, role=this.roomRole(), privateMark=kind==='meeting' && this.privateMeeting, input=this.options().selectedInput, event=kind==='meeting' ? this.selectedCalendarEvent : '';
    const title=this.meetingTitle.trim() || this.defaultRecordingTitle(kind);
    if (!await this.loadRecordings()) return;
    if (id) this.recording.selectEntry(id);
    if (choice && (!choice.matches(this.model) || !this.pickedHub() || context!==this.startContext())) {
      this.recordingOpen=true;this.recording.error='request_updated';
      return;
    }
    this.recordingOpen=true;
    if (!start || !this.recording.canStart()) return;
    this.resetMeetingMode();
    if (await this.recording.start(kind,title,role,privateMark,choice,input)) {
      this.recordingCalendarEvent=event;nativeMeetingServices.setRecordingEvent(event);this.selectedCalendarEvent='';
      this.meetingTitle=''; this.calendarEvents=[]; this.calendarState=''; this.invitationOverride=''; this.privateMeeting=false;this.meetingKind='meeting';
    }
  }

  // Calendar lookup only fills the local draft. Starting an attended or cloud-transcribed meeting uses the selected title.
  private async readMeetingCalendar(): Promise<void> {
    const context=this.getUIContext().getHostContext() as common.UIAbilityContext, owner=this.recordingScope();
    if (!context || this.calendarState==='loading') return;
    const revision=++this.calendarRequest;
    this.calendarState='loading'; this.calendarEvents=[];
    try {
      const events=await nativeMeetingServices.today(context);
      if (!this.pageAvailable || revision!==this.calendarRequest || owner!==this.recordingScope()) return;
      this.calendarEvents=events;
      this.calendarState=this.calendarEvents.length ? 'ready' : 'phone_meeting_calendar_empty';
    } catch (error) {
      if (this.pageAvailable && revision===this.calendarRequest && owner===this.recordingScope())
        this.calendarState=(error as Error).message==='permission_denied' ? 'permission_denied' : 'failed';
    }
  }

  private deleteRecording(): void {
    const id = this.recording.entry?.id, scope = this.model.scope(), pairing = this.model.pairingRevision();
    this.getUIContext().showAlertDialog({ title: tr('knowledge_recording_delete_local'), message: this.recording.entry?.startedAt || tr('confirm'),
      primaryButton: { value: tr('cancel'), action: (): void => {} },
      secondaryButton: { value: tr('delete'), action: (): void => {
        if (this.recordingOpen && this.model.scope() === scope && this.model.pairingRevision() === pairing && this.recording.entry?.id === id) this.recording.deleteLocal();
      } } });
  }

  @Builder recordingSheet() {
    Scroll() { KnowledgeRecordingView({ model:this.recording,recordingAllowed:this.model.isRecordingModuleAllowed(),onStart:():void => this.requestRecordingStart(),onDeleteLocal: (): void => this.deleteRecording() }) }
      .width('100%').height('100%').align(Alignment.TopStart).backgroundColor(Colors.grouped)
  }

  private visibleKinds(): string[] {
    return this.model.canPreviewDemoRecordings() ? KB_KINDS.concat(['recordings']) : KB_KINDS;
  }

  @Local kindsWidth: number = 0;
  @Computed get kindLabelWidths(): Map<string, number> {
    const context = this.getUIContext(), widths = new Map<string, number>();
    for (const kind of this.visibleKinds()) {
      const pixels = context.getMeasureUtils().measureText({
        textContent: this.kindLabel(kind), fontSize: Tokens.interfaceFontVp(15, nativeEnvironment.fontScale),
        fontWeight: 600
      });
      widths.set(kind, context.px2vp(Math.ceil(pixels)));
    }
    return widths;
  }
  private kindSymbolPointSize(): number {
    return 13 * Math.min(nativeEnvironment.fontScale, Tokens.interfaceScaleFor(13));
  }
  @Computed get kindsLayout(): number {
    const context = this.getUIContext();
    const width = this.kindsWidth || (nativeEnvironment.windowWidthPx ? context.px2vp(nativeEnvironment.windowWidthPx)-32 : 360);
    const kinds = this.visibleKinds();
    let labels = 0, symbols = 0;
    for (const kind of kinds) {
      labels += this.kindLabelWidths.get(kind) || 0;
      symbols += this.kindSymbolPointSize() * KIND_SYMBOLS.get(kind)!.advance;
    }
    const count = kinds.length;
    if (labels + symbols + count*28 + (count-1)*8 <= width) return 0;
    if (labels + count*24 + (count-1)*6 <= width) return 1;
    if (labels + count*16 + (count-1)*4 <= width) return 2;
    return 3;
  }
  private kindLabel(kind: string): string { return tr(kind==='people' ? 'kb_people' : kind==='events' ? 'kb_events' : 'knowledge_kind_' + kind); }
  private kindIcon(kind: string): Resource {
    if (kind === 'projects') return $r('sys.symbol.square_stack_3d');
    if (kind === 'chats') return $r('sys.symbol.text_bubble_left_2');
    if (kind === 'people') return $r('sys.symbol.person_3');
    if (kind === 'meetings') return $r('sys.symbol.video');
    if (kind === 'events') return Symbols.calendar;
    if (kind === 'recordings') return $r('sys.symbol.mic');
    return kind === 'topics' ? $r('sys.symbol.lightbulb') : Symbols.document;
  }
  private clock(seconds: number): string {
    const whole = Number.isFinite(seconds) ? Math.max(0,Math.floor(seconds)) : 0;
    return String(Math.floor(whole / 60)) + ':' + String(whole % 60).padStart(2,'0');
  }
  private clockTime(ms:number): string {
    const date=new Date(ms);
    return String(date.getHours()).padStart(2,'0')+':'+String(date.getMinutes()).padStart(2,'0');
  }
  private localDate(raw:string): string {
    const date = new Date(raw);
    return Number.isFinite(date.getTime()) ? date.toLocaleDateString() + ' ' + String(date.getHours()).padStart(2,'0') + ':' + String(date.getMinutes()).padStart(2,'0') : '';
  }
  private projectRows(section: string): BoundRow<ResourceRow>[] {
    return this.entryRowBindings.filter((binding: BoundRow<ResourceRow>) => new KnowledgeListEntry(binding.value).section === section);
  }
  @Computed get visibleProjectSections():string[] {
    return ['focus','active','done','ignored'].filter((section:string)=>this.projectRows(section).length>0);
  }
  private sectionLabel(section: string): string {
    if (section === 'focus') return tr('knowledge_focus');
    if (section === 'active') return tr(this.projectRows('focus').length ? 'knowledge_other_active' : 'knowledge_status_active');
    if (section === 'ignored') return tr('knowledge_not_project');
    return tr('knowledge_status_done');
  }
  private isProjectList(): boolean { return this.workspace.kind === 'projects' && !this.workspace.query.trim(); }

  private visibleLocal(): KnowledgeRecordingEntry[] {
    const q = this.workspace.query.trim().toLowerCase();
    return this.recording.entries.filter((v:KnowledgeRecordingEntry) =>
      v.uploadState !== 'stored' && (!q || [v.title,v.startedAt,v.transcript].join(' ').toLowerCase().includes(q)));
  }

  private tagOptions(): SelectOption[] {
    const options: SelectOption[] = [{ value: tr('knowledge_all_tags') }];
    this.workspace.tags.forEach((tag: string) => options.push({ value: '#' + tag }));
    return options;
  }


  private selectTag(index: number): void { this.workspace.setTag(index === 0 ? '' : this.workspace.tags[index - 1] || ''); }

  @Builder knowledgeFilters() {
    Menu() {
      if (!this.workspace.query.trim()) MenuItemGroup({ header: tr('knowledge_filter_tag') }) {
        ForEach(this.tagOptions(), (option: SelectOption, index: number) => {
          MenuItem({ content: option.value }).selected((this.workspace.tag ? '#' + this.workspace.tag : tr('knowledge_all_tags')) === option.value).selectIcon(true)
            .onClick(() => this.selectTag(index))
        }, (option: SelectOption) => JSON.stringify(option.value))
      }
    }.width(Tokens.popupWidth)
  }

  private knowledgeColumns(): number {
    return workspaceGridColumns(this.rootWidth, this.environment.fontScale, 3);
  }
  private knowledgeSidePadding(): number {
    return workspaceHorizontalPadding(this.rootWidth);
  }

  @Builder searchField(meetings:boolean) {
    Search({value:this.workspace.query,placeholder:tr(meetings ? 'phone_record_search' : 'kb_search_profiles')}).maxFontScale(Tokens.interfaceFontMaxScale)
      .height(this.landingMenu ? PhoneStyle.formSize(44,58,this.environment.fontScale) : Math.max(44,Math.ceil(28*Math.min(this.environment.fontScale,Tokens.interfaceScaleFor(17)))+10))
      .margin({top:this.landingMenu ? 0 : 8,bottom:this.landingMenu ? 0 : 8}).textFont({size:17})
      .borderRadius(Tokens.radiusCapsule).backgroundColor(meetings ? Colors.control : Colors.surface)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusCapsule})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .onChange((value:string) => this.workspace.setQuery(value)).id('knowledge.search')
  }
  @Builder libraryRow(row: ResourceRow, cardSurface: boolean = false) {
    KnowledgeLibraryRow({row:row, language:this.model.language, symbol:this.kindIcon(row.path), cardSurface:cardSurface, open:():void => {
      this.workspace.openEntry(row).then((next:ResourceRow) => this.onOpenResource(next)).catch(() => undefined);
    }})
  }
  @Builder searchHit(hit:KnowledgeSearchHit) {
    Column({space:4}) {
      Text(hit.entry.title || hit.entry.id).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(2)
      Text(this.kindLabel(hit.entry.path) + ' · ' + String(hit.count)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
      ForEach(hit.hits.slice(0,2), (line:string,index:number) => Text(line).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary)
        .maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}), (line:string,index:number) => JSON.stringify([index, line]))
    }.width('100%').padding(14).alignItems(HorizontalAlign.Start).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .onClick(() => { Haptics.tap(); this.workspace.openEntry(hit.entry).then((next:ResourceRow) => this.onOpenResource(next)).catch(() => undefined); })
      .id('knowledge.hit.' + hit.entry.path + '.' + hit.entry.id)
  }
  @Builder sectionEntries() {
    if (this.workspace.query.trim()) {
      ForEach(this.resultRowBindings, (binding:BoundRow<KnowledgeSearchHit>) => {
        ListItem() { this.searchHit(binding.value) }
      }, (binding:BoundRow<KnowledgeSearchHit>) => binding.key)
    } else {
      ForEach(this.entryRowBindings, (binding:BoundRow<ResourceRow>) => {
        ListItem() { this.libraryRow(binding.value) }
      }, (binding:BoundRow<ResourceRow>) => binding.key)
    }
  }
  @Builder listStatus() {
    if (this.workspace.error) ListItem() {
      Row({space:8}) {
        Text(tr(this.workspace.error)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary).layoutWeight(1)
        Button(tr('retry'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.link).onClick(() => this.workspace.refresh()).id('knowledge.retry')
      }.width('100%').padding(10)
    }
    if (this.workspace.loading && !this.workspace.entries.length && !this.workspace.results.length)
      ListItem() { LoadingProgress().width(26).height(26).margin(10) }
    if (!this.workspace.loading && !this.workspace.error && (this.initialKind !== 'recordings' || !this.visibleLocal().length) && (this.workspace.query.trim() ? !this.workspace.results.length : !this.workspace.entries.length))
      ListItem() { Text(tr(this.workspace.query.trim() ? 'knowledge_no_results' : this.workspace.kind === 'recordings' ? 'phone_read_only_recording' : 'knowledge_empty')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary).padding(10) }
  }
  @Builder kindGlyph(kind: string, metrics: KindSymbolMetrics) {
    Stack({alignContent:Alignment.Center}) {
      SymbolGlyph(this.kindIcon(kind)).fontSize(String(this.kindSymbolPointSize() * metrics.size)+'vp')
        .width(this.kindSymbolPointSize() * metrics.size).height(this.kindSymbolPointSize() * metrics.size)
        .scale({x:metrics.horizontalScale,y:1}).fontColor([kind===this.workspace.kind ? Colors.onAction : Colors.ink])
    }.width(this.kindSymbolPointSize() * metrics.advance).height(Tokens.interfaceFontVp(18, nativeEnvironment.fontScale)).clip(true)
  }
  @Builder kindChip(kind:string) {
    Button({type:ButtonType.Capsule}) {
      Row({space:4}) {
        if (this.kindsLayout===0 || this.kindsLayout===3) this.kindGlyph(kind, KIND_SYMBOLS.get(kind)!)
        Text(this.kindLabel(kind)).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15))
          .lineHeight(String(18 * Math.min(nativeEnvironment.fontScale, Tokens.interfaceScaleFor(15)))+'vp')
          .width(this.kindLabelWidths.get(kind)).maxLines(1)
          .fontWeight(kind===this.workspace.kind ? 600 : FontWeight.Medium)
          .fontColor(kind===this.workspace.kind ? Colors.onAction : Colors.ink)
      }
    }.constraintSize({minHeight:34}).padding({left:this.kindsLayout===2 ? 8 : 12,right:this.kindsLayout===2 ? 8 : 12,top:8,bottom:8})
      .backgroundColor(kind===this.workspace.kind ? Colors.action : Colors.control)
      .animation(Motion.state(this.environment.reduceMotion))
      .accessibilitySelected(kind===this.workspace.kind).onClick(() => {
        Haptics.selection();
        Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => { this.workspace.setKind(kind); });
      }).id('knowledge.kind.'+kind)
  }
  @Builder kindBar() {
    Flex({wrap:FlexWrap.Wrap,space:{main:LengthMetrics.vp(this.kindsLayout===2 ? 4 : this.kindsLayout===0 ? 8 : 6),cross:LengthMetrics.vp(8)}}) {
      ForEach(this.visibleKinds(),(kind:string)=>{ this.kindChip(kind) },(kind:string)=>kind)
    }.width('100%').onAreaChange((_previous:Area,area:Area)=>{ const width=Number(area.width);if (width!==this.kindsWidth)this.kindsWidth=width; })
      .id('knowledge.kinds')
  }
  @Builder tagFilter() {
    if (this.workspace.tags.length || this.workspace.tag) {
      Button({ type: ButtonType.Normal, buttonStyle: ButtonStyleMode.TEXTUAL }) {
        Row({space:5}) {
          Text(this.workspace.tag ? '#' + this.workspace.tag : tr('knowledge_all_tags'))
            .fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.link).flexShrink(1).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis})
            .id('knowledge.filters.label')
          SymbolGlyph(Symbols.down).fontSize('12vp').fontColor([Colors.link])
        }
      }.constraintSize({minHeight:Math.max(34,Math.ceil(21*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(17)))+10),maxWidth:'100%'})
        .padding({left:8,right:8,top:5,bottom:5}).backgroundColor(Color.Transparent)
        .accessibilityText(tr('knowledge_filter_tag')).bindMenu(this.knowledgeFilters).id('knowledge.filters')
    }
  }
  @Builder libraryHeading(title:string,count:number,section:string) {
    Row({space:6}) {
      Text(title).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(FontWeight.Medium)
        .lineHeight(PhoneStyle.settingsExampleLineHeight(20.33,27.67,this.environment.fontScale)).fontColor(Colors.secondary).id('knowledge.group.title.'+section)
      Text(String(count)).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(FontWeight.Medium)
        .lineHeight(PhoneStyle.settingsExampleLineHeight(20.33,27.67,this.environment.fontScale)).fontColor(Colors.secondary).opacity(0.7)
      if (section==='ignored') SymbolGlyph(this.showIgnored ? Symbols.down : Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
    }.width('100%').padding({left:20,right:20,top:10,bottom:10}).accessibilityGroup(true)
      .accessibilityText(title+' '+String(count)).onClick(section==='ignored' ? ():void=>{
        Haptics.tap();
        Motion.run(this.getUIContext(), Motion.layout(this.environment.reduceMotion), () => { this.showIgnored=!this.showIgnored; });
      } : undefined).id('knowledge.group.'+section)
  }
  @Builder rootList() {
    Column({space:6}) {
      Row({space:4}) {
        Column() { this.searchField(false) }.layoutWeight(1)
        if (!this.landingMenu && !this.separateTagLine() && !this.workspace.query.trim() && (this.workspace.tags.length || this.workspace.tag))
          Button({type:ButtonType.Circle}) { SymbolGlyph(Symbols.filter).fontSize('16vp').fontColor([Colors.link]) }
            .width(38).height(38).padding(0).backgroundColor(Colors.surface).accessibilityText(tr('knowledge_filter_tag')+' · '+(this.workspace.tag || tr('knowledge_all_tags')))
            .bindMenu(this.knowledgeFilters).id('knowledge.filters')
      }.width('100%').onAreaChange((_:Area,area:Area)=>{this.filterWidth=Number(area.width);})
      if (!this.workspace.query.trim()) {
        Column() { this.kindBar() }.width('100%').margin({
          top:this.landingMenu ? PhoneStyle.formSize(48,41,this.environment.fontScale) : (this.environment.fontScale>1.3 ? 28 : 36),
          bottom:this.landingMenu ? PhoneStyle.formSize(9,3,this.environment.fontScale) : 0})
        if (!this.landingMenu && this.separateTagLine() && (this.workspace.tags.length || this.workspace.tag)) this.tagFilter()
      }
      Refresh({ refreshing: $$this.refreshing }) { List() {
        this.listStatus()
        if (!this.workspace.query.trim() && !this.isProjectList() && this.entryRowBindings.length) ListItem() {
          this.libraryHeading(this.workspace.tag ? '#' + this.workspace.tag : tr('all'),this.workspace.entries.length,'all')
        }
        if (this.isProjectList()) {
          ForEach(this.visibleProjectSections, (section: string) => {
            ListItem() {
              this.libraryHeading(this.sectionLabel(section),this.projectRows(section).length,section)
            }.margin({top:section===this.visibleProjectSections[0] ? 0 : 17.33})
            if (section !== 'ignored' || this.showIgnored) {
              if (this.knowledgeColumns() > 1) {
                ListItem() {
                  GridRow({columns:this.knowledgeColumns(),gutter:{x:workspaceGridGutter(this.knowledgeColumns()),y:workspaceGridGutter(this.knowledgeColumns())}}) {
                    ForEach(this.projectRows(section), (binding: BoundRow<ResourceRow>) => {
                      GridCol({span:1}) { this.libraryRow(binding.value, true) }
                    }, (binding: BoundRow<ResourceRow>) => binding.key)
                  }.width('100%')
                }
              } else {
                ListItemGroup() {
                  ForEach(this.projectRows(section), (binding: BoundRow<ResourceRow>) => {
                    ListItem() { this.libraryRow(binding.value) }
                  }, (binding: BoundRow<ResourceRow>) => binding.key)
                }.divider({strokeWidth:0.5,color:Colors.divider,startMargin:68,endMargin:20}).backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
                  .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
                  .shadow(PhoneStyle.cardElevation(this.environment.dark)).clip(true)
              }
            }
          }, (section: string) => section)
        } else if (this.entryRowBindings.length || this.resultRowBindings.length) {
          if (this.knowledgeColumns() > 1) {
            ListItem() {
              GridRow({columns:this.knowledgeColumns(),gutter:{x:workspaceGridGutter(this.knowledgeColumns()),y:workspaceGridGutter(this.knowledgeColumns())}}) {
                if (this.workspace.query.trim()) {
                  ForEach(this.resultRowBindings, (binding:BoundRow<KnowledgeSearchHit>) => {
                    GridCol({span:1}) { this.searchHit(binding.value) }
                  }, (binding:BoundRow<KnowledgeSearchHit>) => binding.key)
                } else {
                  ForEach(this.entryRowBindings, (binding:BoundRow<ResourceRow>) => {
                    GridCol({span:1}) { this.libraryRow(binding.value, true) }
                  }, (binding:BoundRow<ResourceRow>) => binding.key)
                }
              }.width('100%')
            }
          } else {
            ListItemGroup() {
              this.sectionEntries()
            }.divider({strokeWidth:0.5,color:Colors.divider,startMargin:this.workspace.query.trim() ? 56 : 68,endMargin:this.workspace.query.trim() ? 0 : 20})
              .backgroundColor(Colors.surface).borderRadius(this.workspace.query.trim() ? Tokens.radiusCard : Tokens.radiusPanel)
              .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:this.workspace.query.trim() ? Tokens.radiusCard : Tokens.radiusPanel})
              .shadow(PhoneStyle.cardElevation(this.environment.dark)).clip(true)
          }
        }

      }.width('100%').height('100%') }.onRefreshing(() => {
        this.workspace.refresh().finally(() => { this.refreshing=false; });
      }).layoutWeight(1).width('100%').id('knowledge.entries')
    }.width('100%').height('100%').padding({left:this.knowledgeSidePadding(),right:this.knowledgeSidePadding(),top:this.landingMenu ? 4 : 6,bottom:8}).backgroundColor(Colors.grouped)
      .onAreaChange((_:Area,area:Area)=>{ const w=Number(area.width); if (Number.isFinite(w) && w>0 && Math.abs(w-this.rootWidth)>0.5) this.rootWidth=w; })
  }
  @Builder transcriptionLine() {
    Row({space:8}) {
      if (this.pickedHub()) SymbolGlyph($r('sys.symbol.ellipsis_bubble')).fontSize(Tokens.interfaceFontVp(17,nativeEnvironment.fontScale)).fontColor([Colors.secondary])
      else Image($r('sys.media.ohos_ic_public_device_phone')).width(this.recordingMetric(15,21.3)).height(this.recordingMetric(17,23))
        .fillColor(this.hubUnavailable() ? Colors.working : Colors.secondary).accessibilityLevel('no')
      Text(tr('meeting_transcription_label')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15))
        .fontColor(this.readyForHub() ? Colors.link : Colors.secondary).opacity(this.readyForHub() ? 0.5 : 1)
      Text(this.hubUnavailable() ? tr('meeting_hub_unavailable') : this.sourceName(this.pickedHub()))
        .fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium)
        .fontColor(this.hubUnavailable() ? Colors.working : Colors.ink).layoutWeight(1).textAlign(TextAlign.Start)
      if (this.readyForHub()) Row() {
        PhoneOutlineIcon({name:'choices',iconScale:this.recordingMetric(1,1.33),color:Colors.link})
      }.opacity(0.25)
    }.width('100%').constraintSize({minHeight:PhoneStyle.groupedRowHeight(nativeEnvironment.fontScale)}).padding({left:20,right:20})
  }
  @Builder transcriptionMenu() {
    Menu() {
      ForEach(['device','hub'],(mode:string) => {
        MenuItem({content:this.sourceName(mode==='hub')}).contentFont({size:Tokens.interfaceFontVp(16,nativeEnvironment.fontScale)}).selected(this.pickedHub()===(mode==='hub')).selectIcon(true)
          .onClick(() => this.chooseMode(mode==='hub')).id('recordings.transcription.'+mode)
      },(mode:string)=>mode)
    }.width(250)
  }

  @Builder meetingFormDivider() {
    Divider().strokeWidth(1).color(Colors.divider).margin({left:20,right:20})
  }

  @Builder meetingInviteMenu() {
    Menu() {
      MenuItem({content:tr('phone_meeting_remove')}).contentFontColor(Colors.danger)
        .onClick(() => { this.invitationOverride='off'; }).id('recordings.invite.remove')
    }
  }

  @Builder meetingPeople() {
    Row({space:16}) {
      Column({space:4}) {
        Text(tr('you').slice(0,1)).width(40).height(40).fontSize(Tokens.interfaceFontVp(Tokens.typeHeading,nativeEnvironment.fontScale))
          .fontWeight(FontWeight.Medium).textAlign(TextAlign.Center).backgroundColor(Colors.control).borderRadius(Tokens.radiusCapsule).fontColor(Colors.ink)
        Text(tr('you')).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(Colors.ink)
          .lineHeight(String(this.recordingMetric(15.7,23))+'vp')
      }
      if (this.roomRole()==='off') {
        Button({type:ButtonType.Normal}) {
          Column({space:4}) {
            Stack() {
              SymbolGlyph($r('sys.symbol.person_badge_plus')).fontSize(Tokens.interfaceFontVp(19,nativeEnvironment.fontScale)).fontColor([Colors.link])
            }.width(40).height(40).border({width:1,radius:Tokens.radiusCapsule,color:Colors.link,style:BorderStyle.Dashed})
            Text(tr('phone_meeting_invite')).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(Colors.link)
              .lineHeight(String(this.recordingMetric(15.7,23))+'vp')
          }
        }.padding(0).backgroundColor(Color.Transparent).accessibilityText(tr('phone_meeting_invite'))
          .onClick(() => { this.invitationOverride='invite'; }).id('recordings.invite')
      } else {
        Button({type:ButtonType.Normal}) {
          Column({space:4}) {
            Image(this.look.avatarUri || $r('app.media.phone_persona_face')).width(40).height(40).objectFit(ImageFit.Cover).borderRadius(Tokens.radiusCapsule)
            Text(tr('persona')).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(Colors.ink)
              .lineHeight(String(this.recordingMetric(15.7,23))+'vp')
            Text(tr(meetingRoleLabel(this.roomRole()))).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontColor(Colors.secondary)
              .lineHeight(String(this.recordingMetric(14.3,21.7))+'vp').id('recordings.invite.detail')
          }
        }.padding(0).backgroundColor(Color.Transparent).accessibilitySelected(true)
          .accessibilityText(tr('persona')+' · '+tr(meetingRoleLabel(this.roomRole())))
          .bindMenu(this.meetingInviteMenu).id('recordings.invite')
      }
    }.width('100%').alignItems(VerticalAlign.Top).padding({left:20,right:20,top:18.5,bottom:18.5})
  }

  @Builder recordingHome() {
    Column({space:6}) {
      List() {
        ListItem() {
          Column() {
            Button({type:ButtonType.Normal}) {
              Row({space:12}) {
                PhoneOutlineIcon({name:'calendarAgenda',iconScale:this.recordingMetric(1,1.33),color:Colors.link})
                Text(tr('phone_meeting_calendar')).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeHeading)).fontColor(Colors.link).layoutWeight(1)
                if (this.calendarState==='loading') LoadingProgress().width(18).height(18)
              }.width('100%')
            }.width('100%').constraintSize({minHeight:this.recordingMetric(52.3,59.3)}).padding({left:20,right:20,top:8,bottom:8})
              .backgroundColor(Color.Transparent).onClick(() => this.readMeetingCalendar()).id('recordings.calendar')
            if (this.calendarState && !['loading','ready'].includes(this.calendarState))
              Text(tr(this.calendarState)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor(Colors.secondary).width('100%').padding({left:15,right:15,bottom:9})
            ForEach(this.calendarEvents,(event:MeetingOccurrence) => {
              Button({type:ButtonType.Normal}) {
                Text(this.clockTime(event.start)+'  '+event.title).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontColor(Colors.ink).width('100%')
              }.width('100%').constraintSize({minHeight:44}).padding({left:15,right:15}).backgroundColor(Color.Transparent)
                .onClick(() => { this.setMeetingKind('meeting'); this.meetingTitle=event.title; this.selectedCalendarEvent=event.id; this.calendarEvents=[]; this.calendarState=''; })
            },(event:MeetingOccurrence) => event.key())
          }.width('100%').borderRadius(Tokens.radiusPanel).backgroundColor(Colors.surface)
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
            .shadow(PhoneStyle.cardElevation(this.environment.dark))
        }
        ListItem() {
          Column() {
            Row() {
              TextInput({text:this.meetingTitle,placeholder:this.defaultRecordingTitle(this.meetingKind)})
                .placeholderFont({size:this.placeholderSize(),weight:FontWeight.Medium}).placeholderColor(Colors.tertiary).width('100%')
                .onAreaChange((_:Area,next:Area)=>{this.recordingTitleWidth=Number(next.width);})
                .height(this.meetingTitle ? Math.max(44,Math.ceil(24*nativeEnvironment.fontScale)+8) : 44)
                .padding(0).borderRadius(0).enterKeyType(EnterKeyType.Done)
                .backgroundColor(Color.Transparent).onChange((v:string) => { this.meetingTitle=v; this.selectedCalendarEvent=''; }).id('recordings.title')
            }.width('100%').padding({left:20,right:20}).constraintSize({minHeight:this.recordingMetric(51.3,58.3)})
            this.meetingFormDivider()
            Row() {
              Row() {
                ForEach(['meeting','note'],(kind:string) => {
                  Button({type:ButtonType.Capsule}) {
                    Text(tr(kind==='meeting' ? 'phone_record_meeting' : 'phone_record_note')).fontSize(Tokens.typeCaption)
                      .fontWeight(this.meetingKind===kind ? FontWeight.Bold : FontWeight.Normal).fontColor(Colors.ink)
                      .width('100%').height(29).textAlign(TextAlign.Center).borderRadius(Tokens.radiusCapsule)
                      .backgroundColor(this.meetingKind===kind ? Colors.surface : Color.Transparent)
                      .animation(Motion.state(this.environment.reduceMotion))
                  }.layoutWeight(1).height(32).padding({left:1.5,right:1.5}).responseRegion({x:0,y:-6,width:'100%',height:44})
                    .backgroundColor(Color.Transparent)
                    .accessibilitySelected(this.meetingKind===kind).onClick(() => { Haptics.selection(); this.setMeetingKind(kind); }).id('recordings.kind.'+kind)
                },(kind:string) => kind)
              }.width('100%').height(32).borderRadius(Tokens.radiusCapsule).backgroundColor(Colors.control)
            }.width('100%').constraintSize({minHeight:60}).padding({left:20,right:20})
            if (this.meetingKind==='meeting' && this.canInvite()) {
              this.meetingFormDivider()
              this.meetingPeople()
            }
            if (this.meetingKind==='note') {
              this.meetingFormDivider()
              Row() {
                Text(tr('meeting_note_without_minutes')).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(Colors.secondary)
                  .layoutWeight(1)
              }.width('100%').constraintSize({minHeight:this.recordingMetric(51.3,58.3)}).padding({left:20,right:20,top:7,bottom:7})
                .accessibilityGroup(true).id('recordings.kind.detail')
            }
            this.meetingFormDivider()
              if (this.readyForHub()) {
                Button({type:ButtonType.Normal}) { this.transcriptionLine() }
                  .width('100%').padding(0).backgroundColor(Color.Transparent)
                  .accessibilityText(tr('meeting_transcription_label')+' · '+this.sourceName(this.pickedHub()))
                  .bindMenu(this.transcriptionMenu).id('recordings.transcription')
              } else Column() { this.transcriptionLine() }.width('100%').accessibilityGroup(true)
                .accessibilityText(tr('meeting_transcription_label')+' · '+(this.hubUnavailable() ? tr('meeting_hub_unavailable') : this.sourceName(false)))
                .id('recordings.transcription')
          }.width('100%').alignItems(HorizontalAlign.Start).borderRadius(Tokens.radiusPanel).backgroundColor(Colors.surface)
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
            .shadow(PhoneStyle.cardElevation(this.environment.dark))
        }.margin({top:35})
        ListItem() {
          Text(this.modePrivacy(this.pickedHub())).id('recordings.privacy').width('100%').fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .lineHeight(String(this.recordingMetric(15.7,22.5))+'vp').fontColor(Colors.secondary).padding({left:20,right:20,top:7,bottom:7})
        }
        if (this.visibleLocal().length) ListItem() { PhoneSectionTitle({text:tr('phone_record_local'),trailing:String(this.visibleLocal().length)}) }.margin({top:18})
        ForEach(this.visibleLocal(), (entry:KnowledgeRecordingEntry) => {
          ListItem() {
            Row({space:10}) {
              SymbolGlyph($r('sys.symbol.mic')).fontSize('19vp').fontColor([Colors.danger])
              Column({space:3}) {
                Text(entry.title || this.localDate(entry.startedAt)).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(2)
                Text(this.clock(entry.duration) + ' · ' + this.localDate(entry.startedAt)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(Colors.secondary)
              }.layoutWeight(1).alignItems(HorizontalAlign.Start)
              SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
            }.width('100%').padding(14).borderRadius(PhoneStyle.cardRadius).backgroundColor(Colors.surface)
              .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
              .shadow(PhoneStyle.cardElevation(this.environment.dark))
              .onClick(() => { Haptics.tap(); this.openRecording(entry.id); }).id('recordings.local.' + entry.id)
          }.margin({top:10})
        }, (entry:KnowledgeRecordingEntry) => JSON.stringify([entry.id,entry.title,entry.duration,entry.uploadState,entry.transcriptionState]))
        if (this.demoRecordingEntries().length > 0) ListItem() {
          PhoneSectionTitle({ text: tr('knowledge_kind_recordings'), trailing: String(this.demoRecordingEntries().length) })
        }.margin({ top: 18 })
        ForEach(this.demoRecordingEntries(), (entry: ResourceRow) => {
          ListItem() { this.libraryRow(entry, true) }.margin({ top: 10 })
        }, (entry: ResourceRow) => entry.key())
        if (!this.workspace.query.trim()) ListItem() {
          Row({space:12}) {
            PhoneOutlineIcon({name:'books',iconScale:this.recordingMetric(1,1.33),color:Colors.link})
            Text(tr('phone_all_meetings')).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeHeading)).fontColor(Colors.ink).layoutWeight(1)
            SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
          }.width('100%').padding({left:20,right:20,top:6,bottom:6}).constraintSize({minHeight:this.recordingMetric(52.3,59.3)}).backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
            .shadow(PhoneStyle.cardElevation(this.environment.dark))
            .onClick(() => { Haptics.tap(); this.onAllMeetings(); }).id('recordings.all')
        }.margin({top:18})
      }.width('100%').contentStartOffset(35).layoutWeight(1).id('recordings.list')
      if (this.model.isRecordingModuleAllowed() || this.recording.canStop()) {
        Row() {
          Button({type:ButtonType.Capsule}) {
            Row({space:8}) {
              if (this.recording.captureState === 'recording') {
                NeedsYouPulseDot({ dotSize: 8, tint: Colors.onAction, surface: Colors.danger, bordered: false })
              }
              SymbolGlyph($r('sys.symbol.waveform_circle')).fontSize(Tokens.interfaceFontVp(17, nativeEnvironment.fontScale)).fontWeight(600).fontColor([Colors.onAction])
              Text(tr(this.recording.captureState === 'recording' ? 'knowledge_recording_active' : this.meetingKind==='meeting' ? 'phone_meeting_start' : 'phone_start_note'))
                .fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).minFontSize(10.2).maxFontSize(17).fontWeight(600)
                .fontColor(Colors.onAction).textAlign(TextAlign.Center).maxLines(1).flexShrink(1).id('recordings.capture.label')
            }.width('100%').justifyContent(FlexAlign.Center)
          }.width('100%').constraintSize({minHeight:meetingCaptureHeight(nativeEnvironment.fontScale, this.recordingPageHeight)})
            .padding({left:14,right:14,top:6,bottom:6}).backgroundColor(this.recording.canStop() ? Colors.danger : Colors.action)
            .borderWidth(Colors.systemControls && !this.recording.canStop() ? 0.67 : 0).borderColor(Colors.prominentRim)
            .shadow(PhoneStyle.cardElevation(this.environment.dark))
            .enabled(!this.model.demo && (this.recording.canStart() || this.recording.canStop()))
            .onTouch((event: TouchEvent) => { if (event.type === TouchType.Down && !this.model.demo && (this.recording.canStart() || this.recording.canStop())) Haptics.tap(); })
            .onClick(() => {
              if (this.recording.canStop()) this.openRecording().catch(() => undefined);
              else this.requestRecordingStart();
            }).id('recordings.capture')
        }.width('100%').padding({top:4,bottom:4}).id('knowledge.recording.open')
      } else {
        Text(tr('xiaoyi_recording_module_off'))
          .fontSize(Tokens.meta)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.meta))
          .fontColor(Colors.secondary)
          .width('100%')
          .padding({left:16,right:16,top:12,bottom:12})
          .backgroundColor(Colors.surface)
          .borderRadius(Tokens.radiusCard)
          .id('recordings.module.off')
      }
    }.width('100%').height('100%').padding({left:this.knowledgeSidePadding(),right:this.knowledgeSidePadding(),top:4,bottom:14}).backgroundColor(Colors.grouped).id('recordings.home')
      .onAreaChange((_: Area, next: Area) => {
        const height = Number(next.height); if (height !== this.recordingPageHeight) this.recordingPageHeight = height;
        const width = Number(next.width); if (Number.isFinite(width) && width > 0 && Math.abs(width - this.rootWidth) > 0.5) this.rootWidth = width;
      })
      .bindSheet($$this.recordingOpen,this.recordingSheet(),{title:{title:tr('knowledge_recording_open')},height:SheetSize.LARGE,showClose:true,
        onDisappear:() => { this.recording.stopForBackground().catch(() => undefined); }})
  }
  @Builder listContent() {
    if (this.initialKind === 'recordings' && !this.localMeetings) this.recordingHome()
    else this.rootList()
  }

  private openSafeLink(url: string): void { if (/^https?:\/\//i.test(url.trim())) this.onLink(url.trim()); }

  private askPersona(): void {
    this.workspace.askPersona().then((row: ResourceRow | undefined) => { if (row) this.onChat(row); }).catch(() => undefined);
  }

  private confirmDeleteRecording(): void {
    if (!this.workspace.canDeleteRecording()) return;
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = this.workspace.selected.key();
    this.getUIContext().showAlertDialog({ title: tr('knowledge_forget_recording'), message: tr('knowledge_forget_recording_note'),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => {
        if (!this.model.owns(generation, scope) || this.workspace.selected.key() !== target) return;
        this.workspace.deleteRecording().catch(() => undefined);
      } } });
  }

  private confirmDispatch(): void {
    const machine = this.workspace.machineForDirectory(this.workspace.targetMachines);
    if (!machine || !this.workspace.agentDraft.trim() || !this.workspace.agentLauncher.trim()) return;
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = this.workspace.selected.key();
    this.getUIContext().showAlertDialog({ title: tr('knowledge_start_agent'), message: machine.title + ' · ' + this.workspace.directoryTargets[this.workspace.selectedDirectory]?.path,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('knowledge_start_agent'), action: () => {
        if (!this.model.owns(generation, scope) || this.workspace.selected.key() !== target) return;
        this.workspace.dispatchAgent(machine, this.workspace.agentDraft, this.workspace.agentLauncher)
          .then((row: ResourceRow | undefined) => { if (row) this.onChat(row); }).catch(() => undefined);
      } } });
  }

  private toggleAction(id: string): void {
    this.selectedActions = this.selectedActions.includes(id) ? this.selectedActions.filter((value: string) => value !== id) : this.selectedActions.concat([id]);
  }

  private confirmAddTodos(): void {
    if (!this.selectedActions.length) return;
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = this.workspace.selected.key();
    this.getUIContext().showAlertDialog({ title: tr('knowledge_minutes_add_todos'), message: String(this.selectedActions.length),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('knowledge_minutes_add_todos'), action: () => {
        if (!this.model.owns(generation, scope) || this.workspace.selected.key() !== target) return;
        this.workspace.addMinutesTodos(this.selectedActions.slice()).then((saved: boolean) => { if (saved) this.selectedActions = []; }).catch(() => undefined);
      } } });
  }

  private editSaveKey(): string {
    if (this.workspace.writeState === 'unknown') return 'result_unknown';
    return ['sending', 'pending'].includes(this.workspace.writeState) ? this.workspace.writeState : 'save';
  }

  @Builder metadataForm() {
    Scroll() {
      Column({space:Tokens.gap}) {
        if (this.phoneRecordingDetail && this.recordingTools) {
          Button(tr('back'),{buttonStyle:ButtonStyleMode.TEXTUAL}).fontColor(Colors.link)
            .onClick(() => this.recordingTools=false).id('knowledge.recording.tools.back')
          this.recordingToolContents()
        } else {
          this.editForm()
          if (this.phoneRecordingDetail) this.recordingToolsButton()
        }
      }.width('100%').alignItems(HorizontalAlign.Start).padding(16)
    }
      .width('100%').height('100%').align(Alignment.TopStart).backgroundColor(Colors.grouped)
      .id(this.phoneRecordingDetail && this.recordingTools ? 'knowledge.recording.tools.sheet' : 'knowledge.recording.metadata.sheet')
  }

  @Builder editForm() {
    Column({ space: Tokens.gap }) {
      Text(tr('knowledge_edit_metadata')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
      Select(['P0', 'P1', 'P2', 'P3'].map((value: string): SelectOption => ({ value: value }))).attributeModifier(SelectStyles.inline).value(this.workspace.editImportance)
        .onSelect((index: number) => this.workspace.setEditImportance(['P0', 'P1', 'P2', 'P3'][index] || 'P2')).id('knowledge.edit.importance')
      TextInput({ text: this.workspace.editRelevance, placeholder: tr('knowledge_relevance') }).type(InputType.Number)
        .onChange((value: string) => this.workspace.setEditRelevance(value)).id('knowledge.edit.relevance')
      TextInput({ text: this.workspace.editTags, placeholder: tr('knowledge_tags') }).onChange((value: string) => this.workspace.setEditTags(value)).id('knowledge.edit.tags')
      if (this.workspace.selected.path === 'projects') {
        Select([{ value: tr('knowledge_project_short') }, { value: tr('knowledge_project_long') }]).attributeModifier(SelectStyles.inline)
          .value(this.workspace.editProjectKind === 'long' ? tr('knowledge_project_long') : tr('knowledge_project_short') )
          .onSelect((index: number) => this.workspace.setEditProjectKind(index === 1 ? 'long' : 'short')).id('knowledge.edit.project_kind')
        Select([{ value: tr('knowledge_status_active') }, { value: tr('knowledge_status_paused') },
          { value: tr('knowledge_status_done') }, { value: tr('knowledge_status_ignored') }]).attributeModifier(SelectStyles.inline)
          .value(tr('knowledge_status_' + this.workspace.editStatus))
          .onSelect((index: number) => this.workspace.setEditStatus(['active', 'paused', 'done', 'ignored'][index] || 'active')).id('knowledge.edit.status')
      }
      Row({ space: Tokens.gap }) {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('cancel')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('cancel', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('cancel')).bindTips(tr('cancel')).onClick(() => this.workspace.editOpen = false)
        Button({ type: ButtonType.Circle }) { if (IconActions.busy(this.editSaveKey())) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.onAction); else SymbolGlyph(IconActions.symbol(this.editSaveKey())).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.editSaveKey(), true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr(this.editSaveKey())).bindTips(tr(this.editSaveKey())).enabled(this.workspace.canSaveEdit())
          .onClick(() => this.workspace.saveEdit()).id('knowledge.edit.save')
      }.width('100%').justifyContent(FlexAlign.End)
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
  }

  private openFacts():void { this.facts.begin(Wire.text(Wire.object(this.workspace.detail,'meta'),'pinned')); }

  @Builder entryEvidence() {
    if (this.workspace.agents.length) {
      Column({ space: Tokens.gap }) {
        Text(tr('knowledge_project_agents')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
        ForEach(this.agentRowBindings, (binding: BoundRow<ResourceRow>) => Button({ type: ButtonType.Normal }) { this.navigationLabel(binding.value.title || binding.value.id) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).padding(0).width('100%')
          .onClick(() => this.onChat(binding.value)).id('knowledge.agent.' + binding.value.id), (binding: BoundRow<ResourceRow>) => binding.key)
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
    if (this.workspace.matches.length) {
      Column({ space: Tokens.gap }) {
        Text(tr('knowledge_search_matches')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
        ForEach(this.workspace.matches, (match: Record<string, Object>, index: number) => Text(Wire.text(match, 'line'))
          .fontSize(Tokens.meta).width('100%').id('knowledge.match.' + String(index)),
          (match: Record<string, Object>, index: number) => JSON.stringify([index, Wire.text(match, 'id'), JSON.stringify(match)]))
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
    if (Wire.strings(this.workspace.detail, 'log').length) {
      Column({ space: Tokens.gap }) { Text(tr('knowledge_timeline')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
        ForEach(Wire.strings(this.workspace.detail, 'log').slice().reverse(), (line: string, index: number) => Text(line).fontSize(Tokens.meta).width('100%')
          .id('knowledge.timeline.' + String(index)), (line: string, index: number) => JSON.stringify([index, line]))
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
  }

  private factsChanged():boolean { return this.facts.text.trim()!==this.facts.baseline.trim(); }
  private factsOwner():string { return JSON.stringify([this.model.scope(),this.model.connectionGeneration(),this.workspace.selected.key()]); }
  private focusEmptyFacts():void {
    if (this.facts.baseline || !this.facts.open) return;
    const owner=this.factsOwner();
    setTimeout(() => {
      if (this.facts.open && !this.facts.baseline && this.factsOwner()===owner) {
        try { this.getUIContext().getFocusController().requestFocus('knowledge.facts.text'); } catch (_) { }
      }
    },150);
  }
  private async deleteFacts():Promise<void> {
    const owner=this.factsOwner(), previous=this.facts.baseline;
    if (!previous || this.workspace.busy) return;
    this.facts.close();
    if (await this.workspace.saveFacts('',previous) && owner===this.factsOwner()) {
      this.removedFactsOwner=owner; this.removedFacts=previous;
      const notice=++this.removedFactsNotice;
      setTimeout(() => {
        if (this.removedFactsNotice===notice && this.removedFactsOwner===owner) {
          this.removedFacts=''; this.removedFactsOwner='';
        }
      },5000);
    }
  }
  private async undoFacts():Promise<void> {
    const value=this.removedFacts,owner=this.factsOwner();
    if (!value || this.removedFactsOwner!==owner || this.workspace.busy) return;
    this.removedFacts=''; this.removedFactsOwner='';
    await this.workspace.saveFacts(value,'');
  }
  private async submitFacts():Promise<void> {
    if (this.facts.baseline && !this.facts.text.trim()) await this.deleteFacts();
    else await this.facts.save(this.workspace);
  }

  @Builder recordingUndoNotice() {
    if (this.removedFacts && this.removedFactsOwner===this.factsOwner() && !Wire.text(Wire.object(this.workspace.detail,'meta'),'pinned')) {
      Row({space:12}) {
        Stack() { SymbolGlyph(Symbols.check).fontSize('11vp').fontColor([Color.White]) }
          .width(20).height(20).borderRadius(Tokens.radiusCapsule).backgroundColor(Colors.positive)
        Text(tr('recording_facts_deleted')).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontWeight(FontWeight.Medium)
          .fontColor(Colors.ink).maxLines(3).flexShrink(1)
        Button({type:ButtonType.Normal}) {
          Text(tr('ks_undo')).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontWeight(FontWeight.Medium).fontColor(Colors.link)
        }.height(32).padding(0).backgroundColor(Color.Transparent).enabled(!this.workspace.busy)
          .accessibilityText(tr('ks_undo')).onClick(() => { this.undoFacts().catch(() => {}); }).id('knowledge.facts.undo')
      }.constraintSize({maxWidth:'94%',minHeight:44}).margin({top:8}).padding({left:16,right:16,top:4,bottom:4})
        .backgroundColor(Colors.surface).borderRadius(Tokens.radiusCapsule)
        .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.floatingBorder(this.environment.dark),radius:Tokens.radiusCapsule})
        .shadow(PhoneStyle.floatingElevation(this.environment.dark)).hitTestBehavior(HitTestMode.Transparent).id('knowledge.facts.undo_notice')
    }
  }

  @Builder recordingFactsEditor() {
    Column() {
      Row({space:8}) {
        Button({type:ButtonType.Circle}) { SymbolGlyph($r('sys.symbol.xmark')).fontSize('16vp').fontColor([Colors.ink]) }
          .height(44).width(44).padding(0).backgroundColor(Colors.control)
          .accessibilityText(tr('close')).enabled(!this.workspace.busy).onClick(() => this.facts.close()).id('knowledge.facts.cancel')
        Text(tr('phone_kb_confirmed')).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(600)
          .fontColor(Colors.ink).textAlign(TextAlign.Center).layoutWeight(1).maxLines(2)
        Button({type:ButtonType.Capsule}) {
          if (this.workspace.busy) LoadingProgress().width(19).height(19).color(Colors.link)
          else Text(tr('save')).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(600).fontColor(this.factsChanged() ? Colors.link : Colors.secondary)
        }.height(44).constraintSize({minWidth:50}).padding({left:9,right:9}).backgroundColor(Colors.control)
          .enabled(this.workspace.supportsExpectedPinned && !this.workspace.busy && !['pending','unknown'].includes(this.workspace.writeState) && this.factsChanged())
          .onClick(() => this.submitFacts()).id('knowledge.facts.save')
      }.width('100%').constraintSize({minHeight:74}).padding({left:16,right:16,top:4,bottom:4})
      Scroll() {
        Column({space:8}) {
          TextArea({text:this.facts.text}).accessibilityText(tr('phone_kb_confirmed'))
            .width('100%').height(this.recordingMetric(170,214)).padding({left:20,right:20,top:15,bottom:15})
            .fontSize(Tokens.interfaceFontVp(Tokens.typeBody,nativeEnvironment.fontScale)).fontColor(Colors.ink).backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
            .enabled(!this.workspace.busy).onChange((v:string) => this.facts.edit(v)).id('knowledge.facts.text').onAppear(() => this.focusEmptyFacts())
          Text(tr('recording_facts_instruction').replace('{name}',this.workspace.selected.title || this.workspace.selected.id))
            .fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(Colors.secondary).width('100%')
            .padding({left:20,right:20,bottom:9}).id('knowledge.facts.instruction')
          if (this.facts.conflict) {
            Text(tr('phone_facts_changed')).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontColor(Colors.needs).width('100%')
            Text(this.facts.baseline || tr('empty')).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontColor(Colors.ink)
              .width('100%').padding(16).backgroundColor(Colors.surface).borderRadius(Tokens.radiusCard).copyOption(CopyOptions.LocalDevice).id('knowledge.facts.latest')
          } else if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor(Colors.danger).width('100%')
          if (this.facts.baseline) Button(tr('recording_facts_delete'),{type:ButtonType.Capsule})
            .width('100%').constraintSize({minHeight:52}).fontColor(Colors.danger).backgroundColor(Colors.surface)
            .enabled(!this.workspace.busy).onClick(() => { this.deleteFacts().catch(() => {}); }).id('knowledge.facts.delete')
        }.width('100%').alignItems(HorizontalAlign.Start).padding({left:16,right:16,top:30,bottom:32})
      }.layoutWeight(1).width('100%').align(Alignment.TopStart)
    }.width('100%').height('100%').backgroundColor(Colors.grouped).id('knowledge.facts.sheet')
  }

  private recordingMetric(regular:number,largest:number):number {
    return PhoneStyle.formSize(regular,largest,nativeEnvironment.fontScale);
  }
  private demoRecordingEntries(): ResourceRow[] {
    if (!this.model.canPreviewDemoRecordings()) return [];
    const q = this.workspace.query.trim().toLowerCase();
    return this.workspace.entries.filter((row: ResourceRow) =>
      row.path === 'recordings' && (!q || [row.title, row.detail].join(' ').toLowerCase().includes(q))
    );
  }

  private transcriptView(detail: Record<string, Object>): TranscriptViewData {
    return buildTranscriptView(detail);
  }

  private transcriptRowsShown(view: TranscriptViewData): TranscriptRow[] {
    if (this.transcriptReviewOpen && this.transcriptBookmarksOnly) {
      return filterTranscriptBookmarks(view.rows);
    }
    return view.rows;
  }

  private transcriptMatches(detail: Record<string, Object>): TranscriptRow[] {
    if (!this.transcriptReviewOpen) return [];
    const view = this.transcriptView(detail);
    return findTranscriptMatches(
      view.rows,
      this.transcriptSearchQuery,
      tr('recording_bookmark'),
      this.transcriptBookmarksOnly
    );
  }

  private jumpTranscriptMatch(matches: readonly TranscriptRow[], direction: 1 | -1): void {
    const nextId = stepTranscriptMatch(matches, this.transcriptSelectedId || null, direction);
    if (!nextId) return;
    Haptics.tap();
    this.transcriptSelectedId = nextId;
  }

  private copyRetainedTranscript(rows: readonly TranscriptRow[]): void {
    const text = exportTranscriptText(rows, tr('recording_bookmark'));
    if (!text) return;
    Haptics.success();
    const board = pasteboard.getSystemPasteboard();
    board.setData(pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN, text)).then(() => {
      toastCenter.success(tr('recording_transcript_copied'));
    }).catch(() => {
      toastCenter.error(tr('failed'));
    });
  }

  @Builder recordingHeading(key:string) {
    Row() {
      Text(tr(key)).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Medium)
        .fontColor(Colors.secondary).width('100%')
    }.width('100%').constraintSize({minHeight:this.recordingMetric(40.3,47.7)}).padding({left:20,right:20})
  }
  @Builder recordingFootnote(key:string) {
    Text(tr(key)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
      .lineHeight(String(this.recordingMetric(15.7,22.5))+'vp').width('100%').padding({left:20,right:20,top:7,bottom:7})
  }

  @Computed get importedRecordingNotes():Record<string,Object>|undefined {
    const id=Wire.text(Wire.object(Wire.object(this.workspace.detail,'meta'),'imported_meeting'),'id');
    if (!id) return undefined;
    const linked=Wire.rows(this.workspace.detail,'links').find((item:Record<string,Object>) => Wire.text(item,'kind')==='meetings' && Wire.text(item,'id')===id);
    return linked && /^https?:\/\//i.test(Wire.text(linked,'extra').trim()) ? linked : undefined;
  }
  private recordingToolsTitle():string { return tr('knowledge_minutes')+' · '+tr('knowledge_transcript'); }
  @Builder recordingToolsButton() {
    Button({type:ButtonType.Normal}) {
      Row() {
        Text(this.recordingToolsTitle()).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink).layoutWeight(1)
        SymbolGlyph(this.recordingTools ? Symbols.down : Symbols.expand).fontSize('14vp').fontColor([Colors.secondary])
      }.width('100%')
    }.width('100%').constraintSize({minHeight:44}).margin({top:16}).padding(12).backgroundColor(Colors.surface).borderRadius(Tokens.radiusCard)
      .accessibilityDescription(tr(this.recordingTools ? 'control_expanded' : 'control_collapsed')).onClick(() => this.recordingTools=!this.recordingTools).id('knowledge.recording.tools')
  }
  @Builder recordingToolContents() {
    this.recordingSection(UIUtils.makeBinding(():Record<string,Object> => this.workspace.detail))
    this.relatedEntries(UIUtils.makeBinding(():Record<string,Object> => this.workspace.detail))
    if (this.importedMeeting()) Button(this.importedMeeting()!.title,{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale).onClick(() => {
      const item=this.importedMeeting(); if (item) this.onOpenResource(item);
    }).id('knowledge.recording.imported')
    if (!this.phoneRecordingDetail) Button(tr('knowledge_edit_metadata'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale).onClick(() => this.workspace.beginEdit()).id('knowledge.edit.open')
    if (!this.phoneRecordingDetail && this.workspace.editOpen && !this.metadataSheet) this.editForm()
    Button(tr('knowledge_forget_recording'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.danger)
      .enabled(this.workspace.canDeleteRecording()).onClick(() => this.confirmDeleteRecording()).id('knowledge.recording.delete')
  }

  @Builder recordingDetail() {
    Stack({alignContent:Alignment.Top}) {
      this.recordingDetailScroll()
      this.recordingUndoNotice()
    }.width('100%').height('100%')
  }

  @Builder recordingDetailScroll() {
    Scroll() {
      Column() {
        Column() {
          Row({space:12}) {
            Stack() { SymbolGlyph($r('sys.symbol.waveform_circle')).fontSize('22vp').fontColor([Colors.secondary]) }
              .width(48).height(48).backgroundColor(Colors.control).borderRadius(Tokens.radiusCard).id('knowledge.recording.avatar')
            Text(this.workspace.selected.title || this.workspace.selected.id).fontSize(Tokens.typeTitle).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeTitle)).fontWeight(600).fontColor(Colors.ink).layoutWeight(1).maxLines(3)
          }.width('100%')
          if (this.workspace.selected.detail) Text(this.workspace.selected.detail).fontSize(Tokens.body)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontColor(Colors.ink).opacity(0.85).lineHeight(String(this.recordingMetric(20.3,28.35))+'vp').width('100%').margin({top:12}).id('knowledge.recording.intro')
          Button({type:ButtonType.Capsule}) {
            Row({space:12}) {
              PhoneOutlineIcon({name:'sparkles',iconScale:this.recordingMetric(1,1.33),color:Colors.onAction})
              Text(tr('recording_detail_ask')).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.onAction).textAlign(TextAlign.Center)
                .flexShrink(1).id('knowledge.ask.label')
            }.width('100%').justifyContent(FlexAlign.Center)
          }.width('100%').constraintSize({minHeight:PhoneStyle.formSize(50.3,57.7,nativeEnvironment.fontScale)}).margin({top:14})
            .padding({left:14,right:14}).backgroundColor(Colors.action).borderWidth(Colors.systemControls ? 0.67 : 0)
            .borderColor(Colors.prominentRim).accessibilityText(tr('recording_detail_ask'))
            .onClick(() => this.askPersona()).id('knowledge.ask')
        }.width('100%').alignItems(HorizontalAlign.Start).padding({left:4,right:4,bottom:25.7})
        this.entryEvidence()
        if (this.phoneRecordingDetail && this.importedRecordingNotes) {
          Button({type:ButtonType.Normal}) {
            Row({space:12}) {
              SymbolGlyph(Symbols.document).fontSize('17vp').fontColor([Colors.link])
              Text(tr(['lark','larkcli'].includes(Wire.text(this.importedRecordingNotes,'source')) ? 'phone_lark_meeting_notes' : 'phone_original_meeting_notes'))
                .fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.link).layoutWeight(1).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
            }.width('100%')
          }.width('100%').constraintSize({minHeight:44}).padding({left:20,right:20}).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
            .shadow(PhoneStyle.cardElevation(this.environment.dark))
            .onClick(() => {
              Haptics.tap();
              const notes=this.importedRecordingNotes;
              if (notes) this.openSafeLink(Wire.text(notes,'extra'));
            }).id('knowledge.recording.imported_notes')
        }
        this.recordingHeading('phone_kb_confirmed')
        Column({space:6}) {
          if (Wire.text(Wire.object(this.workspace.detail,'meta'),'pinned')) Text(Wire.text(Wire.object(this.workspace.detail,'meta'),'pinned'))
            .fontSize(17).fontColor(Colors.ink).width('100%').padding({left:20,right:20,top:4,bottom:4}).copyOption(CopyOptions.LocalDevice).id('knowledge.facts.value')
          if (!this.workspace.loading && !this.workspace.error && Object.keys(this.workspace.detail).length>0 && !this.workspace.supportsExpectedPinned) Text(tr('phone_conditional_read_only'))
            .fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary).width('100%').padding({left:20,right:20,top:8,bottom:8}).id('knowledge.facts.read_only')
          if (this.workspace.supportsExpectedPinned) Button({type:ButtonType.Normal}) {
            Row({space:12}) {
              SymbolGlyph(Wire.text(Wire.object(this.workspace.detail,'meta'),'pinned') ? Symbols.edit : Symbols.plus).fontSize('17vp').fontColor([Colors.link])
              Text(tr(Wire.text(Wire.object(this.workspace.detail,'meta'),'pinned') ? 'recording_facts_edit' : 'phone_kb_add_fact')).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).fontColor(Colors.link)
            }.width('100%').padding({top:16.15,bottom:16.15})
          }.width('100%').constraintSize({minHeight:this.recordingMetric(52.3,59.3)})
            .padding({left:20,right:20,top:0,bottom:0}).backgroundColor(Color.Transparent)
            .onClick(() => this.openFacts()).id('knowledge.facts.open')
        }.width('100%').alignItems(HorizontalAlign.Start).backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
          .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
          .shadow(PhoneStyle.cardElevation(this.environment.dark))
          .bindSheet(this.facts.open,this.recordingFactsEditor(),{height:this.facts.baseline ? SheetSize.MEDIUM : SheetSize.LARGE,
            detents:this.facts.baseline ? [SheetSize.MEDIUM,SheetSize.LARGE] : [SheetSize.LARGE,SheetSize.MEDIUM],
            dragBar:true,showClose:false,backgroundColor:Colors.grouped,keyboardAvoidMode:SheetKeyboardAvoidMode.TRANSLATE_AND_RESIZE,
            onWillDismiss:(action:DismissSheetAction) => { if (!this.workspace.busy && !this.factsChanged()) { this.facts.close(); action.dismiss(); } }})
        this.recordingFootnote('phone_kb_fact_note')
        if (Wire.text(this.workspace.detail,'summary')) {
          this.recordingHeading('phone_kb_profile')
          RecordingProfileCard() {
            MarkdownText({text:Wire.text(this.workspace.detail,'summary'),recapReading:true,profileReading:true,
              agentFontScale:this.environment.fontScale,paragraphLineSpacingEm:0.45,onLink:(url:string) => this.openSafeLink(url)})
          }
        }
        if (Wire.text(this.workspace.detail,'path')) {
          if (Wire.text(this.workspace.detail,'summary')) Blank().height(17.3)
          this.recordingHeading('directory')
          Text(Wire.text(this.workspace.detail,'path')).fontFamily('monospace').fontSize(Tokens.typeCode).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCode)).fontColor(Colors.secondary).width('100%')
            .padding({left:20,right:20,top:15,bottom:15}).backgroundColor(Colors.surface).borderRadius(Tokens.radiusPanel)
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:Tokens.radiusPanel})
            .shadow(PhoneStyle.cardElevation(this.environment.dark)).copyOption(CopyOptions.LocalDevice).id('knowledge.recording.directory')
          this.recordingFootnote('recording_detail_folder_access')
        }
        if (this.workspace.error && !(this.facts.open && this.facts.conflict && this.workspace.error==='phone_facts_changed')) Text(tr(this.workspace.error)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.danger).width('100%')
        if (!this.phoneRecordingDetail) {
          this.recordingToolsButton()
          if (this.recordingTools) this.recordingToolContents()
        }
      }.width('100%').alignItems(HorizontalAlign.Start).padding({left:16,right:16,top:31,bottom:24})
    }.width('100%').height('100%').align(Alignment.TopStart).backgroundColor(Colors.grouped).id('knowledge.recording.detail')
      .bindSheet($$this.metadataSheet,this.metadataForm(),{title:{title:this.phoneRecordingDetail && this.recordingTools ? this.recordingToolsTitle() : tr('knowledge_edit_metadata')},height:SheetSize.LARGE,showClose:true,
        onWillDismiss:(dismiss:DismissSheetAction) => {
          if (this.phoneRecordingDetail && this.recordingTools && dismiss.reason===DismissReason.PRESS_BACK) this.recordingTools=false;
          else dismiss.dismiss();
        },
        onDisappear:() => { if (this.phoneRecordingDetail) this.recordingTools=false; this.workspace.editOpen=false; }})
  }

  @Builder detailContent() {
    Scroll() {
      Column({ space: Tokens.section }) {
        Column({ space: Tokens.gap }) {
          if (Wire.text(Wire.object(this.workspace.detail, 'meta'), 'importance')) Text(Wire.text(Wire.object(this.workspace.detail, 'meta'), 'importance') + ' · ' + tr('knowledge_relevance') + ' ' + String(Wire.number(Wire.object(this.workspace.detail, 'meta'), 'relevance')))
            .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
          if (this.workspace.selected.detail) Text(this.workspace.selected.detail).fontSize(Tokens.body).width('100%')
        if (Wire.text(this.workspace.detail, 'summary')) {
          Column({ space: Tokens.gap }) { Text(tr(this.workspace.selected.path === 'recordings' ? 'knowledge_recording_summary' : 'knowledge_profile')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
            MarkdownText({ text: Wire.text(this.workspace.detail, 'summary'), onLink: (url: string) => this.openSafeLink(url) })
          }.width('100%').alignItems(HorizontalAlign.Start).padding(16).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
            .shadow(PhoneStyle.cardElevation(this.environment.dark)).id('knowledge.summary')
        }

          if (this.workspace.selected.path === 'topics' && Wire.rows(this.workspace.detail,'points').length) {
            ForEach(Wire.rows(this.workspace.detail,'points'), (point:Record<string,Object>) => {
              Column({space:5}) {
                if (Wire.strings(point,'path').length) Text(Wire.strings(point,'path').join(' › ')).fontSize(13).fontColor(Colors.link)
                Text(Wire.text(point,'text')).fontSize(17).fontColor(Colors.ink).copyOption(CopyOptions.LocalDevice)
                if (Wire.text(point,'why')) Text(Wire.text(point,'why')).fontSize(14).fontColor(Colors.secondary)
                if (Wire.text(point,'when')) Text(Wire.text(point,'when')).fontSize(12).fontColor(Colors.secondary)
                ForEach(Wire.rows(point,'sources'), (source:Record<string,Object>) => {
                  if (Wire.text(source,'id') && KB_KINDS.includes(Wire.text(source,'kind'))) {
                    Button(Wire.text(source,'name') || Wire.text(source,'id'),{buttonStyle:ButtonStyleMode.TEXTUAL})
                      .fontSize(14).fontColor(Colors.link).onClick(() => this.openRelated(source))
                  }
                }, (source:Record<string,Object>) => JSON.stringify([Wire.text(source,'kind'),Wire.text(source,'id')]))
              }.width('100%').alignItems(HorizontalAlign.Start).padding(14).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
                .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
                .shadow(PhoneStyle.cardElevation(this.environment.dark))
                .id('knowledge.point.' + Wire.text(point,'id'))
            }, (point:Record<string,Object>) => JSON.stringify(point))
          }
          if (this.workspace.selected.path === 'recordings' && this.importedMeeting()) {
            Button({type:ButtonType.Normal}) {
              Row({space:8}) {
                SymbolGlyph(Symbols.calendar).fontSize('17vp').fontColor([Colors.link])
                Text(this.importedMeeting()!.title).fontSize(16).fontColor(Colors.ink).layoutWeight(1)
                SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
              }.width('100%').padding(12)
            }.padding(0).width('100%').backgroundColor(Colors.surface).onClick(() => {
              const row = this.importedMeeting(); if (row) this.onOpenResource(row);
            }).id('knowledge.recording.imported')
          }
          Row({ space: Tokens.gap }) {
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('knowledge_ask_persona')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('knowledge_ask_persona', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('knowledge_ask_persona')).bindTips(tr('knowledge_ask_persona')).onClick(() => this.askPersona()).id('knowledge.ask')
            Button({ type: ButtonType.Circle }) { SymbolGlyph(Symbols.edit).fontSize(Tokens.symbolFont).fontColor([Colors.link]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('knowledge_edit_metadata'))
              .onClick(() => this.workspace.beginEdit()).id('knowledge.edit.open')
          }.width('100%')
          if (this.workspace.selected.path === 'projects') Button({ type: ButtonType.Normal }) {
            Row({ space: Tokens.gap }) {
            SymbolGlyph(Symbols.agent).fontSize(Tokens.symbolFont).fontColor([Colors.ai])
            Text(tr('knowledge_agent_configuration')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontColor(Colors.link).layoutWeight(1)
            SymbolGlyph(this.dispatchExpanded ? Symbols.up : Symbols.down).fontSize(Tokens.meta).fontColor([Colors.secondary])
          }.width('100%')
          }.attributeModifier(ButtonStyles.quiet).width('100%').onClick(() => this.dispatchExpanded = !this.dispatchExpanded).accessibilityText(tr('knowledge_agent_configuration')).accessibilityDescription(tr(this.dispatchExpanded ? 'control_expanded' : 'control_collapsed')).id('knowledge.dispatch.toggle')
          if (this.workspace.selected.path === 'projects' && this.dispatchExpanded) {
            TextArea({ text: this.workspace.agentDraft, placeholder: tr('knowledge_agent_prompt') }).height(Tokens.inputExpanded)
              .onChange((value: string) => this.workspace.setAgentDraft(value)).id('knowledge.agent.prompt')
            if (this.workspace.directoryTargets.length) {
              Select(this.directoryOptions()).attributeModifier(SelectStyles.inline).value(this.workspace.directoryTargets[this.workspace.selectedDirectory]?.label || '').onSelect((index: number) => this.workspace.selectedDirectory = index)
                .id('knowledge.agent.directory')
              TextInput({ text: this.workspace.agentLauncher, placeholder: tr('launcher') }).onChange((value: string) => this.workspace.setAgentLauncher(value))
                .id('knowledge.agent.launcher')
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('knowledge_start_agent')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('knowledge_start_agent', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('knowledge_start_agent')).bindTips(tr('knowledge_start_agent')).enabled(!!this.workspace.agentDraft.trim() && !!this.workspace.agentLauncher.trim() && !this.workspace.busy).onClick(() => this.confirmDispatch()).id('knowledge.agent.start')
            } else Text(tr('knowledge_agent_unavailable')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary'))
          }
          if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger')).width('100%')
        }.alignItems(HorizontalAlign.Start).width('100%')
        if (this.workspace.editOpen) this.editForm()
        this.entryEvidence()
        if (this.workspace.selected.path === 'recordings') this.recordingSection(UIUtils.makeBinding((): Record<string, Object> => this.workspace.detail))
        this.chatInboxSection()
        if (this.workspace.selected.path !== 'recordings' && Wire.text(this.workspace.detail, 'recent')) {
          Column({ space: Tokens.gap }) { Text(tr('knowledge_recent')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
            if (this.chatInboxData()?.pieces) {
              Column({ space: Tokens.spaceXs }) {
                ForEach(this.chatInboxData()!.pieces!, (piece: KBChatInboxPiece) => {
                  if (piece.item) {
                    Button({ type: ButtonType.Normal }) {
                      Row({ space: Tokens.spaceS }) {
                        Text(piece.text)
                          .fontSize(Tokens.meta)
                          .fontColor(Colors.link)
                          .layoutWeight(1)
                        SymbolGlyph(Symbols.inbox)
                          .fontSize('14vp')
                          .fontColor([Colors.link])
                      }
                      .width('100%')
                      .alignItems(VerticalAlign.Top)
                    }
                    .width('100%')
                    .constraintSize({ minHeight: Tokens.controlBar })
                    .padding({ top: Tokens.spaceXs, bottom: Tokens.spaceXs })
                    .backgroundColor(Color.Transparent)
                    .onClick(() => this.openChatInboxItem(piece.item!))
                    .id('knowledge.recent.inbox')
                  } else {
                    Text(piece.text).fontSize(Tokens.meta).width('100%')
                  }
                }, (piece: KBChatInboxPiece) => String(piece.id) + ':' + (piece.item?.id || 'plain'))
              }
              .width('100%')
              .alignItems(HorizontalAlign.Start)
            } else {
              Text(Wire.text(this.workspace.detail, 'recent')).fontSize(Tokens.meta).width('100%')
            }
          }.width('100%').alignItems(HorizontalAlign.Start)
        }
        if (this.workspace.selected.path === 'projects' && Wire.rows(this.workspace.detail, 'moments').length) {
          Column({ space: Tokens.gap }) {
            Text(tr('knowledge_related_moments')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
            ForEach(Wire.rows(this.workspace.detail, 'moments'), (moment: Record<string, Object>) => Button({ type: ButtonType.Normal }) { this.navigationLabel(Wire.text(moment, 'author') + ' · ' + Wire.text(moment, 'text')) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).padding(0).width('100%').onClick(() => this.loadMoment(Wire.text(moment, 'id'))).id('knowledge.moment.' + Wire.text(moment, 'id')),
              (moment: Record<string, Object>, index: number) => JSON.stringify([index, Wire.text(moment, 'id'), JSON.stringify(moment)]))
            if (this.momentText) MarkdownText({ text: this.momentText, onLink: (url: string) => this.openSafeLink(url) })
          }.width('100%').alignItems(HorizontalAlign.Start)
        }
        this.relatedEntries(UIUtils.makeBinding((): Record<string, Object> => this.workspace.detail))
        if (Wire.text(Wire.object(this.workspace.detail, 'meta'), 'url')) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('knowledge_open_link')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('knowledge_open_link', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('knowledge_open_link')).bindTips(tr('knowledge_open_link')).onClick(() => this.openSafeLink(Wire.text(Wire.object(this.workspace.detail, 'meta'), 'url')))
        if (Wire.text(this.workspace.detail, 'path')) Text(Wire.text(this.workspace.detail, 'path')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
        if (this.workspace.selected.path === 'recordings') Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('knowledge_forget_recording')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('knowledge_forget_recording', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('knowledge_forget_recording')).bindTips(tr('knowledge_forget_recording')).enabled(this.workspace.canDeleteRecording()).onClick(() => this.confirmDeleteRecording()).id('knowledge.recording.delete')
      }.padding(Tokens.page).width('100%').alignItems(HorizontalAlign.Start)
    }.align(Alignment.TopStart).scrollable(ScrollDirection.Vertical).scrollBar(BarState.Auto).width('100%').height('100%')
  }

  @Builder navigationLabel(label: string) {
    Row({ space: Tokens.gap }) {
      Text(label).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1)
      SymbolGlyph(Symbols.expand).fontSize(Tokens.meta).fontColor([Colors.secondary])
    }.width('100%').constraintSize({ minHeight: Tokens.touch }).padding({ top: Tokens.inset, bottom: Tokens.inset })
  }

  @Builder relatedEntries(detail: Binding<Record<string, Object>>) {
    if (Wire.rows(detail.value, 'links').length) {
      Column({ space: Tokens.gap }) {
        Text(tr('knowledge_related')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
        ForEach(Wire.rows(detail.value, 'links'), (item: Record<string, Object>) => {
          Button({ type: ButtonType.Normal }) { this.navigationLabel(Wire.text(item, 'title', Wire.text(item, 'id'))) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).padding(0).width('100%').onClick(() => this.openRelated(item)).id('knowledge.related.' + Wire.text(item, 'kind') + '.' + Wire.text(item, 'id'))
        }, (item: Record<string, Object>, index: number) => JSON.stringify([index, Wire.text(item, 'kind') + '/' + Wire.text(item, 'id'), JSON.stringify(item)]))
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
  }

  private chatInboxData(): KBChatInboxPage | null {
    if (this.workspace.selected.path !== 'chats') return null;
    if (isHubPageHidden('inbox', this.model.sidebarConfig, this.model.demo)) return null;
    return parseChatInboxPage('chats', Wire.text(this.workspace.detail, 'recent'), this.workspace.inboxItems);
  }

  private openChatInboxItem(item: KBInboxItem): void {
    Haptics.tap();
    const target = new ResourceRow(item.id, item.title || item.id, 'inbox');
    target.manager = this.workspace.selected.manager;
    target.group = this.workspace.selected.group || this.model.profile.id;
    this.onOpenResource(target);
  }

  private kbInboxTierTone(tier: string, done: boolean): PersonaStatusTone {
    if (tier === 'action' && !done) return 'needs';
    if (tier === 'working' && !done) return 'working';
    return 'neutral';
  }

  private kbInboxTierLabel(tier: string): string {
    if (tier === 'action') return tr('needs_you');
    if (tier === 'working') return tr('flow_inbox_progress');
    if (tier === 'fyi') return tr('flow_inbox_fyi');
    return tr('flow_inbox_collapsed');
  }

  @Builder chatInboxRow(item: KBInboxItem) {
    Button({ type: ButtonType.Normal }) {
      Row({ space: Tokens.spaceM }) {
        SymbolGlyph(item.doneAt ? Symbols.check : Symbols.inbox)
          .fontSize('16vp')
          .fontColor([item.doneAt ? Colors.secondary : Colors.link])
        Column({ space: Tokens.spaceXxs }) {
          Text(item.title || item.id)
            .fontSize(Tokens.typeSecondary)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .fontWeight(FontWeight.Medium)
            .fontColor(item.doneAt ? Colors.secondary : Colors.ink)
            .maxLines(2)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
          Row({ space: Tokens.spaceS }) {
            if (kbInboxTier(item)) {
              PersonaStatusPill({
                tone: this.kbInboxTierTone(kbInboxTier(item)!, !!item.doneAt),
                label: this.kbInboxTierLabel(kbInboxTier(item)!)
              })
            }
            if (kbInboxLine(item)) {
              Text(kbInboxLine(item))
                .fontSize(Tokens.typeLabel)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                .fontColor(Colors.secondary)
                .maxLines(2)
                .textOverflow({ overflow: TextOverflow.Ellipsis })
                .layoutWeight(1)
            }
          }
          .width('100%')
          .alignItems(VerticalAlign.Center)
        }
        .layoutWeight(1)
        .alignItems(HorizontalAlign.Start)
        SymbolGlyph(Symbols.expand)
          .fontSize('12vp')
          .fontColor([Colors.tertiary])
      }
      .width('100%')
      .padding({ left: Tokens.spaceL, right: Tokens.spaceM, top: Tokens.spaceM, bottom: Tokens.spaceM })
      .alignItems(VerticalAlign.Center)
    }
    .width('100%')
    .padding(0)
    .backgroundColor(Color.Transparent)
    .onClick(() => this.openChatInboxItem(item))
    .id('knowledge.inbox.item.' + item.id)
  }

  @Builder chatInboxSection() {
    if (this.chatInboxData()) {
      Column({ space: Tokens.spaceS }) {
        PersonaSectionLabel({
          text: tr('kb_chat_in_inbox'),
          trailing: this.chatInboxData()!.open.length > 0 ? String(this.chatInboxData()!.open.length) : ''
        })
        Column() {
          ForEach(this.chatInboxData()!.open, (item: KBInboxItem, idx: number) => {
            if (idx > 0) {
              Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: 48 })
            }
            this.chatInboxRow(item)
          }, (item: KBInboxItem) => 'open:' + item.id)
          if (this.chatInboxDoneShown) {
            ForEach(this.chatInboxData()!.done, (item: KBInboxItem, idx: number) => {
              if (idx > 0 || this.chatInboxData()!.open.length > 0) {
                Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: 48 })
              }
              this.chatInboxRow(item)
            }, (item: KBInboxItem) => 'done:' + item.id)
          }
          if (this.chatInboxData()!.done.length > 0) {
            if (this.chatInboxData()!.open.length > 0 || this.chatInboxDoneShown) {
              Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.spaceL })
            }
            Button(
              this.chatInboxDoneShown
                ? tr('agent_collapse')
                : tr('kb_chat_inbox_done_count').replace('{count}', String(this.chatInboxData()!.done.length)),
              { type: ButtonType.Normal }
            )
              .buttonStyle(ButtonStyleMode.TEXTUAL)
              .fontSize(Tokens.typeSecondary)
              .fontColor(Colors.link)
              .width('100%')
              .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceM })
              .onClick(() => {
                Haptics.tap();
                Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => {
                  this.chatInboxDoneShown = !this.chatInboxDoneShown;
                });
              })
              .id('knowledge.inbox.done')
          }
        }
        .width('100%')
        .backgroundColor(Colors.surface)
        .borderRadius(PhoneStyle.cardRadius)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: PhoneStyle.cardRadius })
        .shadow(PhoneStyle.cardElevation(this.environment.dark))
        .clip(true)
      }
      .width('100%')
      .alignItems(HorizontalAlign.Start)
      .id('knowledge.inbox.section')
    }
  }

  private minutesDebates(detail: Record<string, Object>): MinutesDebateItem[] {
    const minutes = Wire.object(detail, 'minutes');
    return parseMinutesDebates(minutes['debates'], Wire.rows(minutes, 'decisions').length);
  }

  @Builder minutesDebatesSection(detail: Record<string, Object>) {
    if (this.minutesDebates(detail).length > 0) {
      Column({ space: Tokens.spaceS }) {
        PersonaSectionLabel({
          text: tr('knowledge_minutes_debates'),
          trailing: String(this.minutesDebates(detail).length)
        })
        ForEach(this.minutesDebates(detail), (debate: MinutesDebateItem, idx: number) => {
          Column({ space: Tokens.spaceS }) {
            Row({ space: Tokens.spaceS }) {
              Text(debate.question)
                .fontSize(Tokens.typeSecondary)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
                .fontWeight(FontWeight.Bold)
                .fontColor(Colors.ink)
                .layoutWeight(1)
              if (isKnownDebateStatus(debate.status)) {
                PersonaStatusPill({
                  tone: debate.status === 'open' ? 'working' : 'unread',
                  label: tr(debate.status === 'open' ? 'knowledge_minutes_debate_open' : 'knowledge_minutes_debate_settled')
                })
              }
            }
            .width('100%')
            .alignItems(VerticalAlign.Top)

            if (debate.topic) {
              Text(debate.topic)
                .fontSize(Tokens.typeLabel)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                .fontColor(Colors.secondary)
            }

            Column({ space: Tokens.spaceXs }) {
              ForEach(debate.positions, (pos: MinutesDebatePosition, pIdx: number) => {
                Row({ space: Tokens.spaceS }) {
                  Circle()
                    .width(Tokens.spaceSm)
                    .height(Tokens.spaceSm)
                    .fill(Colors.secondary)
                    .margin({ top: 6 })
                  Column({ space: Tokens.spaceXxs }) {
                    Text(pos.stance)
                      .fontSize(Tokens.typeCaption)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                      .fontColor(Colors.ink)
                    if (pos.who || pos.at > 0) {
                      Row({ space: Tokens.spaceSm }) {
                        if (pos.who) {
                          PersonaStatusPill({ tone: 'neutral', label: pos.who })
                        }
                        if (pos.at > 0) {
                          Text(this.clock(pos.at))
                            .fontSize(Tokens.typeLabel)
                            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                            .fontColor(Colors.secondary)
                            .fontFeature('tnum 1')
                        }
                      }
                    }
                  }
                  .layoutWeight(1)
                  .alignItems(HorizontalAlign.Start)
                }
                .width('100%')
                .alignItems(VerticalAlign.Top)
              }, (pos: MinutesDebatePosition, pIdx: number) => String(pIdx) + ':' + pos.stance)
            }
            .width('100%')

            if (debate.outcome) {
              Row({ space: Tokens.spaceS }) {
                SymbolGlyph(Symbols.check)
                  .fontSize('14vp')
                  .fontColor([Colors.unread])
                Column({ space: Tokens.spaceXxs }) {
                  Text(tr('knowledge_minutes_debate_outcome'))
                    .fontSize(Tokens.typeLabel)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                    .fontWeight(FontWeight.Medium)
                    .fontColor(Colors.unread)
                  Text(debate.outcome.text)
                    .fontSize(Tokens.typeCaption)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                    .fontColor(Colors.ink)
                }
                .layoutWeight(1)
                .alignItems(HorizontalAlign.Start)
              }
              .width('100%')
              .padding(Tokens.spaceS)
              .backgroundColor(Colors.unreadWash)
              .borderRadius(Tokens.radiusControl)
              .alignItems(VerticalAlign.Top)
            }
          }
          .width('100%')
          .alignItems(HorizontalAlign.Start)
          .padding(Tokens.spaceM)
          .backgroundColor(Colors.surface)
          .borderRadius(Tokens.radiusCard)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: Tokens.radiusCard })
          .shadow(PhoneStyle.cardElevation(this.environment.dark))
          .id('knowledge.minutes.debate.' + String(idx))
        }, (debate: MinutesDebateItem, idx: number) => String(idx) + ':' + debate.question)
      }
      .width('100%')
      .alignItems(HorizontalAlign.Start)
      .id('knowledge.minutes.debates')
    }
  }

  private minutesWaitLine(detail: Record<string, Object>): MinutesWaitResult {
    const meta = Wire.object(detail, 'meta');
    const transcription = Wire.object(meta, 'transcription');
    const minutesState = Wire.text(meta, 'minutes_state');
    const hasMinutes = Object.keys(Wire.object(detail, 'minutes')).length > 0;
    return resolveMinutesWait(transcription, minutesState, hasMinutes);
  }

  private minutesWaitText(wait: MinutesWaitResult): string {
    if (wait.line === 'checking') {
      if (wait.doneMinutes !== undefined && wait.totalMinutes !== undefined) {
        return tr('knowledge_minutes_checking_progress')
          .replace('{done}', String(wait.doneMinutes))
          .replace('{total}', String(wait.totalMinutes));
      }
      return tr('knowledge_minutes_checking');
    }
    if (wait.line === 'writing') {
      return tr('knowledge_minutes_writing');
    }
    return '';
  }

  @Builder recordingSection(detail: Binding<Record<string, Object>>) {
    Column({ space: Tokens.gap }) {
      if (Object.keys(Wire.object(detail.value, 'minutes')).length) {
        Text(tr('knowledge_minutes')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
        if (Wire.text(Wire.object(detail.value, 'minutes'), 'markdown')) MarkdownText({ text: Wire.text(Wire.object(detail.value, 'minutes'), 'markdown'), onLink: (url: string) => this.openSafeLink(url) })
        this.minutesDebatesSection(detail.value)
        ForEach(Wire.rows(Wire.object(detail.value, 'minutes'), 'actions'), (action: Record<string, Object>) => {
          Row({ space: Tokens.gap }) { Toggle({ type: ToggleType.Checkbox, isOn: this.selectedActions.includes(Wire.text(action, 'id')) }).onChange(() => this.toggleAction(Wire.text(action, 'id')))
            Text(Wire.text(action, 'what')).fontSize(Tokens.meta).layoutWeight(1)
          }.width('100%').id('knowledge.minutes.action.' + Wire.text(action, 'id'))
        }, (action: Record<string, Object>, index: number) => JSON.stringify([index, Wire.text(action, 'id'), JSON.stringify(action)]))
        if (this.selectedActions.length) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('knowledge_minutes_add_todos')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('knowledge_minutes_add_todos', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('knowledge_minutes_add_todos')).bindTips(tr('knowledge_minutes_add_todos')).onClick(() => this.confirmAddTodos())
      } else if (this.minutesWaitLine(detail.value).line !== 'none') {
        Text(tr('knowledge_minutes')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium)
        Row({ space: Tokens.gap }) {
          LoadingProgress().width(16).height(16).color(Colors.link)
          Text(this.minutesWaitText(this.minutesWaitLine(detail.value)))
            .fontSize(Tokens.meta)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.meta))
            .fontColor(Colors.secondary)
            .layoutWeight(1)
        }.width('100%').alignItems(VerticalAlign.Center).id('knowledge.minutes.wait')
      }
      Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('knowledge_minutes_make')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('knowledge_minutes_make', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('knowledge_minutes_make')).bindTips(tr('knowledge_minutes_make')).enabled(!this.workspace.busy && this.workspace.writeState !== 'pending' && this.workspace.writeState !== 'unknown').onClick(() => this.workspace.makeMinutes()).id('knowledge.minutes.make')
      if (this.transcriptView(detail.value).rows.length > 0 || this.transcriptView(detail.value).trimmedAt || Wire.rows(detail.value, 'segments').length) {
        Column({ space: Tokens.spaceS }) {
          Row({ space: Tokens.spaceS }) {
            Text(tr('knowledge_transcript') + (this.transcriptView(detail.value).segmentCount > 0 ? ' · ' + String(this.transcriptView(detail.value).segmentCount) : ''))
              .fontSize(Tokens.body)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.body))
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.ink)
              .layoutWeight(1)

            if (this.transcriptView(detail.value).rows.length > 0) {
              Button(tr(this.transcriptReviewOpen ? 'close' : 'recording_transcript_review'), { type: ButtonType.Capsule })
                .constraintSize({ minHeight: Tokens.controlBar })
                .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
                .fontSize(Tokens.typeCaption)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                .fontWeight(FontWeight.Medium)
                .backgroundColor(this.transcriptReviewOpen ? Colors.accentWell : Colors.control)
                .fontColor(this.transcriptReviewOpen ? Colors.link : Colors.ink)
                .onClick(() => {
                  Haptics.tap();
                  this.transcriptReviewOpen = !this.transcriptReviewOpen;
                  if (!this.transcriptReviewOpen) {
                    this.transcriptSearchQuery = '';
                    this.transcriptSelectedId = '';
                    this.transcriptBookmarksOnly = false;
                  }
                })
                .id('knowledge.recording.review_toggle')
            }
          }
          .width('100%')
          .alignItems(VerticalAlign.Center)

          if (this.transcriptReviewOpen && this.transcriptView(detail.value).rows.length > 0) {
            Column({ space: Tokens.spaceS }) {
              Row({ space: Tokens.spaceXs }) {
                TextInput({ text: this.transcriptSearchQuery, placeholder: tr('recording_transcript_find') })
                  .layoutWeight(1)
                  .constraintSize({ minHeight: Tokens.controlBar })
                  .fontSize(Tokens.typeSecondary)
                  .fontColor(Colors.ink)
                  .backgroundColor(Colors.control)
                  .borderRadius(Tokens.radiusControl)
                  .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
                  .onChange((value: string) => {
                    this.transcriptSearchQuery = value;
                    this.transcriptSelectedId = '';
                  })
                  .id('knowledge.recording.search')

                Button({ type: ButtonType.Normal }) {
                  SymbolGlyph($r('sys.symbol.chevron_left'))
                    .fontSize('16vp')
                    .fontColor([this.transcriptMatches(detail.value).length > 0 ? Colors.ink : Colors.tertiary])
                }
                .buttonStyle(ButtonStyleMode.TEXTUAL)
                .width(Tokens.controlBar)
                .height(Tokens.controlBar)
                .backgroundColor(Colors.control)
                .borderRadius(Tokens.radiusControl)
                .enabled(this.transcriptMatches(detail.value).length > 0)
                .accessibilityText(tr('recording_transcript_previous'))
                .onClick(() => this.jumpTranscriptMatch(this.transcriptMatches(detail.value), -1))
                .id('knowledge.recording.prev')

                Button({ type: ButtonType.Normal }) {
                  SymbolGlyph($r('sys.symbol.chevron_right'))
                    .fontSize('16vp')
                    .fontColor([this.transcriptMatches(detail.value).length > 0 ? Colors.ink : Colors.tertiary])
                }
                .buttonStyle(ButtonStyleMode.TEXTUAL)
                .width(Tokens.controlBar)
                .height(Tokens.controlBar)
                .backgroundColor(Colors.control)
                .borderRadius(Tokens.radiusControl)
                .enabled(this.transcriptMatches(detail.value).length > 0)
                .accessibilityText(tr('recording_transcript_next'))
                .onClick(() => this.jumpTranscriptMatch(this.transcriptMatches(detail.value), 1))
                .id('knowledge.recording.next')
              }
              .width('100%')

              Row({ space: Tokens.spaceS }) {
                if (this.transcriptView(detail.value).hasBookmarks) {
                  Button(tr('recording_transcript_bookmarks'), { type: ButtonType.Capsule })
                    .constraintSize({ minHeight: Tokens.controlBar })
                    .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
                    .fontSize(Tokens.typeCaption)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                    .fontWeight(FontWeight.Medium)
                    .backgroundColor(this.transcriptBookmarksOnly ? Colors.accentWell : Colors.control)
                    .fontColor(this.transcriptBookmarksOnly ? Colors.link : Colors.ink)
                    .onClick(() => {
                      Haptics.tap();
                      this.transcriptBookmarksOnly = !this.transcriptBookmarksOnly;
                      this.transcriptSelectedId = '';
                    })
                    .id('knowledge.recording.bookmarks')
                }

                Button(tr('recording_transcript_copy'), { type: ButtonType.Capsule })
                  .constraintSize({ minHeight: Tokens.controlBar })
                  .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
                  .fontSize(Tokens.typeCaption)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                  .fontWeight(FontWeight.Medium)
                  .backgroundColor(Colors.control)
                  .fontColor(Colors.link)
                  .onClick(() => this.copyRetainedTranscript(this.transcriptView(detail.value).rows))
                  .id('knowledge.recording.copy')

                Blank().layoutWeight(1)

                if (this.transcriptSearchQuery.trim()) {
                  Text(
                    this.transcriptMatches(detail.value).length === 0
                      ? tr('recording_transcript_no_matches')
                      : this.transcriptSelectedId
                        ? String(Math.max(1, this.transcriptMatches(detail.value).findIndex((m: TranscriptRow) => m.id === this.transcriptSelectedId) + 1)) +
                          ' / ' + String(this.transcriptMatches(detail.value).length)
                        : String(this.transcriptMatches(detail.value).length) + ' ' + tr('recording_transcript_matches')
                  )
                    .fontSize(Tokens.typeLabel)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                    .fontColor(Colors.secondary)
                    .id('knowledge.recording.match_count')
                }
              }
              .width('100%')
              .alignItems(VerticalAlign.Center)
            }
            .width('100%')
            .padding(Tokens.spaceM)
            .backgroundColor(Colors.surface)
            .borderRadius(Tokens.radiusCard)
            .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: Tokens.radiusCard })
            .id('knowledge.recording.review_bar')
          }

          if (this.transcriptView(detail.value).trimmedAt || this.transcriptView(detail.value).rows.length === 0) {
            Text(tr(this.transcriptView(detail.value).trimmedAt ? 'recording_transcript_expired' : 'recording_transcript_empty'))
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontColor(Colors.secondary)
              .width('100%')
              .id('knowledge.recording.transcript_notice')
          }

          ForEach(this.transcriptRowsShown(this.transcriptView(detail.value)), (line: TranscriptRow, index: number) => {
            Row({ space: Tokens.spaceS }) {
              Text(line.clock)
                .fontSize(Tokens.typeLabel)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                .fontColor(line.bookmark ? Colors.link : Colors.secondary)
                .fontWeight(line.bookmark ? FontWeight.Medium : FontWeight.Normal)
                .fontFeature('tnum 1')
                .width(56)
              Text() {
                if (this.transcriptReviewOpen && this.transcriptSearchQuery.trim()) {
                  ForEach(
                    splitTranscriptMatchParts(transcriptRowDisplayText(line, tr('recording_bookmark')), this.transcriptSearchQuery),
                    (part: TranscriptMatchPart, _partIdx: number) => {
                      if (part.hit) {
                        Span(part.text)
                          .fontWeight(FontWeight.Bold)
                          .fontColor(Colors.link)
                      } else {
                        Span(part.text)
                      }
                    },
                    (part: TranscriptMatchPart, partIdx: number) => String(partIdx) + ':' + (part.hit ? '1' : '0') + ':' + part.text
                  )
                } else {
                  Span(transcriptRowDisplayText(line, tr('recording_bookmark')))
                }
              }
                .fontSize(Tokens.meta)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.meta))
                .fontColor(line.bookmark ? Colors.link : Colors.ink)
                .layoutWeight(1)
            }
            .width('100%')
            .padding({ left: Tokens.spaceS, right: Tokens.spaceS, top: Tokens.spaceXs, bottom: Tokens.spaceXs })
            .borderRadius(Tokens.radiusControl)
            .backgroundColor(
              this.transcriptReviewOpen && this.transcriptSelectedId === line.id
                ? Colors.accentWell
                : this.transcriptReviewOpen && this.transcriptSearchQuery.trim() &&
                    this.transcriptMatches(detail.value).some((m: TranscriptRow) => m.id === line.id)
                  ? Colors.aiWash
                  : Color.Transparent
            )
            .alignItems(VerticalAlign.Top)
            .id('knowledge.transcript.' + String(index))
          }, (line: TranscriptRow, index: number) => JSON.stringify([index, JSON.stringify(line)]))
        }
        .width('100%')
        .alignItems(HorizontalAlign.Start)
      } else if (Wire.text(detail.value, 'recent')) Text(Wire.text(detail.value, 'recent')).fontSize(Tokens.meta).width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start).id('knowledge.recording.transcript')
  }

  private importedMeeting(): ResourceRow | undefined {
    const selected = this.workspace.selected;
    const merged = Wire.object(selected.data(),'imported_meeting');
    const data = Wire.text(merged,'id') ? merged : Wire.object(Wire.object(this.workspace.detail,'meta'),'imported_meeting');
    if (!Wire.text(data,'id')) return undefined;
    const row = ResourceRow.from(data,'knowledge');
    row.path = 'meetings'; row.group = selected.group; row.manager = selected.manager;
    if (!row.title || row.title === row.id) row.title = tr('knowledge_kind_meetings');
    return row;
  }
  private directoryOptions(): SelectOption[] { return this.workspace.directoryTargets.map((target: KnowledgeDirectory): SelectOption => ({ value: target.label })); }
  private openRelated(item: Record<string, Object>): void { const target = ResourceRow.from(item, 'knowledge'); target.path = Wire.text(item, 'kind'); target.manager = this.workspace.selected.manager; this.onOpenResource(target); }
  private async loadMoment(id: string): Promise<void> { this.momentText = await this.workspace.openMoment(id).catch(() => ''); }

  build() {
    if (this.row && this.workspace.selected.id && this.workspace.selected.path==='recordings') this.recordingDetail();
    else if (this.row && this.workspace.selected.id) this.detailContent();
    else this.listContent();
  }
}
