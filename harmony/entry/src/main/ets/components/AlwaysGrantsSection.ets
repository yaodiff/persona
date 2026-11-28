import { Colors } from '../design/Colors';
import { ButtonStyles } from '../design/Controls';
import { PersonaSectionLabel, PersonaStatusPill } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr, trError } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import {
  AlwaysGrant,
  alwaysGrantsPath,
  parseAlwaysGrantsResponse,
  parseAlwaysRevokeResponse,
  revokeAlwaysGrantPath
} from '../core/StandingAuth';
import { ClientModel } from '../models/ClientModel';
import { toastCenter } from '../models/ToastCenter';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

const notResetConversations: string[] = [];

function restartScopeKey(scope: string, conversationId: string): string {
  return JSON.stringify([scope, conversationId]);
}

@ComponentV2
export struct AlwaysGrantsSection {
  @Require @Param model: ClientModel;
  @Require @Param conversationId: string;
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private grants: AlwaysGrant[] | undefined = undefined;
  @Local private unavailable: boolean = false;
  @Local private error: string = '';
  @Local private loading: boolean = false;
  @Local private revokingHash: string = '';
  @Local private restartNeeded: boolean = false;
  private loadRevision: number = 0;
  private pendingConfirmKey: string = '';

  aboutToAppear(): void {
    void this.loadGrants();
  }

  @Monitor('model', 'model.connected', 'model.profile', 'conversationId')
  onContextChanged(): void {
    void this.loadGrants();
  }

  private async loadGrants(): Promise<void> {
    const owner = this.model;
    const convId = (this.conversationId || '').trim();
    const rev = ++this.loadRevision;
    this.grants = undefined;
    this.revokingHash = '';
    this.pendingConfirmKey = '';
    this.unavailable = false;
    this.error = '';
    if (!owner.connected || !convId) {
      this.loading = false;
      this.restartNeeded = false;
      return;
    }
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    this.restartNeeded = notResetConversations.includes(restartScopeKey(scope, convId));
    this.loading = true;
    try {
      const res = await owner.repo().call(alwaysGrantsPath(convId));
      if (rev !== this.loadRevision || owner !== this.model || !owner.owns(generation, scope) || convId !== this.conversationId) {
        return;
      }
      this.grants = parseAlwaysGrantsResponse(res);
      this.error = '';
    } catch (err) {
      if (rev !== this.loadRevision || owner !== this.model || !owner.owns(generation, scope) || convId !== this.conversationId) {
        return;
      }
      const errRecord = err as Object as Record<string, Object>;
      const status = typeof errRecord['status'] === 'number' ? Number(errRecord['status']) : 0;
      if (status === 403 || status === 404) {
        this.unavailable = true;
        this.grants = undefined;
        this.error = '';
      } else {
        this.grants = undefined;
        this.error = trError((err as Error).message || 'failed');
      }
    } finally {
      if (rev === this.loadRevision && owner === this.model && owner.owns(generation, scope)) {
        this.loading = false;
      }
    }
  }

  private confirmRevoke(grant: AlwaysGrant): void {
    if (this.revokingHash) return;
    const owner = this.model;
    const convId = (this.conversationId || '').trim();
    if (!convId) return;
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    const confirmKey = JSON.stringify([scope, generation, convId, grant.key_hash]);
    this.pendingConfirmKey = confirmKey;
    const name = grant.tool_name || tr('always_grant_unnamed');
    this.getUIContext().showAlertDialog({
      title: tr('always_grant_revoke_title'),
      message: name + '\n' + tr('always_grant_revoke_message'),
      autoCancel: true,
      alignment: DialogAlignment.Center,
      primaryButton: {
        value: tr('cancel'),
        action: () => {
          if (this.pendingConfirmKey === confirmKey) this.pendingConfirmKey = '';
        }
      },
      secondaryButton: {
        value: tr('standing_revoke'),
        fontColor: Colors.danger,
        action: () => {
          if (
            this.pendingConfirmKey !== confirmKey ||
            owner !== this.model ||
            !owner.owns(generation, scope) ||
            convId !== (this.conversationId || '').trim()
          ) {
            return;
          }
          this.pendingConfirmKey = '';
          void this.revoke(grant, convId);
        }
      }
    });
  }

