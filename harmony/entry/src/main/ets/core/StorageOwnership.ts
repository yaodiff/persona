/** Match the encoded Hub identity, never a substring that could include another paired Hub. */
export function isHubStateKey(key: string, hub: string): boolean {
  if (!hub) return false;
  const scopePrefix = '[' + JSON.stringify(hub) + ',';
  if (['snapshot:', 'view:', 'mutations:', 'attachments:', 'outbox:', 'xiaoyi:'].some(prefix => key.startsWith(prefix + scopePrefix))) return true;
  const targetPrefix = '[' + JSON.stringify(scopePrefix).slice(0, -1);
  return ['draft:', 'draftmedia:', 'transcript:'].some(prefix => key.startsWith(prefix + targetPrefix));
}
