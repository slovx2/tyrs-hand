import { createHash } from 'node:crypto';
import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { basename, join, resolve } from 'node:path';

const [lockArgument, outputArgument] = process.argv.slice(2);
if (!lockArgument || !outputArgument)
  throw new Error('usage: node fetch-release.mjs <browser-artifacts.lock.json> <output-dir>');

const lock = JSON.parse(await readFile(resolve(lockArgument), 'utf8'));
if (lock.schemaVersion !== 1 || lock.local)
  throw new Error('production fetch requires a schema v1 release lock');
const outputRoot = resolve(outputArgument);
const temporaryRoot = `${outputRoot}.partial-${process.pid}`;
await rm(temporaryRoot, { recursive: true, force: true });
await mkdir(temporaryRoot, { recursive: true });

try {
  const artifacts = [
    lock.playwright?.artifacts?.extension,
    lock.playwright?.artifacts?.playwrightCore,
    lock.playwrightMcp?.artifacts?.bundle,
  ];
  for (const artifact of artifacts)
    await downloadArtifact(artifact, temporaryRoot);
  await writeFile(join(temporaryRoot, 'browser-artifacts.lock.json'), `${JSON.stringify(lock, null, 2)}\n`);
  await rm(outputRoot, { recursive: true, force: true });
  await rename(temporaryRoot, outputRoot);
} catch (error) {
  await rm(temporaryRoot, { recursive: true, force: true });
  throw error;
}

async function downloadArtifact(artifact, destination) {
  if (!artifact || typeof artifact.url !== 'string' || !artifact.url.startsWith('https://github.com/') ||
      !/^[a-f0-9]{64}$/.test(artifact.sha256))
    throw new Error('invalid release artifact entry');
  const response = await fetch(artifact.url, { redirect: 'follow' });
  if (!response.ok)
    throw new Error(`artifact download failed (${response.status}): ${artifact.url}`);
  const data = Buffer.from(await response.arrayBuffer());
  const actual = createHash('sha256').update(data).digest('hex');
  if (actual !== artifact.sha256)
    throw new Error(`artifact checksum mismatch: ${artifact.url}`);
  await writeFile(join(destination, basename(new URL(artifact.url).pathname)), data, { mode: 0o644 });
}
