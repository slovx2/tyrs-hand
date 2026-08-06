import { createHash } from "node:crypto";
import { DatabaseSync, type SQLInputValue } from "node:sqlite";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type { Connection } from "@/db/connections";
import type { Attachment } from "@/types/protocol";

type AsyncDatabase = {
  execAsync: (sql: string) => Promise<void>;
  runAsync: (sql: string, ...parameters: unknown[]) => Promise<unknown>;
  getFirstAsync: <T>(sql: string, ...parameters: unknown[]) => Promise<T | null>;
  getAllAsync: <T>(sql: string, ...parameters: unknown[]) => Promise<T[]>;
  withExclusiveTransactionAsync: (operation: (database: AsyncDatabase) => Promise<void>) => Promise<void>;
};

const testState = vi.hoisted(() => ({
  database: null as AsyncDatabase | null,
  files: new Map<string, Uint8Array>(),
  directories: new Set<string>(),
  getToken: vi.fn(async () => "device-token"),
}));

vi.mock("expo-sqlite", () => ({
  openDatabaseAsync: vi.fn(async () => {
    if (!testState.database) throw new Error("测试数据库尚未初始化");
    return testState.database;
  }),
}));

vi.mock("expo-file-system", () => {
  const path = (...parts: string[]) => parts.join("/").replace(/\/{2,}/g, "/");
  class Directory {
    uri: string;
    constructor(...parts: (string | { uri: string })[]) {
      this.uri = path(...parts.map((part) => typeof part === "string" ? part : part.uri));
    }
    get exists() {
      return testState.directories.has(this.uri) ||
        [...testState.files.keys()].some((uri) => uri.startsWith(`${this.uri}/`));
    }
    create() { testState.directories.add(this.uri); }
    delete() {
      testState.directories.delete(this.uri);
      for (const uri of testState.files.keys()) {
        if (uri.startsWith(`${this.uri}/`)) testState.files.delete(uri);
      }
    }
  }
  class File {
    uri: string;
    constructor(first: string | { uri: string }, ...rest: string[]) {
      this.uri = path(typeof first === "string" ? first : first.uri, ...rest);
    }
    get exists() { return testState.files.has(this.uri); }
    get size() { return testState.files.get(this.uri)?.byteLength ?? 0; }
    create() { testState.files.set(this.uri, new Uint8Array()); }
    write(bytes: Uint8Array) { testState.files.set(this.uri, Uint8Array.from(bytes)); }
    delete() { testState.files.delete(this.uri); }
    move(target: File) {
      const bytes = testState.files.get(this.uri);
      if (!bytes) throw new Error("源文件不存在");
      testState.files.set(target.uri, bytes);
      testState.files.delete(this.uri);
    }
  }
  return { Directory, File, Paths: { cache: "mock-cache" } };
});

vi.mock("expo-crypto", () => ({
  CryptoDigestAlgorithm: { SHA256: "SHA-256" },
  digest: vi.fn(async (_algorithm: string, value: ArrayBuffer) => {
    const { createHash: createNodeHash } = await import("node:crypto");
    const bytes = createNodeHash("sha256").update(new Uint8Array(value)).digest();
    return Uint8Array.from(bytes).buffer;
  }),
}));

vi.mock("@/db/connections", () => ({ getToken: testState.getToken }));
vi.mock("@/preview/config", () => ({
  isPreviewMode: false,
  isPreviewServerId: vi.fn(() => false),
}));

const serverId = "10000000-0000-4000-8000-000000000001";
const sessionId = "20000000-0000-4000-8000-000000000001";
const png = Uint8Array.from(Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
  "base64"));
const pngSha = createHash("sha256").update(png).digest("hex");
const connection: Connection = {
  serverId, baseUrl: "https://control.example", name: "测试", deviceId: "device", active: true,
};
let nativeDatabase: DatabaseSync;

function createAsyncDatabase(database: DatabaseSync): AsyncDatabase {
  const adapter: AsyncDatabase = {
    execAsync: async (sql) => { database.exec(sql); },
    runAsync: async (sql, ...parameters) =>
      database.prepare(sql).run(...parameters as SQLInputValue[]),
    getFirstAsync: async <T>(sql: string, ...parameters: unknown[]) =>
      (database.prepare(sql).get(...parameters as SQLInputValue[]) as T | undefined) ?? null,
    getAllAsync: async <T>(sql: string, ...parameters: unknown[]) =>
      database.prepare(sql).all(...parameters as SQLInputValue[]) as T[],
    withExclusiveTransactionAsync: async (operation) => operation(adapter),
  };
  return adapter;
}

function attachment(id: string, sha256 = pngSha): Attachment {
  return { id, sessionId, kind: "image", filename: "image.png", mediaType: "image/png",
    sizeBytes: png.byteLength, sha256, status: "attached", createdAt: "2026-08-06T00:00:00Z" };
}

