import { readFile, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';

const [playwrightArgument, bridgeArgument, outputArgument] = process.argv.slice(2);
if (!playwrightArgument || !bridgeArgument || !outputArgument)
  throw new Error('usage: node write-local-lock.mjs <playwright-output> <bridge-output> <lock-path>');

const playwrightRoot = resolve(playwrightArgument);
const bridgeRoot = resolve(bridgeArgument);
const playwright = JSON.parse(await readFile(join(playwrightRoot, 'playwright-artifacts.json'), 'utf8'));
const bridge = JSON.parse(await readFile(join(bridgeRoot, 'bridge-artifact.json'), 'utf8'));
const lock = {
  schemaVersion: 1,
  generatedAt: new Date().toISOString(),
  local: true,
  playwright: {
    repository: playwright.repository,
    commit: playwright.revision,
    dirty: playwright.dirty,
    extensionVersion: playwright.extensionVersion,
    playwrightCoreVersion: playwright.playwrightCoreVersion,
    artifacts: {
      extension: localArtifact(join(playwrightRoot, 'tyrs-browser-extension.zip'),
          playwright.artifacts['tyrs-browser-extension.zip'].sha256),
      playwrightCore: localArtifact(join(playwrightRoot, 'playwright-core.tgz'),
          playwright.artifacts['playwright-core.tgz'].sha256),
    },
  },
  playwrightMcp: {
    repository: bridge.repository,
    commit: bridge.revision,
    dirty: bridge.dirty,
    bridgeVersion: bridge.bridgeVersion,
    artifacts: {
      bundle: localArtifact(join(bridgeRoot, bridge.artifact), bridge.sha256),
    },
  },
};
await writeFile(resolve(outputArgument), `${JSON.stringify(lock, null, 2)}\n`);

function localArtifact(path, sha256) {
  return { path, sha256 };
}
