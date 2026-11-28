// Shared Persona dimensions; system controls own their internal metrics and material.
export class Tokens {
  static readonly decisionFraction: number = 0.5;
  static readonly composerMinColumns: number = 5;
  static readonly composerNarrowActions: number = 260;
  static readonly divider: number = 0.5;
  static readonly card: number = 20;
  static readonly header: number = 56;
  static readonly compactStatusInset: number = 62;
  static readonly chrome: number = 40;
  static readonly symbol: number = 22;
  static readonly symbolFont: string = String(Tokens.symbol) + 'vp';
  // Persona caps interface text at iOS xxxLarge. UIKit adds six points to its
  // standard interface styles there. Message, document and editor text retain
  // the full accessibility scale.
  static readonly interfaceFontMaxScale: number = 23 / 17;
  static readonly chromeFontMaxScale: number = 40 / 34;
  static interfaceScaleFor(points: number): number {
    const standard = Math.max(11, Math.min(34, points));
    return (standard + 6) / standard;
  }
  // Native text-field placeholders and menu item content accept lengths in vp.
  static interfaceFontVp(points:number,systemScale:number):string {
    return String(points*Math.min(systemScale,Tokens.interfaceScaleFor(points)))+'vp';
  }
  static readonly avatar: number = 40;
  static readonly avatarChoice: number = 56;
  static readonly caption: number = 13;
  static readonly readingMax: number = 680;
  static readonly footer: number = 4;
  static readonly capsule: number = 24;
  static readonly bubble: number = 20;
  static readonly lineHeight: number = 24;
  static readonly inputLines: number = 144;
  static readonly compactPane: number = 340;
  static readonly popupWidth: number = 280;
  // Compact system-like navigation shares a line with back and menu buttons at every text size.
  static readonly navigationTextMaxScale: number = Tokens.interfaceFontMaxScale;
  static readonly headerSegment: number = 192;
  static readonly headerWideSegment: number = 176;
  static readonly minimumInput: number = 136;
  static readonly hairline: number = 1;
  static readonly fine: number = 4;
  static readonly gap: number = 8;
  static readonly inset: number = 12;
  static readonly page: number = 16;
  static readonly section: number = 24;
  static readonly body: number = 17;
  static readonly meta: number = 14;
  static readonly title: number = 20;
  // iOS Theme.Font / Theme.Radius / Theme.Space canonical scales
  static readonly typeDisplay: number = 28;
  static readonly typeTitle: number = 22;
  static readonly typeHeading: number = 17;
  static readonly typeBody: number = 17;
  static readonly typeSecondary: number = 15;
  static readonly typeCaption: number = 13;
  static readonly typeSectionHeader: number = 13;
  static readonly typeLabel: number = 12;
  static readonly typeCode: number = 13;
  static readonly lineHeightDisplay: number = 34;
  static readonly lineHeightTitle: number = 28;
  static readonly lineHeightHeading: number = 22;
  static readonly lineHeightBody: number = 24;
  static readonly lineHeightSecondary: number = 21;
  static readonly lineHeightCaption: number = 18;
  static readonly lineHeightLabel: number = 16;
  static readonly radiusBadge: number = 6;
  static readonly radiusControl: number = 10;
  static readonly radiusCard: number = 14;
  static readonly radiusSheetCard: number = 20;
  static readonly radiusBubble: number = 20;
  static readonly radiusPanel: number = 22;
  static readonly radiusCapsule: number = 999;
  static readonly spaceXxs: number = 2;
  static readonly spaceXs: number = 4;
  static readonly spaceSm: number = 6;
  static readonly spaceS: number = 8;
  static readonly spaceM: number = 12;
  static readonly spaceL: number = 16;
  static readonly spaceXl: number = 24;
  static readonly controlCompact: number = 36;
  static readonly controlBar: number = 44;
  static readonly controlTouch: number = 48;
  static readonly markdownHeading1: number = 32;
  static readonly markdownHeading2: number = 28;
  static readonly markdownHeading3: number = 24;
  static readonly markdownHeading4: number = 20;
  static readonly markdownHeading5: number = 20;
  static readonly markdownTableCell: number = 160;
  static readonly readerPartial: string = '60%';
  static readonly readerFull: Dimension = '100%';
  static readonly touch: number = 48;
  static readonly headerPeerIcons: number = Tokens.touch * 2 + Tokens.gap;
  static readonly qr: number = 240;
  static readonly attachmentPreview: number = 80;
  static readonly attachmentItem: number = 112;
  static readonly input: number = 56;
  static readonly inputExpanded: number = 160;
  static readonly tabHeight: number = 56;
  static readonly tabMargin: number = 12;
  static readonly listTail: number = Tokens.footer;
  static readonly master: number = 320;
  static readonly masterMin: number = 280;
  static readonly masterMax: number = 440;
  static readonly collapsed: number = 64;
  static readonly detailMin: number = 320;
  static readonly codeLimit: number = 200000;
  static readonly maxMessage: number = 100000;
}
