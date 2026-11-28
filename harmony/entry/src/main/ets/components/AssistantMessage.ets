import { ClientModel } from '../models/ClientModel';
import { AssistantFeedModel } from '../models/AssistantFeed';
import { parseAssistantViewBlocks } from '../models/AssistantView';
import { assistantCardResource } from '../models/AssistantCardLinks';
import { ChatMessage, ResourceRow, Wire } from '../models/Wire';
import { AssistantMessagePart, assistantMessageParts } from '../core/AssistantCardText';
import { AssistantViewCard } from './AssistantViewCard';
import { MarkdownText } from './MarkdownText';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct AssistantMessage {
  @Require @Param model: ClientModel;
  @Require @Param feed: AssistantFeedModel;
  @Require @Param message: ChatMessage;
  @Param active: boolean = false;
  @Param streaming: boolean = false;
  @Event onLink: (address: string) => void = () => {};
  @Event onOpenResource: (row: ResourceRow) => void = () => {};
  @Local parts: AssistantMessagePart[] = [];
  @Local item: Record<string, Object> = {};
  private sourceIdentity: string = '';

  aboutToAppear(): void { this.syncSource(); }
  @Monitor('message', 'message.text', 'model.profile', 'model.selected') sourceChanged(): void { this.syncSource(); }
  private syncSource(): void {
    const target = this.model.target(), id = this.message.itemId;
    const identity = JSON.stringify([target, id, this.message.ownerTarget, this.message.text]);
    if (this.sourceIdentity === identity) return;
    this.sourceIdentity = identity;
    const trusted = this.model.selected.kind === 'conversation' && this.message.role === 'assistant' && this.message.ownerTarget === target && !!id;
    this.item = Wire.payload(['id', 'item_id', 'kind', 'role', 'text', 'conversation', 'manager'],
      [id, id, 'text', 'assistant', this.message.text, this.model.selected.id, this.model.profile.manager]);
    this.parts = trusted ? assistantMessageParts(this.message.text, parseAssistantViewBlocks(id, this.message.text), tr('assistant_view_unavailable')) :
      [new AssistantMessagePart('text:0', this.message.text)];
  }
  private openResource(address: string): void {
    if (!this.active || this.message.ownerTarget !== this.model.target()) return;
    const row = assistantCardResource(address, this.model.profile.id, this.model.profile.manager, this.model.sessions.concat(this.model.conversations));
    if (!row) { this.model.error = 'assistant_view_invalid_target'; return; }
    if (!row.title) row.title = tr(row.kind === 'todo' ? 'todos' : row.kind === 'knowledge' ? 'knowledge' : row.kind === 'conversation' ? 'main_chat' : 'agents');
    this.onOpenResource(row);
  }
  build() {
    Column({ space: Tokens.gap }) {
      ForEach(this.parts, (part: AssistantMessagePart, index: number) => {
        if (part.cardKey) AssistantViewCard({ model: this.model, feed: this.feed, item: this.item, cardKey: part.cardKey,
          active: this.active && this.model.connected, onLink: (address: string) => this.onLink(address), onOpenResource: (address: string) => this.openResource(address) })
        else if (part.text) MarkdownText({ text: part.text, chatReply: true, streaming: this.streaming && index === this.parts.length - 1, onLink: (address: string) => this.onLink(address) })
      }, (part: AssistantMessagePart) => part.id)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
}
