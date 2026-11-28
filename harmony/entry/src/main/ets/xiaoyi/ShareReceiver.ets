import { Want } from '@kit.AbilityKit';
import { systemShare } from '@kit.ShareKit';
import { ShareParseResult, ShareRecordInput, parseShareRecords } from '../core/SharePayload';
import { Wire } from '../models/Wire';

export const SEND_DATA_ACTION: string = 'ohos.want.action.sendData';

/**
 * Extract shared content from an incoming HarmonyOS ShareKit / sendData Want.
 * Falls back cleanly to Want parameters/URI when ShareKit is unavailable.
 */
export class ShareReceiver {
  static isShareWant(want: Want): boolean {
    return want.action === SEND_DATA_ACTION;
  }

  static async parseWant(want: Want): Promise<ShareParseResult> {
    const records: ShareRecordInput[] = [];
    if (canIUse('SystemCapability.Collaboration.SystemShare')) {
      try {
        const sharedData = await systemShare.getSharedData(want);
        const rawRecords = sharedData.getRecords();
        for (let i = 0; i < rawRecords.length; i++) {
          const item = rawRecords[i];
          records.push({
            utd: item.utd,
            content: item.content,
            uri: item.uri,
            title: item.title,
            description: item.description
          });
        }
      } catch (_) {
        // Fallback to raw Want parameters below.
      }
    }
    return parseShareRecords(records, want.uri || '', Wire.record(want.parameters));
  }
}
