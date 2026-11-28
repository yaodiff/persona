import type { MeetingAsk } from '../models/MeetingRoom';
import { Symbols } from '../design/Symbols';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { NeedsYouPulseDot, WorkingArcSpinner } from './StatusMotionMarks';
import { KnowledgeRecording } from '../models/KnowledgeRecording';
import type { KnowledgeRecordingEntry } from '../models/KnowledgeRecording';
import { Tokens } from '../design/Tokens';
import { tr, trError } from '../design/Strings';
import { meetingEcho } from '../platform/MeetingEchoPreference';

/** Capture controls for the local recording owned by the current Knowledge Hub/manager. */
@ComponentV2
export struct KnowledgeRecordingView {
  @Event onStart:()=>void=()=>{};
  @Local roomQuestion:string='';
  @Require @Param model: KnowledgeRecording;
  @Param recordingAllowed: boolean = true;
  @Event onDeleteLocal: () => void = () => {};
  @Local private environment: NativeEnvironment = nativeEnvironment;

  @Local elapsed: number = 0;
  private tick: number = -1;
  aboutToAppear(): void { meetingEcho.load(this.getUIContext().getHostContext()); this.updateClock(); }
  @Monitor('model.captureState','model.entry') updateClock(): void {
    if (this.tick !== -1) clearInterval(this.tick);
    this.tick = -1;
    const start = this.model.entry ? Date.parse(this.model.entry.startedAt) : 0;
    if (this.model.captureState !== 'recording' || !Number.isFinite(start) || !start) {
      this.elapsed = this.model.entry?.duration || 0; return;
    }
    const update = ():void => { this.elapsed = Math.max(0,(Date.now() - start) / 1000); };
    update(); this.tick = setInterval(update,1000);
  }
  private duration(): string {
    const n = Math.floor(this.elapsed);
    return String(Math.floor(n/60)).padStart(2,'0') + ':' + String(n%60).padStart(2,'0');
  }
  aboutToDisappear(): void {
    if (this.tick !== -1) clearInterval(this.tick);
    this.tick = -1;
    this.model.stopForBackground().catch(() => undefined);
  }

  private statusKey(): string {
    if (this.model.captureState === 'requesting') return 'knowledge_recording_requesting';
    if (this.model.captureState === 'recording') return 'knowledge_recording_active';
    if (this.model.transcriptionState === 'transcribing') return 'knowledge_recording_transcribing';
    if (this.model.transcriptionState === 'complete') {
      if (this.model.uploadState === 'uploading') return 'knowledge_recording_uploading';
      if (this.model.uploadState === 'stored') return 'knowledge_recording_uploaded';
      if (this.model.uploadState === 'unknown') return 'knowledge_recording_upload_unknown';
      if (this.model.uploadState === 'failed') return 'knowledge_recording_upload_failed';
      if (this.model.uploadState === 'deleted') return 'knowledge_recording_deleted';
      if (this.model.uploadState === 'stale') return 'knowledge_recording_stale';
      if (this.model.uploadState === 'expired') return 'knowledge_recording_expired';
      return 'knowledge_recording_transcribed';
    }
    if (this.model.captureState === 'stopped') return 'knowledge_recording_stopped';
    return 'knowledge_recording_capture_ready';
  }

  private transcribeLabel(): string {
    return this.model.transcriptionState === 'failed' ? tr('knowledge_recording_retry_transcribe') : tr('knowledge_recording_transcribe');
  }

