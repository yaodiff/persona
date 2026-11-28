import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { MotionTokens } from '../core/MotionTokens';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { ClientModel } from '../models/ClientModel';
import { agentPendingCards } from '../models/InteractionDecision';
import { DecisionDrafts } from '../models/QuestionWorkspace';
import { Decision } from '../models/Wire';
import { InteractionDecisionForm } from './InteractionDecisionForm';

@ComponentV2
export struct AgentPendingCards {
  @Require @Param model: ClientModel;
  @Require @Param room: number;
  @Param keyboard: boolean = false;
  @Event onCountHeight:(height:number)=>void = (_height:number):void => {};
  @Event admission: () => boolean = () => false;
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Local contentHeight: number = 0;
  @Local private env: NativeEnvironment = nativeEnvironment;
  private list: Scroller = new Scroller();
  private drafts: DecisionDrafts = new DecisionDrafts();
  private previousIds: string[] = [];

  @Computed get cards(): Decision[] { return agentPendingCards(this.model.pendingDecisions, this.model.decision); }
  @Computed get owner(): string { return JSON.stringify([this.model.scope(), this.model.connectionGeneration(), this.model.target()]); }
  @Computed get overRoom(): boolean { return this.keyboard || this.contentHeight > this.room + 1; }
  @Computed get showCount(): boolean { return this.cards.length > 1 && this.overRoom; }
  private stackHeight(): number | undefined {
    if (this.contentHeight<=0) return undefined;
    return Math.min(this.room,this.contentHeight+(this.overRoom ? 24 : 0));
  }

  aboutToAppear(): void { this.updateCards(); }
  @Monitor('cards', 'owner') updateCards(): void {
    const ids = this.cards.map((request: Decision) => request.id);
    this.drafts.bind(this.owner);
    this.drafts.retain(ids);
    if (this.previousIds.length && ids.some((id: string) => !this.previousIds.includes(id))) {
      try {
        this.list.scrollTo({
          xOffset: 0,
          yOffset: 0,
          animation: this.env.reduceMotion ? false : { duration: MotionTokens.stateMs, curve: Curve.EaseOut }
        });
      } catch (_) {
        this.list.scrollEdge(Edge.Top);
      }
    }
    this.previousIds = ids;
  }

  private ticket(request: Decision): () => boolean {
    const owner = this.model, scope = owner.scope(), generation = owner.connectionGeneration(), target = owner.target();
    return (): boolean => this.admission() && this.model === owner && owner.connected && owner.owns(generation, scope) &&
      owner.target() === target && (owner.pendingDecisions.includes(request) || !owner.pendingDecisions.length && owner.decision === request);
  }

  build() {
    Column({ space: 8 }) {
      if (this.showCount) {
        Row({ space: 6 }) {
          Circle().width(7).height(7).fill(Colors.needsMark).accessibilityLevel('no')
          Text(tr('chat_pending_cards_count').replace('{count}', String(this.cards.length)))
            .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(600).fontColor(Colors.ink)
            .lineHeight(String(15.75*Math.min(this.env.fontScale,Tokens.interfaceScaleFor(13)))+'vp')
            .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        }
          .padding({ left: 10, right: 10, top: 4, bottom: 4 })
          .backgroundColor(Colors.styleID === 'glass' ? Colors.glassSurface : Colors.surface)
          .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
          .borderRadius(12)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.floatingBorder(this.env.dark), radius: 12 })
          .shadow(PhoneStyle.floatingElevation(this.env.dark))
          .margin({ left: 2 })
          .id('chat.pending.count')
          .onAreaChange((_old:Area,area:Area) => {
            const h=Number(area.height); if (h>0) this.onCountHeight(h);
          })
      }
      Stack({ alignContent: Alignment.Top }) {
        Scroll(this.list) {
          Column() {
            Column({ space: 12 }) {
              ForEach(this.cards, (request: Decision) => {
                Column() {
                  InteractionDecisionForm({ model: this.model, request: request, agentCard: true,
                    heightLimit: this.cards.length === 1 ? this.room : 0, draft: this.drafts.draft(request), draftSession: this.owner,
                    admission: this.ticket(request), onLink: this.onLink })
                }.width('100%')
                  .transition(Motion.fadeScale(this.env.reduceMotion, 0.97))
                  .id('chat.pending.card.' + request.id)
              }, (request: Decision) => this.owner + '/' + request.id + '/' + request.raw)
            }.width('100%')
            .onAreaChange((_old: Area, area: Area) => {
              const height = Number(area.height);
              if (Number.isFinite(height) && Math.abs(height - this.contentHeight) > 0.5) this.contentHeight = height;
            })
          }.width('100%').padding({top:this.overRoom ? 16 : 2,bottom:this.overRoom ? 12 : 4})
        }.width('100%').height(this.stackHeight()).align(Alignment.TopStart).scrollBar(BarState.Auto)
          .constraintSize({ maxHeight: this.room }).id('chat.pending.cards')
        if (this.overRoom) {
          Row().width('100%').height(16)
            .linearGradient({ direction: GradientDirection.Bottom, colors: [[Colors.canvas, 0], [Color.Transparent, 1]] })
            .hitTestBehavior(HitTestMode.None).accessibilityLevel('no')
        }
      }.width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start).padding({bottom:this.keyboard ? 0 : 2})
  }
}
