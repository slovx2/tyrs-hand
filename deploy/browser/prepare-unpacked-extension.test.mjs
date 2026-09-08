import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const script = new URL("./prepare-unpacked-extension.mjs", import.meta.url).pathname;
// 仅包含 manifest.json 的 ZIP；夹具验证解包与 ID 校验，不模拟 CRX 签名验签。
const zip = Buffer.from("UEsDBBQAAAAIAKmIKF3vxdD9OgAAAD4AAAANAAAAbWFuaWZlc3QuanNvbqtWyk3My0xLLS6JL0stKs7Mz1OyMtZRykvMTVWyUnq2tfvF+qnPOlc+3ThVSUcJrkLJUM9Az0CpFgBQSwECHgMUAAAACACpiChd78XQ/ToAAAA+AAAADQAAAAAAAAABAAAApIEAAAAAbWFuaWZlc3QuanNvblBLBQYAAAAAAQABADsAAABlAAAAAAA=", "base64");
const key = Buffer.from("tyrs-extension-test-public-key");
const id = [...createHash("sha256").update(key).digest().subarray(0, 16)]
  .map(byte => String.fromCharCode(97 + (byte >> 4), 97 + (byte & 15))).join("");

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "tyrs-unpacked-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const proof = Buffer.concat([Buffer.from([10, key.length]), key]);
  const header = Buffer.concat([Buffer.from([18, proof.length]), proof]);
  const prefix = Buffer.alloc(12);
  prefix.write("Cr24");
  prefix.writeUInt32LE(3, 4);
  prefix.writeUInt32LE(header.length, 8);
  const crx = join(root, "extension.crx");
  const output = join(root, "固定扩展目录 with spaces");
  await writeFile(crx, Buffer.concat([prefix, header, zip]));
  const run = (expectedId = id) => spawnSync(process.execPath, [script, crx, output, expectedId], { encoding: "utf8" });
  return { crx, output, run };
}

test("解包保留版本并注入固定公钥，重复安装沿用同一路径", async t => {
  const f = await fixture(t);
  const first = f.run();
  assert.equal(first.status, 0, first.stderr);
  const manifest = JSON.parse(await readFile(join(f.output, "manifest.json"), "utf8"));
  assert.equal(manifest.version, "1.0.0");
  assert.equal(manifest.key, key.toString("base64"));
  await writeFile(join(f.output, "old-file"), "旧版本文件");
  const updated = f.run();
  assert.equal(updated.status, 0, updated.stderr);
  await assert.rejects(readFile(join(f.output, "old-file")), { code: "ENOENT" });
  assert.deepEqual(JSON.parse(await readFile(join(f.output, "manifest.json"), "utf8")), manifest);
});

test("扩展 ID 不匹配或 CRX 损坏时保留已安装目录", async t => {
  const f = await fixture(t);
  const first = f.run();
  assert.equal(first.status, 0, first.stderr);
  const before = await readFile(join(f.output, "manifest.json"), "utf8");
  const mismatched = f.run("a".repeat(32));
  assert.notEqual(mismatched.status, 0);
  assert.match(mismatched.stderr, /extension id mismatch/);
  assert.equal(await readFile(join(f.output, "manifest.json"), "utf8"), before);
  await writeFile(f.crx, "invalid");
  const corrupt = f.run();
  assert.notEqual(corrupt.status, 0);
  assert.match(corrupt.stderr, /CRX magic is invalid/);
  assert.equal(await readFile(join(f.output, "manifest.json"), "utf8"), before);
});
