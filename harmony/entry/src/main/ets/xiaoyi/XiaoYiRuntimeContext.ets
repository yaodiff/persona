import { common } from '@kit.AbilityKit';
import { formatXiaoYiError } from '../core/XiaoYiActions';
import { tr } from '../design/Strings';
import { HeadlessHubFailure, HeadlessHubService } from './HeadlessHubService';

/**
 * Process-wide context bridge allowing InsightIntent executors and AppIntentEntity queries
 * (which do not receive a common.Context directly) to access Asset Store and LocalStore safely.
 */
export class XiaoYiRuntimeContext {
  private static appContext: common.Context | undefined = undefined;

  static bind(context: common.Context): void {
    try {
      XiaoYiRuntimeContext.appContext = context.getApplicationContext();
    } catch (_) {
      XiaoYiRuntimeContext.appContext = context;
    }
  }

  static get(): common.Context | undefined {
    return XiaoYiRuntimeContext.appContext;
  }

  static openHub(overrideContext?: common.Context): HeadlessHubService {
    const ctx = overrideContext || XiaoYiRuntimeContext.appContext;
    if (!ctx) {
      throw new HeadlessHubFailure('xiaoyi_not_paired');
    }
    return new HeadlessHubService(ctx);
  }

  static formatError(error: Error): string {
    const code = error instanceof HeadlessHubFailure ? error.code : (error.message || 'xiaoyi_failed');
    const host = error instanceof HeadlessHubFailure ? error.host : '';
    const target = error instanceof HeadlessHubFailure ? error.target : '';
    const descriptor = formatXiaoYiError(code, host, target);
    let text = tr(descriptor.key);
    if (text === descriptor.key) {
      text = fallbackErrorText(descriptor.key);
    }
    if (host) text = text.replace('{host}', host);
    if (target) text = text.replace('{target}', target);
    return text;
  }
}

function fallbackErrorText(key: string): string {
  switch (key) {
    case 'xiaoyi_not_paired': return 'Not paired yet. Open Persona to connect your Hub.';
    case 'xiaoyi_auth_expired': return 'The pairing has expired. Open Persona to pair again.';
    case 'xiaoyi_already_handled': return 'This request was already handled.';
    case 'xiaoyi_ssh_open_app': return 'This Hub connects over an SSH tunnel. Open Persona to run this action.';
    case 'xiaoyi_unlock_required': return 'Unlock your phone before approving requests.';
    case 'xiaoyi_offline': return 'Cannot reach the Hub right now. Check your network connection.';
    case 'xiaoyi_machine_offline': return '{target} is offline. Check that your computer is awake and Persona Link is running.';
    case 'xiaoyi_machine_not_found': return 'Could not find a computer named {target}.';
    case 'xiaoyi_no_online_machine': return 'No computer is online right now.';
    case 'xiaoyi_no_live_session': return 'No active session found on {target}. Open Persona to start one.';
    case 'xiaoyi_hub_update_required': return 'Please update your Persona Hub to use this action.';
    case 'xiaoyi_need_app_for_new_session': return 'Start a session on {target} in Persona once so XiaoYi knows your folder and launcher.';
    case 'xiaoyi_nothing_pending': return 'Nothing needs your approval right now.';
    case 'xiaoyi_needs_screen': return 'Open Persona to answer this request.';
    case 'xiaoyi_nothing_to_send': return 'Nothing to send.';
    case 'xiaoyi_water_not_enabled': return 'First add Water tracking in Health inside Persona.';
    case 'xiaoyi_recording_module_off': return 'Meeting recording is off on this hub.';
    case 'xiaoyi_timeout': return 'The Hub is taking longer than expected. Open Persona to view progress.';
    case 'xiaoyi_rate_limited': return 'Too many items pushed recently. Try again later.';
    default: return 'Unable to complete the request. Open Persona to check details.';
  }
}
