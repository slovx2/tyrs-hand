import { execFileSync, spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { mkdir, mkdtemp, rename, rm, writeFile } from 'node:fs/promises'
import { basename, dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export function verifyArtifact(pin, harness, bytes) {
  const artifact = pin.artifacts?.[harness]
  const name = `codex-harness-adapter-${harness}_${pin.commit}_linux_amd64.tar.gz`
  const url = `${pin.repository}/releases/download/${pin.goModuleVersion}/${name}`
  if (!artifact || artifact.url !== url || !/^[a-f0-9]{64}$/.test(artifact.sha256))
    throw new Error('适配器制品锁不完整或来源与版本不符')
  if (createHash('sha256').update(bytes).digest('hex') !== artifact.sha256)
    throw new Error('适配器制品 SHA-256 校验失败')
  return name
}

// 发布构建只消费固定制品；本地验收委托适配器自身的构建入口。
async function main() {
  const [harness, source, output, mode] = process.argv.slice(2)
  if (!['claude', 'pi'].includes(harness) || !source || !output)
    throw new Error('需要 harness、适配器源码目录和输出目录')
  if (mode && mode !== '--local-acceptance') throw new Error('未知构建选项')
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
  const pin = JSON.parse(readFileSync(resolve(root, 'protocol/adapter-lock.json')))
  const adapter = resolve(source)
  const versions = JSON.parse(readFileSync(resolve(adapter, 'protocol/versions.json')))
  for (const key of ['node', 'claudeAgentSdk', 'claudeCli', 'codexProtocol', 'piCodingAgent', 'piCli', 'piPlanMode', 'piTuiKit', 'piSubagents'])
    if (versions[key] !== pin[key]) throw new Error(`适配器版本与消费锁不符: ${key}`)
  if (mode === '--local-acceptance') {
    const result = spawnSync('sh', [resolve(adapter, `scripts/package-${harness}-runtime.sh`), adapter, resolve(output), mode], { stdio: 'inherit' })
    if (result.error) throw result.error
    process.exitCode = result.status ?? 1
    return
  }
  const commit = execFileSync('git', ['-C', adapter, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
  if (commit !== pin.commit) throw new Error('适配器提交与消费锁不符')
  if (execFileSync('git', ['-C', adapter, 'status', '--porcelain'], { encoding: 'utf8' }).trim())
    throw new Error('适配器源码存在未提交修改')
  const artifact = pin.artifacts?.[harness]
  if (!artifact) throw new Error('消费锁缺少制品')
  const response = await fetch(artifact.url, { signal: AbortSignal.timeout(120_000) })
  if (!response.ok) throw new Error(`下载适配器制品失败: HTTP ${response.status}`)
  const bytes = Buffer.from(await response.arrayBuffer())
  const name = verifyArtifact(pin, harness, bytes)
  await mkdir(output, { recursive: true })
  const temporary = await mkdtemp(join(resolve(output), '.verify-'))
  try {
    await writeFile(join(temporary, name), bytes)
    await rename(join(temporary, name), join(output, name))
    await writeFile(join(output, `${name}.sha256`), `${artifact.sha256}  ${basename(name)}\n`)
  } finally {
    await rm(temporary, { recursive: true, force: true })
  }
  console.log(`已校验固定制品: ${name}`)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main()
