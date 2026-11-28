import { IconActions } from '../design/IconActions';
import { composerReadingSize } from '../core/ComposerSize';
import { agentReplyBodySize } from '../core/Markdown';
import { PhoneStyle } from '../design/PhoneStyle';
import { LengthMetrics } from '@kit.ArkUI';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { QuestionDraft } from '../models/QuestionWorkspace';
import { Symbols } from '../design/Symbols';
import { MarkdownText } from './MarkdownText';
import { RuleSuggestionBar } from './RuleSuggestionBar';
import { ClientModel } from '../models/ClientModel';
import { Decision, DecisionField, ResourceRow, Wire } from '../models/Wire';
import { AgentDecisionQueue, AgentDecisionCard } from '../models/AgentDecisionQueue';
import { util } from '@kit.ArkTS';
import {
  InteractionChoice, InteractionSchemaField, InteractionValidation,
  interactionCardAgent, interactionCanHandBack, interactionQuestionHeader, interactionPermissionTool, interactionPermissionSubject, interactionSessionAllowance, interactionAllowanceScope,
  recordedPlanMode, interactionCanDefer, interactionCanSubmit, interactionSupervisorHeld, interactionToolName, interactionElicitationDefaults, interactionElicitationMessage,
  interactionElicitationMode, interactionElicitationSupported, interactionElicitationUrl, interactionHistoryResolution,
  interactionPermissionFieldPlaceholder, interactionPlanTitle, interactionQuestionOptions,
  interactionSchemaFields, interactionSkipReason, interactionScreenCanCancel, interactionScreenMulti, interactionScreenOptions,
  interactionScreenQuestion, interactionScreenSubmitLabel, validateInteractionElicitation
} from '../models/InteractionDecision';
import { Tokens } from '../design/Tokens';
import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { MotionTokens } from '../core/MotionTokens';
import { ButtonStyles } from '../design/Controls';
import { tr } from '../design/Strings';
import { Haptics } from '../platform/Haptics';

