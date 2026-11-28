import { Colors } from '../design/Colors';
import { Symbols } from '../design/Symbols';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import {
  isRuleSuggestionOffered,
  parseRuleSuggestion,
  parseRuleSuggestionMade,
  ruleScopePlace,
  RuleSuggestion,
  ruleSuggestionAlwaysPath,
  RuleSuggestionAnswer,
  RuleSuggestionLineState,
  ruleSuggestionMadeKind,
  ruleSuggestionNotNowPath,
  sharedRuleSuggestionTracker
} from '../core/RuleSuggestions';
import { ClientModel } from '../models/ClientModel';
import { toastCenter } from '../models/ToastCenter';
import { Haptics } from '../platform/Haptics';
import { HttpFailure } from '../platform/HubTransport';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

@ComponentV2
export struct RuleSuggestionBar {
  @Require @Param model: ClientModel;
  @Param rawSuggestion: Object | undefined = undefined;
  @Require @Param cardKey: string;
  @Param manager: string = '';
  @Event onSettled: (suggestionId: string) => void = (_id: string) => {};

  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private revision: number = 0;

  private suggestion(): RuleSuggestion | null {
    return parseRuleSuggestion(this.rawSuggestion);
  }

  private scopeKey(): string {
    return this.model.scope();
  }

  private lineState(): RuleSuggestionLineState {
    const _rev = this.revision;
    void _rev;
    return sharedRuleSuggestionTracker.lineState(this.suggestion(), this.cardKey, this.scopeKey());
  }

  private detailText(s: RuleSuggestion): string {
    const kind = ruleSuggestionMadeKind(s);
    if (kind === 'task_trust') {
      const place = ruleScopePlace(s.scope);
      if (s.scope.directory && place) {
        return tr('rule_suggestion_creates_task_trust_scoped')
          .replace('{directory}', s.scope.directory)
          .replace('{machine}', place);
      }
      return tr('rule_suggestion_creates_task_trust');
    }
    if (kind === 'standing_authorization') {
      const place = ruleScopePlace(s.scope);
      const action = s.scope.action || 'action';
      return tr('rule_suggestion_creates_standing')
        .replace('{action}', action)
        .replace('{directory}', s.scope.directory || '/')
        .replace('{machine}', place || '');
    }
    if (kind === 'setting') {
      return tr('rule_suggestion_creates_setting');
    }
    return '';
  }

  private madeText(creates: string, setting: string, asked: RuleSuggestion): string {
    const resolved = creates || asked.creates;
    if (resolved === 'tasks.trust') return tr('rule_suggestion_made_task_trust');
    if (resolved === 'standing_authorization') return tr('rule_suggestion_made_standing');
    const name = setting || asked.setting;
    if (!name) return tr('rule_suggestion_made_none');
    return tr('rule_suggestion_made_setting').replace('{setting}', name);
  }

  private httpStatus(err: Object): number {
    if (err instanceof HttpFailure) return err.status;
    const rec = err as Record<string, Object>;
    return typeof rec['status'] === 'number' ? (rec['status'] as number) : 0;
  }

  private async submit(answer: RuleSuggestionAnswer, s: RuleSuggestion): Promise<void> {
    if (!isRuleSuggestionOffered(s)) return;
    const owner = this.model;
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    if ((!owner.connected && !owner.demo) || sharedRuleSuggestionTracker.isSending(s.id, scope) ||
      sharedRuleSuggestionTracker.isQuiet(s.id, scope)) {
      return;
    }

    const mgr = this.manager && this.manager !== owner.profile.manager ? this.manager : undefined;
    sharedRuleSuggestionTracker.markSending(s.id, scope, answer);
    this.revision++;
    Haptics.tap();
    try {
      const path = answer === 'always'
        ? ruleSuggestionAlwaysPath(s.id, mgr)
        : ruleSuggestionNotNowPath(s.id, mgr);
      const raw = await owner.repo().call(path, 'POST', {});
      if (!owner.owns(generation, scope)) return;
      if (answer === 'always') {
        const made = parseRuleSuggestionMade(raw);
        sharedRuleSuggestionTracker.markMade(this.cardKey, scope, s.id, this.madeText(made.creates, made.setting, s));
        Haptics.success();
      }
      sharedRuleSuggestionTracker.markSettled(s.id, scope);
      this.onSettled(s.id);
    } catch (err) {
      if (!owner.owns(generation, scope)) return;
      const status = this.httpStatus(err as Object);
      if (status === 404) {
        sharedRuleSuggestionTracker.markSettled(s.id, scope);
        this.onSettled(s.id);
      } else if (status === 409) {
        Haptics.error();
        toastCenter.error(tr('rule_suggestion_conflict'));
      } else {
        Haptics.error();
        toastCenter.error(tr('rule_suggestion_failed'));
      }
    } finally {
      sharedRuleSuggestionTracker.clearSending(s.id, scope);
      this.revision++;
    }
  }

  build() {
    if (this.lineState().status === 'offered' || this.lineState().status === 'sending') {
      Column({ space: Tokens.spaceXs }) {
        Row({ space: Tokens.spaceS }) {
          SymbolGlyph($r('sys.symbol.checkmark_shield'))
            .fontSize('14vp')
            .fontColor([Colors.secondary])
            .margin({ top: 2 })
          Column({ space: Tokens.spaceXxs }) {
            Text(this.lineState().suggestion!.text)
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontColor(Colors.secondary)
            if (this.detailText(this.lineState().suggestion!)) {
              Text(this.detailText(this.lineState().suggestion!))
                .fontSize(Tokens.typeLabel)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                .fontColor(Colors.secondary)
            }
          }
          .layoutWeight(1)
          .alignItems(HorizontalAlign.Start)
        }
        .width('100%')
        .alignItems(VerticalAlign.Top)

        Row({ space: Tokens.spaceS }) {
          Blank().layoutWeight(1)
          Button(tr('rule_suggestion_not_now'), { type: ButtonType.Capsule })
            .constraintSize({ minHeight: Tokens.controlBar })
            .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontWeight(FontWeight.Medium)
            .backgroundColor(Colors.control)
            .fontColor(Colors.ink)
            .enabled(this.lineState().status === 'offered')
            .onClick(() => {
              this.submit('not_now', this.lineState().suggestion!).catch(() => undefined);
            })
            .id('rule.suggestion.not_now')

          Button(tr('rule_suggestion_always'), { type: ButtonType.Capsule })
            .constraintSize({ minHeight: Tokens.controlBar })
            .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontWeight(FontWeight.Medium)
            .backgroundColor(Colors.accentWell)
            .fontColor(Colors.link)
            .enabled(this.lineState().status === 'offered')
            .onClick(() => {
              this.submit('always', this.lineState().suggestion!).catch(() => undefined);
            })
            .id('rule.suggestion.always')
        }
        .width('100%')
      }
      .width('100%')
      .padding({ top: Tokens.spaceS })
      .id('rule.suggestion')
    } else if (this.lineState().status === 'made') {
      Row({ space: Tokens.spaceS }) {
        SymbolGlyph(Symbols.check)
          .fontSize('14vp')
          .fontColor([Colors.positive])
        Text(this.lineState().madeText || '')
          .fontSize(Tokens.typeCaption)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
          .fontColor(Colors.secondary)
          .layoutWeight(1)
      }
      .width('100%')
      .padding({ top: Tokens.spaceS })
      .alignItems(VerticalAlign.Top)
      .id('rule.suggestion.made')
    }
  }
}
