import { Tokens } from './Tokens';
import { Colors } from './Colors';

type PersonaButtonRole = 'primary' | 'secondary' | 'quiet' | 'chrome' | 'icon' | 'iconPrimary';

/** An ArkUI attribute modifier keeps native Button behavior and one set of style roles across modules. */
class PersonaButtonModifier implements AttributeModifier<ButtonAttribute> {
  private role: PersonaButtonRole;
  constructor(role: PersonaButtonRole) { this.role = role; }
  applyNormalAttribute(button: ButtonAttribute): void {
    if (this.role === 'icon' || this.role === 'iconPrimary') {
      const primary = this.role === 'iconPrimary';
      button.buttonStyle(primary ? ButtonStyleMode.EMPHASIZED : ButtonStyleMode.TEXTUAL)
        .backgroundColor(primary ? Colors.action : Color.Transparent).fontColor(primary ? Colors.onAction : Colors.link)
        .width(Tokens.touch).height(Tokens.touch).constraintSize({ minWidth: Tokens.touch, minHeight: Tokens.touch, maxWidth: Tokens.touch, maxHeight: Tokens.touch })
        .padding(0).borderRadius(Tokens.capsule);
      return;
    }
    if (this.role === 'chrome') {
      button.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).fontColor(Colors.ink)
        .width(Tokens.chrome).height(Tokens.chrome).padding(0).borderRadius(Tokens.card)
        .responseRegion({ x: -Tokens.fine, y: -Tokens.fine, width: Tokens.touch, height: Tokens.touch });
      return;
    }
    button.constraintSize({ minHeight: Tokens.touch }).fontSize(Tokens.body)
      .labelStyle({ maxLines: 1, overflow: TextOverflow.Ellipsis })
      .padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: Tokens.gap });
    if (this.role === 'primary') button.buttonStyle(ButtonStyleMode.EMPHASIZED).backgroundColor(Colors.action)
      .fontColor(Colors.onAction).fontWeight(FontWeight.Medium).borderRadius(Tokens.capsule)
      .shadow({ radius: 10, color: '#24007AFF', offsetY: 3 });
    else if (this.role === 'secondary') button.buttonStyle(ButtonStyleMode.NORMAL).backgroundColor(Colors.control)
      .fontColor(Colors.link).fontWeight(FontWeight.Medium).borderRadius(Tokens.capsule)
      .border({ width: 0.5, color: Colors.divider, radius: Tokens.capsule });
    else button.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).fontColor(Colors.link)
      .padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.gap, bottom: Tokens.gap });
  }
}

export class ButtonStyles {
  static touchRegion(side: number): Rectangle {
    const hit = Math.max(44, side), offset = (side - hit) / 2;
    return { x: offset, y: offset, width: hit, height: hit };
  }
  static readonly icon: AttributeModifier<ButtonAttribute> = new PersonaButtonModifier('icon');
  static readonly iconPrimary: AttributeModifier<ButtonAttribute> = new PersonaButtonModifier('iconPrimary');
  static readonly primary: AttributeModifier<ButtonAttribute> = new PersonaButtonModifier('primary');
  static readonly secondary: AttributeModifier<ButtonAttribute> = new PersonaButtonModifier('secondary');
  static readonly quiet: AttributeModifier<ButtonAttribute> = new PersonaButtonModifier('quiet');
  static readonly chrome: AttributeModifier<ButtonAttribute> = new PersonaButtonModifier('chrome');
}

/** Native Select remains the picker; compact text treatment avoids another filled action slab. */
class PersonaSelectModifier implements AttributeModifier<SelectAttribute> {
  applyNormalAttribute(select: SelectAttribute): void {
    select.controlSize(ControlSize.SMALL).constraintSize({ minHeight: Tokens.touch }).font({ size: Tokens.body, weight: FontWeight.Normal })
      .fontColor(Colors.secondary).backgroundColor(Color.Transparent);
  }
}
export class SelectStyles {
  static readonly inline: AttributeModifier<SelectAttribute> = new PersonaSelectModifier();
}
