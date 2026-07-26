import { createHash } from 'node:crypto';
import { readFile, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';

const [playwrightArgument, bridgeArgument, agentArgument, extensionId, outputArgument] = process.argv.slice(2);
if (!playwrightArgument || !bridgeArgument || !agentArgument || !extensionId || !outputArgument)
  throw new Error('usage: node write-local-lock.mjs <playwright-output> <bridge-output> <agent-output> <extension-id> <lock-path>');
if (!/^[a-p]{32}$/.test(extensionId))
  throw new Error('invalid extension ID');

const playwrightRoot = resolve(playwrightArgument);
const bridgeRoot = resolve(bridgeArgument);
const agentRoot = resolve(agentArgument);
const playwright = JSON.parse(await readFile(join(playwrightRoot, 'playwright-artifacts.json'), 'utf8'));
const bridge = JSON.parse(await readFile(join(bridgeRoot, 'bridge-artifact.json'), 'utf8'));
const agent = JSON.parse(await readFile(join(agentRoot, 'browser-agent-artifact.json'), 'utf8'));
if (agent.revision !== bridge.revision)
  throw new Error('Bridge and Browser Agent revisions differ');
const extensionPath = join(playwrightRoot, 'tyrs-browser-extension.crx');
const lock = {
  schemaVersion: 1,
  generatedAt: new Date().toISOString(),
  local: true,
  extensionId,
  playwright: {
    repository: playwright.repository,
    commit: playwright.revision,
    dirty: playwright.dirty,
    extensionVersion: playwright.extensionVersion,
    playwrightCoreVersion: playwright.playwrightCoreVersion,
    artifacts: {
      extension: localArtifact(extensionPath, await sha256(extensionPath)),
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
    browserAgent: {
      repository: agent.repository,
      commit: agent.revision,
      dirty: agent.dirty,
      agentVersion: agent.agentVersion,
      extensionVersion: agent.extensionVersion,
      nodeVersion: agent.nodeVersion,
      artifacts: Object.fromEntries(Object.entries(agent.artifacts).map(([architecture, artifact]) => [
        architecture, localArtifact(join(agentRoot, artifact.artifact), artifact.sha256),
      ])),
    },
  },
};
await writeFile(resolve(outputArgument), `${JSON.stringify(lock, null, 2)}\n`);

function localArtifact(path, sha256) {
  return { path, sha256 };
}

async function sha256(path) {
  return createHash('sha256').update(await readFile(path)).digest('hex');
}
