import { LengthMetrics } from '@kit.ArkUI';
import { PhoneTodoStartSection } from './PhoneTodoStartSection';
import { TodoWorkspace } from '../models/TodoWorkspace';
import { PhoneTodoDetailEditor } from '../models/PhoneTodoDetailEditor';
import { ResourceRow, Wire } from '../models/Wire';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import { PhoneStyle } from '../design/PhoneStyle';
import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { Haptics } from '../platform/Haptics';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { relativeActivityDate } from '../core/PhoneDate';

@ComponentV2
export struct PhoneTodoDetailForm {
  @Require @Param workspace: TodoWorkspace;
  @Require @Param editor: PhoneTodoDetailEditor;
  @Param locale: string = '';
  @Param now: number = 0;
  @Param status: string = '';
  @Param tone: ResourceColor = Colors.link;
  @Param statusLine:string = '';
  @BuilderParam statusMenu: () => void;

  private metric(normal: number, largest: number): number { return PhoneStyle.formSize(normal,largest,nativeEnvironment.fontScale); }
  private canChange(field: string): boolean {
    return this.workspace.detailEditable() && !this.workspace.saving && !this.editor.unavailable.includes(field);
  }
  private created(): string {
    const stamp=Date.parse(this.workspace.selected.createdAt);
    return Number.isFinite(stamp) ? tr('todo_detail_created').replace('{text}',relativeActivityDate(stamp,this.now,this.locale,
      tr('agent_time_just_now'),tr('kb_minutes_ago'),tr('kb_yesterday'))) : '';
  }
  private activeCount(): string {
    return tr('todo_detail_agents').replace('{n}',String(this.workspace.selected.agents.filter(a => !a.releasedAt).length));
  }
  private flag(field:string):boolean {
    return field==='due' ? this.editor.plan.hasDue : field==='allDay' ? this.editor.plan.allDay : this.editor.plan.hasReminder;
  }
  private moment(field:string):number { return field==='due' ? this.editor.plan.due : this.editor.plan.reminder; }
  private timed(field:string):boolean { return field==='remind' || !this.editor.plan.allDay; }
  private toggle(field: string, value: boolean): void {
    Haptics.selection();
    const plan=this.editor.plan.copy();
    if (field==='due') plan.hasDue=value;
    if (field==='allDay') plan.allDay=value;
    if (field==='remind') plan.hasReminder=value;
    Motion.run(this.getUIContext(), Motion.layout(nativeEnvironment.reduceMotion), () => {
      this.editor.change(this.workspace,plan,field==='allDay' ? 'due' : field);
    });
  }
  private dateLabel(value: number): string {
    return new Date(value).toLocaleDateString(this.locale || undefined,{year:'numeric',month:'short',day:'numeric'});
  }
  private clockLabel(value: number): string {
    return new Date(value).toLocaleTimeString(this.locale || undefined,{hour:'numeric',minute:'2-digit'});
  }
  private pick(field: string, time: boolean): void {
    if (!this.canChange(field)) return;
    Haptics.tap();
    const selected=this.workspace.selected;
    const current=new Date(field==='due' ? this.editor.plan.due : this.editor.plan.reminder);
    const apply=(choice:Date):void => {
      if (!this.canChange(field) || this.workspace.selected.id!==selected.id || this.workspace.selected.manager!==selected.manager) return;
      const p=this.editor.plan.copy(), value=new Date(field==='due' ? p.due : p.reminder);
      if (time) value.setHours(choice.getHours(),choice.getMinutes(),0,0); else value.setFullYear(choice.getFullYear(),choice.getMonth(),choice.getDate());
      if (field==='due') p.due=value.getTime(); else p.reminder=Math.max(value.getTime(),Math.min(current.getTime(),Date.now()));
      this.editor.change(this.workspace,p,field);
    };
    if (time) this.getUIContext().showTimePickerDialog({selected:current,onAccept:(v:TimePickerResult):void => {
      const date=new Date(current); date.setHours(v.hour ?? current.getHours(),v.minute ?? current.getMinutes()); apply(date);
    }});
    else this.getUIContext().showDatePickerDialog({selected:current,start:field==='remind' ? new Date(Math.min(current.getTime(),Date.now())) : new Date(1900,0,1),
      end:new Date(2100,11,31),onDateAccept:(value:Date):void => apply(value)});
  }
  private projectRows(): ResourceRow[] {
    const rows=this.workspace.detailProjects.length ? this.workspace.detailProjects.slice() : this.workspace.projects.slice();
    rows.sort((a:ResourceRow,b:ResourceRow) => Number(Wire.text(b.data(),'extra').startsWith('long'))-Number(Wire.text(a.data(),'extra').startsWith('long')) ||
      (a.title||a.id).localeCompare(b.title||b.id));
    const current=this.editor.plan.project;
    if (current && !rows.some((row:ResourceRow)=>row.id===current)) rows.unshift(new ResourceRow(current,current,'knowledge'));
    return rows;
  }
  private projectName(): string {
    const id=this.editor.plan.project;
    const row=this.projectRows().find((r:ResourceRow) => r.id===id);
    return id ? row?.title || id : tr('todo_detail_none');
  }
  private choose(field: string, value: string): void {
    Haptics.selection();
    const plan=this.editor.plan.copy(); if (field==='project') plan.project=value; else plan.priority=value;
    this.editor.change(this.workspace,plan,field);
  }

