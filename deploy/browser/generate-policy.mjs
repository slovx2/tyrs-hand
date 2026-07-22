import { readFile, writeFile } from "node:fs/promises";

const [lockPath, tokenPath, outputPath] = process.argv.slice(2);
if (!lockPath || !tokenPath || !outputPath)
  throw new Error("usage: node generate-policy.mjs <release-lock.json> <extension-token> <output.json>");

const lock = JSON.parse(await readFile(lockPath, "utf8"));
const extensionToken = (await readFile(tokenPath, "utf8")).trim();
if (!/^[a-p]{32}$/.test(lock.extensionId) || !extensionToken)
  throw new Error("release lock or extension token is invalid");

const updateURL = "http://127.0.0.1:8931/extension/update.xml";
const policy = {
  ExtensionSettings: {
    [lock.extensionId]: {
      installation_mode: "force_installed",
      update_url: updateURL,
      override_update_url: true,
    },
  },
  "3rdparty": {
    extensions: {
      [lock.extensionId]: {
        policy: {
          relayUrl: "ws://127.0.0.1:8932/extension",
          statusUrl: "http://127.0.0.1:8931/extension-status",
          extensionToken,
        },
      },
    },
  },
};

await writeFile(outputPath, `${JSON.stringify(policy, null, 2)}\n`, { mode: 0o644 });
