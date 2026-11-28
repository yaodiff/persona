import { common } from '@kit.AbilityKit';
import { uniformTypeDescriptor } from '@kit.ArkData';
import { util } from '@kit.ArkTS';
import { pasteboard } from '@kit.BasicServicesKit';
import { fileIo, fileUri } from '@kit.CoreFileKit';
import { systemShare } from '@kit.ShareKit';
import {
  CHAT_EXPORT_DEFAULT_LIMIT,
  ChatExportMessageInput,
  ChatExportResult,
  exportChatToMarkdown
} from '../core/ChatExport';
import { Colors } from '../design/Colors';
import { IconActions } from '../design/IconActions';
import { PersonaSheetHeader } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import { ClientModel } from '../models/ClientModel';
import { toastCenter } from '../models/ToastCenter';
import { ChatMessage, Wire } from '../models/Wire';
import { Haptics } from '../platform/Haptics';

/**
 * Markdown export preview & share/copy sheet aligned with iOS `ChatExportSheet.swift`.
 */
@ComponentV2
export struct ChatExportSheet {
  @Require @Param model: ClientModel;
  @Event onClose: () => void = () => {};

  @Local private includeTimestamps: boolean = true;
  @Local private includeWorkSteps: boolean = false;
  @Local private sharing: boolean = false;

  @Computed get chatTitle(): string {
    return this.model.selected.title || tr('conversation');
  }

  @Computed get exportInputs(): ChatExportMessageInput[] {
    return this.model.messages.map((msg: ChatMessage): ChatExportMessageInput => ({
      id: msg.id,
      role: msg.role,
      text: msg.text,
      work: msg.work,
      toolCount: msg.toolCount,
      thoughtCount: msg.thoughtCount,
      at: msg.at
    }));
  }

  @Computed get exported(): ChatExportResult {
    const data = this.model.selected.data();
    const machineName = this.model.machineName(this.model.selected.machine) || this.model.selected.machine;
    const modelName = Wire.text(data, 'model') || Wire.text(data, 'launcher');
    const assistantSpeaker = this.model.selected.kind === 'conversation'
      ? tr('persona')
      : (Wire.text(data, 'slot_name') || Wire.text(data, 'launcher') || tr('assistant'));

    return exportChatToMarkdown(this.exportInputs, {
      title: this.chatTitle,
      machineName: machineName,
      modelName: modelName,
      userSpeaker: tr('chat_lift_me'),
      assistantSpeaker: assistantSpeaker,
      workLabel: tr('work_summary'),
      includeTimestamps: this.includeTimestamps,
      includeWorkSteps: this.includeWorkSteps,
      limit: CHAT_EXPORT_DEFAULT_LIMIT,
      truncatedNotice: tr('export_truncated_notice')
    });
  }