  private confirmTranscription(): void {
    const owner = this.model, entry = owner.entry;
    if (!entry || !owner.canTranscribe()) return;
    this.getUIContext().showAlertDialog({ title: this.transcribeLabel(), message: tr('knowledge_recording_save_notice'),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('confirm'), action: () => {
        if (this.model === owner && owner.entry === entry && owner.canTranscribe()) owner.transcribeAndUpload().catch(() => false);
      } } });
  }

  private recordingOptions(): SelectOption[] {
    return this.model.entries.map((entry: KnowledgeRecordingEntry): SelectOption => ({
      value: entry.title || new Date(entry.startedAt).toLocaleString()
    }));
  }

  private recordingIndex(): number {
    const selectedId = this.model.entry?.id || '';
    const index = this.model.entries.findIndex((entry: KnowledgeRecordingEntry) => entry.id === selectedId);
    return index >= 0 ? index : 0;
  }

  private canWithdraw():boolean {
    return ['negotiating','connecting','live','reconnecting','waiting_network','finishing'].includes(this.model.hubAudio.phase);
  }
  private hubStatus():string {
    const phase=this.model.hubAudio.phase;
    if (phase==='device') return this.model.entry?.effectiveTranscription==='device' ? 'meeting_audio_fallback' : 'meeting_audio_previous';
    return 'meeting_audio_'+phase;
  }
  private hasHubTranscription():boolean {
    return this.model.hubAudio.phase!=='device' || this.model.entry?.askedTranscription==='hub';
  }
  private phoneLines():boolean {
    return this.model.hubAudio.usePhoneLines(this.model.entry?.roomRole || 'off');
  }
  private currentLine():string { return this.phoneLines() ? this.model.room.latest : this.model.hubAudio.latestLine; }
  private currentCaption():string {
    return this.model.hubAudio.showCaption() ? this.model.hubAudio.caption : '';
  }
  @Builder hubTranscription() {
    Column({space:7}) {
      Text(tr('meeting_cloud_transcription')).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17))
        .fontWeight(FontWeight.Medium).fontColor(Colors.ink)
      Text(tr(this.hubStatus())).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary).id('recordings.hub.status')
      if (this.model.hubAudio.problem && this.model.hubAudio.problem!=='no_interim' && this.model.hubAudio.phase==='live') {
        Text(tr('meeting_audio_processing_problem')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.working)
      }
      if (this.model.hubAudio.ackSeconds>0) {
        Text(tr('meeting_audio_received').replace('{seconds}',String(Math.floor(this.model.hubAudio.ackSeconds))))
          .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).id('recordings.hub.received')
      }
      if (this.currentLine() || this.currentCaption()) {
        if (this.currentLine()) {
          Text(tr(this.phoneLines() ? 'phone_meeting_local_transcription' : 'meeting_cloud_transcription')).fontSize(13)
            .maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).id('recordings.hub.source')
          Text(this.currentLine()).fontSize(16).fontColor(Colors.ink)
            .copyOption(CopyOptions.LocalDevice).width('100%').id('recordings.hub.line')
        }
        if (this.currentCaption()) {
          if (this.phoneLines() || !this.currentLine()) Text(tr('meeting_cloud_transcription')).fontSize(13)
            .maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).id('recordings.hub.captionSource')
          Text(this.currentCaption()).fontSize(16).fontColor(Colors.secondary).width('100%').id('recordings.hub.caption')
        }
      } else if (this.phoneLines() && this.model.entry?.roomRole==='off' && this.model.roomSpeechProblem) {
        Text(tr(this.model.roomSpeechProblem)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary)
      }
      if (this.canWithdraw()) {
        Button(tr('meeting_audio_withdraw'),{buttonStyle:ButtonStyleMode.TEXTUAL}).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15))
          .fontColor(Colors.danger).onClick(() => { Haptics.attention(); this.model.withdrawHubAudio(); }).id('recordings.hub.withdraw')
        Text(tr('meeting_audio_withdraw_note')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
      }
    }.width('100%').alignItems(HorizontalAlign.Start).padding(15).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.environment.dark)).id('recordings.hub')
  }

  @Builder meetingRoom() {
    Column({space:8}) {
      Text(tr('phone_meeting_room')).fontSize(18).maxFontScale(Tokens.interfaceScaleFor(18)).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
      Text(tr(this.model.room.state==='ended' ? 'phone_meeting_closed' : this.model.room.state==='unavailable' ? 'phone_meeting_unavailable' :
        this.model.room.state==='ready' ? 'phone_meeting_live_ready' : 'phone_meeting_live_starting')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary)
      if (this.model.roomSpeechProblem) Text(tr(this.model.roomSpeechProblem)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary)
      if (this.model.room.problem) Row({space:8}) {
        Text(tr(this.model.room.problem)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary).layoutWeight(1)
        Button(tr('retry'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.link).onClick(() => { Haptics.tap(); this.model.room.sync(); })
      }.width('100%')
      if (!this.hasHubTranscription() && this.model.room.latest) Text(this.model.room.latest).fontSize(16).fontColor(Colors.ink).width('100%').id('recordings.room.line')
      if (this.model.captureState==='recording') Row({space:8}) {
        Text(tr('phone_meeting_speaker')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.ink).layoutWeight(1)
        Toggle({type:ToggleType.Switch,isOn:this.model.entry?.roomRole==='participant'}).enabled(!this.model.speakerChanging)
          .accessibilityText(tr('phone_meeting_speaker')).onChange((on:boolean)=>{ Haptics.selection(); this.model.setRoomSpeaker(on,meetingEcho.enabled && meetingEcho.supported); })
          .id('recordings.room.speaker')
      }.width('100%')
      ForEach(this.model.room.shownAsks,(ask:MeetingAsk) => {
        Column({space:4}) {
          Text(ask.question).fontSize(15).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
          Text(ask.answer || tr(ask.state==='failed' ? 'phone_meeting_no_answer' : 'pending')).fontSize(15).fontColor(Colors.secondary)
        }.width('100%').alignItems(HorizontalAlign.Start).id('recordings.room.answer.'+ask.id)
      },(ask:MeetingAsk)=>JSON.stringify([ask.id,ask.state,ask.answer]))
      if (this.model.captureState==='recording') Row({space:6}) {
        TextInput({text:this.roomQuestion,placeholder:tr('phone_meeting_question')}).layoutWeight(1)
          .onChange((v:string)=>this.roomQuestion=v).id('recordings.room.question')
        Button(tr('send'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale).enabled(!!this.roomQuestion.trim()).fontColor(Colors.link)
          .onClick(() => { Haptics.tap(); const value=this.roomQuestion; this.roomQuestion=''; this.model.askRoom(value).then((ok:boolean) => { if (!ok && !this.roomQuestion) this.roomQuestion=value; }); })
          .id('recordings.room.ask')
      }.width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start).padding(15).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(this.environment.dark)).id('recordings.room')
  }
  build() {
    Column({space:12}) {
      Column({space:8}) {
        Row({space:8}) {
          if (this.model.captureState === 'recording') {
            NeedsYouPulseDot({ dotSize: 9, tint: Colors.danger, surface: Colors.surface, bordered: true })
          }
          SymbolGlyph($r('sys.symbol.mic_fill')).fontSize('19vp').fontColor([Colors.danger])
          Text(tr(this.statusKey())).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).layoutWeight(1)
            .id('knowledge.recording.status')
          if (this.model.captureState === 'requesting' || this.model.transcriptionState === 'transcribing' || this.model.uploadState === 'uploading')
            WorkingArcSpinner({ diameter: 16, tint: Colors.working, surface: Color.Transparent, showCoreDot: false })
        }.width('100%').alignItems(VerticalAlign.Center)
        if (this.model.entry) {
          Text(this.model.entry.title || new Date(this.model.entry.startedAt).toLocaleString()).fontSize(18).maxFontScale(Tokens.interfaceScaleFor(18)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(2)
        }
        Text(this.duration()).fontSize(32).maxFontScale(Tokens.interfaceScaleFor(32)).fontWeight(FontWeight.Bold).fontFeature('tnum 1').fontColor(Colors.ink).id('knowledge.recording.duration')
        if (this.model.canStop()) Button(tr('knowledge_recording_stop')).maxFontScale(Tokens.interfaceFontMaxScale).width('100%').height(43).fontColor(Color.White).backgroundColor(Colors.danger)
          .onClick(() => { Haptics.attention(); this.model.stop().catch(() => false); }).id('knowledge.recording.stop')
        else if (this.recordingAllowed) Button(tr('phone_record_start')).maxFontScale(Tokens.interfaceFontMaxScale).width('100%').height(43).fontColor(Color.White).backgroundColor(Colors.danger)
          .enabled(this.model.canStart())
          .onTouch((event: TouchEvent) => { if (event.type === TouchType.Down && this.model.canStart()) Haptics.tap(); })
          .onClick(() => this.onStart()).id('knowledge.recording.start')
        else Text(tr('xiaoyi_recording_module_off')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary).width('100%').id('knowledge.recording.module_off')
        if (this.model.hubAudio.phase==='device' && this.model.entry?.askedTranscription!=='hub') Text(tr('phone_record_audio_local')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(15).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
        .border({width:this.model.captureState === 'recording' ? 0.8 : PhoneStyle.surfaceEdgeWidth(0.5),
          color:this.model.captureState === 'recording' ? Colors.danger : PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
        .shadow(PhoneStyle.cardElevation(this.environment.dark))
        .animation(Motion.state(this.environment.reduceMotion))
      if (this.hasHubTranscription()) this.hubTranscription()
      if (this.model.room.active && this.model.entry?.roomRole!=='off') this.meetingRoom()
      if (this.model.entries.length > 1) Select(this.recordingOptions()).selected(this.recordingIndex())
        .enabled(this.model.canSelectEntry()).onSelect((i:number) => {
          Haptics.selection();
          const selected = this.model.entries[i]; if (selected) this.model.selectEntry(selected.id);
        }).width('100%').id('knowledge.recording.select')
      if (this.model.error) Text(trError(this.model.error)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.danger).width('100%').id('knowledge.recording.error')
      if (this.model.entry && this.model.captureState === 'stopped') Column({space:4}) {
        if (this.model.canPlay()) Button({type:ButtonType.Normal}) {
          Row({space:9}) {
            SymbolGlyph(this.model.playingId ? Symbols.stop : $r('sys.symbol.play_fill')).fontSize('17vp').fontColor([Colors.link])
            Text(tr(this.model.playingId ? 'stop' : 'phone_record_play')).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink).layoutWeight(1)
          }.width('100%').height(44)
        }.padding({left:12,right:12}).width('100%').backgroundColor(Colors.surface)
          .onClick(() => { Haptics.tap(); this.model.playSelected().catch(() => false); }).id('knowledge.recording.play')
        if (this.model.canTranscribe()) Button(this.transcribeLabel(),{buttonStyle:ButtonStyleMode.TEXTUAL}).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16))
          .width('100%').enabled(this.model.canTranscribe()).onClick(() => { Haptics.tap(); this.confirmTranscription(); }).id('knowledge.recording.transcribe')
        if (this.model.transcriptionState === 'transcribing') Button(tr('cancel'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale)
          .onClick(() => { Haptics.tap(); this.model.cancelTranscription().catch(() => undefined); }).id('knowledge.recording.cancel')
        if (this.model.canRetryUpload()) Button(tr('knowledge_recording_retry_upload'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale)
          .onClick(() => { Haptics.tap(); this.model.retryUpload().catch(() => false); }).id('knowledge.recording.retry_upload')
      }.width('100%').padding(6).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
        .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
        .shadow(PhoneStyle.cardElevation(this.environment.dark))
      if (this.model.entry?.transcript) Column({space:6}) {
        Text(tr('knowledge_transcript')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium).fontColor(Colors.secondary)
        Text(this.model.entry!.transcript).fontSize(17).fontColor(Colors.ink).copyOption(CopyOptions.LocalDevice).id('knowledge.recording.transcript')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(14).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
        .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(this.environment.dark),radius:PhoneStyle.cardRadius})
        .shadow(PhoneStyle.cardElevation(this.environment.dark))
      if (this.model.canDeleteLocal()) Button(tr('knowledge_recording_delete_local'),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.danger)
        .onClick(() => { Haptics.attention(); this.onDeleteLocal(); }).id('knowledge.recording.delete_local')
    }.padding({left:16,right:16,top:8,bottom:18}).width('100%').alignItems(HorizontalAlign.Start)
  }
}