@ComponentV2
export struct InteractionDecisionForm {
  @Require @Param model: ClientModel;
  @Require @Param request: Decision;
  @Param readOnly: boolean = false;
  @Param docked: boolean = false;
  @Local private headerEnvironment: NativeEnvironment = nativeEnvironment;
  @Param resizeDock: boolean = false;
  @Event onDockResize:(phase:string,delta:number)=>void = (_phase:string,_delta:number)=>{};
  @Local private dockTitleHeight: number = 0;
  @Local private planNaturalHeight: number = 0;
  @Computed get dockPlanPreview(): string {
    return this.taskPlanSource().split('\n',31).slice(0,30).join('\n');
  }
  private dockPlanLineHeight(lines: number): number {
    // The fold measures an N-line native body Text. Markdown uses its own reading
    // face. Only the N - 1 gaps between its lines have extra leading.
    const scale = nativeEnvironment.fontScale;
    const body = composerReadingSize(scale);
    const markdownPoints = agentReplyBodySize(scale);
    return body.line + markdownPoints * 0.26 * (lines - 1) / lines;
  }
  private dockPlanShownHeight():number {
    const full=this.planNaturalHeight;
    if (!full) return 0;
    const maximum = this.model.style === 'beautiful' ? 6 : 8;
    const line = this.dockPlanLineHeight(maximum), cap=this.dockBodyLimit(), toggle=this.agentFontSize(13)*1.2+4;
    const lines=Math.max(2,Math.min(maximum,Math.floor((cap-toggle+0.5)/line)));
    const shown=Math.min(full,lines*line);
    return this.dockPlanPreview.length===this.taskPlanSource().length && full-shown<2.5*line ? full : shown;
  }
  @Builder dockPlanWords() {
    Column({space:4}) {
      if (this.model.style==='beautiful') {
        Text(tr('style_plan_question')).fontSize(17).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
          .width('100%').textAlign(TextAlign.Start).margin({bottom:6})
      }
      Scroll() {
        Column() {
          MarkdownText({text:this.dockPlanPreview,chatReply:true,recapReading:true,dockPlan:true,onLink:this.onLink})
        }.width('100%').onAreaChange((_before:Area,area:Area):void => {
          const h=Number(area.height);if (h>0 && Math.abs(h-this.planNaturalHeight)>0.5) this.planNaturalHeight=h;
        })
      }.width('100%').height(this.planNaturalHeight ? this.dockPlanShownHeight() : undefined)
        .scrollBar(BarState.Off).enableScrollInteraction(false).align(Alignment.TopStart)
      if (this.dockPlanPreview.length<this.taskPlanSource().length || this.planNaturalHeight>this.dockPlanShownHeight()+1) Button({type:ButtonType.Normal}) {
        Row({space:4}) {
          Text(tr('phone_decision_full_text')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium).fontColor(Colors.link)
          SymbolGlyph(Symbols.expand).fontSize(Tokens.interfaceFontVp(12,nativeEnvironment.fontScale)).fontColor([Colors.link])
        }
      }.padding(0).backgroundColor(Color.Transparent).height(this.agentFontSize(13)*1.2)
        .responseRegion({x:0,y:-6,width:'100%',height:36}).accessibilityText(tr('phone_decision_full_text'))
        .onClick(() => { if (this.admission()) this.fullPlan=true; }).id('interaction.task.plan.expand')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
  @Param agentCard: boolean = false;
  @Param dockSource: string = '';
  @Event onDockFold: () => void = () => {};
  @Event onViewTask: () => void = () => {};
  @Param taskQueue: AgentDecisionQueue | undefined = undefined;
  @Param taskCard: AgentDecisionCard | undefined = undefined;
  @Param taskAgent: ResourceRow | undefined = undefined;
  @Param pinFooter: boolean = false;
  @Param fillAvailable: boolean = false;
  @Param heightLimit: number = 0;
  @Local inlineBodyHeight: number = 0;
  @Local inlineFooterHeight: number = 44;
  @Local inlineHeaderHeight: number = 28;
  @Local cardFolded: boolean = false;
  @Local questionPage: number = 0;
  @Local reviewingQuestions: boolean = false;
  @Local otherQuestion: string = '';
  @Local commandExpanded: boolean = false;
  @Local fullPlan: boolean = false;
  @Param draft: QuestionDraft | undefined = undefined;
  @Param draftSession: string = '';
  @Event admission: () => boolean = (): boolean => true;
  @Event onLater: () => void = (): void => {};
  @Event onResolved: () => void = (): void => {};
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Local selected: string = '';
  @Local selectedScreen: string = '';
  @Local screenOptions: string[] = [];
  @Local screenText: string = '';
  @Local openedUrl: string = '';
  @Local busy: boolean = false;
  @Local deferred: boolean = false;
  @Local private pressedButton: string = '';
  @Local textValues: Record<string, string> = Wire.payload([], []) as Record<string, string>;
  @Local questionOptions: Record<string, InteractionChoice[]> = {};
  @Local questionPicks: Record<string, string[]> = Wire.payload([], []) as Record<string, string[]>;
  @Local formValues: Record<string, Object> = Wire.payload([], []);

  aboutToAppear(): void { this.resetForm(); if (this.draft && this.draft.id === this.request.id && this.draft.raw === this.request.raw && this.draft.owns(this.draftSession)) { const text: Record<string, string> = {}, picks: Record<string, string[]> = {}; Object.keys(this.draft.text).forEach((key: string) => text[key] = this.draft!.text[key]); Object.keys(this.draft.picks).forEach((key: string) => picks[key] = this.draft!.picks[key].slice()); this.textValues = text; this.questionPicks = picks; } }
  aboutToDisappear(): void { this.fullPlan=false; if (this.draft && this.draft.id === this.request.id && this.draft.raw === this.request.raw) this.draft.save(this.textValues, this.questionPicks, this.draftSession); }
  @Monitor('request.id', 'request.raw') requestChanged(): void { this.resetForm(); }
  @Monitor('request.revision') revisionChanged(): void {
    this.selectedScreen = '';
    this.screenOptions = [];
    this.screenText = '';
    this.deferred = false;
  }

  private resetForm(): void {
    this.cardFolded = false; this.questionPage=0; this.reviewingQuestions=false; this.otherQuestion=''; this.commandExpanded=false;
    this.fullPlan = false;
    this.selected = '';
    this.selectedScreen = '';
    this.screenOptions = [];
    this.screenText = '';
    this.openedUrl = '';
    this.busy = false;
    this.deferred = false;
    this.textValues = Wire.payload([], []) as Record<string, string>;
    this.questionPicks = Wire.payload([], []) as Record<string, string[]>;
    this.formValues = interactionElicitationDefaults(this.request);
    const choices: Record<string, InteractionChoice[]> = {};
    if (this.request.kind === 'question') this.request.fields.forEach((field: DecisionField) => choices[field.key] = interactionQuestionOptions(this.request, field.key));
    this.questionOptions = choices;
  }

  private persistQuestionDraft(): void { if (this.request.kind === 'question' && this.draft && this.draft.id===this.request.id && this.draft.raw===this.request.raw && this.draft.owns(this.draftSession) && this.admission()) this.draft.save(this.textValues, this.questionPicks, this.draftSession); }
  private canRespond(): boolean {
    if (!this.admission() || this.busy || !interactionCanSubmit(this.request,this.readOnly)) return false;
    if (this.taskQueue) return !!this.taskCard && this.taskCard.request===this.request && !!this.taskAgent &&
      this.taskQueue.enabled(this.model,this.taskAgent,this.taskCard);
    return this.model.canApprove();
  }

  private canDefer(): boolean {
    return !this.taskQueue && interactionCanDefer(this.request, this.model.selected.kind, this.readOnly, this.busy);
  }

  private headerPrompt(): string {
    if (this.request.kind === 'screen') return interactionScreenQuestion(this.request);
    if (this.request.kind === 'elicitation') return interactionElicitationMessage(this.request);
    // Each question already labels its own body; only a distinct Hub prompt belongs above it.
    return this.request.prompt;
  }

  private submitButtonLabel(): string {
    return this.request.kind === 'screen' && interactionScreenMulti(this.request) ?
      interactionScreenSubmitLabel(this.request) : tr('confirm');
  }

  private submitIconKey(): string {
    if (this.request.kind === 'plan' || this.request.kind === 'permission') {
      if (this.selected === 'deny') return 'deny';
      if (this.selected === 'approve') return 'approve';
    }
    return 'confirm';
  }
  private submitIconLabel(): string {
    if ((this.request.kind === 'plan' || this.request.kind === 'permission') && ['approve', 'deny'].includes(this.selected))
      return tr('confirm') + ' ' + tr(this.selected === 'deny' ? 'deny' : this.request.kind === 'permission' ? 'allow' : 'approve');
    return this.submitButtonLabel();
  }

  private submitButtonOption(): string {
    return this.request.kind === 'screen' && interactionScreenMulti(this.request) ? 'submit' :
      this.request.kind === 'screen' ? this.selectedScreen : this.selected;
  }

  private statusLabel(): string {
    if (this.request.status === 'open') return tr('answer');
    if (this.request.status === 'pending') return tr('pending');
    if (this.request.status === 'resolved') return tr('completed');
    if (this.request.status === 'unknown') return tr('result_unknown');
    if (this.request.status === 'expired' || this.request.status === 'cancelled' || this.request.status === 'dismissed') return tr('canceled');
    return this.request.status;
  }

  private setText(key: string, value: string): void {
    if (!this.admission()) return;
    const next: Record<string, string> = Wire.payload([], []) as Record<string, string>;
    Object.keys(this.textValues).forEach((name: string) => next[name] = this.textValues[name]);
    next[key] = value;
    this.textValues = next;
    this.persistQuestionDraft();
  }

  private setValue(key: string, value: Object | undefined): void {
    const next: Record<string, Object> = Wire.payload([], []);
    Object.keys(this.formValues).filter((name: string) => name !== key).forEach((name: string) => next[name] = this.formValues[name]);
    if (value !== undefined && value !== null) next[key] = value;
    this.formValues = next;
  }

  private setQuestionPicks(key: string, values: string[]): void {
    const next: Record<string, string[]> = Wire.payload([], []) as Record<string, string[]>;
    Object.keys(this.questionPicks).forEach((name: string) => next[name] = this.questionPicks[name]);
    next[key] = values;
    this.questionPicks = next;
  }

  private setScreenOptions(values: string[]): void { this.screenOptions = values; }

  private questionChoices(field: DecisionField): InteractionChoice[] { return this.questionOptions[field.key] || []; }

  private pickedQuestion(field: DecisionField, value: string): boolean {
    return (this.questionPicks[field.key] || []).includes(value);
  }

  private pickQuestion(field: DecisionField, choice: InteractionChoice, checked: boolean | undefined = undefined): void {
    if (field.kind !== 'multi_choice') {
      this.setQuestionPicks(field.key, [choice.label]);
      this.setText(field.key + ':other', '');
      return;
    }
    const next = (this.questionPicks[field.key] || []).filter((item: string) => item !== choice.label);
    const picked = checked === undefined ? !this.pickedQuestion(field, choice.label) : checked;
    if (picked) next.push(choice.label);
    const ordered = this.questionChoices(field).filter((item: InteractionChoice) => next.includes(item.label)).map((item: InteractionChoice) => item.label);
    this.setQuestionPicks(field.key, ordered);
    this.persistQuestionDraft();
  }

  private toggleScreenOption(choice: InteractionChoice, checked: boolean | undefined = undefined): void {
    const next = this.screenOptions.filter((key: string) => key !== choice.key);
    const picked = checked === undefined ? !this.screenOptions.includes(choice.key) : checked;
    if (picked) next.push(choice.key);
    const ordered = interactionScreenOptions(this.request).filter((item: InteractionChoice) => next.includes(item.key)).map((item: InteractionChoice) => item.key);
    this.setScreenOptions(ordered);
  }

  private screenPicked(choice: InteractionChoice): boolean {
    return this.screenOptions.includes(choice.key);
  }

  private screenFreeText(): boolean {
    return interactionScreenOptions(this.request).some((choice: InteractionChoice) => choice.kind === 'type_something');
  }

  private selectedScreenChoice(): InteractionChoice | undefined {
    return interactionScreenOptions(this.request).find((choice: InteractionChoice) => choice.key === this.selectedScreen);
  }

  private schemaFields(): InteractionSchemaField[] { return interactionSchemaFields(this.request); }

  private schemaText(field: InteractionSchemaField): string {
    const value = this.formValues[field.key];
    return value === undefined || value === null ? '' : String(value);
  }

  private schemaPicked(field: InteractionSchemaField, index: number): boolean {
    const value = this.formValues[field.key], choice = field.choices[index];
    if (field.type === 'array') return Array.isArray(value) && (value as Object[]).some((item: Object) => String(item) === String(choice));
    return value !== undefined && String(value) === String(choice);
  }

  private schemaValidation(): InteractionValidation {
    return validateInteractionElicitation(this.request, this.formValues);
  }

  private inputType(field: InteractionSchemaField): InputType {
    if (field.secret) return InputType.Password;
    if (field.type === 'number' || field.type === 'integer') return InputType.Number;
    return InputType.Normal;
  }

  private canSubmit(): boolean {
    if (!this.canRespond()) return false;
    if (this.request.kind === 'plan' || this.request.kind === 'permission') {
      if (!this.selected) return false;
      if (this.selected === 'deny') return true;
      if (this.request.kind === 'permission') return !this.request.fields.some((field: DecisionField) =>
        field.required && !(this.textValues[field.key] || '').trim());
      return true;
    }
    if (this.request.kind === 'question') return this.request.fields.length > 0 && this.request.fields.every((field: DecisionField) =>
      !!(this.textValues[field.key + ':other'] || (this.questionPicks[field.key] || []).join(', ')).trim());
    if (this.request.kind === 'screen') {
      if (interactionScreenMulti(this.request)) return this.screenOptions.length > 0 || !!this.screenText.trim();
      const choice = this.selectedScreenChoice();
      return !!choice && choice.kind !== 'unknown' && (choice.kind !== 'type_something' || !!this.screenText.trim());
    }
    if (this.request.kind === 'elicitation') {
      const mode = interactionElicitationMode(this.request);
      if (mode === 'url') return !!interactionElicitationUrl(this.request) && this.openedUrl === interactionElicitationUrl(this.request);
      return interactionElicitationSupported(this.request) && this.schemaValidation().valid;
    }
    return false;
  }

  private answerValues(): Record<string, Object> {
    const values: Record<string, Object> = Wire.payload([], []);
    if (this.request.kind === 'question') {
      this.request.fields.forEach((field: DecisionField) => {
        const other = (this.textValues[field.key + ':other'] || '').trim();
        const options = this.questionChoices(field).filter((choice: InteractionChoice) => (this.questionPicks[field.key] || []).includes(choice.label))
          .map((choice: InteractionChoice) => choice.label);
        const answer = other || options.join(', ');
        if (answer) values[field.key] = answer;
      });
    } else if (this.request.kind === 'plan') {
      values['feedback'] = this.textValues['feedback'] || '';
      if (this.selected==='approve' && this.textValues['mode']) values['mode']=this.textValues['mode'];
    } else if (this.request.kind === 'permission') {
      if (this.selected !== 'deny') this.request.fields.forEach((field: DecisionField) => {
        const value = (this.textValues[field.key] || '').trim();
        if (value) values[field.key] = value;
      });
    } else if (this.request.kind === 'screen') {
      values['options'] = this.screenOptions;
      values['text'] = this.screenText;
    } else if (this.request.kind === 'elicitation') {
      Object.keys(this.formValues).forEach((key: string) => values[key] = this.formValues[key]);
      if (this.openedUrl) values['opened_url'] = this.openedUrl;
    }
    return values;
  }

  private clearSecrets(): void {
    const keys: string[] = this.request.kind === 'permission' ? this.request.fields.filter((field: DecisionField) => field.kind === 'secret').map((field: DecisionField) => field.key) :
      this.request.kind === 'elicitation' ? this.schemaFields().filter((field: InteractionSchemaField) => field.secret).map((field: InteractionSchemaField) => field.key) : [];
    const next: Record<string, string> = Wire.payload([], []) as Record<string, string>;
    Object.keys(this.textValues).filter((key: string) => !keys.includes(key)).forEach((key: string) => next[key] = this.textValues[key]);
    this.textValues = next;
    keys.forEach((key: string) => this.setValue(key, undefined));
  }

  private submit(option: string): void {
    if (!this.canRespond()) return;
    if (option === 'dismiss') {
      if ((!this.taskQueue && !this.agentCard) || interactionToolName(this.request) === 'ask_user') return;
    } else if (this.request.kind === 'question' && option === 'cancel') {
      if (!interactionScreenCanCancel(this.request)) return;
    } else if (this.request.kind === 'elicitation' && (option === 'decline' || option === 'cancel')) {
      // Decline and cancel remain available when a form is unsupported or incomplete.
    } else if (!this.canSubmit()) return;
    const values = option === 'dismiss' || this.request.kind === 'question' && option === 'cancel' || this.request.kind === 'elicitation' && option !== 'accept' ?
      Wire.payload([], []) : this.answerValues();
    this.busy = true;
    this.clearSecrets();
    const request=this.request, raw=request.raw, agent=this.taskAgent, card=this.taskCard;
    const answer=this.taskQueue && agent && card ? this.taskQueue.answerForm(this.model,agent,card,option,values,util.generateRandomUUID(),this.admission) :
      this.model.answer(option,values,request,this.admission);
    answer.then(() => {
      if (request===this.request && request.raw===raw && request.status==='resolved') {
        Haptics.success();
        this.onResolved();
      }
    })
      .catch(() => { if (request===this.request && request.raw===raw && this.admission()) { Haptics.error(); this.model.error='failed'; } })
      .finally(() => { if (request===this.request && request.raw===raw) this.busy=false; });
  }

  private historySummary(): string {
    const result = interactionHistoryResolution(this.request), rows: string[] = [];
    if (this.request.kind === 'question') Object.keys(result).forEach((key: string) => rows.push(key + ': ' + Wire.text(result, key)));
    else if (this.request.kind === 'plan') {
      if (result['approve'] === true) rows.push(tr('approve'));
      else if (result['approve'] === false) rows.push(tr('deny'));
      if (Wire.text(result, 'feedback')) rows.push(Wire.text(result, 'feedback'));
    } else if (this.request.kind === 'permission') {
      if (result['allow'] === true) rows.push(tr('allow'));
      else if (result['allow'] === false) rows.push(tr('deny'));
    } else if (this.request.kind === 'screen') {
      const choice = Wire.text(result, 'option_label');
      if (choice) rows.push(choice);
      const options = Wire.strings(result, 'options_labels');
      if (options.length) rows.push(options.join(', '));
      if (Wire.text(result, 'text')) rows.push(Wire.text(result, 'text'));
    } else if (this.request.kind === 'elicitation') {
      const action = Wire.text(result, 'action');
      if (action === 'accept') rows.push(tr('approve'));
      else if (action === 'decline') rows.push(tr('deny'));
      else if (action === 'cancel') rows.push(tr('canceled'));
    }
    return rows.join('\n');
  }

  @Builder actionChoice(label: string, value: string) {
    Row({ space: Tokens.inset }) {
      Text(label).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1)
      Radio({ value: value, group: 'persona-interaction-action-' + this.request.id })
        .checked(this.selected === value)
        .radioStyle({ checkedBackgroundColor: Colors.action, uncheckedBorderColor: Colors.secondary, indicatorColor: Colors.onAction })
        .accessibilityText(label).enabled(this.canRespond())
        .onChange((checked: boolean) => { if (checked) this.selected = value; })
    }.width('100%').constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.fine, bottom: Tokens.fine })
      .enabled(this.canRespond()).onClick(() => { if (this.canRespond()) this.selected = value; })
      .id('interaction.action.' + value)
  }

  @Builder questionChoice(field: DecisionField, choice: InteractionChoice) {
    Row({ space: Tokens.inset }) {
      Column({ space: Tokens.fine }) {
        Text(choice.label).fontSize(Tokens.body).fontColor(Colors.ink).width('100%')
        if (choice.detail) Text(choice.detail).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
      }.layoutWeight(1).alignItems(HorizontalAlign.Start).onClick(() => {
        if (this.canRespond()) this.pickQuestion(field, choice);
      })
      if (field.kind === 'multi_choice') Checkbox({ name: choice.label })
        .select(this.pickedQuestion(field, choice.label)).selectedColor(Colors.action).unselectedColor(Colors.secondary)
        .accessibilityText(choice.label).responseRegion({ x: -Tokens.fine, y: -Tokens.fine, width: Tokens.touch, height: Tokens.touch }).enabled(this.canRespond())
        .onChange((checked: boolean) => this.pickQuestion(field, choice, checked))
      else Radio({ value: choice.label, group: 'persona-interaction-question-' + this.request.id + '-' + field.key })
        .checked(this.pickedQuestion(field, choice.label))
        .radioStyle({ checkedBackgroundColor: Colors.action, uncheckedBorderColor: Colors.secondary, indicatorColor: Colors.onAction })
        .accessibilityText(choice.label).responseRegion({ x: -Tokens.fine, y: -Tokens.fine, width: Tokens.touch, height: Tokens.touch }).enabled(this.canRespond())
        .onChange((checked: boolean) => { if (checked) this.pickQuestion(field, choice); })
    }.width('100%').constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.fine, bottom: Tokens.fine })
      .enabled(this.canRespond()).onClick(() => { if (this.canRespond() && field.kind !== 'multi_choice') this.pickQuestion(field, choice); })
      .id('interaction.question.' + field.key + '.' + choice.label)
  }

  @Builder screenChoice(choice: InteractionChoice) {
    Row({ space: Tokens.inset }) {
      Column({ space: Tokens.fine }) {
        Text(choice.label).fontSize(Tokens.body).fontColor(Colors.ink).width('100%')
        if (choice.detail) Text(choice.detail).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
      }.layoutWeight(1).alignItems(HorizontalAlign.Start).onClick(() => {
        if (!this.canRespond()) return;
        if (interactionScreenMulti(this.request) && choice.kind === 'plain') this.toggleScreenOption(choice);
        else if (!interactionScreenMulti(this.request)) this.selectedScreen = choice.key;
      })
      if (interactionScreenMulti(this.request) && choice.kind === 'plain') Checkbox({ name: choice.label })
        .select(this.screenPicked(choice)).selectedColor(Colors.action).unselectedColor(Colors.secondary)
        .accessibilityText(choice.label).responseRegion({ x: -Tokens.fine, y: -Tokens.fine, width: Tokens.touch, height: Tokens.touch }).enabled(this.canRespond())
        .onChange((checked: boolean) => this.toggleScreenOption(choice, checked))
      else if (!interactionScreenMulti(this.request)) Radio({ value: choice.key, group: 'persona-interaction-screen-' + this.request.id })
        .checked(this.selectedScreen === choice.key)
        .radioStyle({ checkedBackgroundColor: Colors.action, uncheckedBorderColor: Colors.secondary, indicatorColor: Colors.onAction })
        .accessibilityText(choice.label).responseRegion({ x: -Tokens.fine, y: -Tokens.fine, width: Tokens.touch, height: Tokens.touch }).enabled(this.canRespond())
        .onChange((checked: boolean) => { if (checked) this.selectedScreen = choice.key; })
    }.width('100%').constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.fine, bottom: Tokens.fine })
      .enabled(this.canRespond()).onClick(() => {
        if (!this.canRespond()) return;
        if (!interactionScreenMulti(this.request)) this.selectedScreen = choice.key;
      }).id('interaction.screen.' + choice.key)
  }

  @Builder schemaChoice(field: InteractionSchemaField, index: number) {
    Row({ space: Tokens.inset }) {
      Text(field.choiceLabels[index]).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1).onClick(() => {
        if (!this.canRespond()) return;
        if (field.type === 'array') this.toggleSchemaChoice(field, index, !this.schemaPicked(field, index));
        else this.setValue(field.key, field.choices[index]);
      })
      if (field.type === 'array') Checkbox({ name: field.choiceLabels[index] })
        .select(this.schemaPicked(field, index)).selectedColor(Colors.action).unselectedColor(Colors.secondary)
        .accessibilityText(field.choiceLabels[index]).responseRegion({ x: -Tokens.fine, y: -Tokens.fine, width: Tokens.touch, height: Tokens.touch }).enabled(this.canRespond())
        .onChange((checked: boolean) => this.toggleSchemaChoice(field, index, checked))
      else Radio({ value: String(field.choices[index]), group: 'persona-interaction-field-' + this.request.id + '-' + field.key })
        .checked(this.schemaPicked(field, index))
        .radioStyle({ checkedBackgroundColor: Colors.action, uncheckedBorderColor: Colors.secondary, indicatorColor: Colors.onAction })
        .accessibilityText(field.choiceLabels[index]).responseRegion({ x: -Tokens.fine, y: -Tokens.fine, width: Tokens.touch, height: Tokens.touch }).enabled(this.canRespond())
        .onChange((checked: boolean) => { if (checked) this.setValue(field.key, field.choices[index]); })
    }.width('100%').constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.fine, bottom: Tokens.fine })
      .enabled(this.canRespond()).onClick(() => {
        if (!this.canRespond()) return;
        if (field.type !== 'array') this.setValue(field.key, field.choices[index]);
      }).id('interaction.field.choice.' + field.key + '.' + String(index))
  }

  private toggleSchemaChoice(field: InteractionSchemaField, index: number, checked: boolean): void {
    const selected: Object[] = Array.isArray(this.formValues[field.key]) ? this.formValues[field.key] as Object[] : [];
    const choice = field.choices[index], next = selected.filter((item: Object) => String(item) !== String(choice));
    if (checked) next.push(choice);
    const ordered = field.choices.filter((item: Object) => next.some((value: Object) => String(value) === String(item)));
    this.setValue(field.key, ordered.length ? ordered : undefined);
  }

  @Builder questionField(field: DecisionField) {
    Column({ space: Tokens.gap }) {
      Text(field.title).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      ForEach(this.questionChoices(field), (choice: InteractionChoice) => this.questionChoice(field, choice), (choice: InteractionChoice) => choice.label)
      TextInput({ text: this.textValues[field.key + ':other'] || '', placeholder: tr('other') })
        .width('100%').constraintSize({ minHeight: Tokens.touch }).fontColor(Colors.ink).placeholderColor(Colors.secondary).caretColor(Colors.action)
        .fontSize(Tokens.body).placeholderFont({ size: Tokens.body })
        .enabled(this.canRespond()).onChange((value: string) => this.setText(field.key + ':other', value)).id('interaction.question.' + field.key + '.other')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder permissionField(field: DecisionField) {
    Column({ space: Tokens.gap }) {
      Text(field.title).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      TextInput({ text: this.textValues[field.key] || '', placeholder: interactionPermissionFieldPlaceholder(this.request, field.key) })
        .type(field.kind === 'secret' ? InputType.Password : InputType.Normal).showPasswordIcon(false)
        .width('100%').constraintSize({ minHeight: Tokens.touch }).fontColor(Colors.ink).placeholderColor(Colors.secondary).caretColor(Colors.action)
        .fontSize(Tokens.body).placeholderFont({ size: Tokens.body })
        .enabled(this.canRespond()).onChange((value: string) => this.setText(field.key, value)).id('interaction.field.' + field.key)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder elicitationField(field: InteractionSchemaField) {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Text(field.title).fontSize(Tokens.body).fontWeight(FontWeight.Medium).layoutWeight(1)
        if (field.required) Text(tr('required')).fontSize(Tokens.meta).fontColor(Colors.secondary)
      }.width('100%')
      if (field.description) Text(field.description).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
      if (field.type === 'boolean') {
        Toggle({ type: ToggleType.Switch, isOn: this.formValues[field.key] === true })
          .selectedColor(Colors.action).enabled(this.canRespond()).onChange((checked: boolean) => this.setValue(field.key, checked)).id('interaction.field.' + field.key)
      } else if (field.choices.length > 0) {
        ForEach(field.choices, (_choice: Object, index: number) => this.schemaChoice(field, index), (choice: Object, index: number) => field.key + ':' + String(index) + ':' + String(choice))
      } else {
        TextInput({ text: this.schemaText(field), placeholder: field.title }).type(this.inputType(field)).showPasswordIcon(false)
          .width('100%').constraintSize({ minHeight: Tokens.touch }).fontColor(Colors.ink).placeholderColor(Colors.secondary).caretColor(Colors.action)
          .fontSize(Tokens.body).placeholderFont({ size: Tokens.body })
          .enabled(this.canRespond()).onChange((value: string) => this.setValue(field.key, value || undefined)).id('interaction.field.' + field.key)
      }
      if (this.schemaValidation().errors[field.key]) Text(tr(this.schemaValidation().errors[field.key] === 'required' ? 'required' : 'failed')).fontSize(Tokens.meta)
        .fontColor(Colors.needs).width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder elicitationForm() {
    if (interactionElicitationSupported(this.request)) {
      ForEach(this.schemaFields(), (field: InteractionSchemaField) => this.elicitationField(field), (field: InteractionSchemaField) => field.key)
    } else {
      Text(tr('unknown_field')).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
    }
  }

  @Builder bodyContent() {
    if (this.request.kind === 'question') {
      ForEach(this.request.fields, (field: DecisionField) => this.questionField(field), (field: DecisionField) => field.key)
    } else if (this.request.kind === 'plan') {
      this.actionChoice(tr('approve'), 'approve')
      this.actionChoice(tr('deny'), 'deny')
      if (this.selected === 'deny') TextArea({ text: this.textValues['feedback'] || '', placeholder: tr('feedback') })
        .width('100%').constraintSize({ minHeight: Tokens.touch }).fontSize(Tokens.body).lineHeight(Tokens.lineHeight)
        .fontColor(Colors.ink).placeholderColor(Colors.secondary).caretColor(Colors.action)
        .enabled(this.canRespond()).onChange((value: string) => this.setText('feedback', value))
    } else if (this.request.kind === 'permission') {
      ForEach(this.request.fields, (field: DecisionField) => this.permissionField(field), (field: DecisionField) => field.key)
      this.actionChoice(tr('allow'), 'approve')
      this.actionChoice(tr('deny'), 'deny')
    } else if (this.request.kind === 'screen') {
      ForEach(interactionScreenOptions(this.request), (choice: InteractionChoice) => this.screenChoice(choice), (choice: InteractionChoice) => choice.key)
      if (interactionScreenMulti(this.request) && this.screenFreeText()) TextInput({ text: this.screenText, placeholder: tr('other') })
        .width('100%').constraintSize({ minHeight: Tokens.touch }).fontSize(Tokens.body).placeholderFont({ size: Tokens.body })
        .fontColor(Colors.ink).placeholderColor(Colors.secondary).caretColor(Colors.action)
        .enabled(this.canRespond()).onChange((value: string) => this.screenText = value).id('interaction.screen.other')
      else if (!interactionScreenMulti(this.request) && this.selectedScreenChoice()?.kind === 'type_something') TextInput({ text: this.screenText, placeholder: tr('other') })
        .width('100%').constraintSize({ minHeight: Tokens.touch }).fontSize(Tokens.body).placeholderFont({ size: Tokens.body })
        .fontColor(Colors.ink).placeholderColor(Colors.secondary).caretColor(Colors.action)
        .enabled(this.canRespond()).onChange((value: string) => this.screenText = value).id('interaction.screen.other')
    } else if (this.request.kind === 'elicitation') {
      if (interactionElicitationMode(this.request) === 'url' && interactionElicitationUrl(this.request)) {
        Text(interactionElicitationUrl(this.request)).fontSize(Tokens.meta).width('100%')
      } else if (interactionElicitationMode(this.request) === 'url') {
        Text(tr('unsafe_endpoint')).fontSize(Tokens.meta).fontColor(Colors.needs).width('100%')
      } else {
        this.elicitationForm()
      }
    }
  }

  private openUrl(): void {
    const address = interactionElicitationUrl(this.request);
    if (!address || !this.canRespond()) return;
    this.onLink(address);
    this.openedUrl = address;
  }

  @Builder headerContent() {
    Column({ space: Tokens.gap }) {
      if (this.request.status !== 'open' || this.readOnly || this.deferred) Row({ space: Tokens.gap }) {
        Text(this.deferred ? tr('interaction_question_later') : this.statusLabel()).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).layoutWeight(1)
        if (this.readOnly) Text(tr('read_only')).fontSize(Tokens.meta).fontColor(Colors.secondary)
      }.width('100%')
      if (this.request.kind === 'plan' && this.taskPlanSource()) {
        Column() {
          MarkdownText({ text: this.taskPlanSource(), chatReply: true, onLink: this.onLink })
        }
          .width('100%')
          .alignItems(HorizontalAlign.Start)
          .padding(12)
          .backgroundColor(this.headerEnvironment.dark ? Colors.hairlineGlass : Colors.subtle)
          .borderRadius(Tokens.radiusCard - 2)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.headerEnvironment.dark), radius: Tokens.radiusCard - 2 })
      } else if (this.headerPrompt()) {
        Text(this.headerPrompt()).fontSize(Tokens.title).fontWeight(FontWeight.Medium).fontColor(Colors.ink).width('100%')
      }
    }.width('100%').alignItems(HorizontalAlign.Start)

  }

  private taskPlanSource():string {
    const envelope=Wire.parse(this.request.raw), details=Wire.object(envelope,'interaction');
    return Wire.text(Object.keys(details).length ? details : envelope,'plan') || this.request.prompt;
  }

  private cardHeading():string {
    if (this.request.kind==='plan') return tr('phone_agent_plan_approval');
    if (this.request.kind==='permission') return tr('phone_agent_needs_approval');
    if (this.request.kind==='question') {
      if (this.agentCard) return tr('phone_agent_asks').replace('{agent}',interactionCardAgent(this.request,this.model.selected.data()));
      const provider=interactionToolName(this.request)==='ask_user' ? 'Persona' : Wire.text(this.taskAgent?.data() || {},'agent_label') || Wire.text(this.taskAgent?.data() || {},'agent');
      return tr('phone_agent_asks').replace('{agent}',provider==='claude' ? 'Claude' : provider==='codex' ? 'Codex' : provider || tr('agent'));
    }
    return tr('needs_you');
  }

  private foldPreview():string {
    if (this.taskCard) return this.taskCard.preview;
    if (!this.agentCard) return '';
    if (this.request.kind==='question') return this.request.fields[0]?.title || this.request.prompt;
    if (this.request.kind==='plan') return interactionPlanTitle(this.taskPlanSource(),tr('plan'));
    if (this.request.kind==='permission') return interactionPermissionSubject(this.request) || interactionPermissionTool(this.request);
    return this.headerPrompt();
  }

  private toggleCard():void {
    if (!this.admission()) return;
    Haptics.tap();
    if (this.docked) this.onDockFold();
    else Motion.run(this.getUIContext(), Motion.state(this.headerEnvironment.reduceMotion), () => { this.cardFolded=!this.cardFolded; });
  }

  @Builder planReader() {
    Column() {
      Row() {
        Button({type:ButtonType.Circle}) { SymbolGlyph(IconActions.symbol('close')).fontSize('19vp').fontColor([Colors.ink]) }
          .attributeModifier(ButtonStyles.chrome).accessibilityText(tr('close')).onClick(() => this.fullPlan=false).id('interaction.task.reader.close')
        Text(interactionPlanTitle(this.taskPlanSource(),tr('plan'))).layoutWeight(1).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17))
          .fontWeight(FontWeight.Medium).fontColor(Colors.ink).textAlign(TextAlign.Center).maxLines(1)
          .textOverflow({overflow:TextOverflow.Ellipsis}).id('interaction.task.reader.title')
        Blank().width(Tokens.touch).accessibilityLevel('no')
      }.width('100%').padding({left:12,right:12,top:6,bottom:6})
      Scroll() {
        Column() { MarkdownText({text:this.taskPlanSource(),chatReply:true,onLink:this.onLink}) }
          .width('100%').alignItems(HorizontalAlign.Start).padding(16)
      }.width('100%').layoutWeight(1).align(Alignment.TopStart).id('interaction.task.reader')
    }.width('100%').height('100%').backgroundColor(Colors.surface)
  }

  @Builder dockMenu() {
    Menu() {
      if ((this.taskQueue || this.agentCard) && interactionCanHandBack(this.request)) MenuItem({content:tr('phone_interaction_terminal')})
        .enabled(this.canRespond()).onClick(() => this.submit('dismiss'))
    }
  }
  private agentFontSize(points:number):number {
    return points*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(points));
  }
  private headerControlSide():number {
    return this.agentCard || this.docked ? 28*Math.max(1,Math.min(nativeEnvironment.fontScale,67/51)) : 28;
  }
  private dockSkipTarget():number {
    // The 44-point target grows with the interface, with a fixed eight-point
    // overlap at each edge of the header, like the two pager arrows.
    return 44 * this.headerControlSide() / 28;
  }
  private dockGlyphScale(points:number):number {
    return Math.min(this.headerEnvironment.fontScale,Tokens.interfaceScaleFor(points));
  }
  @Computed get systemDockHeader():boolean {
    return this.docked && Colors.systemControls && Colors.styleID==='glass';
  }
  private dockPlanCaptionLine():string | undefined {
    return this.taskQueue && this.request.kind==='plan' ? String(this.agentFontSize(13)*1.23)+'vp' : undefined;
  }
  private agentActionHeight():number {
    return 30 + this.agentFontSize(17)*1.2;
  }
  @Computed get supervisorHeld():boolean {
    return !this.readOnly && interactionSupervisorHeld(this.request);
  }
  @Builder supervisorNote() {
    Row({space:8}) {
      SymbolGlyph($r('sys.symbol.eye')).fontSize(13).fontColor([Colors.secondary]).accessibilityLevel('no')
      Column({space:3}) {
        Text(tr('interaction_supervisor_handling')).fontSize(13).fontWeight(600).fontColor(Colors.ink)
        Text(tr('interaction_supervisor_escalation')).fontSize(13).fontColor(Colors.secondary)
      }.layoutWeight(1).alignItems(HorizontalAlign.Start)
    }.width('100%').padding(12).alignItems(VerticalAlign.Top).backgroundColor(Colors.control).borderRadius(12)
      .accessibilityGroup(true).id('interaction.supervisor-held')
  }
  private headerSemanticColor(): ResourceColor {
    if (this.request.kind === 'plan') return Colors.needsMark;
    if (this.request.kind === 'permission') return Colors.workingMark;
    if (this.request.kind === 'question') return Colors.link;
    return Colors.needsMark;
  }
  private headerTitleColor(): ResourceColor {
    if (this.systemDockHeader) return $r('app.color.decision_header_title');
    if (this.request.kind === 'permission') return Colors.working;
    if (this.request.kind === 'plan' || this.request.kind === 'question') return $r('app.color.decision_header_title');
    return Colors.needs;
  }
  private panelSurface(): ResourceColor {
    return Colors.styleCard || (Colors.styleID === 'glass' ? Colors.glassSurface : Colors.surface);
  }
  private secondaryButtonFill(): ResourceColor {
    return Colors.styleID === 'glass' ? Colors.pressOverlay : Colors.control;
  }
  private secondaryButtonBorder(): ResourceColor {
    return PhoneStyle.floatingBorder(this.headerEnvironment.dark);
  }
  private secondaryButtonInk(): ResourceColor {
    return Colors.ink;
  }
  @Builder moreDotsGlyph(tint: ResourceColor) {
    Row({ space: 3.5 }) {
      ForEach([0, 1, 2], (_dot: number) => {
        Circle().width(4).height(4).fill(tint)
      }, (dot: number) => String(dot))
    }.accessibilityLevel('no')
  }
  @Builder taskHeader() {
    Column({space:4}) {
      Row({space:this.docked ? 6 : 5}) {
        Button({type:ButtonType.Normal}) {
          Row({space:this.docked ? 6 : 5}) {
            if (this.docked && this.request.kind==='plan') {
              Stack() {
                PhoneOutlineIcon({name:'decisionClipboard',color:this.systemDockHeader ? Colors.needsMark : this.headerSemanticColor()})
                  .scale({x:this.dockGlyphScale(12),y:this.dockGlyphScale(12)})
              }.width(13*this.dockGlyphScale(12)).height(this.headerControlSide()-4).accessibilityLevel('no')
            } else {
              SymbolGlyph(this.request.kind==='plan' ? Symbols.document : this.request.kind==='question' ? $r('sys.symbol.ellipsis_bubble') : $r('sys.symbol.hand_raised_fingers_spread'))
                .fontSize(this.agentCard || this.docked ? Tokens.interfaceFontVp(13,this.headerEnvironment.fontScale) : '13vp').fontColor([this.headerSemanticColor()])
            }
            Text(this.cardHeading()).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(600)
              .fontColor(this.headerTitleColor()).maxLines(1)
              .layoutWeight(this.docked && !this.cardFolded ? 1 : 0).textOverflow({overflow:TextOverflow.Ellipsis})
            if (this.cardFolded && this.foldPreview()) Text(this.foldPreview()).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
              .fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
          }.align(Alignment.Start).width('100%').padding({right:this.docked ? 4 : 0}).clip(this.docked)
        }.layoutWeight(1).height(this.headerControlSide()).backgroundColor(Color.Transparent).padding(0)
          .onClick(() => this.toggleCard()).accessibilityText(this.cardHeading()).accessibilityDescription(tr(this.cardFolded ? 'control_collapsed' : 'control_expanded'))
        if (!this.docked && !this.supervisorHeld && this.agentCard && interactionScreenCanCancel(this.request)) Button(tr('decision_skip'),{type:ButtonType.Normal})
          .height(this.headerControlSide()).padding({left:3,right:3}).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.link)
          .backgroundColor(Color.Transparent).enabled(this.canRespond()).onClick(() => this.submit('cancel')).id('interaction.task.skip')
        if (!this.cardFolded && this.request.kind==='plan') Button({type:ButtonType.Circle}) {
          if (this.docked) {
            PhoneOutlineIcon({name:'decisionEnlarge',color:this.systemDockHeader ? $r('app.color.decision_header_secondary') : Colors.secondary})
              .scale({x:this.dockGlyphScale(13),y:this.dockGlyphScale(13)})
          } else {
            SymbolGlyph($r('sys.symbol.arrow_up_left_and_arrow_down_right')).fontSize('13vp').fontColor([Colors.secondary])
          }
        }.width(this.headerControlSide()).height(this.headerControlSide()).padding(0).backgroundColor(Color.Transparent).accessibilityText(tr('phone_plan_fullscreen'))
          .onClick(() => { if (this.admission()) this.fullPlan=true; }).id('interaction.task.expand_plan')
        if (!this.supervisorHeld && (this.taskQueue || this.agentCard) && interactionCanHandBack(this.request)) Button({type:ButtonType.Circle}) {
          Row({space:this.docked ? 2.17 : 2}) {
            ForEach([0,1,2],(_dot:number) => {
              Circle().width(this.docked ? 2.67 : 2.5).height(this.docked ? 2.67 : 2.5)
                .fill(this.systemDockHeader ? $r('app.color.decision_header_secondary') : Colors.secondary)
            },(dot:number) => String(dot))
          }.scale({x:this.docked ? this.dockGlyphScale(13) : 1,y:this.docked ? this.dockGlyphScale(13) : 1})
        }.width(this.headerControlSide()).height(this.headerControlSide()).padding(0).backgroundColor(Color.Transparent).enabled(this.canRespond()).accessibilityText(tr('more'))
          .bindMenu(this.dockMenu).id('interaction.task.menu')
        Button({type:ButtonType.Circle}) {
          if (this.docked) {
            PhoneOutlineIcon({name:'decisionChevron',color:this.systemDockHeader ? $r('app.color.decision_header_secondary') : Colors.secondary})
              .rotate({angle:this.cardFolded ? 180 : 0}).animation(Motion.state(this.headerEnvironment.reduceMotion))
              .scale({x:this.dockGlyphScale(13),y:this.dockGlyphScale(13)})
          } else {
            SymbolGlyph(Symbols.down).fontSize('13vp').fontColor([Colors.secondary])
              .rotate({angle:this.cardFolded ? 180 : 0}).animation(Motion.state(this.headerEnvironment.reduceMotion))
          }
        }.width(this.agentCard || this.docked ? this.headerControlSide() : 27).height(this.agentCard || this.docked ? this.headerControlSide() : 27).padding(0)
          .backgroundColor(this.systemDockHeader ? $r('app.color.decision_header_chip') : Colors.control)
          .accessibilityText(tr(this.cardFolded ? 'agent_expand' : 'agent_collapse'))
          .accessibilityDescription(tr(this.cardFolded ? 'control_collapsed' : 'control_expanded')).onClick(() => this.toggleCard()).id('interaction.task.fold')
      }.width('100%')
      if (this.docked && !this.cardFolded && (this.taskAgent || (!this.supervisorHeld && interactionScreenCanCancel(this.request)))) {
        Row({space:Tokens.spaceM}) {
          if (this.taskAgent) Button(tr('decision_view_in_chat'),{type:ButtonType.Normal})
            .height(this.headerControlSide()).padding({left:0,right:6}).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
            .fontColor(this.systemDockHeader ? Colors.needsMark : Colors.link)
            .responseRegion({x:-6,y:-5,width:'100%',height:Math.max(44,this.headerControlSide())}).backgroundColor(Color.Transparent)
            .onClick(() => { if (this.admission()) this.onViewTask(); }).id('interaction.task.source')
          if (!this.supervisorHeld && interactionScreenCanCancel(this.request)) Button(tr('decision_skip'),{type:ButtonType.Normal})
            .height(this.headerControlSide()).padding({left:3,right:3}).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.link)
            .responseRegion({x:0,y:-5,width:'100%',height:Math.max(44,this.headerControlSide())})
            .backgroundColor(Color.Transparent).enabled(this.canRespond()).onClick(() => this.submit('cancel')).id('interaction.task.skip')
        }.width('100%').alignItems(VerticalAlign.Center)
      }
    }.width('100%').onAreaChange((_before:Area,area:Area) => {
      const height=Number(area.height);
      if (height>0 && Math.abs(this.inlineHeaderHeight-height)>0.5) this.inlineHeaderHeight=height;
    })
  }

  private approveTaskPlan(mode:string):void {
    if (!this.canRespond()) return;
    Haptics.tap();
    this.selected='approve'; this.setText('mode',mode); this.submit('approve');
  }

  // System control sizes track the control font independently of the uncapped plan body.
  private dockActionSize(primary:boolean):number {
    const growth=(Math.max(17,this.agentFontSize(17))-17)/6;
    return 17*1.2+(primary ? 30+growth*4 : 14+growth*2);
  }
  private dockActionInk(): ResourceColor {
    return this.model.style==='system' ? Colors.ink : Colors.onAction;
  }
  @Builder dockSecondary(label:string,id:string,edits:boolean) {
    Button(tr(label),{type:ButtonType.Capsule}).layoutWeight(1)
      .constraintSize({minHeight:Math.max(Tokens.controlBar,this.dockActionSize(false))})
      .padding({left:Tokens.spaceM,right:Tokens.spaceM,top:Tokens.spaceXs,bottom:Tokens.spaceXs})
      .fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontWeight(FontWeight.Medium)
      .labelStyle({maxLines:2,minFontSize:12,maxFontSize:Tokens.typeSecondary,overflow:TextOverflow.Ellipsis})
      .fontColor(this.secondaryButtonInk())
      .backgroundColor(this.secondaryButtonFill())
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:this.secondaryButtonBorder(),radius:Tokens.radiusCapsule})
      .shadow(PhoneStyle.cardElevation(this.headerEnvironment.dark))
      .scale({x:this.pressedButton===id ? MotionTokens.pressScale : 1,y:this.pressedButton===id ? MotionTokens.pressScale : 1})
      .animation(Motion.release(this.headerEnvironment.reduceMotion))
      .onTouch((event:TouchEvent) => {
        if (event.type===TouchType.Down) this.pressedButton=id;
        else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton===id) this.pressedButton=''; }
      })
      .enabled(this.canRespond()).onClick(() => {
        Haptics.tap();
        if (edits) this.approveTaskPlan('acceptEdits'); else this.selected='deny';
      }).id(id)
  }

  @Builder beautifulPlanChoice(label:string,detail:string,mode:string,id:string) {
    Button({type:ButtonType.Normal}) {
      Row({space:10}) {
        Circle().width(16).height(16).fill(Color.Transparent).stroke(Colors.secondary).strokeWidth(1)
        Column({space:2}) {
          Text(label).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.ink)
          Text(detail).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
      }.width('100%').constraintSize({minHeight:49}).padding({top:4,bottom:4})
    }.width('100%').padding(0).borderRadius(0).backgroundColor(Color.Transparent)
      .accessibilityText(label+'，'+detail).enabled(this.canRespond()).onClick(()=>this.approveTaskPlan(mode)).id(id)
  }
  @Builder beautifulPlanChoices() {
    Column({space:4}) {
      if (recordedPlanMode(this.request)) {
        this.beautifulPlanChoice(tr('approve'),this.terminalPlanLabel(recordedPlanMode(this.request)),recordedPlanMode(this.request),'interaction.task.approve')
      } else {
        this.beautifulPlanChoice(tr('session_task_approve_run'),tr('style_plan_run_detail'),'bypassPermissions','interaction.task.approve')
        this.beautifulPlanChoice(tr('style_plan_edits'),tr('style_plan_edits_detail'),'acceptEdits','interaction.task.edits')
      }
      TextInput({text:this.textValues['feedback'] || '',placeholder:tr('style_plan_return_hint')})
        .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).height(32).padding({left:10,right:8,top:4,bottom:4})
        .backgroundColor(Color.Transparent).enabled(this.canRespond()).enterKeyType(EnterKeyType.Send)
        .onSubmit(()=>{if((this.textValues['feedback'] || '').trim()) this.submit('deny');})
        .onChange((value:string)=>{this.selected=value.trim() ? 'deny' : '';this.setText('feedback',value);}).id('interaction.task.feedback')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
  @Builder beautifulPlanSubmit() {
    Row() {
      Blank().layoutWeight(1)
      Button(tr('send'),{type:ButtonType.Capsule}).constraintSize({minWidth:56,minHeight:28}).height(28).padding({left:12,right:12})
        .fontSize(14).fontColor(Color.White).backgroundColor(Colors.action)
        .enabled(this.canRespond() && !!(this.textValues['feedback'] || '').trim())
        .onClick(()=>this.submit('deny')).id('interaction.task.return')
    }.width('100%').padding({top:6,bottom:6})
  }
  private terminalPlanLabel(mode:string):string {
    if(mode==='acceptEdits') return tr('style_plan_mode_edits');
    if(mode==='bypassPermissions') return tr('style_plan_mode_all');
    return mode==='auto' ? tr('style_plan_mode_auto') : tr('style_plan_mode_default');
  }
  @Builder taskPlanActions() {
    Column({space:this.docked ? 12 : 5}) {
      if (this.model.style==='beautiful') {
        this.beautifulPlanChoices()
        this.beautifulPlanSubmit()
      } else if (this.selected==='deny') {
        TextArea({text:this.textValues['feedback'] || '',placeholder:tr('feedback')}).height(48)
          .onChange((value:string) => this.setText('feedback',value)).id('interaction.task.feedback')
        Row({space:6}) {
          Button(tr('cancel')).layoutWeight(1).fontSize(13).onClick(() => this.selected='')
          Button(tr('session_task_return')).layoutWeight(1).fontSize(13).enabled(this.canRespond()).onClick(() => this.submit('deny'))
        }.width('100%')
      } else if (this.docked) {
        Button({type:ButtonType.Capsule}) {
          Row({space:6}) {
            if (!recordedPlanMode(this.request)) SymbolGlyph($r('sys.symbol.play_fill')).fontSize(Tokens.interfaceFontVp(14,nativeEnvironment.fontScale)).fontColor([this.dockActionInk()])
            Text(tr(recordedPlanMode(this.request) ? 'approve' : 'session_task_approve_run')).fontSize(17).maxFontScale(21/17)
              .fontColor(this.dockActionInk()).fontWeight(FontWeight.Medium).maxLines(1)
          }
        }.width('100%').height(this.dockActionSize(true)).padding({left:8,right:8}).backgroundColor(Colors.action)
          .scale({x:this.pressedButton==='plan_approve' ? MotionTokens.pressScale : 1,y:this.pressedButton==='plan_approve' ? MotionTokens.pressScale : 1})
          .animation(Motion.release(this.headerEnvironment.reduceMotion))
          .onTouch((event:TouchEvent) => {
            if (event.type===TouchType.Down) this.pressedButton='plan_approve';
            else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton==='plan_approve') this.pressedButton=''; }
          })
          .accessibilityText(tr(recordedPlanMode(this.request) ? 'approve' : 'session_task_approve_run')).enabled(this.canRespond())
          .onClick(() => this.approveTaskPlan(recordedPlanMode(this.request) || 'bypassPermissions')).id('interaction.task.approve')
        Row({space:8}) {
          if (!recordedPlanMode(this.request)) this.dockSecondary('session_task_auto_edits','interaction.task.edits',true)
          this.dockSecondary('session_task_return','interaction.task.return',false)
        }.width('100%')
      } else {
        Button({type:ButtonType.Capsule}) {
          Row({space:6}) {
            SymbolGlyph($r('sys.symbol.play_fill')).fontSize('14vp').fontColor([Colors.onAction])
            Text(tr(recordedPlanMode(this.request) ? 'approve' : 'session_task_approve_run')).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontWeight(FontWeight.Medium).fontColor(Colors.onAction)
          }
        }.width('100%').constraintSize({minHeight:44}).accessibilityText(tr(recordedPlanMode(this.request) ? 'approve' : 'session_task_approve_run'))
          .backgroundColor(Colors.action).enabled(this.canRespond())
          .scale({x:this.pressedButton==='plan_approve' ? MotionTokens.pressScale : 1,y:this.pressedButton==='plan_approve' ? MotionTokens.pressScale : 1})
          .animation(Motion.release(this.headerEnvironment.reduceMotion))
          .onTouch((event:TouchEvent) => {
            if (event.type===TouchType.Down) this.pressedButton='plan_approve';
            else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton==='plan_approve') this.pressedButton=''; }
          })
          .onClick(() => this.approveTaskPlan(recordedPlanMode(this.request) || 'bypassPermissions')).id('interaction.task.approve')
        Row({space:Tokens.spaceS}) {
          if (!recordedPlanMode(this.request)) Button(tr('session_task_auto_edits'),{type:ButtonType.Capsule}).layoutWeight(1)
            .fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .fontWeight(FontWeight.Medium).constraintSize({minHeight:Tokens.controlBar})
            .padding({left:Tokens.spaceM,right:Tokens.spaceM,top:Tokens.spaceXs,bottom:Tokens.spaceXs})
            .labelStyle({maxLines:2,minFontSize:12,maxFontSize:Tokens.typeSecondary,overflow:TextOverflow.Ellipsis})
            .backgroundColor(this.secondaryButtonFill()).fontColor(this.secondaryButtonInk())
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:this.secondaryButtonBorder(),radius:Tokens.radiusCapsule})
            .scale({x:this.pressedButton==='plan_edits' ? MotionTokens.pressScale : 1,y:this.pressedButton==='plan_edits' ? MotionTokens.pressScale : 1})
            .animation(Motion.release(this.headerEnvironment.reduceMotion))
            .onTouch((event:TouchEvent) => {
              if (event.type===TouchType.Down) this.pressedButton='plan_edits';
              else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton==='plan_edits') this.pressedButton=''; }
            })
            .enabled(this.canRespond())
            .onClick(() => this.approveTaskPlan('acceptEdits')).id('interaction.task.edits')
          Button(tr('session_task_return'),{type:ButtonType.Capsule}).layoutWeight(1)
            .fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .fontWeight(FontWeight.Medium).constraintSize({minHeight:Tokens.controlBar})
            .padding({left:Tokens.spaceM,right:Tokens.spaceM,top:Tokens.spaceXs,bottom:Tokens.spaceXs})
            .labelStyle({maxLines:2,minFontSize:12,maxFontSize:Tokens.typeSecondary,overflow:TextOverflow.Ellipsis})
            .backgroundColor(this.secondaryButtonFill()).fontColor(this.secondaryButtonInk())
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:this.secondaryButtonBorder(),radius:Tokens.radiusCapsule})
            .scale({x:this.pressedButton==='plan_return' ? MotionTokens.pressScale : 1,y:this.pressedButton==='plan_return' ? MotionTokens.pressScale : 1})
            .animation(Motion.release(this.headerEnvironment.reduceMotion))
            .onTouch((event:TouchEvent) => {
              if (event.type===TouchType.Down) this.pressedButton='plan_return';
              else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton==='plan_return') this.pressedButton=''; }
            })
            .enabled(this.canRespond()).onClick(() => { Haptics.tap(); this.selected='deny'; }).id('interaction.task.return')
        }.width('100%')
      }
      if (this.taskQueue?.error) Text(tr(this.taskQueue.error)).fontSize(12).fontColor(Colors.danger)
    }.width('100%')
  }

  private sendPermission(action:string):void {
    if (!this.canRespond() || action==='allow_session' && !interactionSessionAllowance(this.request)) return;
    this.selected=action==='deny' ? 'deny' : 'approve';
    this.submit(action);
  }
  private permissionAllowed():boolean {
    return this.canRespond() && !this.request.fields.some((field:DecisionField) => field.required && !(this.textValues[field.key] || '').trim());
  }
  private permissionScope():string {
    const scope=interactionAllowanceScope(this.request);
    return scope.startsWith('decision_scope_') ? tr(scope) : scope;
  }
  private pickedAnswer(field:DecisionField):string {
    return (this.textValues[field.key+':other'] || (this.questionPicks[field.key] || []).join(', ')).trim();
  }
  private chooseAgentAnswer(field:DecisionField,choice:InteractionChoice):void {
    if (!this.canRespond()) return;
    this.otherQuestion='';this.pickQuestion(field,choice);
    if (field.kind==='multi_choice') return;
    if (this.request.fields.length===1) this.submit('answer');
    else if (this.questionPage+1<this.request.fields.length) this.questionPage++;
    else this.reviewingQuestions=true;
  }
  @Computed get permissionSubject():string { return interactionPermissionSubject(this.request); }
  private longPermissionCommand():boolean {
    const body=this.permissionSubject;
    return body.length>320 || body.split('\n').length>5;
  }
  @Builder agentPermissionBody() {
    Column({space:12}) {
      Row({space:8}) {
        if (interactionToolName(this.request)==='Bash') Text('>_').fontFamily('monospace').fontSize(Tokens.interfaceFontVp(12,nativeEnvironment.fontScale)).fontWeight(600)
          .fontColor(Colors.link).padding({left:2,right:2}).border({width:1,color:Colors.link,radius:3}).accessibilityLevel('no')
        else SymbolGlyph(Symbols.document).fontSize('17vp').fontColor([Colors.link])
        Text(interactionPermissionTool(this.request) || tr('tool')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15))
          .fontWeight(600).fontColor(Colors.ink).id('interaction.permission.tool')
      }.width('100%')
      if (this.permissionSubject) Text(this.permissionSubject).fontFamily('monospace')
        .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.ink)
        .letterSpacing(String(this.agentFontSize(13)/80)+'vp').lineHeight(String(this.agentFontSize(13)*1.2)+'vp')
        .maxLines(this.commandExpanded || !this.longPermissionCommand() ? undefined : 5).textOverflow({overflow:TextOverflow.Ellipsis})
        .width('100%').padding(12).backgroundColor(Colors.subtle).borderRadius(10).id('interaction.permission.command')
      if (this.longPermissionCommand()) Button(tr(this.commandExpanded ? 'agent_collapse' : 'agent_expand'),{buttonStyle:ButtonStyleMode.TEXTUAL})
        .fontSize(13).fontColor(Colors.link).onClick(() => {
          Haptics.tap();
          Motion.run(this.getUIContext(), Motion.state(this.headerEnvironment.reduceMotion), () => {
            this.commandExpanded=!this.commandExpanded;
          });
        }).id('interaction.permission.expand')
      ForEach(this.request.fields,(field:DecisionField) => this.permissionField(field),(field:DecisionField) => field.key)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
  @Builder agentPermissionActions() {
    Column({space:12}) {
      Row({space:8}) {
        Button(tr('deny'),{type:ButtonType.Capsule}).layoutWeight(1).height(this.agentActionHeight()).constraintSize({minHeight:this.agentActionHeight()}).fontSize(17)
          .maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(FontWeight.Medium)
          .backgroundColor(this.secondaryButtonFill()).fontColor(this.secondaryButtonInk())
          .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:this.secondaryButtonBorder(),radius:this.agentActionHeight()/2})
          .enabled(this.canRespond())
          .scale({x:this.pressedButton==='deny'?MotionTokens.pressScale:1,y:this.pressedButton==='deny'?MotionTokens.pressScale:1})
          .animation(Motion.release(this.headerEnvironment.reduceMotion))
          .onTouch((event:TouchEvent) => {
            if (event.type===TouchType.Down) this.pressedButton='deny';
            else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton==='deny') this.pressedButton=''; }
          })
          .onClick(() => { Haptics.tap(); this.sendPermission('deny'); }).id('interaction.permission.deny')
        Button(tr('allow'),{type:ButtonType.Capsule}).layoutWeight(1).height(this.agentActionHeight()).constraintSize({minHeight:this.agentActionHeight()}).fontSize(17)
          .maxFontScale(Tokens.interfaceScaleFor(17)).backgroundColor(Colors.action).fontColor(Colors.onAction).enabled(this.permissionAllowed())
          .scale({x:this.pressedButton==='allow'?MotionTokens.pressScale:1,y:this.pressedButton==='allow'?MotionTokens.pressScale:1})
          .animation(Motion.release(this.headerEnvironment.reduceMotion))
          .onTouch((event:TouchEvent) => {
            if (event.type===TouchType.Down) this.pressedButton='allow';
            else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton==='allow') this.pressedButton=''; }
          })
          .onClick(() => { Haptics.tap(); this.sendPermission('allow'); }).id('interaction.permission.allow')
      }.width('100%')
      if (interactionSessionAllowance(this.request)) {
        Row() {
          Text(tr('decision_allow_session')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).lineHeight(String(this.agentFontSize(13)*1.2)+'vp').fontColor(Colors.ink)
            .accessibilityRole(AccessibilityRoleType.BUTTON).enabled(this.permissionAllowed())
            .responseRegion({x:0,y:-6,width:'100%',height:'160%'})
            .onClick(() => this.sendPermission('allow_session')).id('interaction.permission.allow_session')
        }.width('100%').justifyContent(FlexAlign.Center)
        if (this.permissionScope()) Text(this.permissionScope()).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).lineHeight(String(this.agentFontSize(13)*1.2)+'vp').fontColor(Colors.secondary)
          .width('100%').id('interaction.permission.scope')
      }
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder agentQuestion(field:DecisionField) {
    Column({space:12}) {
      if (interactionQuestionHeader(this.request,field.key) || this.request.fields.length>1)
        Text([interactionQuestionHeader(this.request,field.key),this.request.fields.length>1 ? String(this.questionPage+1)+'/'+String(this.request.fields.length) : '']
          .filter((item:string) => !!item).join(' · ')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12))
          .fontWeight(600).fontColor(Colors.secondary).width('100%')
      Text(field.title).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(600).fontColor(Colors.ink)
        .width('100%').id('interaction.question.title')
      Column() {
        ForEach(this.questionChoices(field),(choice:InteractionChoice,i:number) => {
          Row({space:12}) {
            Text(String(i+1)).width(28).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).textAlign(TextAlign.Center).fontColor(Colors.secondary)
            Column({space:4}) {
              Text(choice.label).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(500).fontColor(Colors.ink)
              if (choice.detail) Text(choice.detail).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
            }.layoutWeight(1).alignItems(HorizontalAlign.Start)
            if (this.pickedQuestion(field,choice.label)) SymbolGlyph(Symbols.check).fontSize('16vp').fontColor([Colors.link])
          }.width('100%').padding(12).constraintSize({minHeight:choice.detail ? 64 : 48}).enabled(this.canRespond())
            .accessibilityGroup(true).accessibilitySelected(this.pickedQuestion(field,choice.label)).accessibilityText(choice.label)
            .onClick(() => {
              Haptics.selection();
              Motion.run(this.getUIContext(), Motion.state(this.headerEnvironment.reduceMotion), () => {
                this.chooseAgentAnswer(field,choice);
              });
            }).id('interaction.question.'+field.key+'.'+choice.label)
          Divider().color(Colors.divider).strokeWidth(0.5).margin({left:52})
        },(choice:InteractionChoice) => choice.label)
        if (!this.questionChoices(field).length || this.otherQuestion===field.key || this.textValues[field.key+':other']) {
          TextInput({text:this.textValues[field.key+':other'] || '',placeholder:tr('decision_other_choice')})
            .width('100%').constraintSize({minHeight:48}).fontSize(16).enabled(this.canRespond())
            .onChange((v:string) => this.setText(field.key+':other',v)).id('interaction.question.'+field.key+'.other')
        } else Button({type:ButtonType.Normal}) {
          Row({space:12}) {
            SymbolGlyph(Symbols.edit).width(28).fontSize('14vp').fontColor([Colors.secondary])
            Text(tr('decision_other_choice')).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.secondary)
          }.width('100%').padding(12)
        }.width('100%').constraintSize({minHeight:48}).backgroundColor(Color.Transparent).padding(0).enabled(this.canRespond())
          .onClick(() => this.otherQuestion=field.key).id('interaction.question.other')
      }.width('100%').backgroundColor(Colors.subtle).borderRadius(12).clip(true)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
  @Builder agentQuestionActions() {
    Row({space:8}) {
      if (this.questionPage>0 || this.reviewingQuestions) Button(tr('decision_previous'),{type:ButtonType.Capsule}).layoutWeight(1).fontSize(15)
        .fontWeight(FontWeight.Medium).constraintSize({minHeight:42})
        .backgroundColor(this.secondaryButtonFill()).fontColor(this.secondaryButtonInk())
        .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:this.secondaryButtonBorder(),radius:21})
        .scale({x:this.pressedButton==='q_prev'?MotionTokens.pressScale:1,y:this.pressedButton==='q_prev'?MotionTokens.pressScale:1})
        .animation(Motion.release(this.headerEnvironment.reduceMotion))
        .onTouch((event:TouchEvent) => {
          if (event.type===TouchType.Down) this.pressedButton='q_prev';
          else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton==='q_prev') this.pressedButton=''; }
        })
        .enabled(this.canRespond()).onClick(() => {
          Haptics.tap();
          if (this.reviewingQuestions) this.reviewingQuestions=false;
          else this.questionPage--;
        }).id('interaction.question.previous')
      if (this.reviewingQuestions || this.request.fields.length===1)
        Button(tr('answer'),{type:ButtonType.Capsule}).layoutWeight(1).fontSize(16).fontWeight(FontWeight.Medium).constraintSize({minHeight:42})
          .backgroundColor(Colors.action).fontColor(Colors.onAction)
          .scale({x:this.pressedButton==='q_submit'?MotionTokens.pressScale:1,y:this.pressedButton==='q_submit'?MotionTokens.pressScale:1})
          .animation(Motion.release(this.headerEnvironment.reduceMotion))
          .onTouch((event:TouchEvent) => {
            if (event.type===TouchType.Down) this.pressedButton='q_submit';
            else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton==='q_submit') this.pressedButton=''; }
          })
          .enabled(this.canSubmit()).onClick(() => { Haptics.tap(); this.submit('answer'); }).id('interaction.submit')
      else if (this.request.fields.length>0) Button(tr(this.questionPage+1<this.request.fields.length ? 'decision_next' : 'review'),{type:ButtonType.Capsule})
        .layoutWeight(1).fontSize(15).fontWeight(FontWeight.Medium).constraintSize({minHeight:42})
        .backgroundColor(Colors.action).fontColor(Colors.onAction)
        .scale({x:this.pressedButton==='q_next'?MotionTokens.pressScale:1,y:this.pressedButton==='q_next'?MotionTokens.pressScale:1})
        .animation(Motion.release(this.headerEnvironment.reduceMotion))
        .onTouch((event:TouchEvent) => {
          if (event.type===TouchType.Down) this.pressedButton='q_next';
          else if (event.type===TouchType.Up || event.type===TouchType.Cancel) { if (this.pressedButton==='q_next') this.pressedButton=''; }
        })
        .enabled(this.canRespond() && !!this.pickedAnswer(this.request.fields[this.questionPage]))
        .onClick(() => { Haptics.tap(); if (this.questionPage+1<this.request.fields.length) this.questionPage++;else this.reviewingQuestions=true; })
        .id('interaction.question.next')
    }.width('100%')
  }
  @Builder agentBody() {
    if (this.readOnly) this.formBody()
    else if (this.request.kind==='permission') this.agentPermissionBody()
    else if (this.request.kind==='plan') MarkdownText({text:this.taskPlanSource(),chatReply:true,onLink:this.onLink})
    else if (this.request.kind==='question' && this.request.fields.length) {
      if (this.reviewingQuestions) Column({space:8}) {
        ForEach(this.request.fields,(field:DecisionField) => {
          Text(field.title).fontSize(15).fontWeight(600).fontColor(Colors.ink).width('100%')
          Text(this.pickedAnswer(field)).fontSize(15).fontColor(Colors.secondary).width('100%')
        },(field:DecisionField) => field.key)
      }.width('100%').alignItems(HorizontalAlign.Start)
      else this.agentQuestion(this.request.fields[this.questionPage])
    } else this.formBody()
  }
  private agentHasFooter():boolean {
    if (this.readOnly) return false;
    if (this.request.kind!=='question' || this.request.fields.length!==1) return true;
    const field=this.request.fields[0];
    return field.kind==='multi_choice' || !this.questionChoices(field).length || this.otherQuestion===field.key || !!this.textValues[field.key+':other'];
  }
  @Builder agentFooter() {
    if (this.request.kind==='permission') this.agentPermissionActions()
    else if (this.request.kind==='plan') this.taskPlanActions()
    else if (this.request.kind==='question') this.agentQuestionActions()
    else this.responseFooter()
    if (this.model.error && ['permission','plan','question'].includes(this.request.kind)) Text(tr(this.model.error)).fontSize(13).fontColor(Colors.danger)
  }
  @Builder agentDecisionCard() {
    Column({space:12}) {
      this.taskHeader()
      if (!this.cardFolded) {
        if (this.heightLimit>0) Scroll() {
          Column() { this.agentBody() }.width('100%').alignItems(HorizontalAlign.Start)
            .onAreaChange((_before:Area,after:Area) => {
              const h=Number(after.height); if (h>0 && Math.abs(this.inlineBodyHeight-h)>0.5) this.inlineBodyHeight=h;
            })
        }.width('100%').height(this.inlineBodyHeight>0 ? Math.min(this.inlineBodyHeight,this.inlineSpace()) : undefined)
          .constraintSize({maxHeight:this.inlineSpace()}).align(Alignment.TopStart).id('interaction.body')
        else Column() { this.agentBody() }.width('100%').alignItems(HorizontalAlign.Start).id('interaction.body')
        if (this.agentHasFooter()) Column() { this.agentFooter() }.width('100%')
          .onAreaChange((_before:Area,after:Area) => {
            const h=Number(after.height); if (h>0 && Math.abs(this.inlineFooterHeight-h)>0.5) this.inlineFooterHeight=h;
          })
        if (Object.keys(this.ruleSuggestionObject()).length > 0) {
          RuleSuggestionBar({
            model: this.model,
            rawSuggestion: this.ruleSuggestionObject(),
            cardKey: 'card|' + this.request.id,
            manager: this.ruleSuggestionManager(),
            onSettled: () => { this.model.refresh().catch(() => undefined); }
          })
        }
      }
    }.width('100%').alignItems(HorizontalAlign.Start).padding({left:16,right:16,top:12,bottom:16})
      .backgroundColor(this.panelSurface())
      .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.floatingBorder(this.headerEnvironment.dark),radius:22}).borderRadius(22)
      .shadow(PhoneStyle.floatingElevation(this.headerEnvironment.dark))
      .id('interaction.card')
      .bindSheet($$this.fullPlan,this.planReader(),{height:SheetSize.LARGE,showClose:false,dragBar:false,preferType:SheetType.BOTTOM})
  }

  @Builder responseFooter() {
    Flex({ wrap: FlexWrap.Wrap, justifyContent: FlexAlign.End, alignItems: ItemAlign.Center, space: { main: LengthMetrics.vp(Tokens.gap), cross: LengthMetrics.vp(Tokens.gap) } }) {
      if (this.deferred && !this.readOnly) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('interaction_question_reopen')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('interaction_question_reopen', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('interaction_question_reopen')).bindTips(tr('interaction_question_reopen'))
        .onClick(() => this.deferred = false).id('interaction.reopen')
      else if (!this.readOnly && this.request.kind === 'question') {
        if (this.pinFooter || this.fillAvailable) Flex({ wrap: FlexWrap.Wrap, justifyContent: FlexAlign.End, alignItems: ItemAlign.Center, space: { main: LengthMetrics.vp(0), cross: LengthMetrics.vp(Tokens.gap) } }) {
          if (this.pinFooter || this.canDefer()) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('interaction_question_later')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('interaction_question_later', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('interaction_question_later')).bindTips(tr('interaction_question_later')).flexShrink(0).margin({ end: LengthMetrics.vp(Tokens.gap) }).enabled(this.canDefer())
            .onClick(() => { this.deferred = true; this.onLater(); }).id('interaction.later')
          if (interactionScreenCanCancel(this.request) && !this.pinFooter && !this.docked) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('cancel')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('cancel', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('cancel')).bindTips(tr('cancel')).flexShrink(0).margin({ end: LengthMetrics.vp(Tokens.gap) })
            .enabled(this.canRespond()).onClick(() => this.submit('cancel')).id('interaction.cancel')
          if (interactionScreenCanCancel(this.request) && this.pinFooter) Button({ type: ButtonType.Circle }) { this.moreDotsGlyph(Colors.ink) }
            .attributeModifier(ButtonStyles.chrome).enabled(this.canRespond()).accessibilityText(tr('more'))
            .margin({ end: LengthMetrics.vp(Tokens.gap) }).bindMenu([{ value: tr('interaction_question_cancel'), action: () => this.submit('cancel') }]).flexShrink(0).id('interaction.more')
          Button(tr('answer'), { type: ButtonType.Capsule }).fontSize(16).fontWeight(FontWeight.Medium)
            .height(38).padding({ left: 16, right: 16 }).backgroundColor(Colors.action).fontColor(Colors.onAction)
            .accessibilityText(tr('answer')).enabled(this.canSubmit())
            .onClick(() => this.submit('answer')).id('interaction.submit')
        }.width('100%')
        else {
          if (this.canDefer()) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('interaction_question_later')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('interaction_question_later', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('interaction_question_later')).bindTips(tr('interaction_question_later'))
            .onClick(() => { this.deferred = true; this.onLater(); }).id('interaction.later')
          if (interactionScreenCanCancel(this.request)) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('cancel')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('cancel', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('cancel')).bindTips(tr('cancel'))
            .enabled(this.canRespond()).onClick(() => this.submit('cancel')).id('interaction.cancel')
          Button(tr('answer'), { type: ButtonType.Capsule }).fontSize(16).fontWeight(FontWeight.Medium)
            .height(38).padding({ left: 16, right: 16 }).backgroundColor(Colors.action).fontColor(Colors.onAction)
            .accessibilityText(tr('answer')).enabled(this.canSubmit())
            .onClick(() => this.submit('answer')).id('interaction.submit')
        }
      } else if (!this.readOnly && this.request.kind === 'elicitation') {
        if (interactionElicitationMode(this.request) === 'url' && interactionElicitationUrl(this.request)) {
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('open')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('open', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('open')).bindTips(tr('open')).enabled(this.canRespond())
            .onClick(() => this.openUrl()).id('interaction.open')
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('continue')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('continue', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('continue')).bindTips(tr('continue')).enabled(this.canSubmit())
            .onClick(() => this.submit('accept')).id('interaction.continue')
        } else if (interactionElicitationMode(this.request) !== 'url' && interactionElicitationSupported(this.request)) {
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('confirm')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('confirm', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('confirm')).bindTips(tr('confirm')).enabled(this.canSubmit())
            .onClick(() => this.submit('accept')).id('interaction.accept')
        }
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('deny')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('deny', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('deny')).bindTips(tr('deny')).enabled(this.canRespond())
          .onClick(() => this.submit('decline')).id('interaction.decline')
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('cancel')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('cancel', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('cancel')).bindTips(tr('cancel')).enabled(this.canRespond())
          .onClick(() => this.submit('cancel')).id('interaction.cancel')
      } else if (!this.readOnly) {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol(this.submitIconKey())).fontSize(Tokens.symbolFont).fontColor([IconActions.color(this.submitIconKey(), this.submitIconKey() !== 'deny')]) }.attributeModifier(this.submitIconKey() === 'deny' ? ButtonStyles.icon : ButtonStyles.iconPrimary).accessibilityText(this.submitIconLabel()).bindTips(this.submitIconLabel()).enabled(this.canSubmit())
          .onClick(() => this.submit(this.submitButtonOption())).id('interaction.submit')
      }
      if (this.taskQueue ? this.taskQueue.error : this.model.error)
        Text(tr(this.taskQueue ? this.taskQueue.error : this.model.error)).fontSize(Tokens.meta).fontColor(Colors.needs).width('100%')
    }.width('100%').padding({ top: Tokens.gap })

  }

  @Builder formBody() {
    this.headerContent()
    if (this.readOnly) {
      if (this.historySummary()) Text(this.historySummary()).fontSize(Tokens.body).fontColor(Colors.ink).width('100%')
    } else if (!this.deferred) this.bodyContent()
  }

  // The grip moves with the card: a local y coordinate would reset part of each
  // drag as layout follows the finger. Read the stable window coordinates instead.
  @Local private gripWidth:number = 0;
  private gripWindowY:number = 0;
  private gripWindowX:number = 0;
  private gripDistance:number = 0;
  private gripDown:boolean = false;
  private gripMoved:boolean = false;
  private gripEnded:number = 0;
  private trackGrip(event:TouchEvent):void {
    const touch=event.changedTouches[0] || event.touches[0];
    if (event.type===TouchType.Down && touch) {
      this.gripDown=true;this.gripMoved=false;this.gripDistance=0;this.gripWindowY=touch.windowY;this.gripWindowX=touch.windowX;
      event.stopPropagation();return;
    }
    if (!this.gripDown) return;
    if (event.type===TouchType.Move && touch) {
      const dy=touch.windowY-this.gripWindowY;
      if (!this.gripMoved && Math.abs(dy)>=4 && Math.abs(dy)>Math.abs(touch.windowX-this.gripWindowX)) {
        this.gripMoved=true;Haptics.selection();this.onDockResize('begin',0);
      }
      if (this.gripMoved) { this.gripDistance=dy;this.onDockResize('move',dy); }
    } else if (event.type===TouchType.Up || event.type===TouchType.Cancel) {
      this.gripDown=false;this.gripEnded=Date.now();
      if (this.gripMoved) { Haptics.tap(); this.onDockResize(event.type===TouchType.Up ? 'end' : 'cancel',this.gripDistance); }
      else if (event.type===TouchType.Up) { Haptics.tap(); this.onDockResize('toggle',0); }
    }
    event.stopPropagation();
  }
  @Builder dockGrip() {
    Button({type:ButtonType.Normal}) {
      Row() {
        Blank().width(38*this.headerControlSide()/28).height(5*this.headerControlSide()/28)
          .backgroundColor(Colors.tertiary).borderRadius(4)
      }.width('100%').height(16).justifyContent(FlexAlign.Center).alignItems(VerticalAlign.Center)
    }.width('100%').height(16).padding(0).backgroundColor(Color.Transparent)
       .onAreaChange((_before:Area,area:Area):void => {
        const w=Number(area.width);if (w>0 && Math.abs(w-this.gripWidth)>0.5) this.gripWidth=w;
      }).responseRegion({x:-8,y:-8,width:this.gripWidth>0 ? this.gripWidth+16 : '100%',height:32}).accessibilityText(tr('phone_decision_height'))
      .accessibilityDescription(tr('phone_decision_height_hint')).accessibilityCustomActions([
        {name:tr('phone_decision_taller'),onAction:() => this.onDockResize('up',0)},
        {name:tr('phone_decision_shorter'),onAction:() => this.onDockResize('down',0)}])
      .onTouch((event:TouchEvent):void => this.trackGrip(event))
      .onClick(() => { if (Date.now()-this.gripEnded>400) this.onDockResize('toggle',0); }).id('interaction.grabber')
  }
  private dockHeadingAway():boolean {
    // On a short keyboard viewport keep the answer interface ahead of the optional title row.
    return !this.resizeDock && nativeEnvironment.keyboardHeightPx>0 && !this.cardFolded &&
      this.dockTitleHeight>0 && this.heightLimit-16-this.dockTitleHeight-12-12-16<120;
  }
  private dockBodyLimit():number {
    const header=this.dockHeadingAway() ? 0 : this.dockTitleHeight || this.headerControlSide();
    return Math.max(44,this.heightLimit-header-this.inlineFooterHeight-(this.resizeDock ? 16 : 12)-16-(header>0 ? 12 : 0)-12);
  }
  @Builder dockBacklight() {
    if (Colors.styleID === 'clay') {
      Rect().width('100%').height('100%').radius(22).fill(Colors.surface)
        .shadow({ radius: 6, color: nativeEnvironment.dark ? '#CC3A3445' : '#CCFFFFFF', offsetX: -4, offsetY: -4 })
        .hitTestBehavior(HitTestMode.None).accessibilityLevel('no')
    }
  }
  private ruleSuggestionObject(): Record<string, Object> {
    const envelope = Wire.parse(this.request.raw);
    const top = Wire.object(envelope, 'suggestion');
    if (Object.keys(top).length > 0) return top;
    return Wire.object(Wire.object(envelope, 'interaction'), 'suggestion');
  }

  private ruleSuggestionManager(): string {
    const envelope = Wire.parse(this.request.raw);
    return Wire.text(envelope, 'manager') || this.model.profile.manager;
  }

  @Builder dockPanel() {
    Column() {
      if (this.resizeDock && !this.cardFolded) this.dockGrip()
      if (!this.dockHeadingAway()) Column({space:2}) {
        this.taskHeader()
        if (!this.cardFolded && this.dockSource) Text(this.dockSource).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
          .lineHeight(this.dockPlanCaptionLine()).fontColor(Colors.secondary).maxLines(2).width('100%').id('interaction.task.origin')
        if (!this.cardFolded && interactionSkipReason(this.request)) Text(tr(interactionSkipReason(this.request)))
          .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).lineHeight(this.dockPlanCaptionLine())
          .fontColor($r('app.color.persona_dock_warning')).width('100%').id('interaction.task.skip_reason')
      }.width('100%').alignItems(HorizontalAlign.Start).onAreaChange((_before:Area,area:Area):void => {
        const h=Number(area.height);if (h>0 && Math.abs(h-this.dockTitleHeight)>0.5) this.dockTitleHeight=h;
      })
      if (!this.cardFolded) Column({space:12}) {
        Scroll() {
          Column() {
            if (this.taskQueue && this.request.kind==='plan') {
              this.dockPlanWords()
              if (this.model.style==='beautiful') this.beautifulPlanChoices()
            }
            else this.formBody()
          }.width('100%').alignItems(HorizontalAlign.Start).onAreaChange((_before:Area,area:Area):void => {
            const h=Number(area.height);if (h>0 && Math.abs(h-this.inlineBodyHeight)>0.5) this.inlineBodyHeight=h;
          })
        }.width('100%').height(Math.min(this.inlineBodyHeight || this.dockBodyLimit(),this.dockBodyLimit()))
          .scrollBar(BarState.Auto).align(Alignment.TopStart).id('interaction.body')
        Column() {
          if (this.taskQueue && this.request.kind==='plan' && this.model.style==='beautiful') this.beautifulPlanSubmit()
          else if (this.taskQueue && this.request.kind==='plan') this.taskPlanActions()
          else this.responseFooter()
          if (Object.keys(this.ruleSuggestionObject()).length > 0) {
            RuleSuggestionBar({
              model: this.model,
              rawSuggestion: this.ruleSuggestionObject(),
              cardKey: 'card|' + this.request.id,
              manager: this.ruleSuggestionManager(),
              onSettled: () => { this.model.refresh().catch(() => undefined); }
            })
          }
        }.width('100%').onAreaChange((_before:Area,area:Area):void => {
          const h=Number(area.height);if (h>=0 && Math.abs(h-this.inlineFooterHeight)>0.5) this.inlineFooterHeight=h;
        })
      }.width('100%').padding({top:this.dockHeadingAway() ? 0 : 12})
    }.width('100%').padding({left:16,right:16,top:this.resizeDock && !this.cardFolded ? 0 : 12,bottom:16})
      .alignItems(HorizontalAlign.Start).backgroundColor(this.panelSurface())
      .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
      .borderRadius(22)
      .background(this.dockBacklight)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.floatingBorder(this.headerEnvironment.dark),radius:22})
      .shadow(PhoneStyle.floatingElevation(this.headerEnvironment.dark)).id('interaction.card')
      .bindSheet($$this.fullPlan,this.planReader(),{height:SheetSize.LARGE,showClose:false,dragBar:false,preferType:SheetType.BOTTOM})
  }
  private inlineSpace(): number {
    if (this.agentCard) return Math.max(1,this.heightLimit-this.inlineHeaderHeight-40-(this.agentHasFooter() ? this.inlineFooterHeight+12 : 0));
    return Math.max(1, this.heightLimit - this.inlineFooterHeight - (this.docked || this.taskQueue ? this.inlineHeaderHeight + 28 : 22));
  }
  build() {
    if (this.supervisorHeld) Column({space:12}) {
      this.taskHeader()
      if (!this.cardFolded) this.supervisorNote()
    }.width('100%').padding({left:16,right:16,top:12,bottom:16}).alignItems(HorizontalAlign.Start)
      .backgroundColor(this.panelSurface())
      .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.floatingBorder(this.headerEnvironment.dark),radius:22}).borderRadius(22)
      .shadow(PhoneStyle.floatingElevation(this.headerEnvironment.dark)).id('interaction.card')
      .bindSheet($$this.fullPlan,this.planReader(),{height:SheetSize.LARGE,showClose:false,dragBar:false,preferType:SheetType.BOTTOM})
    else if (this.agentCard) this.agentDecisionCard()
    else if (this.docked) this.dockPanel()
    else if (this.heightLimit > 0) Column({space: 6}) {
      if (this.docked || this.taskQueue) this.taskHeader()
      if (!this.cardFolded) Scroll() {
        Column({space: Tokens.gap}) {
          if (this.docked && this.dockSource) Text(this.dockSource).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
            .fontColor(Colors.secondary).maxLines(2).width('100%').id('interaction.task.origin')
          if (this.docked && interactionSkipReason(this.request)) Text(tr(interactionSkipReason(this.request)))
            .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).width('100%').id('interaction.task.skip_reason')
          if (!this.docked && !this.taskQueue) Row({space: 6}) {
            Circle().width(7).height(7).fill(this.headerSemanticColor())
            Text(this.cardHeading())
              .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(600).fontColor(this.headerTitleColor())
          }
          if (this.taskQueue && this.request.kind==='plan') MarkdownText({text:this.taskPlanSource(),chatReply:true,onLink:this.onLink})
          else this.formBody()
        }.width('100%').alignItems(HorizontalAlign.Start).onAreaChange((_before: Area, area: Area) => {
          const h = Number(area.height);
          if (Number.isFinite(h) && h > 0 && Math.abs(this.inlineBodyHeight - h) > 0.5) this.inlineBodyHeight = h;
        })
      }.width('100%').height(this.inlineBodyHeight > 0 ? Math.min(this.inlineBodyHeight, this.inlineSpace()) : undefined)
        .constraintSize({maxHeight: this.inlineSpace()}).scrollBar(BarState.Auto).align(Alignment.TopStart).id('interaction.body')
      if (!this.cardFolded) Column() {
        if (this.taskQueue && this.request.kind==='plan') this.taskPlanActions()
        else this.responseFooter()
        if (Object.keys(this.ruleSuggestionObject()).length > 0) {
          RuleSuggestionBar({
            model: this.model,
            rawSuggestion: this.ruleSuggestionObject(),
            cardKey: 'card|' + this.request.id,
            manager: this.ruleSuggestionManager(),
            onSettled: () => { this.model.refresh().catch(() => undefined); }
          })
        }
      }.width('100%').onAreaChange((_before: Area, area: Area) => {
        const h = Number(area.height);
        if (Number.isFinite(h) && h > 0 && Math.abs(this.inlineFooterHeight - h) > 0.5) this.inlineFooterHeight = h;
      })
    }.width('100%').padding({left:this.docked ? 16 : 14,right:this.docked ? 16 : 14,top:10,bottom:12}).alignItems(HorizontalAlign.Start)
      .backgroundColor(this.panelSurface())
      .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.floatingBorder(this.headerEnvironment.dark),radius:22}).borderRadius(22)
      .shadow(PhoneStyle.floatingElevation(this.headerEnvironment.dark)).id('interaction.card')
      .bindSheet($$this.fullPlan,this.planReader(),{height:SheetSize.LARGE,showClose:false,dragBar:false,preferType:SheetType.BOTTOM})
    else if (this.fillAvailable && !this.pinFooter && this.request.kind !== 'question') Scroll() {
      Column({ space: Tokens.gap }) { this.formBody(); this.responseFooter() }
        .width('100%').padding({ top: Tokens.page }).alignItems(HorizontalAlign.Start)
    }.width('100%').height('100%').scrollBar(BarState.Auto).align(Alignment.TopStart).id('interaction.body')
    else Column({ space: Tokens.gap }) {
      if (this.pinFooter || this.fillAvailable) Scroll() {
        Column({ space: Tokens.gap }) { this.formBody() }.width('100%').alignItems(HorizontalAlign.Start)
      }.width('100%').layoutWeight(1).scrollBar(BarState.Auto).align(Alignment.TopStart).id('interaction.body')
      else this.formBody()
      this.responseFooter()
    }.width('100%').height(this.pinFooter || this.fillAvailable ? '100%' : undefined)
      .padding({ left: this.fillAvailable ? 0 : Tokens.page, right: this.fillAvailable ? 0 : Tokens.page, top: Tokens.page, bottom: this.pinFooter || this.fillAvailable ? 0 : Tokens.page }).alignItems(HorizontalAlign.Start)
  }
}
