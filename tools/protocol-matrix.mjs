import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { randomUUID } from 'node:crypto'
import { startControlInfrastructure } from './protocol-control-infra.mjs'
import { runMigrationMatrix } from './protocol-migration-matrix.mjs'
import { runRecoveryMatrix } from './protocol-recovery-matrix.mjs'
import { collectMacNetworkDiagnostics } from './protocol-macos-diagnostics.mjs'
import { writePostgresDiagnostics } from './protocol-postgres-diagnostics.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const adapter = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(root, 'adapter-source'))
const artifactsRoot = resolve(process.env.PROTOCOL_ARTIFACT_DIR ?? resolve(root, '.artifacts/protocol'))
// CI 各分片共用同一 runId；汇总作业合并执行记录与通信证据后统一跑覆盖率门禁。
const runId = process.env.PROTOCOL_RUN_ID || randomUUID()
const shard = process.env.PROTOCOL_SHARD || 'all'
if (!['all', 'control', 'ssh-1', 'ssh-2', 'migration', 'recovery-adapter'].includes(shard))
  throw new Error(`未知协议分片: ${shard}`)
const runsShard = name => shard === 'all' || shard === name
const artifacts = resolve(artifactsRoot, 'runs', runId)
const codex = process.env.TYRS_HAND_TEST_CODEX_BIN ?? 'codex'
const go = process.env.GO ?? 'go'
const controlOnly = process.argv.includes('--control-only')
if (process.versions.node !== '24.14.0') throw new Error('协议矩阵必须使用 Node 24.14.0')
mkdirSync(artifacts, { recursive: true })
writeFileSync(resolve(artifactsRoot, 'latest.json'), JSON.stringify({ runId, directory: artifacts,
  scope: controlOnly ? 'control-runtime-e2e' : process.argv.includes('--runtime-only') ? 'runtime-only' : 'full-matrix', shard }))
const env = { ...process.env, PROTOCOL_ARTIFACT_DIR: artifacts,
  CODEX_SCHEMA_DIR: resolve(root, 'protocol/codex-app-server/0.157.1/json-schema'),
  PROTOCOL_RUN_ID: runId, TYRS_HAND_TEST_CODEX_BIN: codex,
  TYRS_HAND_TEST_CLAUDE_BIN: resolve(adapter, 'scripts/worker-runtime'),
}
writeFileSync(resolve(artifacts, 'run.json'), JSON.stringify({ runId: env.PROTOCOL_RUN_ID, startedAt: new Date().toISOString() }))
const run = (command, args, cwd = root) => execFileSync(command, args, { cwd, env, stdio: 'inherit' })
const output = (command, args, cwd = root) => execFileSync(command, args, { cwd, env, encoding: 'utf8' }).trim()
const version = output(codex, ['--version'])
if (version !== 'codex-cli 0.157.1') throw new Error(`Codex 测试 CLI 版本错误: ${version}`)
const pin = JSON.parse(readFileSync(resolve(root, 'protocol/adapter-lock.json'), 'utf8'))
const actual = output('git', ['rev-parse', 'HEAD'], adapter)
if (actual !== pin.commit) throw new Error(`适配器 commit 不匹配: ${actual}; 预期 ${pin.commit}`)
if (process.env.CI && output('git', ['status', '--porcelain'], adapter))
  throw new Error('CI 禁止使用未提交的适配器源码')
run('npm', ['run', 'build'], adapter)
writeFileSync(resolve(artifacts, 'combination.json'), JSON.stringify({ node: process.versions.node,
  codex: version, adapterCommit: actual, adapterDirty: !!output('git', ['status', '--porcelain'], adapter),
  workerCommit: output('git', ['rev-parse', 'HEAD']), workerDirty: !!output('git', ['status', '--porcelain']),
  claudeAgentSdk: pin.claudeAgentSdk, claudeCli: pin.claudeCli, codexProtocol: pin.codexProtocol,
  platform: process.platform, arch: process.arch,
}, null, 2))

