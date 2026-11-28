import { XiaoYiStatusSummary } from '../../../src/main/ets/core/XiaoYiActions';
import { HeadlessApproveResult, HeadlessPushResult, HeadlessSendTaskResult } from '../../../src/main/ets/xiaoyi/HeadlessHubService';
import { XiaoYiRuntimeContext } from '../../../src/main/ets/xiaoyi/XiaoYiRuntimeContext';

/**
 * HarmonyOS 7 APP_SKILL entry points for Persona Workspace, Agent dispatch, and unlock-gated approvals.
 */
export class WorkspaceSkill {
  static async checkStatus(): Promise<XiaoYiStatusSummary> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      return await hub.checkStatus();
    } finally {
      await hub.close();
    }
  }

  static async sendAgentTask(
    prompt: string,
    machineName: string = '',
    sessionName: string = '',
    newSession: boolean = false
  ): Promise<HeadlessSendTaskResult> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      return await hub.sendAgentTask(prompt, machineName, sessionName, newSession);
    } finally {
      await hub.close();
    }
  }

  static async approveFirstPending(): Promise<HeadlessApproveResult> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      return await hub.approveFirstPending();
    } finally {
      await hub.close();
    }
  }

  static async pushItem(title: string, body: string, app: string = '', link: string = ''): Promise<HeadlessPushResult> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      return await hub.pushSubscriptionItem(title, body, app, link);
    } finally {
      await hub.close();
    }
  }
}
