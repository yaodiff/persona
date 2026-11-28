import { snippet } from './items.js'

const sentinel = 'BOTMUX_NOTHING_TO_SEND'

// Only a marker-only reply defers to earlier words. Keep the original formatting
// for runtimes that already supply a display-ready preview.
export function assistantPreviewText(text) {
  if (!text.includes(sentinel)) return text
  const visible = text.split('\n').filter(line => line.trim() !== sentinel).join('\n')
  return visible.trim() ? visible : undefined
}

export function assistantPreview(text) {
  if (typeof text !== 'string') return snippet(text)
  const visible = assistantPreviewText(text)
  return visible === undefined ? undefined : snippet(visible)
}