// 构建先完成，再限制运行时只能访问本地 Mock HTTP；缺少隔离依赖立即失败。
const controlSuites = [
  { name: 'bootstrap-queue-lifecycle', pkg: './internal/bootstrap', test: 'TestWorkerControlNativeQueueLifecycleRealSSH',
    cases: ['QUEUE-002'], engines: ['codex'] },
  { name: 'bootstrap-queue-worker-restart', pkg: './internal/bootstrap', test: 'TestWorkerControlNativeQueueWholeWorkerRestartRealSSH',
    cases: ['QUEUE-003'], engines: ['codex'] },
  { name: 'bootstrap-queue-approval-start', pkg: './internal/bootstrap', test: 'TestWorkerControlNativeQueueApprovalAndStartRealSSH',
    cases: ['QUEUE-004'], engines: ['codex'] },
  { name: 'bootstrap-queue-admission-recovery', pkg: './internal/bootstrap', test: 'TestWorkerControlNativeQueueAdmissionRecoveryRealSSH',
    cases: ['QUEUE-005'], engines: ['codex'] },
  { name: 'bootstrap-queue-observed-recovery', pkg: './internal/bootstrap', test: 'TestWorkerControlNativeQueueObservedRecoveryRealSSH',
    cases: ['QUEUE-006'], engines: ['codex'] },
  { name: 'bootstrap-queue-binding', pkg: './internal/bootstrap', test: 'TestWorkerControlNativeQueueBindingRealSSH',
    cases: ['QUEUE-007'], engines: ['codex'] },
  { name: 'bootstrap-claude-queue-lifecycle', pkg: './internal/bootstrap', test: 'TestWorkerControlClaudeQueueLifecycleRealSSH',
    cases: ['QUEUE-002'], engines: ['claude-code'] },
  { name: 'bootstrap-claude-queue-worker-restart', pkg: './internal/bootstrap', test: 'TestWorkerControlClaudeQueueWholeWorkerRestartRealSSH',
    cases: ['QUEUE-003'], engines: ['claude-code'] },
  { name: 'bootstrap-claude-queue-approval-start', pkg: './internal/bootstrap', test: 'TestWorkerControlClaudeQueueApprovalAndStartRealSSH',
    cases: ['QUEUE-004'], engines: ['claude-code'] },
  { name: 'bootstrap-claude-queue-admission-recovery', pkg: './internal/bootstrap', test: 'TestWorkerControlClaudeQueueAdmissionRecoveryRealSSH',
    cases: ['QUEUE-005'], engines: ['claude-code'] },
  { name: 'bootstrap-claude-queue-observed-recovery', pkg: './internal/bootstrap', test: 'TestWorkerControlClaudeQueueObservedRecoveryRealSSH',
    cases: ['QUEUE-006'], engines: ['claude-code'] },
  { name: 'bootstrap-claude-queue-binding', pkg: './internal/bootstrap', test: 'TestWorkerControlClaudeQueueBindingRealSSH',
    cases: ['QUEUE-007'], engines: ['claude-code'] },
  { name: 'bootstrap-fixture-isolation', pkg: './internal/bootstrap', test: 'TestControlRuntimeFixturesIsolateDatabase',
    cases: [], engines: [] },
  { name: 'bootstrap-confirmation', pkg: './internal/bootstrap', test: 'TestWorkerControlRemoteConfirmationAfterRegistrationRealSSH',
    cases: ['FAILURE-009'], engines: ['claude-code'] },
  { name: 'bootstrap-control-outage', pkg: './internal/bootstrap', test: 'TestWorkerControlClaudeNetworkOutageRealSSH',
    cases: ['FAILURE-005'], engines: ['claude-code'] },
  { name: 'bootstrap-control-outage-codex', pkg: './internal/bootstrap', test: 'TestWorkerControlCodexNetworkOutageRealSSH',
    cases: ['FAILURE-005'], engines: ['codex'] },
  { name: 'bootstrap-control', pkg: './internal/bootstrap', test: 'TestWorkerControlRealSSHBothEngines',
    cases: ['CHANNELS-002', 'AUTOMATION-001', 'AUTOMATION-002', 'APPROVAL-006'] },
  { name: 'bootstrap-github-disabled', pkg: './internal/bootstrap', test: 'TestWorkerControlGitHubDisabledRealSSH',
    cases: ['MIGRATION-002'], engines: ['codex', 'claude-code'] },
  { name: 'bootstrap-channels-relay', pkg: './internal/bootstrap', test: 'TestWorkerControlChannelsRelayRealSSH',
    cases: ['CHANNELS-001'], engines: ['claude-code'] },
  { name: 'bootstrap-codex-revert', pkg: './internal/bootstrap', test: 'TestWorkerControlCodexRevertRealSSH',
    cases: ['SESSION-006'], engines: ['codex'] },
  { name: 'bootstrap-claude-revert', pkg: './internal/bootstrap', test: 'TestWorkerControlClaudeRevertRealSSH',
    cases: ['SESSION-007'], engines: ['claude-code'] },
  { name: 'bootstrap-codex-stdin', pkg: './internal/bootstrap', test: 'TestWorkerControlCodexStdinApprovalRealSSH',
    cases: ['APPROVAL-009'], engines: ['codex'] },
  { name: 'bootstrap-mcp', pkg: './internal/bootstrap', test: 'TestWorkerControlMcpRealSSH',
    cases: ['MCP-014'], engines: ['claude-code'] },
  { name: 'bootstrap-codex-forms', pkg: './internal/bootstrap', test: 'TestWorkerControlCodexFormsRealSSH',
    cases: ['MCP-020'], engines: ['codex'] },
  { name: 'bootstrap-codex-title', pkg: './internal/bootstrap', test: 'TestWorkerControlCodexTitleIsolationRealSSH',
    cases: ['TITLE-001'], engines: ['codex'] },
  { name: 'bootstrap-claude-permissions', pkg: './internal/bootstrap', test: 'TestWorkerControlClaudePermissionsRealSSH',
    cases: ['PERMISSION-012'], engines: ['claude-code'] },
  { name: 'bootstrap-live', pkg: './internal/bootstrap', test: 'TestWorkerControlLiveCodexRealSSH',
    cases: ['MIGRATION-004'] },
]
// 权限授权必须使用 CLI 自身 OS 沙箱；macOS 不能嵌套 seatbelt，不能豁免含模型的外层隔离。
// 正式权限链只在 Linux 仅回环的 network namespace 中执行。
if (process.platform === 'linux') controlSuites.push({ name: 'bootstrap-permissions', pkg: './internal/bootstrap',
  test: 'TestWorkerControlPermissionsRealSSH', cases: ['PERMISSION-009'], engines: ['codex'] })