  private async revoke(grant: AlwaysGrant, expectedConvId: string): Promise<void> {
    const owner = this.model;
    const convId = (this.conversationId || '').trim();
    if (!owner.connected || !convId || convId !== expectedConvId || !grant.key_hash || this.revokingHash) return;
    const path = revokeAlwaysGrantPath(convId, grant.key_hash);
    if (owner.operationBlocked(path)) {
      toastCenter.error(tr('pending_action'));
      return;
    }
    const generation = owner.connectionGeneration();
    const scope = owner.scope();
    this.revokingHash = grant.key_hash;
    this.error = '';
    try {
      const res = await owner.repo().call(
        path,
        'DELETE',
        {},
        '',
        () => owner === this.model && owner.owns(generation, scope) && convId === (this.conversationId || '').trim()
      );
      if (owner !== this.model || !owner.owns(generation, scope) || convId !== (this.conversationId || '').trim()) return;
      const outcome = parseAlwaysRevokeResponse(res);
      this.grants = (this.grants || []).filter((item: AlwaysGrant) => item.key_hash !== grant.key_hash);
      Haptics.success();
      toastCenter.success(tr('always_grant_revoked_toast'));
      if (outcome.restart_required) {
        const key = restartScopeKey(scope, convId);
        if (!notResetConversations.includes(key)) {
          notResetConversations.push(key);
        }
        this.restartNeeded = true;
      }
    } catch (err) {
      if (owner === this.model && owner.owns(generation, scope) && convId === (this.conversationId || '').trim()) {
        Haptics.error();
        const message = trError((err as Error).message || 'failed');
        this.error = message;
        toastCenter.error(message);
      }
    } finally {
      if (this.revokingHash === grant.key_hash) {
        this.revokingHash = '';
      }
    }
  }

  private formattedCreatedAt(raw: string): string {
    if (!raw) return '';
    const ms = Date.parse(raw);
    if (!Number.isFinite(ms)) return '';
    try {
      return new Date(ms).toLocaleDateString();
    } catch (_) {
      return '';
    }
  }

