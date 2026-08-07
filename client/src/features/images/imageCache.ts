import { CryptoDigestAlgorithm, digest } from "expo-crypto";
import { Directory, File, Paths } from "expo-file-system";

import { getToken, type Connection } from "@/db/connections";
import { getDatabase, runDatabaseWrite } from "@/db/database";
import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import type { Attachment } from "@/types/protocol";
import { classifyImageSource, decodeDataImage, detectImageType, externalImageLimit } from "./imageRules";

const ATTACHMENT_LIMIT = 25 * 1024 * 1024;
const CONNECTION_BUDGET = 256 * 1024 * 1024;
const HTTP_TTL_MS = 24 * 60 * 60 * 1000;
const FETCH_TIMEOUT_MS = 15_000;
const pending = new Map<string, Promise<string>>();

type ImageFailureStage = "download" | "response_read" | "validation" | "sha256" |
  "temp_write" | "atomic_move" | "sqlite_commit" | "rn_image_read";

type ImageLogContext = {
  attachmentId?: string | undefined;
  mediaType?: string | undefined;
  declaredSize?: number | undefined;
  actualSize?: number | undefined;
};

function errorName(error: unknown): string {
  return error instanceof Error && error.name ? error.name : "UnknownError";
}

function errorCode(stage: ImageFailureStage, error: unknown): string {
  if (error instanceof Error) {
    const httpStatus = /^图片下载失败（([0-9]{3})）$/.exec(error.message)?.[1];
    if (httpStatus) return `http_${httpStatus}`;
    const known: Record<string, string> = {
      图片大小校验失败: "size_mismatch",
      图片类型校验失败: "media_type_mismatch",
      图片摘要校验失败: "sha256_mismatch",
      图片缓存写入不完整: "partial_write",
      图片超过大小限制: "size_limit",
      远程内容不是图片: "non_image_response",
    };
    const knownCode = known[error.message];
    if (knownCode) return knownCode;
    if (error.name === "AbortError") return "request_aborted";
    if (error.name === "TypeError") return "native_type_error";
  }
  return `${stage}_failed`;
}

export function logImageFailure(stage: ImageFailureStage, context: ImageLogContext,
  error: unknown): void {
  console.warn("[TYRS_IMAGE]", {
    stage,
    attachmentId: context.attachmentId ?? null,
    mediaType: context.mediaType ?? null,
    declaredSize: context.declaredSize ?? null,
    actualSize: context.actualSize ?? null,
    errorType: errorName(error),
    errorCode: errorCode(stage, error),
  });
}

type CacheRow = {
  uri: string;
  media_type: string;
  size_bytes: number;
  sha256: string;
  expires_at: string | null;
};

export type ImageReference =
  | { type: "attachment"; attachment: Attachment }
  | { type: "uri"; uri: string };

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
}

async function sha256(bytes: Uint8Array): Promise<string> {
  const stableBytes = Uint8Array.from(bytes);
  return bytesToHex(new Uint8Array(await digest(CryptoDigestAlgorithm.SHA256, stableBytes)));
}

async function sourceKey(source: string): Promise<string> {
  return sha256(new TextEncoder().encode(source));
}

function extension(mediaType: string): string {
  if (mediaType === "image/jpeg") return ".jpg";
  if (mediaType === "image/gif") return ".gif";
  if (mediaType === "image/webp") return ".webp";
  return ".png";
}

function cacheDirectory(serverId: string): Directory {
  return new Directory(Paths.cache, "tyrs-hand-images", serverId);
}

async function cached(serverId: string, key: string): Promise<CacheRow | null> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<CacheRow>(`SELECT uri,media_type,size_bytes,sha256,expires_at
    FROM image_cache_entries WHERE server_id=? AND cache_key=?`, serverId, key);
  if (!row) return null;
  const file = new File(row.uri);
  if (!file.exists || file.size !== row.size_bytes) {
    await runDatabaseWrite((db) => db.runAsync(
      "DELETE FROM image_cache_entries WHERE server_id=? AND cache_key=?", serverId, key));
    return null;
  }
  await runDatabaseWrite((db) => db.runAsync(`UPDATE image_cache_entries SET last_accessed_at=?
    WHERE server_id=? AND cache_key=?`, new Date().toISOString(), serverId, key));
  return row;
}

