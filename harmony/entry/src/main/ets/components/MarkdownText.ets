import { LengthMetrics, Theme } from '@kit.ArkUI';
import { pasteboard } from '@kit.BasicServicesKit';
import { tr } from '../design/Strings';
import { IconActions } from '../design/IconActions';
import { Symbols } from '../design/Symbols';
import { PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { MotionTokens } from '../core/MotionTokens';
import { MarkdownBlock, MarkdownInline, MarkdownProseRun, MarkdownTableRow, agentReplyBodySize, hasHanProse, markdownProseRuns, recapGlyphSize } from '../core/Markdown';
import {
  IncrementalMarkdownCache,
  KeyedMarkdownBlock,
  countCodeLines,
  formatCodeLanguageBadge,
  formatCodeLineCount,
  trimCodeForCopy
} from '../core/StreamMarkdown';
import { Tokens } from '../design/Tokens';
import { Colors } from '../design/Colors';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { Haptics } from '../platform/Haptics';
import { toastCenter } from '../models/ToastCenter';
import { StreamTailCaret } from './StatusMotionMarks';

@ComponentV2
export struct MarkdownText {
  @Require @Param text: string;
  @Param streaming: boolean = false;
  @Param fitContent: boolean = false;
  @Param nativeTextSelection: boolean = true;
  @Param fixedType: boolean = false;
  @Param agentPrompt: boolean = false;
  @Param chatReply: boolean = false;
  @Param agentReply: boolean = false;
  @Param agentFontScale: number = 1;
  @Param recapReading: boolean = false;
  @Param profileReading: boolean = false;
  @Param dockPlan: boolean = false;
  @Param paragraphLineSpacingEm: number = 0.26;
  @Event onLink: (url: string) => void = (_url: string) => {};
  @Event onSelectionChange: (selected: boolean) => void = (_selected: boolean) => {};
  @Local entries: KeyedMarkdownBlock[] = [];
  private cache: IncrementalMarkdownCache = new IncrementalMarkdownCache();
  @Local private environment: NativeEnvironment = nativeEnvironment;
  @Local private copiedCodeKey: string = '';
  @Local private pressedCodeKey: string = '';
  @Local private wrappedCodeKeys: string[] = [];
  private copyResetTimer: number = -1;
  private disposed: boolean = false;
  @Local foreground: ResourceColor = $r('sys.color.font_primary');
  @Local secondary: ResourceColor = $r('sys.color.font_secondary');
  @Local emphasize: ResourceColor = $r('sys.color.font_emphasize');
  @Local codeFill: ResourceColor = $r('sys.color.comp_background_tertiary');
  onWillApplyTheme(theme: Theme): void {
    this.foreground = theme.colors.fontPrimary; this.secondary = theme.colors.fontSecondary;
    this.emphasize = theme.colors.fontEmphasize; this.codeFill = theme.colors.compBackgroundTertiary;
  }

  aboutToAppear(): void {
    this.disposed = false;
    this.entries = this.cache.update(this.text);
  }
  aboutToDisappear(): void {
    this.disposed = true;
    this.wrappedCodeKeys = [];
    if (this.copyResetTimer !== -1) {
      clearTimeout(this.copyResetTimer);
      this.copyResetTimer = -1;
    }
  }
  @Monitor('text') textChanged(): void {
    this.entries = this.cache.update(this.text);
    if (this.wrappedCodeKeys.length > 0) {
      this.wrappedCodeKeys = this.wrappedCodeKeys.filter((key: string) =>
        this.entries.some((entry: KeyedMarkdownBlock) => key === entry.key || key.startsWith(entry.key + '.'))
      );
    }
  }

  @Computed get scaleAgentProse(): boolean { return Math.abs(this.agentFontScale - 1) > 0.001; }
  @Computed get recapProse(): boolean { return this.recapReading && this.chatReply && !this.dockPlan; }
  @Computed get agentBodyVp(): number { return agentReplyBodySize(this.agentFontScale); }
  @Computed get agentHanVp(): number { return agentReplyBodySize(this.agentFontScale, true); }
  // The recording profile uses MarkdownUI's display face at larger reading sizes. Its standard face already matches.
  @Computed get profileDisplayProse():boolean { return this.profileReading && this.agentBodyVp >= 20; }
  @Computed get promptBodyVp(): number { return Math.min(53, Math.max(14, Tokens.body * this.agentFontScale)); }

  private inlineSpacing(size: number, lineSpacingEm: number, readingBody:boolean = false): number {
    if (this.recapProse || readingBody && this.profileDisplayProse) return 0;
    if (this.recapReading) return this.readingPoints(size) * (this.dockPlan ? 0.37 : lineSpacingEm);
    return 0;
  }

  private inlineLineHeight(size: number, readingBody: boolean, spacingEm: number, nodes: MarkdownInline[], promptBody: boolean = false): string | undefined {
    // Theme.Level.body uses a 1.45-em advance. On ArkUI a fixed advance keeps its first and last lines at their intrinsic height.
    if (promptBody) return String(this.promptBodyVp * 1.45) + 'vp';
    // The native recap advances 23.5 pt at standard type and 66.5 pt at the largest reading size.
    if (readingBody && this.recapProse) return String(this.readingPoints(size) * (1.125 + spacingEm)) + 'vp';
    if (readingBody && this.agentBodyVp >= 17 && this.agentBodyVp <= 20) {
      // The system text face precedes the CJK Display crossover. Its two- and four-line replies
      // measure 44.7/93.3pt at standard type and 50.7/106pt at the next native category.
      const points = this.agentBodyVp;
      const advance = points <= 19 ? 73/3 + (points-17) * 11/6 : 28 + points-19;
      return String(advance) + 'vp';
    }
    if (readingBody && this.scaleAgentProse) {
      // SwiftUI adds ReplyMetrics.lineSpacing to the system body line advance.
      // The display CJK face advances 69.3pt per line of 48pt mixed prose in the native chat.
      const points = this.agentBodyVp;
      if (points >= 20 && hasHanProse(nodes)) {
        // The measured iPhone display sizes advance by 29, 38 and 45pt at 20, 26 and 31pt.
        // its largest display face advances by 69⅓pt. Retain the requested leading between those native sizes.
        const advance = points <= 26 ? 29 + (points-20)*1.5 :
          points <= 31 ? 38 + (points-26)*1.4 : 45 + (points-31)*(73/3)/17;
        return String(advance + points*(spacingEm-0.26)) + 'vp';
      }
      return String(points * (1.19 + spacingEm)) + 'vp';
    }
    return this.chatReply && !this.recapReading ? String(Math.ceil(size * 1.45)) + 'fp' : undefined;
  }

  private readingPoints(size: number): number {
    const scale = nativeEnvironment.fontScale;
    // MarkdownUI uses a metric relative to body: 17 points at standard type and 48 at AX5.
    const readingScale = scale <= 1 ? scale : 1 + (scale - 1) * (48 / 17 - 1) / (3.2 - 1);
    return size * readingScale;
  }
  private readingFont(size: number): number | string {
    return this.recapReading ? String(this.readingPoints(size)) + 'vp' : size;
  }
  private proseFont(han: boolean): string | undefined {
    if (this.recapProse) return String(recapGlyphSize(this.readingPoints(Tokens.body), han)) + 'vp';
    if (this.profileDisplayProse && !han) return String(this.agentBodyVp) + 'vp';
    return han ? String(this.agentHanVp) + 'vp' : undefined;
  }
  private recapTopInset(): number {
    // The smaller display face loses 5 vp of intrinsic first-line ascent at the largest reading size.
    return Math.max(0, this.readingPoints(Tokens.body) - Tokens.body) * 5 / (48 - Tokens.body);
  }
  private headingSize(level: number): number {
    if (this.chatReply) {
      if (level === 1) return Math.round(Tokens.body * 1.3);
      return level <= 3 ? Tokens.body : 15;
    }
    if (level <= 1) return Tokens.markdownHeading1;
    if (level === 2) return Tokens.markdownHeading2;
    if (level === 3) return Tokens.markdownHeading3;
    if (level === 4) return Tokens.markdownHeading4;
    if (level === 5) return Tokens.markdownHeading5;
    return Tokens.body;
  }

  private isCodeWrapped(key: string): boolean {
    return this.wrappedCodeKeys.includes(key);
  }

  private toggleCodeWrap(key: string): void {
    Haptics.selection();
    Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => {
      this.wrappedCodeKeys = this.isCodeWrapped(key)
        ? this.wrappedCodeKeys.filter((entry: string) => entry !== key)
        : this.wrappedCodeKeys.concat([key]);
    });
  }

  private copyCode(text: string, key: string): void {
    try {
      pasteboard.getSystemPasteboard().setDataSync(pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN, trimCodeForCopy(text)));
      if (this.disposed) return;
      Haptics.selection();
      this.copiedCodeKey = key;
      if (this.copyResetTimer !== -1) clearTimeout(this.copyResetTimer);
      this.copyResetTimer = setTimeout(() => {
        if (!this.disposed && this.copiedCodeKey === key) this.copiedCodeKey = '';
        this.copyResetTimer = -1;
      }, 1600);
      toastCenter.show(tr('copied'));
    } catch (_) {
      if (this.disposed) return;
      Haptics.error();
      toastCenter.error(tr('failed'));
    }
  }

  private tableTextAlign(table: MarkdownBlock, column: number): TextAlign {
    const alignment = table.alignments[column] || 'start';
    if (alignment === 'center') return TextAlign.Center;
    if (alignment === 'end') return TextAlign.End;
    return TextAlign.Start;
  }

  @Builder inlineText(nodes: MarkdownInline[], lineSpacingEm: number, size: number = Tokens.body, weight: FontWeight = FontWeight.Normal,
    alignment: TextAlign = TextAlign.Start, breakAll: boolean = false, quiet: boolean = false, readingBody: boolean = false,
    promptBody: boolean = false, italicDefault: boolean = false) {
    Text() {
      ForEach(nodes, (node: MarkdownInline) => this.inlineNode(node, weight, italicDefault, '', quiet, readingBody),
        (node: MarkdownInline, index: number) => String(index))
    }
      .fontSize(promptBody ? String(this.promptBodyVp) + 'vp' :
        readingBody && (this.scaleAgentProse || this.fixedType) ? String(this.agentBodyVp) + 'vp' : this.readingFont(size))
      .fontColor(quiet ? this.secondary : this.foreground).fontWeight(weight)
      .lineHeight(this.inlineLineHeight(size, readingBody, lineSpacingEm, nodes, promptBody))
      .lineSpacing(LengthMetrics.vp(this.inlineSpacing(size, lineSpacingEm, readingBody)), { onlyBetweenLines: true })
      .textAlign(alignment).wordBreak(breakAll ? WordBreak.BREAK_ALL : WordBreak.BREAK_WORD)
      .copyOption(this.nativeTextSelection ? CopyOptions.LocalDevice : CopyOptions.None).onTextSelectionChange((start: number, end: number) => this.onSelectionChange(end > start))
      .width(this.fitContent ? undefined : '100%')
  }

  @Builder inlineNode(node: MarkdownInline, weight: FontWeight, italic: boolean, url: string, quiet: boolean, readingBody: boolean) {
    if (node.kind === 'bold' || node.kind === 'italic' || node.kind === 'link') {
      ForEach(node.children, (child: MarkdownInline) => this.inlineNode(child,
        node.kind === 'bold' ? (this.chatReply ? FontWeight.Medium : FontWeight.Bold) : weight,
        italic || node.kind === 'italic', node.kind === 'link' ? node.url : url, quiet, readingBody),
        (child: MarkdownInline, index: number) => String(index))
    } else if (url) {
      Span(node.text).fontFamily(node.kind === 'code' ? 'monospace' : undefined)
        .fontSize(node.kind === 'code' ? this.readingFont(this.chatReply ? 16 : Tokens.meta) : undefined)
        .fontWeight(weight).fontStyle(italic ? FontStyle.Italic : FontStyle.Normal).fontColor(this.emphasize)
        .textBackgroundStyle({ color: node.kind === 'code' && !this.chatReply ? this.codeFill : Color.Transparent, radius: Tokens.fine })
        .decoration({ type: TextDecorationType.Underline, color: this.emphasize }).onClick(() => this.onLink(url))
    } else if (node.kind === 'code') {
      Span(node.text).fontFamily('monospace').fontSize(this.readingFont(this.chatReply ? 15.5 : Tokens.typeCode))
        .fontWeight(weight).fontStyle(italic ? FontStyle.Italic : FontStyle.Normal)
        .fontColor(this.recapProse ? Colors.inlineCodeInk : this.chatReply ? Colors.inlineCodeInk : this.secondary)
        .textBackgroundStyle({
          color: this.recapProse ? Color.Transparent : this.chatReply ? Colors.hairlineGlass : this.codeFill,
          radius: Tokens.radiusBadge - 1
        })
    } else if (readingBody && (this.scaleAgentProse || this.recapProse)) {
      ForEach(markdownProseRuns(node.text), (run: MarkdownProseRun) => {
        Span(run.text).fontSize(this.proseFont(run.han))
          .fontColor(quiet ? this.secondary : this.foreground).fontWeight(weight).fontStyle(italic ? FontStyle.Italic : FontStyle.Normal)
      }, (run: MarkdownProseRun, index: number) => String(index))
    } else {
      Span(node.text).fontColor(quiet ? this.secondary : this.foreground).fontWeight(weight)
        .fontStyle(italic ? FontStyle.Italic : FontStyle.Normal)
    }
  }

  @Builder codeBlockCard(block: MarkdownBlock, blockKey: string) {
    Column({ space: 0 }) {
      Row({ space: 8 }) {
        Text(formatCodeLanguageBadge(block.language))
          .fontFamily('monospace')
          .fontSize(11)
          .maxFontScale(Tokens.interfaceScaleFor(11))
          .fontWeight(FontWeight.Bold)
          .letterSpacing(0.6)
          .fontColor(Colors.link)
          .maxLines(1)
          .textOverflow({ overflow: TextOverflow.Ellipsis })
          .padding({ left: 7, right: 7, top: 2.5, bottom: 2.5 })
          .backgroundColor(Colors.pressOverlay)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: Tokens.radiusBadge })
          .borderRadius(Tokens.radiusBadge)
        if (countCodeLines(block.text) > 0) {
          Text(formatCodeLineCount(countCodeLines(block.text)))
            .fontFamily('monospace')
            .fontSize(Tokens.typeLabel)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
            .fontColor(Colors.secondary)
            .maxLines(1)
        }
        Blank().layoutWeight(1)
        Button({ type: ButtonType.Capsule }) {
          Row({ space: Tokens.spaceXs }) {
            SymbolGlyph(this.isCodeWrapped(blockKey) ? $r('sys.symbol.arrow_left_arrow_right') : $r('sys.symbol.textformat'))
              .fontSize('12vp')
              .fontColor([this.isCodeWrapped(blockKey) ? Colors.link : Colors.secondary])
            Text(tr(this.isCodeWrapped(blockKey) ? 'code_unwrap_lines' : 'code_wrap_lines'))
              .fontSize(Tokens.typeLabel)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
              .fontWeight(FontWeight.Medium)
              .fontColor(this.isCodeWrapped(blockKey) ? Colors.link : Colors.secondary)
              .maxLines(1)
          }
        }
          .height(26)
          .padding({ left: 9, right: 9 })
          .backgroundColor(this.isCodeWrapped(blockKey)
            ? Colors.selection
            : Colors.pressOverlay)
          .border({
            width: PhoneStyle.surfaceEdgeWidth(0.5),
            color: this.isCodeWrapped(blockKey) ? Colors.link : PhoneStyle.cardBorder(this.environment.dark),
            radius: Tokens.radiusCapsule
          })
          .responseRegion({ x: -4, y: -9, width: 68, height: Tokens.controlBar })
          .accessibilityText(tr(this.isCodeWrapped(blockKey) ? 'code_unwrap_lines' : 'code_wrap_lines'))
          .bindTips(tr(this.isCodeWrapped(blockKey) ? 'code_unwrap_lines' : 'code_wrap_lines'))
          .onClick(() => this.toggleCodeWrap(blockKey))
          .id('markdown.code.wrap')
        Button({ type: ButtonType.Capsule }) {
          Row({ space: Tokens.spaceXs }) {
            SymbolGlyph(this.copiedCodeKey === blockKey ? Symbols.check : IconActions.symbol('copy'))
              .fontSize('12.5vp')
              .fontColor([this.copiedCodeKey === blockKey ? Colors.positive : Colors.secondary])
            Text(this.copiedCodeKey === blockKey ? tr('copied') : tr('copy'))
              .fontSize(Tokens.typeLabel)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
              .fontWeight(FontWeight.Medium)
              .fontColor(this.copiedCodeKey === blockKey ? Colors.positive : Colors.secondary)
              .maxLines(1)
          }
        }
          .height(26)
          .padding({ left: 10, right: 10 })
          .backgroundColor(this.copiedCodeKey === blockKey
            ? Colors.unreadWash
            : Colors.pressOverlay)
          .border({
            width: this.copiedCodeKey === blockKey ? 0.5 : PhoneStyle.surfaceEdgeWidth(0.5),
            color: this.copiedCodeKey === blockKey
              ? Colors.positive
              : PhoneStyle.cardBorder(this.environment.dark),
            radius: Tokens.radiusCapsule
          })
          .responseRegion({ x: -6, y: -9, width: 76, height: Tokens.controlBar })
          .scale({
            x: this.pressedCodeKey === blockKey ? MotionTokens.pressScale : 1,
            y: this.pressedCodeKey === blockKey ? MotionTokens.pressScale : 1
          })
          .animation(Motion.release(this.environment.reduceMotion))
          .onTouch((event: TouchEvent) => {
            if (event.type === TouchType.Down) this.pressedCodeKey = blockKey;
            else if (event.type === TouchType.Up || event.type === TouchType.Cancel) {
              if (this.pressedCodeKey === blockKey) this.pressedCodeKey = '';
            }
          })
          .accessibilityText(tr('copy') + (block.language ? ' ' + block.language : ''))
          .bindTips(tr('copy'))
          .onClick(() => this.copyCode(block.text, blockKey))
          .id('markdown.code.copy')
      }
        .width('100%')
        .padding({ left: Tokens.spaceM, right: 10, top: Tokens.spaceS, bottom: Tokens.spaceS })
        .backgroundColor(Colors.codeHunk)
        .alignItems(VerticalAlign.Center)
      Divider().strokeWidth(Tokens.divider).color(this.environment.dark ? Colors.hairlineGlass : Colors.divider)
      if (this.isCodeWrapped(blockKey)) {
        Text(trimCodeForCopy(block.text))
          .fontFamily('monospace')
          .fontSize(this.chatReply ? 13.5 : Tokens.meta)
          .lineHeight(this.chatReply ? '21fp' : '20fp')
          .fontColor(Colors.ink)
          .wordBreak(WordBreak.BREAK_WORD)
          .copyOption(this.nativeTextSelection ? CopyOptions.LocalDevice : CopyOptions.None)
          .onTextSelectionChange((start: number, end: number) => this.onSelectionChange(end > start))
          .padding({ left: 14, right: 14, top: 12, bottom: 12 })
          .width('100%')
      } else {
        Scroll() {
          Text(trimCodeForCopy(block.text))
            .fontFamily('monospace')
            .fontSize(this.chatReply ? 13.5 : Tokens.meta)
            .lineHeight(this.chatReply ? '21fp' : '20fp')
            .fontColor(Colors.ink)
            .copyOption(this.nativeTextSelection ? CopyOptions.LocalDevice : CopyOptions.None)
            .onTextSelectionChange((start: number, end: number) => this.onSelectionChange(end > start))
            .padding({ left: 14, right: 14, top: 12, bottom: 12 })
        }
          .scrollable(ScrollDirection.Horizontal)
          .align(Alignment.TopStart)
          .scrollBar(BarState.Auto)
          .width('100%')
      }
    }
      .width('100%')
      .alignItems(HorizontalAlign.Start)
      .backgroundColor(Colors.codeCanvas)
      .borderRadius(Tokens.radiusCard)
      .clip(true)
      .border({
        width: PhoneStyle.surfaceEdgeWidth(0.5),
        color: PhoneStyle.cardBorder(this.environment.dark),
        radius: Tokens.radiusCard
      })
      .shadow(this.chatReply ? PhoneStyle.cardElevation(this.environment.dark) : { radius: 0, color: Color.Transparent })
  }

  @Builder blockView(block: MarkdownBlock, first: boolean = false, blockKey: string = '', quoted: boolean = false) {
    if (block.kind === 'paragraph') {
      this.inlineText(block.inlines, this.profileDisplayProse ? 0.26 : this.paragraphLineSpacingEm, Tokens.body, FontWeight.Normal,
        TextAlign.Start, false, quoted, !quoted && (this.recapProse || this.profileDisplayProse), false, quoted)
    } else if (block.kind === 'heading') {
      Column() {
        this.inlineText(block.inlines, 0.26, this.headingSize(block.level),
          this.chatReply && block.level > 1 ? FontWeight.Medium : FontWeight.Bold,
          TextAlign.Start, false, quoted || this.chatReply && block.level >= 4, false, false, quoted)
      }.alignItems(HorizontalAlign.Start).width(this.fitContent ? undefined : '100%')
        .padding({ top: this.chatReply && !first ? (block.level === 1 ? 8 : 4) : 0 })
    } else if (block.kind === 'code') {
      this.codeBlockCard(block, blockKey || 'code')
    } else if (block.kind === 'raw') {
      Text(block.text).fontSize(Tokens.body).fontColor(quoted ? this.secondary : this.foreground).wordBreak(WordBreak.BREAK_WORD)
        .fontStyle(quoted ? FontStyle.Italic : FontStyle.Normal)
        .copyOption(this.nativeTextSelection ? CopyOptions.LocalDevice : CopyOptions.None).onTextSelectionChange((start: number, end: number) => this.onSelectionChange(end > start)).width('100%')
    } else if (block.kind === 'rule') {
      Divider().strokeWidth(0.5).color(Colors.divider).width('100%')
        .margin({ top: Tokens.gap, bottom: Tokens.gap })
    } else if (block.kind === 'quote') {
      Column() {
        Column({ space: Tokens.gap }) {
          ForEach(block.children, (child: MarkdownBlock, index: number) => this.blockView(child, index === 0, blockKey + '.q' + String(index), true),
            (_child: MarkdownBlock, index: number) => 'quote.' + String(index))
        }
          .width('100%')
          .alignItems(HorizontalAlign.Start)
          .padding({ left: 12, top: 2, bottom: 2 })
          .border({ width: { left: 3.5 }, color: Colors.interfaceAccent, radius: { topLeft: 2, bottomLeft: 2 } })
      }
        .padding({ left: 10, right: 14, top: 10, bottom: 10 })
        .width('100%')
        .alignItems(HorizontalAlign.Start)
        .backgroundColor(Colors.recapTint)
        .borderRadius(12)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: 12 })
    } else if (block.kind === 'list') {
      Column({ space: this.dockPlan ? this.readingPoints(Tokens.body)*0.2 : Tokens.gap }) {
        ForEach(block.items, (item: MarkdownBlock, index: number) => this.listItem(item, blockKey + '.l' + String(index), quoted),
          (_item: MarkdownBlock, index: number) => 'list.' + String(index))
      }.width('100%').alignItems(HorizontalAlign.Start)
        .padding({top:this.dockPlan && !first ? Math.max(0,this.readingPoints(Tokens.body)*0.2-6) : 0})
    } else if (block.kind === 'table') {
      Scroll() {
        Column({ space: 0 }) {
          ForEach(block.rows, (row: MarkdownTableRow, rowIndex: number) => this.tableRow(row, block, rowIndex),
            (_row: MarkdownTableRow, index: number) => 'row.' + String(index))
        }
          .backgroundColor(Colors.surface)
          .borderRadius(12)
          .clip(true)
          .border({
            width: PhoneStyle.surfaceEdgeWidth(0.5),
            color: PhoneStyle.cardBorder(this.environment.dark),
            radius: 12
          })
      }
        .scrollable(ScrollDirection.Horizontal).align(Alignment.TopStart).scrollBar(BarState.Auto).width('100%')
    }
  }

  @Builder listItem(item: MarkdownBlock, itemKey: string = '', quoted: boolean = false) {
    Row({ space: this.dockPlan ? 10 : Tokens.inset }) {
      Text(item.marker).fontSize(this.dockPlan ? this.readingFont(Tokens.body) : Tokens.body).fontColor(this.secondary)
      Column({ space: Tokens.gap }) {
        ForEach(item.children, (child: MarkdownBlock, index: number) => this.blockView(child, index === 0, itemKey + '.i' + String(index), quoted),
          (_child: MarkdownBlock, index: number) => 'item.' + String(index))
      }.layoutWeight(1).alignItems(HorizontalAlign.Start)
    }.width('100%').alignItems(VerticalAlign.Top).padding({left:this.dockPlan ? this.readingPoints(Tokens.body)*0.65 : 0})
  }

  @Builder tableRow(row: MarkdownTableRow, table: MarkdownBlock, rowIndex: number) {
    Column({ space: 0 }) {
      if (rowIndex > 0) {
        Divider()
          .strokeWidth(rowIndex === 1 ? 0.75 : Tokens.divider)
          .color(this.environment.dark ? Colors.hairlineGlass : Colors.divider)
      }
      Row() {
        ForEach(row.cells, (cell: MarkdownInline[], index: number) => this.tableCell(cell, table, index, row.header),
          (_cell: MarkdownInline[], index: number) => 'cell.' + String(index))
      }
        .alignItems(VerticalAlign.Top)
        .backgroundColor(row.header
          ? Colors.subtle
          : rowIndex % 2 === 0
            ? (this.environment.dark ? Colors.pressOverlay : Colors.codeSurface)
            : Colors.surface)
    }
  }

  @Builder tableCell(cell: MarkdownInline[], table: MarkdownBlock, index: number, header: boolean) {
    Column() {
      this.inlineText(cell, 0.22, this.chatReply ? 14.5 : Tokens.meta, header ? FontWeight.Bold : FontWeight.Normal, this.tableTextAlign(table, index), true)
    }
      .width(Tokens.markdownTableCell)
      .padding({ left: Tokens.spaceM, right: Tokens.spaceM, top: 9, bottom: 9 })
      .alignItems(HorizontalAlign.Start)
      .border(index > 0 ? { width: { left: Tokens.divider }, color: this.environment.dark ? Colors.hairlineGlass : Colors.divider } : { width: 0 })
  }

  build() {
    Column({ space: this.dockPlan ? 6 : Tokens.gap }) {
      ForEach(this.entries, (entry: KeyedMarkdownBlock, index: number) => {
        if (this.agentReply && entry.block.kind === 'paragraph') {
          Column() {
            this.inlineText(entry.block.inlines, this.paragraphLineSpacingEm, Tokens.body, FontWeight.Normal,
              TextAlign.Start, false, false, true)
          }.width('100%').alignItems(HorizontalAlign.Start)
            .padding({ top: index > 0 && this.entries[index - 1]?.block.kind === 'paragraph' ? 10 - Tokens.gap : 0 })
        } else if (this.agentPrompt && entry.block.kind === 'paragraph') {
          this.inlineText(entry.block.inlines, this.paragraphLineSpacingEm, Tokens.body, FontWeight.Normal,
            TextAlign.Start, false, false, false, true)
        } else {
          this.blockView(entry.block, index === 0, entry.key)
        }
      },
        (entry: KeyedMarkdownBlock) => entry.key)
      if (this.streaming) StreamTailCaret()
    }.width(this.fitContent ? undefined : '100%').alignItems(HorizontalAlign.Start)
      .padding({ top: this.recapProse ? this.recapTopInset() : 0 })
  }
}
