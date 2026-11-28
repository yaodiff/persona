import { HubAccess } from '../core/HubAccess';
import { common } from '@kit.AbilityKit';
import { asset } from '@kit.AssetStoreKit';
import { util } from '@kit.ArkTS';

const PREFIX: string = 'persona.harmony.device-token.v1:';

function keyFor(hubId: string): Uint8Array {
  if (!hubId || hubId.length > 128 || /[\u0000-\u0020\u007f]/.test(hubId)) throw new Error('invalid_hub_id');
  return new util.TextEncoder().encodeInto(PREFIX + hubId);
}

function assetAlias(hubId: string): asset.AssetMap {
  const query: asset.AssetMap = new Map();
  query.set(asset.Tag.ALIAS, keyFor(hubId));
  return query;
}

function assetErrorCode(error: Error): number {
  const fields = error as Object as Record<string, Object>;
  const code = fields['code'];
  return typeof code === 'number' ? code as number : 0;
}

export class CredentialVault {
  private readonly context: common.Context;
  constructor(context: common.Context) { this.context = context; }

  async get(hubId: string, receiveAccess?: (access: HubAccess | undefined) => void): Promise<string | null> {
    let values: Array<asset.AssetMap>;
    const query = assetAlias(hubId); query.set(asset.Tag.RETURN_TYPE, asset.ReturnType.ALL);
    try { values = await asset.query(query); }
    catch (error) {
      if (assetErrorCode(error as Error) === 24000002) return null;
      throw new Error('storage_unavailable');
    }
    if (values.length === 0) return null;
    const secret = values[0].get(asset.Tag.SECRET);
    if (!(secret instanceof Uint8Array) || secret.length === 0) return null;
    try { return HubAccess.restore(util.TextDecoder.create('utf-8', { fatal: true }).decodeToString(secret), receiveAccess); }
    catch (_) { return null; }
  }

  async put(hubId: string, token: string, access?: HubAccess): Promise<void> {
    if (!this.context) throw new Error('storage_unavailable');
    if (!token || token.length > 8192 || /[\r\n\u0000]/.test(token)) throw new Error('invalid_credential');
    const alias = keyFor(hubId);
    const query = new Map<asset.Tag, asset.Value>();
    query.set(asset.Tag.ALIAS, alias);
    const values: asset.AssetMap = new Map();
    values.set(asset.Tag.SECRET, new util.TextEncoder().encodeInto(access ? access.protect(token) : token));
    try { await asset.update(query, values); }
    catch (error) {
      if (assetErrorCode(error as Error) !== 24000002) throw new Error('storage_unavailable');
      const entry: asset.AssetMap = new Map(values);
      entry.set(asset.Tag.ALIAS, alias);
      entry.set(asset.Tag.ACCESSIBILITY, asset.Accessibility.DEVICE_FIRST_UNLOCKED);
      entry.set(asset.Tag.AUTH_TYPE, asset.AuthType.NONE);
      entry.set(asset.Tag.SYNC_TYPE, asset.SyncType.NEVER);
      try { await asset.add(entry); } catch (_) { throw new Error('storage_unavailable'); }
    }
  }

  async remove(hubId: string): Promise<void> {
    try { await asset.remove(assetAlias(hubId)); }
    catch (error) {
      if (assetErrorCode(error as Error) !== 24000002) throw new Error('storage_unavailable');
    }
  }
}