function imageResponse(): Response {
  return new Response(png, { status: 200, headers: {
    "content-type": "image/png", "content-length": String(png.byteLength),
  } });
}

beforeAll(async () => {
  nativeDatabase = new DatabaseSync(":memory:");
  testState.database = createAsyncDatabase(nativeDatabase);
  const { getDatabase } = await import("@/db/database");
  await getDatabase();
});

beforeEach(() => {
  nativeDatabase.exec("DELETE FROM connections;");
  nativeDatabase.prepare(`INSERT INTO connections(server_id,base_url,name,device_id,active,
    created_at,updated_at) VALUES (?,?,?,?,1,?,?)`).run(serverId, connection.baseUrl,
  connection.name, connection.deviceId, "2026-08-06T00:00:00Z", "2026-08-06T00:00:00Z");
  testState.files.clear();
  testState.directories.clear();
  testState.getToken.mockClear();
  vi.unstubAllGlobals();
});

afterAll(() => nativeDatabase.close());

describe("图片缓存", () => {
  it("鉴权下载附件并校验服务端 SHA-256", async () => {
    const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => imageResponse());
    vi.stubGlobal("fetch", fetch);
    const { resolveImageURI } = await import("./imageCache");

    const uri = await resolveImageURI(connection, { type: "attachment",
      attachment: attachment("60000000-0000-4000-8000-000000000001") });

    expect(uri).toContain(pngSha);
    expect(testState.getToken).toHaveBeenCalledWith(serverId);
    expect(fetch).toHaveBeenCalledWith(
      "https://control.example/api/v1/client/attachments/60000000-0000-4000-8000-000000000001/content",
      expect.objectContaining({ headers: expect.objectContaining({ Authorization: "Bearer device-token" }),
        credentials: "omit" }));
    await expect(resolveImageURI(connection, { type: "attachment",
      attachment: attachment("60000000-0000-4000-8000-000000000002", "0".repeat(64)) }))
      .rejects.toThrow("摘要校验失败");
  });

  it("合并并发下载并在失败后允许重试", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => {
      await gate;
      return imageResponse();
    });
    vi.stubGlobal("fetch", fetch);
    const { resolveImageURI } = await import("./imageCache");
    const reference = { type: "attachment" as const,
      attachment: attachment("60000000-0000-4000-8000-000000000003") };
    const first = resolveImageURI(connection, reference);
    const second = resolveImageURI(connection, reference);
    release();
    await expect(Promise.all([first, second])).resolves.toHaveLength(2);
    expect(fetch).toHaveBeenCalledTimes(1);

    const external = { type: "uri" as const, uri: "https://images.example/retry.png" };
    fetch.mockRejectedValueOnce(new Error("offline"));
    await expect(resolveImageURI(connection, external)).rejects.toThrow("offline");
    fetch.mockResolvedValueOnce(imageResponse());
    await expect(resolveImageURI(connection, external)).resolves.toContain("mock-cache");
  });

  it("外链不发送认证信息，过期刷新失败时使用离线缓存", async () => {
    const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => imageResponse());
    vi.stubGlobal("fetch", fetch);
    const { resolveImageURI } = await import("./imageCache");
    const reference = { type: "uri" as const, uri: "https://images.example/offline.png" };
    const cached = await resolveImageURI(connection, reference);
    expect(fetch.mock.calls[0]?.[1]).toEqual(expect.objectContaining({
      headers: { Accept: "image/*" }, credentials: "omit", redirect: "follow",
    }));
    await testState.database!.runAsync(
      "UPDATE image_cache_entries SET expires_at=? WHERE server_id=?",
      "2020-01-01T00:00:00Z", serverId);
    fetch.mockRejectedValueOnce(new Error("offline"));
    await expect(resolveImageURI(connection, reference)).resolves.toBe(cached);
  });

  it("按连接执行 LRU 淘汰，本机与 data 来源遵循各自规则", async () => {
    const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => imageResponse());
    vi.stubGlobal("fetch", fetch);
    const { enforceImageCacheBudget, resolveImageURI } = await import("./imageCache");
    const first = await resolveImageURI(connection,
      { type: "uri", uri: "https://images.example/old.png" });
    const second = await resolveImageURI(connection,
      { type: "uri", uri: "https://images.example/new.png" });
    await testState.database!.runAsync(
      "UPDATE image_cache_entries SET last_accessed_at=? WHERE uri=?", "2020-01-01T00:00:00Z", first);
    await enforceImageCacheBudget(serverId, png.byteLength);
    expect(testState.files.has(first)).toBe(false);
    expect(testState.files.has(second)).toBe(true);

    const local = "content://media/external/images/1";
    await expect(resolveImageURI(connection, { type: "uri", uri: local })).resolves.toBe(local);
    const data = `data:image/png;base64,${Buffer.from(png).toString("base64")}`;
    await expect(resolveImageURI(connection, { type: "uri", uri: data })).resolves.toContain("mock-cache");
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});
