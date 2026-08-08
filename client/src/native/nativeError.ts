export function normalizeNativeTransportError(error: unknown): Error {
  const message = error instanceof Error ? error.message : String(error);
  const marker = "→ Caused by:";
  const markerIndex = message.lastIndexOf(marker);
  const detail = (markerIndex >= 0 ? message.slice(markerIndex + marker.length) : message)
    .trim().replace(/^go\.[^:]+:\s*/, "");
  return new Error(detail || "SSH 原生传输失败");
}
