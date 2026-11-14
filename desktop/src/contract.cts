export function notificationRoute(value: string): string | undefined {
  if (!value.startsWith("/") || value.startsWith("//") || value.includes("\\") || /[\u0000-\u001f\u007f]/.test(value)) return undefined;
  const url = new URL(value, "https://desktop.invalid");
  return url.origin === "https://desktop.invalid" ? `${url.pathname}${url.search}` : undefined;
}