const suites = controlOnly ? controlSuites : [
  { name: 'runtime', pkg: './internal/hostworker', test: 'TestRuntimeRegistryRealSSHBothEngines',
    cases: ['ENTRY-001', 'ISOLATION-001', 'ISOLATION-003', 'FAILURE-001', 'FILES-002', 'FILES-003', 'FILES-004', 'FILES-006', 'EVENTS-003'] },
  { name: 'isolation', pkg: './internal/hostworker', test: 'TestRuntimeIsolationRealSSHBothEngines',
    cases: ['ISOLATION-004'], engines: ['codex', 'claude-code'] },
  { name: 'parallel-approvals', pkg: './internal/hostworker', test: 'TestRuntimeParallelApprovalsRealSSHBothEngines',
    cases: ['ISOLATION-005'] },
  { name: 'files-acceptance', pkg: './internal/hostworker', test: 'TestRuntimeFilesRealSSHBothEngines',
    cases: ['FILES-001', 'FILES-002', 'FILES-003', 'FILES-004', 'FILES-006', 'FILES-007'], engines: ['codex', 'claude-code'] },
  { name: 'command-permissions', pkg: './internal/hostworker', test: 'TestRuntimeCommandPermissionsRealSSHBothEngines',
    cases: ['PERMISSION-command'] },
  { name: 'thread-permissions', pkg: './internal/hostworker', test: 'TestRuntimeThreadPermissionsRealSSHBothEngines',
    cases: ['PERMISSION-007'] },
  { name: 'permission-grants', pkg: './internal/hostworker', test: 'TestRuntimePermissionGrantsRealSSH',
    cases: ['PERMISSION-011'], engines: ['claude-code'] },
  { name: 'catalog', pkg: './internal/hostworker', test: 'TestRuntimeModelCatalogRealSSHBothEngines',
    cases: ['CATALOG-001'] },
  { name: 'config', pkg: './internal/hostworker', test: 'TestRuntimeConfigRealSSHBothEngines',
    cases: ['CONFIG-007'] },
  { name: 'experimental-features', pkg: './internal/hostworker', test: 'TestRuntimeExperimentalFeaturesRealSSH',
    cases: ['FEATURE-002'], engines: ['claude-code'] },
  { name: 'shell-commands', pkg: './internal/hostworker', test: 'TestRuntimeShellCommandsRealSSHBothEngines',
    cases: ['SHELL-002'] },
  { name: 'codex-mcp-oauth', pkg: './internal/hostworker', test: 'TestRuntimeCodexMcpOAuthRealSSH',
    cases: ['MCP-018'], engines: ['codex'] },
  { name: 'codex-mcp-oauth-headers', pkg: './internal/hostworker', test: 'TestRuntimeCodexMcpOAuthHeadersRealSSH',
    cases: ['MCP-019'], engines: ['codex'] },
  { name: 'codex-mcp', pkg: './internal/hostworker', test: 'TestRuntimeCodexMcpRealSSH',
    cases: ['MCP-015', 'MCP-016'], engines: ['codex'] },
  { name: 'codex-mcp-pagination', pkg: './internal/hostworker', test: 'TestRuntimeCodexMcpPaginationRealSSH',
    cases: ['MCP-017'], engines: ['codex'] },
  { name: 'diagnostics', pkg: './internal/hostworker', test: 'TestRuntimeDiagnosticsRealSSHBothEngines',
    cases: ['DIAGNOSTICS-002'], engines: ['codex', 'claude-code'] },
  { name: 'codex-mcp-stream', pkg: './internal/hostworker', test: 'TestRuntimeCodexMcpEventStreamRealSSH',
    cases: ['MCP-021'], engines: ['codex'] },
  { name: 'claude-account', pkg: './internal/hostworker', test: 'TestRuntimeClaudeAccountRealSSH',
    cases: ['ACCOUNT-003'], engines: ['claude-code'] },
  { name: 'codex-account', pkg: './internal/hostworker', test: 'TestRuntimeCodexAccountRealSSH',
    cases: ['ACCOUNT-001'], engines: ['codex'] },
  { name: 'context-injection', pkg: './internal/hostworker', test: 'TestRuntimeContextInjectionRealSSH',
    cases: ['CONTEXT-006'], engines: ['claude-code'] },
  { name: 'codex-context', pkg: './internal/hostworker', test: 'TestRuntimeCodexContextRealSSH',
    cases: ['CONTEXT-007'], engines: ['codex'] },
  { name: 'codex-turn-settings', pkg: './internal/hostworker', test: 'TestRuntimeCodexTurnSettingsRealSSH',
    cases: ['TURNSETTINGS-003'], engines: ['codex'] },
  { name: 'codex-plugins', pkg: './internal/hostworker', test: 'TestRuntimeCodexPluginsRealSSH',
    cases: ['PLUGIN-002'], engines: ['codex'] },
  { name: 'review', pkg: './internal/hostworker', test: 'TestRuntimeReviewRealSSH',
    cases: ['REVIEW-004'], engines: ['claude-code'] },
  { name: 'codex-review', pkg: './internal/hostworker', test: 'TestRuntimeCodexReviewRealSSH',
    cases: ['REVIEW-006'], engines: ['codex'] },
  { name: 'hooks', pkg: './internal/hostworker', test: 'TestRuntimeHooksRealSSH',
    cases: ['HOOKS-004'], engines: ['claude-code'] },
  { name: 'history', pkg: './internal/hostworker', test: 'TestRuntimeHistoryRealSSH',
    cases: ['HISTORY-002'], engines: ['claude-code'] },
  { name: 'session', pkg: './internal/hostworker', test: 'TestRuntimeSessionRealSSH',
    cases: ['SESSION-001', 'GOAL-003'], engines: ['claude-code'] },
  { name: 'codex-session', pkg: './internal/hostworker', test: 'TestRuntimeCodexSessionRealSSH',
    cases: ['SESSION-003'], engines: ['codex'] },
  { name: 'codex-catalog', pkg: './internal/hostworker', test: 'TestRuntimeCodexCatalogRealSSH',
    cases: ['CATALOG-002'], engines: ['codex'] },
  { name: 'codex-migration', pkg: './internal/hostworker', test: 'TestRuntimeCodexMigrationRealSSH',
    cases: ['MIGRATION-003'], engines: ['codex'] },
  { name: 'codex-metadata', pkg: './internal/hostworker', test: 'TestRuntimeCodexMetadataRealSSH',
    cases: ['SESSION-005', 'HISTORY-004', 'GOAL-004'], engines: ['codex'] },
  { name: 'codex-projects', pkg: './internal/hostworker', test: 'TestRuntimeCodexProjectsRealSSH',
    cases: ['PROJECT-001'], engines: ['codex'] },
  { name: 'codex-attachments', pkg: './internal/hostworker', test: 'TestRuntimeCodexAttachmentsRealSSH',
    cases: ['ATTACHMENT-001'], engines: ['codex'] },
  { name: 'codex-timeline', pkg: './internal/hostworker', test: 'TestRuntimeCodexTimelineRealSSH',
    cases: ['HISTORY-006'], engines: ['codex'] },
  { name: 'claude-timeline', pkg: './internal/hostworker', test: 'TestRuntimeClaudeTimelineRealSSH',
    cases: ['HISTORY-007'], engines: ['claude-code'] },
  { name: 'codex-items', pkg: './internal/hostworker', test: 'TestRuntimeCodexItemsRealSSH',
    cases: ['HISTORY-005'], engines: ['codex'] },
  { name: 'codex-error', pkg: './internal/hostworker', test: 'TestRuntimeCodexErrorRealSSH',
    cases: ['EVENTS-011'], engines: ['codex'] },
  { name: 'codex-events', pkg: './internal/hostworker', test: 'TestRuntimeCodexEventsRealSSH',
    cases: ['EVENTS-008'], engines: ['codex'] },
  { name: 'codex-approvals', pkg: './internal/hostworker', test: 'TestRuntimeCodexApprovalsRealSSH',
    cases: ['APPROVAL-008'], engines: ['codex'] },
  { name: 'claude-events', pkg: './internal/hostworker', test: 'TestRuntimeClaudeEventsRealSSH',
    cases: ['EVENTS-009'], engines: ['claude-code'] },
  { name: 'claude-event-gaps', pkg: './internal/hostworker', test: 'TestRuntimeClaudeEventGapsRealSSH',
    cases: ['EVENTS-010'], engines: ['claude-code'] },
  { name: 'goal-execution', pkg: './internal/hostworker', test: 'TestRuntimeGoalExecutionRealSSH',
    cases: ['GOAL-005'], engines: ['claude-code'] },
  { name: 'turn-control', pkg: './internal/hostworker', test: 'TestRuntimeTurnControlRealSSHBothEngines',
    cases: ['SUBMIT-004', 'EVENTS-005'] },
  { name: 'mcp', pkg: './internal/hostworker', test: 'TestRuntimeMcpRealSSH',
    cases: ['MCP-005'], engines: ['claude-code'] },
  { name: 'mcp-oauth', pkg: './internal/hostworker', test: 'TestRuntimeMcpOAuthRealSSH',
    cases: ['MCP-013'], engines: ['claude-code'] },
  { name: 'plan-approval', pkg: './internal/hostworker', test: 'TestRuntimePlanApprovalRealSSH',
    cases: ['PLAN-003'], engines: ['claude-code'] },
  { name: 'approval-lifecycle', pkg: './internal/hostworker', test: 'TestRuntimeApprovalLifecycleRealSSH',
    cases: ['APPROVAL-005'], engines: ['claude-code'] },
  { name: 'approval-arbitration', pkg: './internal/hostworker', test: 'TestRuntimeApprovalArbitrationRealSSH',
    cases: ['APPROVAL-001'], engines: ['claude-code'] },
  { name: 'capability-surface', pkg: './internal/hostworker', test: 'TestRuntimeCapabilitySurfaceRealSSH',
    cases: ['CAPABILITY-003'], engines: ['codex', 'claude-code'] },
  { name: 'claude-event-kinds', pkg: './internal/hostworker', test: 'TestRuntimeClaudeEventKindsRealSSH',
    cases: ['EVENTS-001'], engines: ['claude-code'] },
  { name: 'claude-runtime-crash', pkg: './internal/hostworker', test: 'TestRuntimeClaudeCrashRealSSH',
    cases: ['FAILURE-002'], engines: ['claude-code'] },
  { name: 'mcp-native-isolation', pkg: './internal/hostworker', test: 'TestRuntimeMcpNativeIsolationRealSSH',
    cases: ['MCP-003'], engines: ['claude-code'] },
  { name: 'cross-entry-isolation', pkg: './internal/hostworker', test: 'TestRuntimeCrossEntryIsolationRealSSH',
    cases: ['ISOLATION-002'], engines: ['codex', 'claude-code'] },
  { name: 'claude-permission-changes', pkg: './internal/hostworker', test: 'TestRuntimeClaudePermissionChangesRealSSH',
    cases: ['PERMISSION-001'], engines: ['claude-code'] },
  { name: 'bootstrap', pkg: './internal/bootstrap', test: 'TestWorkerBootstrapRealSSHSharedBudgetAndGitTool',
    cases: ['ENTRY-002', 'TOOLS-002'] },
  { name: 'bootstrap-queue-budget', pkg: './internal/bootstrap', test: 'TestWorkerBootstrapQueueRealSSHSharedBudgetAndGitTool',
    cases: ['QUEUE-001'] },
]
// 分片模式下 Control 专项由 control 分片（--control-only）执行，完整矩阵不再重复。
if (!controlOnly && !process.argv.includes('--runtime-only') && shard === 'all') {
  suites.push(...controlSuites)
}
const selected = controlOnly || shard === 'all' ? suites
  : shard.startsWith('ssh-') ? suites.filter((_, index) => index % 2 === Number(shard.at(-1)) - 1) : []
