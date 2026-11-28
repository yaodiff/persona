import {
  HeadlessAskResult,
  HeadlessKnowledgeHit,
  HeadlessRecordingStartResult,
  HeadlessTodoResult,
  HeadlessWaterResult
} from '../../../src/main/ets/xiaoyi/HeadlessHubService';
import { XiaoYiRuntimeContext } from '../../../src/main/ets/xiaoyi/XiaoYiRuntimeContext';

/**
 * HarmonyOS 7 APP_SKILL entry points for everyday Persona Assistant capabilities.
 */
export class AssistantSkill {
  static async askPersona(question: string): Promise<HeadlessAskResult> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      return await hub.askPersona(question);
    } finally {
      await hub.close();
    }
  }

  static async addTodo(title: string, notes: string = ''): Promise<HeadlessTodoResult> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      return await hub.addTodo(title, notes);
    } finally {
      await hub.close();
    }
  }

  static async searchKnowledge(query: string): Promise<HeadlessKnowledgeHit[]> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      return await hub.searchKnowledge(query);
    } finally {
      await hub.close();
    }
  }

  static async logWater(amountMl?: number): Promise<HeadlessWaterResult> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      return await hub.logWater(amountMl);
    } finally {
      await hub.close();
    }
  }

  /** Fail-closed meeting recording preflight: throws `xiaoyi_recording_module_off` when the Hub's recording module is off. */
  static async startRecording(): Promise<HeadlessRecordingStartResult> {
    const hub = XiaoYiRuntimeContext.openHub();
    try {
      return await hub.prepareRecordingStart();
    } finally {
      await hub.close();
    }
  }
}