  private copyMarkdown(): void {
    try {
      pasteboard.getSystemPasteboard().setDataSync(
        pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN, this.exported.markdown)
      );
      Haptics.success();
      toastCenter.success(tr('export_copied_toast'));
    } catch (_) {
      Haptics.error();
      toastCenter.error(tr('failed'));
    }
  }

  private async shareMarkdown(): Promise<void> {
    if (this.sharing || !this.exported.markdown) return;
    this.sharing = true;
    let filePath = '';
    let shared = false;
    try {
      const context = this.getUIContext().getHostContext() as common.UIAbilityContext | undefined;
      if (context && context.cacheDir) {
        const dir = context.cacheDir + '/chat-export';
        if (!fileIo.accessSync(dir)) fileIo.mkdirSync(dir, true);
        for (const name of fileIo.listFileSync(dir)) {
          if (!name.endsWith('.md')) continue;
          const oldPath = dir + '/' + name;
          try {
            if (Date.now() - fileIo.statSync(oldPath).mtime * 1000 > 86400000) fileIo.unlinkSync(oldPath);
          } catch (_) {}
        }
        const safeBase = (this.chatTitle.replace(/[^a-zA-Z0-9\u4e00-\u9fa5_-]+/g, '-').replace(/^-+|-+$/g, '') || 'chat-export').slice(0, 48);
        filePath = dir + '/' + safeBase + '-' + util.generateRandomUUID().slice(0, 8) + '.md';
        const encoded = new util.TextEncoder().encodeInto(this.exported.markdown);
        const file = fileIo.openSync(filePath, fileIo.OpenMode.CREATE | fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.TRUNC);
        try {
          fileIo.writeSync(file.fd, encoded.buffer as ArrayBuffer);
        } finally {
          fileIo.closeSync(file);
        }
        const record: systemShare.SharedRecord = {
          utd: uniformTypeDescriptor.UniformDataType.PLAIN_TEXT,
          uri: fileUri.getUriFromPath(filePath),
          title: this.chatTitle
        };
        await new systemShare.ShareController(new systemShare.SharedData(record)).show(context, {});
        shared = true;
        Haptics.success();
      }
    } catch (_) {
      if (filePath && !shared) {
        try { fileIo.unlinkSync(filePath); } catch (_) {}
      }
    } finally {
      this.sharing = false;
    }
    if (!shared) {
      this.copyMarkdown();
    }
  }

  @Builder optionRow(label: string, checked: boolean, id: string, onChange: (value: boolean) => void) {
    Row({ space: Tokens.spaceM }) {
      Text(label)
        .fontSize(Tokens.typeBody)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
        .fontColor(Colors.ink)
        .layoutWeight(1)
        .maxLines(1)
        .textOverflow({ overflow: TextOverflow.Ellipsis })
      Toggle({ type: ToggleType.Switch, isOn: checked })
        .selectedColor(Colors.action)
        .onChange((isOn: boolean) => {
          Haptics.tap();
          onChange(isOn);
        })
        .id(id)
    }
    .width('100%')
    .constraintSize({ minHeight: Tokens.controlBar })
    .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceXs, bottom: Tokens.spaceXs })
    .alignItems(VerticalAlign.Center)
  }

  build() {
    Column({ space: 0 }) {
      PersonaSheetHeader({
        title: tr('export_chat'),
        subtitle: this.chatTitle,
        closeIdentity: 'export.close',
        showDivider: true,
        onClose: () => this.onClose()
      })

      Column({ space: Tokens.spaceM }) {
        Column({ space: 0 }) {
          this.optionRow(tr('export_include_timestamps'), this.includeTimestamps, 'export.toggle.timestamps', (v: boolean) => {
            this.includeTimestamps = v;
          })
          Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.spaceL })
          this.optionRow(tr('export_include_work_steps'), this.includeWorkSteps, 'export.toggle.work', (v: boolean) => {
            this.includeWorkSteps = v;
          })
        }
        .width('100%')
        .backgroundColor(Colors.sheetCard)
        .borderRadius(Tokens.radiusCard)
        .border({
          width: PhoneStyle.surfaceEdgeWidth(0.5),
          color: Colors.hairlineGlass,
          radius: Tokens.radiusCard
        })

        Row({ space: Tokens.spaceS }) {
          Text(tr('export_preview_count').replace('{count}', String(this.exported.messageCount)))
            .fontSize(Tokens.typeCaption)
            .fontColor(Colors.secondary)
            .layoutWeight(1)
          if (this.exported.truncatedCount > 0) {
            Text(tr('export_truncated_badge').replace('{count}', String(this.exported.truncatedCount)))
              .fontSize(Tokens.typeLabel)
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.working)
              .padding({ left: Tokens.spaceS, right: Tokens.spaceS, top: 2, bottom: 2 })
              .backgroundColor(Colors.workingWash)
              .borderRadius(Tokens.radiusCapsule)
          }
        }
        .width('100%')
        .padding({ left: Tokens.spaceXs, right: Tokens.spaceXs })

        Scroll() {
          Text(this.exported.markdown)
            .fontSize(Tokens.typeCode)
            .fontFamily('monospace')
            .lineHeight(Tokens.lineHeightCaption)
            .fontColor(Colors.ink)
            .width('100%')
            .copyOption(CopyOptions.LocalDevice)
            .id('export.preview.text')
        }
        .width('100%')
        .layoutWeight(1)
        .padding(Tokens.spaceL)
        .backgroundColor(Colors.codeSurface)
        .borderRadius(Tokens.radiusCard)
        .border({
          width: PhoneStyle.surfaceEdgeWidth(0.5),
          color: Colors.hairlineGlass,
          radius: Tokens.radiusCard
        })
        .scrollBar(BarState.Auto)

        Row({ space: Tokens.spaceS }) {
          Button({ type: ButtonType.Capsule }) {
            Row({ space: Tokens.spaceXs }) {
              SymbolGlyph(IconActions.symbol('copy'))
                .fontSize('15vp')
                .fontColor([Colors.onAction])
              Text(tr('export_copy_markdown'))
                .fontSize(Tokens.typeSecondary)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
                .fontWeight(FontWeight.Medium)
                .fontColor(Colors.onAction)
                .maxLines(1)
            }
          }
          .layoutWeight(1)
          .height(Tokens.controlBar)
          .backgroundColor(Colors.action)
          .onClick(() => this.copyMarkdown())
          .id('export.copy')

          Button({ type: ButtonType.Capsule }) {
            Row({ space: Tokens.spaceXs }) {
              if (this.sharing) {
                LoadingProgress().width(15).height(15).color(Colors.ink)
              } else {
                SymbolGlyph($r('sys.symbol.share'))
                  .fontSize('15vp')
                  .fontColor([Colors.ink])
              }
              Text(tr('export_save_share'))
                .fontSize(Tokens.typeSecondary)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
                .fontWeight(FontWeight.Medium)
                .fontColor(Colors.ink)
                .maxLines(1)
            }
          }
          .layoutWeight(1)
          .height(Tokens.controlBar)
          .backgroundColor(Colors.control)
          .enabled(!this.sharing)
          .onClick(() => { void this.shareMarkdown(); })
          .id('export.share')
        }
        .width('100%')
      }
      .width('100%')
      .layoutWeight(1)
      .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceL })
    }
    .width('100%')
    .height('100%')
    .backgroundColor(Colors.sheetBackground)
    .id('export.sheet')
  }
}
