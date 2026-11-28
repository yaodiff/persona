import { AbilityConstant, AgentUIExtensionAbility, UIExtensionContentSession, Want } from '@kit.AbilityKit';
import { Strings } from '../../design/Strings';
import { Wire } from '../../models/Wire';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';

/**
 * Native ArkUI card host rendered inside XiaoYi's chat stream.
 * Respects AgentUIExtensionAbility's no-subwindow constraint (no AlertDialog/subwindows).
 */
export default class PersonaAgentUIAbility extends AgentUIExtensionAbility {
  onCreate(_launchParam: AbilityConstant.LaunchParam): void {
    Strings.bind(this.context.resourceManager);
    XiaoYiRuntimeContext.bind(this.context);
  }

  onSessionCreate(want: Want, session: UIExtensionContentSession): void {
    Strings.bind(this.context.resourceManager);
    XiaoYiRuntimeContext.bind(this.context);
    const params = Wire.record(want.parameters);
    const storage = new LocalStorage();
    storage.setOrCreate('agentCardParamsJson', JSON.stringify(params));
    session.loadContent('pages/AgentCardPage', storage);
  }

  onSessionDestroy(_session: UIExtensionContentSession): void {
    // Card view handles its own teardown in aboutToDisappear.
  }
}
