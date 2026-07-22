import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { spawnSync } from 'node:child_process';

const [lockArgument, playwrightArgument, mcpArgument] = process.argv.slice(2);
if (!lockArgument || !playwrightArgument || !mcpArgument)
  throw new Error('usage: node verify-source-lock.mjs <source-lock.json> <playwright-dir> <mcp-dir>');

const lock = JSON.parse(await readFile(resolve(lockArgument), 'utf8'));
if (lock.schemaVersion !== 1)
  throw new Error('unsupported browser source lock schema');
verifyRepository('playwright', resolve(playwrightArgument), lock.playwright);
verifyRepository('playwright-mcp', resolve(mcpArgument), lock.playwrightMcp);

function verifyRepository(name, directory, expected) {
  if (!expected || !/^[a-f0-9]{40}$/.test(expected.commit))
    throw new Error(`invalid ${name} source lock`);
  const revision = git(directory, ['rev-parse', 'HEAD']).trim();
  if (revision !== expected.commit) {
    throw new Error(`${name} is at ${revision}, expected ${expected.commit}; ` +
      'update deploy/browser/source-lock.json or set TYRS_BROWSER_ALLOW_UNPINNED=1 for a local experiment');
  }
  const origin = git(directory, ['remote', 'get-url', 'origin']).trim();
  if (!origin.endsWith(`slovx2/${name}.git`))
    throw new Error(`${name} origin is not the managed fork: ${origin}`);
}

function git(directory, args) {
  const result = spawnSync('git', args, { cwd: directory, encoding: 'utf8' });
  if (result.status !== 0)
    throw new Error(`git ${args.join(' ')} failed: ${result.stderr}`);
  return result.stdout;
}
