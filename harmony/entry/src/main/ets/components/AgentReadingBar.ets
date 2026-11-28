import { AgentReadingHeader } from '../models/AgentReadingHeader';
import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { nativeEnvironment } from '../platform/NativeEnvironment';

@ComponentV2
export struct AgentReadingBar {
  @Require @Param state:AgentReadingHeader;
  @Require @Param owner:string;
  @Event covered:() => boolean=():boolean=>false;
  @BuilderParam full:() => void;
  @BuilderParam slim:() => void;

  aboutToAppear():void { this.bindOwner(); }
  @Monitor('owner') bindOwner():void {
    const owner=this.owner;
    this.state.attach(owner,this.covered,(value:number):void => {
      if (!this.state.owns(owner)) return;
      if (nativeEnvironment.reduceMotion) this.state.collapse=value;
      else this.getUIContext().animateTo(Motion.reveal(false),() => { this.state.collapse=value; });
    });
  }
  aboutToDisappear():void { this.state.detach(this.owner); }
  build() {
    Stack({alignContent:Alignment.Top}) {
      if (this.state.progress<0.5) Column() { this.full() }
        .width('100%').constraintSize({minHeight:this.state.collapse>0 ? this.state.height : undefined})
        .onAreaChange((_:Area,area:Area) => this.state.measure(this.owner,Number(area.height)))
        .translate({y:-this.state.collapse}).opacity(1-Math.min(1,this.state.progress*2))
        .hitTestBehavior(this.state.progress<0.5 ? HitTestMode.Default : HitTestMode.None)
        .accessibilityLevel(this.state.progress<0.5 ? 'auto' : 'no-hide-descendants')
      if (this.state.progress>=0.5) Column() { this.slim() }.width('100%')
        .opacity(Math.max(0,(this.state.progress-0.5)*2))
    }.width('100%').height(this.state.height>0 && this.state.collapse>0 ? Math.max(AgentReadingHeader.slimHeight,this.state.height-this.state.collapse) : undefined)
      .clip(true).backgroundColor(Colors.canvas)
      .onTouch((event:TouchEvent) => {
        if (event.type===TouchType.Down) this.state.overlay(this.owner,'header-touch',true);
        if (event.type===TouchType.Up || event.type===TouchType.Cancel) this.state.overlay(this.owner,'header-touch',false);
      })
  }
}