  build() {
    if (!this.unavailable) {
      Column({ space: Tokens.spaceSm }) {
        PersonaSectionLabel({
          text: tr('always_grants_heading'),
          trailing: this.grants && this.grants.length > 0 ? String(this.grants.length) : ''
        })

        if (this.restartNeeded) {
          Row({ space: Tokens.spaceS }) {
            SymbolGlyph($r('sys.symbol.exclamationmark_triangle_fill'))
              .fontSize('16vp')
              .fontColor([Colors.danger])
              .flexShrink(0)
            Text(tr('always_grants_restart_needed'))
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontColor(Colors.danger)
              .layoutWeight(1)
          }
          .width('100%')
          .padding(Tokens.spaceM)
          .backgroundColor(Colors.dangerWash)
          .borderRadius(Tokens.radiusCard)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: Colors.danger, radius: Tokens.radiusCard })
          .id('always.restart.banner')
        }

        Column({ space: 0 }) {
          if (this.loading && !this.grants) {
            Row() {
              LoadingProgress().width(24).height(24).color(Colors.link)
            }
            .width('100%')
            .constraintSize({ minHeight: Tokens.controlBar })
            .justifyContent(FlexAlign.Center)
            .alignItems(VerticalAlign.Center)
            .padding(Tokens.spaceM)
          } else if (this.error && !this.grants) {
            Column({ space: Tokens.spaceS }) {
              Text(this.error)
                .fontSize(Tokens.typeSecondary)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
                .fontColor(Colors.danger)
              Button(tr('retry'), { type: ButtonType.Capsule })
                .constraintSize({ minWidth: 88, minHeight: Tokens.controlBar })
                .fontSize(Tokens.typeSecondary)
                .fontColor(Colors.link)
                .backgroundColor(Colors.needsWash)
                .onClick(() => { void this.loadGrants(); })
                .id('always.retry')
            }
            .width('100%')
            .padding(Tokens.spaceL)
            .alignItems(HorizontalAlign.Center)
          } else if (!this.grants || this.grants.length === 0) {
            Text(tr('always_grants_empty'))
              .fontSize(Tokens.typeSecondary)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
              .fontColor(Colors.secondary)
              .width('100%')
              .padding(Tokens.spaceL)
              .id('always.empty')
          } else {
            ForEach(this.grants, (grant: AlwaysGrant, index: number) => {
              if (index > 0) {
                Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.spaceL })
              }
              Row({ space: Tokens.spaceM }) {
                Column({ space: Tokens.spaceXxs }) {
                  Text(grant.tool_name || tr('always_grant_unnamed'))
                    .fontSize(Tokens.typeBody)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
                    .fontWeight(FontWeight.Medium)
                    .fontColor(Colors.ink)
                    .maxLines(1)
                    .textOverflow({ overflow: TextOverflow.Ellipsis })
                  if (this.formattedCreatedAt(grant.created_at)) {
                    Text(this.formattedCreatedAt(grant.created_at))
                      .fontSize(Tokens.typeCaption)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                      .fontColor(Colors.secondary)
                      .maxLines(1)
                  }
                }
                .layoutWeight(1)
                .alignItems(HorizontalAlign.Start)

                Button({ type: ButtonType.Capsule }) {
                  Row({ space: Tokens.spaceXs }) {
                    if (this.revokingHash === grant.key_hash) {
                      LoadingProgress().width(14).height(14).color(Colors.danger)
                    }
                    Text(tr('standing_revoke'))
                      .fontSize(Tokens.typeCaption)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                      .fontWeight(FontWeight.Medium)
                      .fontColor(Colors.danger)
                      .maxLines(1)
                  }
                }
                .height(Tokens.controlBar)
                .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
                .backgroundColor(Colors.dangerWash)
                .responseRegion(ButtonStyles.touchRegion(Tokens.controlBar))
                .enabled(!this.revokingHash)
                .onClick(() => this.confirmRevoke(grant))
                .id('always.revoke.' + grant.key_hash)
              }
              .width('100%')
              .constraintSize({ minHeight: Tokens.controlTouch })
              .padding({ left: Tokens.spaceL, right: Tokens.spaceM, top: Tokens.spaceS, bottom: Tokens.spaceS })
              .alignItems(VerticalAlign.Center)
              .id('always.row.' + grant.key_hash)
            }, (grant: AlwaysGrant) => grant.key_hash + ':' + grant.tool_name)

            if (this.error) {
              Text(this.error)
                .fontSize(Tokens.typeCaption)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                .fontColor(Colors.danger)
                .width('100%')
                .padding({ left: Tokens.spaceL, right: Tokens.spaceL, bottom: Tokens.spaceM })
            }
          }
        }
        .width('100%')
        .backgroundColor(Colors.sheetCard)
        .borderRadius(PhoneStyle.cardRadius)
        .border({
          width: PhoneStyle.surfaceEdgeWidth(0.5),
          color: PhoneStyle.cardBorder(this.env.dark),
          radius: PhoneStyle.cardRadius
        })
        .shadow(PhoneStyle.cardElevation(this.env.dark))
        .clip(true)

        Text(tr('always_grants_scope_footer'))
          .fontSize(Tokens.typeCaption)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
          .fontColor(Colors.secondary)
          .width('100%')
          .padding({ left: Tokens.spaceXs, right: Tokens.spaceXs })
      }
      .width('100%')
      .alignItems(HorizontalAlign.Start)
      .id('always.grants.section')
    }
  }
}