// 每个包只编译一次；测试二进制放在临时目录，不进入上传的证据。
const binaries = mkdtempSync(resolve(tmpdir(), 'tyrs-protocol-bin-'))
const compiled = new Map()
for (const suite of selected) {
  if (!compiled.has(suite.pkg)) {
    const binary = resolve(binaries, `${suite.pkg.replaceAll(/[^A-Za-z0-9]+/g, '-')}.test`)
    run(go, ['test', '-c', '-tags=integration', '-o', binary, suite.pkg])
    compiled.set(suite.pkg, binary)
  }
  suite.binary = compiled.get(suite.pkg)
}
const command = process.platform === 'darwin' ? '/usr/bin/sandbox-exec' : 'unshare'
const isolation = process.platform === 'darwin'
  ? ['-p', '(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip "localhost:*") (remote unix-socket))']
  : ['--user', '--map-root-user', '--net', '/bin/sh', '-ec', 'ip link set lo up; exec "$@"', 'runtime-test']
const xml = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;')
let runtimeExecutions = ''
const runtimeFailures = []
const failedWindows = []
const infrastructure = selected.some(suite => suite.name === 'bootstrap-control')
  ? await startControlInfrastructure({ evidenceDir: artifacts }) : undefined
Object.assign(env, infrastructure?.env ?? {})
try {
for (const suite of selected) {
  // macOS 禁止套用第二层 sandbox-exec。此专项无 Turn/模型调用，测试的就是运行时 OS 沙箱。
  // 所有含 SDK/LLM 的链路仍运行在仅允许本机网络的外层沙箱内。
  const nativePermissionTest = suite.name === 'command-permissions' && process.platform === 'darwin'
  const startedAt = Date.now()
  const runtime = spawnSync(nativePermissionTest ? go : command, [...(nativePermissionTest ? [] : [...isolation, go]), 'tool', 'test2json', '-t', '-p', `${suite.name}-e2e`, suite.binary,
    '-test.v', `-test.run=^${suite.test}$`, '-test.timeout=180s'],
    { cwd: root, env, encoding: 'utf8', timeout: 200_000, maxBuffer: 16 * 1024 * 1024 })
  writeFileSync(resolve(artifacts, `${suite.name}.jsonl`), runtime.stdout ?? '')
  console.log(`[protocol] ${suite.name} ${Math.round((Date.now() - startedAt) / 1000)}s`)
  const events = (runtime.stdout ?? '').split('\n').filter(Boolean).map(line => JSON.parse(line))
  const succeeded = events.some(event => event.Action === 'pass' && event.Test === suite.test)
  const failed = runtime.error || runtime.status !== 0 || !succeeded || events.some(event => event.Action === 'skip' || event.Action === 'fail')
  writeFileSync(resolve(artifacts, `${suite.name}-junit.xml`), `<?xml version="1.0"?><testsuite name="${suite.name}-e2e" tests="1" failures="${failed ? 1 : 0}" skipped="0"><testcase name="${suite.test}">${failed ? `<failure message="${xml(runtime.error ?? '真实 SSH 验收失败')}">${xml(runtime.stdout)}</failure>` : ''}</testcase></testsuite>`)
  if (failed) {
    failedWindows.push({ suite: suite.name, pid: runtime.pid, startedAt, completedAt: Date.now(),
      status: runtime.status, signal: runtime.signal })
    process.stderr.write(runtime.stdout ?? '')
    process.stderr.write(runtime.stderr ?? '')
    runtimeFailures.push({ suite: suite.name, error: String(runtime.error ?? '真实 SSH 双引擎验收失败'), status: runtime.status })
  }
  runtimeExecutions += (suite.engines ?? ['codex', 'claude-code']).map(engine => JSON.stringify({
    runId: env.PROTOCOL_RUN_ID, engine, caseName: suite.test,
    caseIds: [...suite.cases.filter(id => !['AUTOMATION-001', 'AUTOMATION-002', 'APPROVAL-006'].includes(id) || engine === 'claude-code'),
      ...(suite.name === 'runtime' && engine === 'claude-code' ? ['CONFIG-001', 'CAPABILITY-002', 'HISTORY-003'] : [])], status: failed ? 'failed' : 'passed',
  })).join('\n') + '\n'
  // 每个专项均即时落盘；前序失败不能吞掉后续真实验收或冒充成功。
  writeFileSync(resolve(artifacts, 'executions.jsonl'), runtimeExecutions)
}
} finally {
  infrastructure?.close()
  rmSync(binaries, { recursive: true, force: true })
}
if (infrastructure) {
  try {
    const database = writePostgresDiagnostics(artifacts, runId)
    if (!database.passed) runtimeFailures.push({ suite: 'postgres-diagnostics',
      error: database.errors.join('；') })
  } catch (error) {
    runtimeFailures.push({ suite: 'postgres-diagnostics', error: String(error) })
  }
}
collectMacNetworkDiagnostics(artifacts, failedWindows)
if (!controlOnly && !process.argv.includes('--runtime-only')) {
  if (runsShard('migration')) {
    const migration = await runMigrationMatrix({ root, artifacts, runId, env })
    runtimeFailures.push(...migration.failures)
    runtimeExecutions += migration.executions.map(value => JSON.stringify(value)).join('\n') + '\n'
  }
  if (runsShard('recovery-adapter')) {
    const recovery = await runRecoveryMatrix({ root, artifacts, runId, env })
    runtimeFailures.push(...recovery.failures)
    runtimeExecutions += recovery.executions.map(value => JSON.stringify(value)).join('\n') + '\n'
  }
}
if (controlOnly) {
  try {
    const { writeRuntimeWireReport } = await import('./protocol-inventory/runtime-wire.mjs')
    const wire = writeRuntimeWireReport({ root, directory: artifacts, runId,
      executions: runtimeExecutions.split('\n').filter(Boolean).map(line => JSON.parse(line)) })
    if (!wire.passed) runtimeFailures.push({ suite: 'runtime-wire',
      error: `真实通信校验失败：${wire.errors.length} 项，详见 runtime-wire.json` })
  } catch (error) {
    // 校验器或证据读取失败同样失败，并继续保存所有专项的执行结果。
    runtimeFailures.push({ suite: 'runtime-wire', error: String(error) })
  }
}
if (!runtimeFailures.length) console.log(controlOnly ? '真实 Control、双 SSH、SDK、Discord 审批与重启后的定时任务验收通过；这不代表三端 GUI 或完整协议矩阵通过。' :
  '真实 SSH 双引擎和 Worker 启动验收通过；这不代表完整协议矩阵通过。')
