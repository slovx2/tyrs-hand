import assert from 'node:assert/strict'
import { readFile, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { sha256 } from './protocol-migration-infra.mjs'
import { pendingJournal, verifyMigratedJournal } from './protocol-migration-journal.mjs'

// 以真实旧数据库快照隔离 Worker 回滚；不更改数据库协议字段，也不宣称原库向后兼容。
export async function rollbackJournal({ root, control, worker, models, previous, proxy, open, threadId }) {
  const calls = structuredClone(models.calls)
  const snapshot = control.snapshotDatabase()
  await writeFile(resolve(root, 'old-control.dump'), snapshot, { mode: 0o600 })
  await control.upgrade()
  await worker.start('new')
  const first = await verifyMigratedJournal(previous)
  const markerPath = resolve(worker.state, 'control-state/codex-runtime-scope-v1')
  const marker = await readFile(markerPath)
  assert.equal(marker.toString(), '1\n')
  assert.equal(control.sql('SELECT protocol_version FROM workers'), '33')
  const modern = JSON.parse(await readFile(previous.path))
  assert.equal(modern.journalFormatVersion, 1, '真实新版写出明确格式来源')
  assert.equal(modern.task.snapshot.runtime.engine, 'codex')
  assert.deepEqual(models.calls, calls)
  await worker.process.stop()
  await control.restoreOldDatabase(snapshot)
  assert.equal(control.sql('SELECT protocol_version FROM workers'), '32')
  await worker.start('old')
  const rewritten = await pendingJournal(worker, proxy)
  assert.equal(rewritten.value.journalFormatVersion, undefined, '旧32真实重写须自然移除未知来源字段')
  assert.equal(rewritten.value.task.snapshot.runtime.engine, undefined)
  assert.deepEqual(models.calls, calls, '旧版恢复已完成工具不能重放模型')
  const client = await open('codex')
  const history = await client.request('thread/resume', { threadId })
  assert.match(JSON.stringify(history.thread.turns), /MIGRATION_PENDING_WRITE_OK/)
  await client.close()
  const crash = await worker.crash()
  rewritten.bytes = await readFile(rewritten.path)
  rewritten.value = JSON.parse(rewritten.bytes)
  assert.equal(rewritten.value.journalFormatVersion, undefined)
  assert.equal(rewritten.value.task.snapshot.runtime.engine, undefined)
  assert.deepEqual(await readFile(markerPath), marker)
  assert.deepEqual(await readFile(previous.path + '.before-runtime-scope'), previous.bytes)
  rewritten.originalBackupBytes = previous.bytes
  // 只有本轮内容确实不同才需要新增备份；同内容不制造虚假的迁移世代。
  rewritten.backupPath = previous.path + '.before-runtime-scope' +
    (sha256(rewritten.bytes) === sha256(previous.bytes) ? '' : '.' + sha256(rewritten.bytes))
  await writeFile(resolve(root, 'journal-after-rollback.json'), rewritten.bytes, { mode: 0o600 })
  return { journal: rewritten, report: { database: 'restored-old-snapshot', snapshotSHA256: sha256(snapshot),
    markerSHA256: sha256(marker), sourceProtocolVersion: 32, targetProtocolVersion: 33,
    firstMigration: first, oldWriterRemovedFormatAndEngine: true,
    rewrittenSHA256: sha256(rewritten.bytes), originalBackupSHA256: sha256(previous.bytes), crash } }
}
