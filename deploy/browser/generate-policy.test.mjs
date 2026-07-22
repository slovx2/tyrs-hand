import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const scriptPath = new URL("./generate-policy.mjs", import.meta.url);
const extensionId = "ljjpfmlebedjianbadehibibioaknkfb";

async function fixture(t, { id = extensionId, token = "extension-token" } = {}) {
  const root = await mkdtemp(join(tmpdir(), "tyrs-browser-policy-"));
  t.after(() => rm(root, { recursive: true, force: true }));

  const lockPath = join(root, "lock.json");
  const tokenPath = join(root, "token");
  const outputPath = join(root, "policy.json");
  await writeFile(lockPath, JSON.stringify({ extensionId: id }));
  await writeFile(tokenPath, token);
  return { lockPath, tokenPath, outputPath };
}

test("策略强制后续升级继续使用 localhost 更新源", async (t) => {
  const paths = await fixture(t);
  execFileSync(process.execPath, [scriptPath.pathname, paths.lockPath, paths.tokenPath, paths.outputPath]);

  const policy = JSON.parse(await readFile(paths.outputPath, "utf8"));
  assert.deepEqual(policy.ExtensionSettings[extensionId], {
    installation_mode: "force_installed",
    update_url: "http://127.0.0.1:8931/extension/update.xml",
    override_update_url: true,
  });
  assert.equal(policy["3rdparty"].extensions[extensionId].policy.extensionToken, "extension-token");
});

test("非法扩展 ID 会拒绝生成策略", async (t) => {
  const paths = await fixture(t, { id: "invalid" });
  const result = spawnSync(process.execPath, [
    scriptPath.pathname,
    paths.lockPath,
    paths.tokenPath,
    paths.outputPath,
  ]);
  assert.notEqual(result.status, 0);
});

test("空 Extension Token 会拒绝生成策略", async (t) => {
  const paths = await fixture(t, { token: "\n" });
  const result = spawnSync(process.execPath, [
    scriptPath.pathname,
    paths.lockPath,
    paths.tokenPath,
    paths.outputPath,
  ]);
  assert.notEqual(result.status, 0);
});