async function save(serverId: string, key: string, bytes: Uint8Array, expectedMediaType: string,
  expectedSize?: number, expectedSha?: string, expiresAt?: string,
  logContext: ImageLogContext = {}): Promise<string> {
  const context = { ...logContext, mediaType: expectedMediaType, declaredSize: expectedSize,
    actualSize: bytes.byteLength };
  let detected: string;
  try {
    if (expectedSize !== undefined && bytes.byteLength !== expectedSize) throw new Error("图片大小校验失败");
    const actualType = detectImageType(bytes);
    if (!actualType || !expectedMediaType.toLowerCase().startsWith("image/") ||
      actualType !== expectedMediaType.toLowerCase()) throw new Error("图片类型校验失败");
    detected = actualType;
  } catch (error) {
    logImageFailure("validation", context, error);
    throw error;
  }
  let actualSha: string;
  try {
    actualSha = await sha256(bytes);
    if (expectedSha && actualSha !== expectedSha.toLowerCase()) throw new Error("图片摘要校验失败");
  } catch (error) {
    logImageFailure("sha256", context, error);
    throw error;
  }

  const directory = cacheDirectory(serverId);
  const temporary = new File(directory, `${key}.${Date.now()}.tmp`);
  const temporaryURI = temporary.uri;
  const target = new File(directory, `${key}${extension(detected)}`);
  try {
    try {
      directory.create({ intermediates: true, idempotent: true });
      temporary.create({ overwrite: true });
      temporary.write(bytes);
      if (temporary.size !== bytes.byteLength) throw new Error("图片缓存写入不完整");
    } catch (error) {
      logImageFailure("temp_write", context, error);
      throw error;
    }
    try {
      if (target.exists) target.delete();
      temporary.move(target);
    } catch (error) {
      logImageFailure("atomic_move", context, error);
      throw error;
    }
    const now = new Date().toISOString();
    try {
      await runDatabaseWrite((database) => database.runAsync(`INSERT INTO image_cache_entries(
        server_id,cache_key,uri,media_type,size_bytes,sha256,expires_at,last_accessed_at,created_at)
        VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id,cache_key) DO UPDATE SET
        uri=excluded.uri,media_type=excluded.media_type,size_bytes=excluded.size_bytes,
        sha256=excluded.sha256,expires_at=excluded.expires_at,last_accessed_at=excluded.last_accessed_at`,
      serverId, key, target.uri, detected, bytes.byteLength, actualSha, expiresAt ?? null, now, now));
    } catch (error) {
      logImageFailure("sqlite_commit", context, error);
      throw error;
    }
    await enforceImageCacheBudget(serverId);
    return target.uri;
  } finally {
    const leftover = new File(temporaryURI);
    if (leftover.exists) leftover.delete();
  }
}

async function fetchBytes(url: string, options: RequestInit, limit: number,
  logContext: ImageLogContext = {}): Promise<{
  bytes: Uint8Array; mediaType: string;
}> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), FETCH_TIMEOUT_MS);
  try {
    let response: Response;
    try {
      response = await fetch(url, { ...options, signal: controller.signal });
      if (!response.ok) throw new Error(`图片下载失败（${response.status}）`);
    } catch (error) {
      logImageFailure("download", logContext, error);
      throw error;
    }
    const mediaType = (response.headers.get("content-type") ?? "").split(";", 1)[0]!.toLowerCase();
    const declared = Number(response.headers.get("content-length") ?? "0");
    const responseContext = { ...logContext, mediaType, declaredSize: declared || undefined };
    try {
      if (!mediaType.startsWith("image/")) throw new Error("远程内容不是图片");
      if (declared > limit) throw new Error("图片超过大小限制");
    } catch (error) {
      logImageFailure("validation", responseContext, error);
      throw error;
    }
    let bytes: Uint8Array;
    try {
      bytes = new Uint8Array(await response.arrayBuffer());
    } catch (error) {
      logImageFailure("response_read", responseContext, error);
      throw error;
    }
    if (bytes.byteLength > limit) {
      const error = new Error("图片超过大小限制");
      logImageFailure("validation", { ...responseContext, actualSize: bytes.byteLength }, error);
      throw error;
    }
    return { bytes, mediaType };
  } finally {
    clearTimeout(timer);
  }
}

