import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import test from 'node:test'
import { closeTestConnections, connections, loadMobileDatabase } from './fixtures/sqlite-driver.mjs'
import { prepareMobileMigration, verifyMobileMigration } from '../protocol-mobile-migration.mjs'

test('迁移锁冲突后释放锁可再次打开，不永久缓存失败且不泄漏连接', async () => {
  const root = await mkdtemp(join(tmpdir(), 'mobile-migration-lock-'))
  const file = join(root, 'legacy.db')
  const legacy = new DatabaseSync(file)
  try {
    legacy.exec(await readFile(new URL('./fixtures/database-v12.sql', import.meta.url), 'utf8'))
    legacy.exec(`INSERT INTO connection_profiles VALUES ('old','machine','旧入口',1,'old-fingerprint',
      'localhost',2222,'worker','old-key','old-fingerprint','before','before');
      INSERT INTO drafts VALUES ('old','thread','原草稿',NULL,'[]','before');
      INSERT INTO pending_submissions VALUES ('old','message','thread',NULL,'{}','unknown',NULL,'before','before');
      BEGIN IMMEDIATE;`)
    const mobile = await loadMobileDatabase(file)
    const failures = await Promise.allSettled([mobile.getDatabase(), mobile.getDatabase()])
    assert.ok(failures.every(result => result.status === 'rejected' && /locked/.test(result.reason.message)))
    assert.equal(connections.length, 1, '并发读取应共用一次打开尝试')
    legacy.exec('ROLLBACK')
    const reopened = await mobile.getDatabase()
    assert.equal((await reopened.getFirstAsync('PRAGMA user_version')).user_version, 14)
    assert.equal(connections.length, 2, '释放锁后的显式读取应重新打开')
    assert.equal(connections[0].closed, true, '失败的连接必须关闭')
    assert.equal((await reopened.getFirstAsync('SELECT text FROM drafts')).text, '原草稿')
    assert.equal((await reopened.getFirstAsync('SELECT state FROM pending_submissions')).state, 'unknown')
    assert.equal((await reopened.getFirstAsync('SELECT engine FROM connection_profiles')).engine, 'codex')
    assert.deepEqual(await reopened.getAllAsync('PRAGMA foreign_key_check'), [])
  } finally {
    if (legacy.isTransaction) legacy.exec('ROLLBACK')
    legacy.close()
    closeTestConnections()
    await rm(root, { recursive: true, force: true })
  }
})

test('迁移事务后段失败完整回滚，重试和再次打开保留全部旧数据及双引擎身份约束', async () => {
  const root = await mkdtemp(join(tmpdir(), 'mobile-migration-rollback-'))
  const file = join(root, 'legacy.db')
  let inspector
  try {
    const state = await prepareMobileMigration(file, { workerId: 'worker', port: 2222,
      fingerprint: 'old-fingerprint', workspace: '/fixture', baseURL: 'http://127.0.0.1:1',
      thread: { id: 'old-thread', updatedAt: 1, turns: [] } })
    inspector = new DatabaseSync(file)
    inspector.exec(`CREATE TRIGGER fail_runtime_backfill BEFORE UPDATE ON connection_profiles
      BEGIN SELECT RAISE(ABORT,'fixture migration failure'); END;`)
    const mobile = await loadMobileDatabase(file)
    await assert.rejects(mobile.getDatabase(), /fixture migration failure/)
    assert.equal(inspector.prepare('PRAGMA user_version').get().user_version, 12)
    for (const [table, before] of Object.entries(state.snapshot)) {
      assert.deepEqual(inspector.prepare(`PRAGMA table_info(${table})`).all().map(row => row.name), before.columns)
      assert.deepEqual(inspector.prepare(`SELECT * FROM ${table} ORDER BY rowid`).all(), before.rows)
    }
    assert.equal(connections.at(-1).closed, true)
    inspector.exec('DROP TRIGGER fail_runtime_backfill')
    const retried = await mobile.getDatabase()
    assert.equal((await retried.getFirstAsync('PRAGMA user_version')).user_version, 14)
    closeTestConnections()
    const result = await verifyMobileMigration(state,
      { thread: { id: 'new-claude-thread', updatedAt: 2, turns: [] }, port: 3333, fingerprint: 'claude-fingerprint' })
    assert.equal(result.passed, true)
  } finally {
    inspector?.close()
    closeTestConnections()
    await rm(root, { recursive: true, force: true })
  }
})

test('未来版本仍明确拒绝且关闭未发布连接，重复打开不能降级或删除原数据', async () => {
  const root = await mkdtemp(join(tmpdir(), 'mobile-migration-future-'))
  const file = join(root, 'future.db')
  const database = new DatabaseSync(file)
  try {
    database.exec("CREATE TABLE protected_data(value TEXT); INSERT INTO protected_data VALUES ('保留'); PRAGMA user_version=15;")
    const mobile = await loadMobileDatabase(file)
    for (let index = 0; index < 2; index++) {
      await assert.rejects(mobile.getDatabase(), /版本高于当前客户端/)
      assert.equal(connections.length, index + 1)
      assert.ok(connections.every(connection => connection.closed))
      assert.equal(database.prepare('PRAGMA user_version').get().user_version, 15)
      assert.equal(database.prepare('SELECT value FROM protected_data').get().value, '保留')
      assert.deepEqual(database.prepare("SELECT name FROM sqlite_master WHERE type='table'").all().map(row => row.name), ['protected_data'])
    }
  } finally {
    database.close()
    closeTestConnections()
    await rm(root, { recursive: true, force: true })
  }
})
