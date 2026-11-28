import { DashboardPoint, dashboardTimeTicks } from '../models/MachineDashboard';
import { Haptics } from '../platform/Haptics';

@ComponentV2
export struct MachineChart {
  @Require @Param points: DashboardPoint[];
  @Require @Param end: number;
  @Param seconds: number = 3600;
  @Param step: number = 10;
  @Param tint: string = '#007AFF';
  @Param alternateTint: string = '';
  @Param ceiling: number = 0;
  @Param chartHeight: number = 24;
  @Param selectedTime: number = 0;
  @Param dark: boolean = false;
  @Param interactive: boolean = false;
  @Event pick: (time: number) => void = () => {};
  @Local plotWidth: number = 0;
  private ctx: CanvasRenderingContext2D = new CanvasRenderingContext2D(new RenderingContextSettings(true));
  private ready: boolean = false;

  @Monitor('points','end','seconds','tint','alternateTint','ceiling','chartHeight','selectedTime','dark','plotWidth')
  redraw(): void { this.paint(); }

  private x(at: number): number { return 1 + (at - this.end + this.seconds) * (this.plotWidth - 2) / this.seconds; }
  private y(value: number, highest: number): number {
    return this.chartHeight - 1 - Math.min(1, value / highest) * (this.chartHeight - 3);
  }

  private grid(): void {
    const ctx = this.ctx, width = this.plotWidth-1, height = this.chartHeight-1;
    ctx.strokeStyle = this.dark ? '#2C2C2E' : '#E5E5EA'; ctx.lineWidth = 0.6;
    ctx.setLineDash([2, 3]);
    ctx.beginPath();
    for (const time of dashboardTimeTicks(this.end,this.seconds)) {
      const x = this.x(time);
      ctx.moveTo(x,1); ctx.lineTo(x,height);
    }
    for (let i = 0; i <= 2; i++) {
      const y = 1 + (height-1)*i/2;
      ctx.moveTo(1,y); ctx.lineTo(width,y);
    }
    ctx.stroke();
    ctx.setLineDash([]);
  }

  private line(alternate: boolean, color: string, highest: number): void {
    const ctx = this.ctx, bottom = this.chartHeight - 1;
    let run: DashboardPoint[] = [];
    const draw = (): void => {
      if (!run.length) return;
      ctx.lineJoin = 'round';
      ctx.lineCap = 'round';
      ctx.beginPath();
      for (let i = 0; i < run.length; i++) {
        const p = run[i], value = alternate ? p.alternate : p.value;
        const x = this.x(p.time), y = this.y(value,highest);
        if (i === 0) ctx.moveTo(x,y); else ctx.lineTo(x,y);
        if (run.length === 1) { ctx.fillStyle = color; ctx.fillRect(x-0.7,y-0.7,1.4,1.4); }
      }
      ctx.strokeStyle = color; ctx.lineWidth = this.chartHeight > 30 ? 1.85 : 1.4; ctx.stroke();
      if (!alternate && !this.alternateTint && run.length > 1) {
        ctx.lineTo(this.x(run[run.length-1].time), bottom); ctx.lineTo(this.x(run[0].time), bottom); ctx.closePath();
        const gradient = ctx.createLinearGradient(0, 1, 0, bottom);
        const topAlpha = this.interactive ? (this.dark ? '38' : '24') : (this.dark ? '42' : '24');
        const hex = color.startsWith('#') && color.length === 7 ? color.slice(1) : '';
        if (hex) {
          gradient.addColorStop(0, '#' + topAlpha + hex);
          gradient.addColorStop(1, '#00' + hex);
          ctx.fillStyle = gradient;
          ctx.fill();
        } else {
          ctx.globalAlpha = this.interactive ? 0.1 : (this.dark ? 0.22 : 0.11); ctx.fillStyle = color; ctx.fill(); ctx.globalAlpha = 1;
        }
      }
      run = [];
    };
    for (const p of this.points) {
      if ((alternate ? p.alternate : p.value) < 0) draw(); else run.push(p);
    }
    draw();
  }

  private paint(): void {
    if (!this.ready) return;
    this.ctx.clearRect(0,0,this.plotWidth,this.chartHeight);
    if (this.plotWidth < 3 || this.seconds <= 0) return;
    if (this.interactive) this.grid();
    if (!this.points.length) return;
    const high = this.ceiling || this.points.reduce((value:number,p:DashboardPoint) => Math.max(value,p.value,p.alternate),1);
    this.line(false,this.tint,high);
    if (this.alternateTint) this.line(true,this.alternateTint,high);
    const point = this.selectedTime ? this.points.find((p:DashboardPoint) => p.time === this.selectedTime) : undefined;
    if (point && (point.value >= 0 || point.alternate >= 0)) {
      const x = this.x(point.time);
      this.ctx.beginPath(); this.ctx.moveTo(x,0); this.ctx.lineTo(x,this.chartHeight);
      this.ctx.strokeStyle = this.dark ? '#BBBCC4' : '#555660'; this.ctx.lineWidth = 1; this.ctx.stroke();
      if (point.value >= 0) this.mark(x,this.y(point.value,high),this.tint);
      if (point.alternate >= 0 && this.alternateTint) this.mark(x,this.y(point.alternate,high),this.alternateTint);
    }
  }
  private mark(x:number,y:number,color:string):void {
    this.ctx.beginPath(); this.ctx.arc(x,y,4,0,2*Math.PI);
    this.ctx.fillStyle = color; this.ctx.fill();
    this.ctx.strokeStyle = this.dark ? '#242426' : '#FFFFFF'; this.ctx.lineWidth = 1.5; this.ctx.stroke();
  }
  private scrub(x:number):void {
    if (!this.interactive || !this.points.length || this.plotWidth < 3 || !Number.isFinite(x)) return;
    const fraction = Math.max(0,Math.min(1,(x-1)/(this.plotWidth-2)));
    const time = Math.round(this.end - this.seconds + this.seconds * fraction);
    let nearest = time, distance = this.step*2;
    for (const p of this.points) {
      if (p.value < 0 && p.alternate < 0) continue;
      const offset = Math.abs(p.time - time);
      if (offset <= distance) { nearest = p.time; distance = offset; }
    }
    // Keep the inspected clock time in an empty stretch. It has no reading or marker.
    if (nearest !== this.selectedTime) this.pick(nearest);
  }
  private drag(event:GestureEvent):void {
    if (event.fingerList.length) this.scrub(event.fingerList[0].localX);
  }
  @Builder plot() {
    Canvas(this.ctx).width('100%').height(this.chartHeight)
      .onReady(() => { this.ready = true; this.paint(); })
      .onAreaChange((_:Area,a:Area) => { this.plotWidth = Number(a.width); })
  }
  build() {
    if (this.interactive) {
      Stack() { this.plot() }.width('100%').height(this.chartHeight)
        .onClick((event:ClickEvent) => { Haptics.selection(); this.scrub(event.x); })
        .parallelGesture(PanGesture({fingers:1,direction:PanDirection.Horizontal,distance:6})
          .onActionStart((event:GestureEvent) => { Haptics.selection(); this.drag(event); })
          .onActionUpdate((event:GestureEvent) => this.drag(event)))
    } else {
      this.plot()
    }
  }
}
