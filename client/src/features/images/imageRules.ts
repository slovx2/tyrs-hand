export const externalImageLimit = 10 * 1024 * 1024;

export type ImageSourceRule = "network" | "data" | "local" | "unsupported";

export function classifyImageSource(uri: string): ImageSourceRule {
  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(uri)?.[1]?.toLowerCase();
  if (scheme === "http" || scheme === "https") return "network";
  if (scheme === "data") return "data";
  if (scheme === "file" || scheme === "content" || scheme === "asset") return "local";
  return "unsupported";
}

export function detectImageType(bytes: Uint8Array): string | null {
  if (bytes.length >= 8 && bytes[0] === 0x89 && bytes[1] === 0x50 && bytes[2] === 0x4e &&
    bytes[3] === 0x47 && bytes[4] === 0x0d && bytes[5] === 0x0a && bytes[6] === 0x1a &&
    bytes[7] === 0x0a) return "image/png";
  if (bytes.length >= 3 && bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff) {
    return "image/jpeg";
  }
  if (bytes.length >= 6) {
    const signature = String.fromCharCode(...bytes.slice(0, 6));
    if (signature === "GIF87a" || signature === "GIF89a") return "image/gif";
  }
  if (bytes.length >= 12 && String.fromCharCode(...bytes.slice(0, 4)) === "RIFF" &&
    String.fromCharCode(...bytes.slice(8, 12)) === "WEBP") return "image/webp";
  return null;
}

export function decodeDataImage(uri: string, limit = externalImageLimit): {
  bytes: Uint8Array; mediaType: string;
} {
  const match = /^data:(image\/[a-z0-9.+-]+)(?:;[^,]*)?;base64,([a-z0-9+/=\s]+)$/i.exec(uri);
  if (!match) throw new Error("无效的 data 图片");
  const binary = globalThis.atob(match[2]!.replace(/\s/g, ""));
  if (binary.length > limit) throw new Error("图片超过大小限制");
  return {
    bytes: Uint8Array.from(binary, (character) => character.charCodeAt(0)),
    mediaType: match[1]!.toLowerCase(),
  };
}