  @Builder summary() {
    Column({space:6}) {
      TextInput({text:this.editor.title,placeholder:tr('todo_detail_title')})
        .width('100%').height(this.metric(28,35.3)).padding(0).borderRadius(0).backgroundColor(Color.Transparent)
        .fontSize(Tokens.interfaceFontVp(22,nativeEnvironment.fontScale)).fontWeight(600).fontColor(Colors.ink)
        .enabled(this.canChange('title')).enterKeyType(EnterKeyType.Done).onFocus(() => this.editor.focus(this.workspace))
        .onChange((v:string) => this.editor.type(v)).onSubmit(() => this.editor.commitTitle(this.workspace))
        .onBlur(() => this.editor.commitTitle(this.workspace)).id('todo.detail.title')
      Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Center,space:{main:LengthMetrics.vp(6),cross:LengthMetrics.vp(4)}}) {
        Text(this.status).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(600).fontColor(this.tone)
          .padding({left:7,right:7,top:2,bottom:2}).backgroundColor(Colors.control).borderRadius(10)
          .bindContextMenu(this.statusMenu,ResponseType.LongPress)
        Text('·').fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
        Text(this.activeCount()).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
        if (this.created()) {
          Text('·').fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
          Text(this.created()).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
        }
      }.width('100%')
      if (this.statusLine) Text(this.statusLine).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary)
      if (this.workspace.selected.doneReason) Text(this.workspace.selected.doneReason).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary)
    }.alignItems(HorizontalAlign.Start).width('100%').constraintSize({minHeight:this.metric(94.3,109)}).padding({left:20,right:20,top:19,bottom:15})
      .backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark)).id('todo.detail')
  }
  @Builder words(value: string) {
    Text(value).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.ink).layoutWeight(1)
  }
  @Builder mark(symbol: Resource) {
    SymbolGlyph(symbol).fontSize(Tokens.interfaceFontVp(20,nativeEnvironment.fontScale)).fontColor([Colors.link]).width(28)
  }
  @Builder rule(inset: number) { Divider().strokeWidth(0.5).color(Colors.divider).margin({left:inset}) }
  @Builder toggleRow(field: string, label: string) {
    Row({space:12}) {
      if (field!=='allDay') this.mark(field==='due' ? $r('sys.symbol.calendar') : $r('sys.symbol.bell'))
      this.words(tr(label))
      Toggle({type:ToggleType.Switch,isOn:this.flag(field)}).selectedColor(Colors.positive).width(63).height(28)
        .hitTestBehavior(HitTestMode.None).accessibilityLevel('no')
    }.padding({left:20,right:20}).width('100%').constraintSize({minHeight:this.metric(51.8,58.8)})
      .accessibilityGroup(true).accessibilityRole(AccessibilityRoleType.SWITCH).accessibilityText(tr(label)).accessibilityChecked(this.flag(field))
      .enabled(this.canChange(field==='allDay'?'due':field)).onClick(() => this.toggle(field,!this.flag(field))).id('todo.plan.'+field)
  }
  @Builder dateButtons(field: string) {
    Row({space:6}) {
      Button(this.dateLabel(this.moment(field)),{type:ButtonType.Normal}).fontSize(Tokens.interfaceFontVp(16,nativeEnvironment.fontScale))
        .fontColor(Colors.ink).backgroundColor(Colors.control).borderRadius(9).padding({left:8,right:8}).height(this.metric(36,43))
        .enabled(this.canChange(field)).onClick(() => this.pick(field,false)).id(field==='due'?'todo.plan.dueDate':'todo.plan.remindAt')
      if (this.timed(field)) Button(this.clockLabel(this.moment(field)),{type:ButtonType.Normal}).fontSize(Tokens.interfaceFontVp(16,nativeEnvironment.fontScale))
        .fontColor(Colors.ink).backgroundColor(Colors.control).borderRadius(9).padding({left:8,right:8}).height(this.metric(36,43))
        .enabled(this.canChange(field)).onClick(() => this.pick(field,true)).id('todo.plan.'+field+'.time')
    }
  }
  @Builder dateRow(field: string) {
    if (nativeEnvironment.fontScale>1.6 && this.timed(field)) {
      Column({space:6}) {
        Text(tr(field==='due'?'todo_detail_due_value':'todo_detail_remind_at')).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.ink)
        this.dateButtons(field)
      }.width('100%').alignItems(HorizontalAlign.Start).constraintSize({minHeight:104.5}).padding({left:20,right:20,top:10,bottom:10})
    } else {
      Row({space:6}) {
        this.words(tr(field==='due'?'todo_detail_due_value':'todo_detail_remind_at')); this.dateButtons(field)
      }.padding({left:20,right:20}).width('100%').constraintSize({minHeight:this.metric(66.2,76)})
    }
  }
  @Builder choices(field: string) {
    Menu() {
      if (field==='priority') {
        ForEach(['high','','low'],(value:string) => {
          MenuItem({content:tr('todo_detail_'+(value || 'normal'))}).selected(this.editor.plan.priority===value).selectIcon(true)
            .onClick(() => this.choose(field,value)).id('todo.plan.priority.'+(value||'normal'))
        },(value:string) => value || 'normal')
      } else {
        MenuItem({content:tr('todo_detail_none')}).selected(!this.editor.plan.project).selectIcon(true)
          .onClick(() => this.choose(field,'')).id('todo.plan.project.none')
        ForEach(this.projectRows(),(row:ResourceRow) => {
          MenuItem({content:row.title || row.id}).selected(this.editor.plan.project===row.id).selectIcon(true)
            .onClick(() => this.choose(field,row.id)).id('todo.plan.project.'+row.id)
        },(row:ResourceRow)=>row.id)
      }
    }
  }
  @Builder choiceRow(field: string) {
    Row({space:12}) {
      this.mark(field==='priority' ? $r('sys.symbol.flag') : $r('sys.symbol.folder'))
      this.words(tr('todo_detail_'+field))
      Text(field==='project' ? this.projectName() : tr('todo_detail_'+(this.editor.plan.priority || 'normal')))
        .fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.secondary).maxLines(1).flexShrink(1)
      SymbolGlyph($r('sys.symbol.chevron_down')).fontSize('11vp').fontColor([Colors.tertiary])
    }.width('100%').constraintSize({minHeight:this.metric(51.8,58.8)}).padding({left:20,right:20})
      .enabled(this.canChange(field)).bindMenu(this.choices(field)).id('todo.plan.'+field)
  }

  build() {
    Column({space:35}) {
      this.summary()
      Column() {
        this.toggleRow('due','todo_detail_due')
        if (this.editor.plan.hasDue) {
          this.rule(60);this.toggleRow('allDay','todo_detail_all_day')
          this.rule(20);this.dateRow('due')
        }
        this.rule(20);this.toggleRow('remind','todo_detail_remind')
        if (this.editor.plan.hasReminder) {
          this.rule(60);this.dateRow('remind')
          if (Wire.text(this.workspace.selected.data(),'reminded_at')) Text(tr('todo_detail_reminded').replace('{text}',this.dateLabel(Date.parse(Wire.text(this.workspace.selected.data(),'reminded_at')))))
            .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
        }
        this.rule(60);this.choiceRow('priority')
        this.rule(60);this.choiceRow('project')
      }.width('100%').backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
        .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
        .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark)).clip(true)
        .animation(Motion.layout(nativeEnvironment.reduceMotion))
      if (this.workspace.selected.startError || this.workspace.selected.origin && !this.workspace.selected.agents.length)
        PhoneTodoStartSection({workspace:this.workspace})
    }.width('100%').alignItems(HorizontalAlign.Start).padding({top:23})
  }
}