async function previewAttachmentURI(attachment: Attachment): Promise<string | null> {
  if (!isPreviewMode) return null;
  const { previewImageURI } = await import("@/preview/imageAssets");
  return previewImageURI(attachment.id);
}

async function resolveAttachment(connection: Connection, attachment: Attachment): Promise<string> {
  if (isPreviewServerId(connection.serverId)) {
    const uri = await previewAttachmentURI(attachment);
    if (uri) return uri;
  }
  const key = attachment.sha256.toLowerCase();
  const hit = await cached(connection.serverId, key);
  if (hit) return hit.uri;
  const token = await getToken(connection.serverId);
  if (!token) throw new Error("设备凭证不存在，请重新连接");
  const context = { attachmentId: attachment.id, mediaType: attachment.mediaType,
    declaredSize: attachment.sizeBytes };
  const result = await fetchBytes(`${connection.baseUrl}/api/v1/client/attachments/${attachment.id}/content`, {
    headers: { Authorization: `Bearer ${token}`, Accept: "image/*" }, credentials: "omit",
  }, ATTACHMENT_LIMIT, context);
  return save(connection.serverId, key, result.bytes, attachment.mediaType,
    attachment.sizeBytes, attachment.sha256, undefined, context);
}

async function resolveExternal(connection: Connection, uri: string): Promise<string> {
  if (isPreviewServerId(connection.serverId) && uri === "preview://agent-markdown") {
    const { previewMarkdownImageURI } = await import("@/preview/imageAssets");
    return previewMarkdownImageURI();
  }
  const rule = classifyImageSource(uri);
  if (rule === "local") return uri;
  if (rule === "unsupported") throw new Error("不支持的图片地址");
  const key = await sourceKey(uri);
  const hit = await cached(connection.serverId, key);
  const expired = hit?.expires_at && Date.parse(hit.expires_at) <= Date.now();
  if (hit && !expired) return hit.uri;
  try {
    const result = rule === "data" ? decodeDataImage(uri) : await fetchBytes(uri, {
      headers: { Accept: "image/*" }, credentials: "omit", redirect: "follow",
    }, externalImageLimit);
    const expiresAt = rule === "data" ? undefined : new Date(Date.now() + HTTP_TTL_MS).toISOString();
    return await save(connection.serverId, key, result.bytes, result.mediaType,
      undefined, undefined, expiresAt);
  } catch (error) {
    if (hit) return hit.uri;
    throw error;
  }
}

export async function resolveImageURI(connection: Connection, reference: ImageReference): Promise<string> {
  const identity = reference.type === "attachment" ? `attachment:${reference.attachment.id}` :
    `uri:${reference.uri}`;
  const pendingKey = `${connection.serverId}:${identity}`;
  const current = pending.get(pendingKey);
  if (current) return current;
  const operation = (reference.type === "attachment" ?
    resolveAttachment(connection, reference.attachment) : resolveExternal(connection, reference.uri))
    .finally(() => pending.delete(pendingKey));
  pending.set(pendingKey, operation);
  return operation;
}

export async function enforceImageCacheBudget(serverId: string,
  budget = CONNECTION_BUDGET): Promise<void> {
  const database = await getDatabase();
  const total = await database.getFirstAsync<{ bytes: number }>(`SELECT COALESCE(sum(size_bytes),0) bytes
    FROM image_cache_entries WHERE server_id=?`, serverId);
  let remaining = total?.bytes ?? 0;
  if (remaining <= budget) return;
  const rows = await database.getAllAsync<{ cache_key: string; uri: string; size_bytes: number }>(
    `SELECT cache_key,uri,size_bytes FROM image_cache_entries WHERE server_id=?
     ORDER BY last_accessed_at,created_at`, serverId);
  for (const row of rows) {
    if (remaining <= budget) break;
    const file = new File(row.uri);
    if (file.exists) file.delete();
    await runDatabaseWrite((db) => db.runAsync(
      "DELETE FROM image_cache_entries WHERE server_id=? AND cache_key=?", serverId, row.cache_key));
    remaining -= row.size_bytes;
  }
}

export async function clearImageCache(serverId: string): Promise<void> {
  const directory = cacheDirectory(serverId);
  if (directory.exists) directory.delete();
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM image_cache_entries WHERE server_id=?", serverId));
}