writeFileSync(resolve(artifacts, 'runtime-failures.json'), JSON.stringify({ runId, failures: runtimeFailures }, null, 2))
writeFileSync(resolve(artifacts, 'executions.jsonl'), runtimeExecutions)
if (!process.argv.includes('--runtime-only') && !controlOnly && runsShard('recovery-adapter')) {
  let adapterFailure
  try { run('npm', ['run', 'test:protocol'], adapter) } catch (error) { adapterFailure = error }
  const executionPath = resolve(artifacts, 'executions.jsonl')
  writeFileSync(executionPath, readFileSync(executionPath, 'utf8') + runtimeExecutions)
  // 即使 adapter 用例失败仍输出本轮覆盖缺口，不能由旧成功报表掩盖失败；分片模式由汇总作业统一计算。
  let inventoryFailure
  if (shard === 'all')
    try { run(process.execPath, ['tools/protocol-inventory/inventory.mjs']) } catch (error) { inventoryFailure = error }
  if (runtimeFailures.length) throw new Error(`真实运行时验收失败: ${runtimeFailures.map(item => item.suite).join(', ')}；完整报告已保留`)
  if (adapterFailure) throw adapterFailure
  if (inventoryFailure) throw inventoryFailure
}
if (runtimeFailures.length) throw new Error(`真实运行时验收失败: ${runtimeFailures.map(item => item.suite).join(', ')}`)
