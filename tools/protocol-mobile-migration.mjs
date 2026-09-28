import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { DatabaseSync } from 'node:sqlite'
import { closeTestConnections, loadMobileDatabase } from './mobile-e2e/fixtures/sqlite-driver.mjs'

const tables = ['connection_profiles', 'control_machine_links', 'ssh_projects', 'projects',
  'threads', 'thread_reads', 'drafts', 'pending_submissions', 'outbox', 'pending_message_previews', 'app_settings']
const cachedThread = thread => JSON.stringify({ thread: { ...thread, sessionId: thread.id },
  archived: false, workspaceId: null, projectId: 'project', history: { kind: 'summary' } })

export async function verifyLegacyMobileSchema(sourcePath, commit) {
  const source = await readFile(sourcePath, 'utf8')
  assert.match(source, /DATABASE_VERSION = 12;/)
  const sql = name => {
    const match = source.match(new RegExp('const ' + name + ' = `([\\s\\S]*?)`;'))
    assert.ok(match, `旧源码缺少 ${name}`)
    return match[1]
  }
  const expected = `-- 固定升级前客户端 ${commit} 的 v12 建表语句，保留真实索引和约束。\n` +
    sql('schema') + sql('machineSchema') + '\nPRAGMA user_version=12;\n'
  assert.equal(await readFile(new URL('./mobile-e2e/fixtures/database-v12.sql', import.meta.url), 'utf8'), expected)
}

// 输入历史来自旧 Worker 的真实 thread/read；这里只建立升级前客户端本应持有的缓存和待发数据。
export async function prepareMobileMigration(file, { workerId, port, fingerprint, workspace, thread, baseURL }) {
  const database = new DatabaseSync(file)
  try {
    database.exec(await readFile(new URL('./mobile-e2e/fixtures/database-v12.sql', import.meta.url), 'utf8'))
    database.prepare('INSERT INTO connection_profiles VALUES (?,?,?,?,?,?,?,?,?,?,?,?)').run(
      'old-profile', 'machine', '升级前 Codex', 1, fingerprint, '127.0.0.1', port,
      'developer', 'retained-key-reference', fingerprint, 'before', 'before')
    database.prepare('INSERT INTO control_machine_links VALUES (?,?,?,?,?,?,?,?)').run(
      'old-profile', 'old-control', baseURL, workerId, 'Worker', 'device-fixture', 'before', 'before')
    database.prepare('INSERT INTO ssh_projects VALUES (?,?,?,?,?)').run('old-profile', 'project', workspace, 'before', 'before')
    database.prepare('INSERT INTO projects VALUES (?,?,?,?,?,?,?)').run('old-profile', 'project', 'workspace',
      '旧项目', workspace, JSON.stringify({ id: 'project', workspaceId: null, name: '旧项目',
        relativePath: workspace, cwd: workspace, kind: 'directory', availabilityStatus: 'available',
        branch: null, dirty: false }), 'before')
    database.prepare('INSERT INTO threads VALUES (?,?,?,?,?)').run('old-profile', thread.id, 0, thread.updatedAt,
      cachedThread(thread))
    database.prepare('INSERT INTO thread_reads VALUES (?,?,?,?)').run('old-profile', thread.id, 1, 'before')
    const preferences = JSON.stringify({ model: 'mock-model', effort: 'high', serviceTier: null,
      collaborationMode: 'default', permissions: ':workspace' })
    database.prepare('INSERT INTO drafts VALUES (?,?,?,?,?,?)').run('old-profile', thread.id,
      '尚未发送的草稿', preferences, '[]', 'before')
    const pending = JSON.stringify({ threadId: thread.id, input: [{ type: 'text', text: '待核实是否发送' }] })
    database.prepare('INSERT INTO pending_submissions VALUES (?,?,?,?,?,?,?,?,?)').run('old-profile',
      'pending-message', thread.id, 'project', pending, 'unknown', '连接中断', 'before', 'before')
    database.prepare('INSERT INTO outbox VALUES (?,?,?,?,?,?,?,?,?,?,?)').run('old-profile', 'outbox-message',
      'submit_message', 'project', thread.id, pending, 'failed', 2, '保留错误', 'before', 'before')
    database.prepare('INSERT INTO pending_message_previews VALUES (?,?,?,?,?,?,?)').run('old-profile',
      'pending-message', thread.id, 'project', '待发送预览', '[]', 'before')
    database.prepare('INSERT INTO app_settings VALUES (?,?)').run('lastTurnPreferences:old-profile', preferences)
    database.prepare('INSERT INTO app_settings VALUES (?,?)').run('selectedProject:old-profile', 'project')
    const snapshot = Object.fromEntries(tables.map(table => [table, {
      columns: database.prepare(`PRAGMA table_info(${table})`).all().map(row => row.name),
      rows: database.prepare(`SELECT * FROM ${table} ORDER BY rowid`).all(),
    }]))
    return { file, snapshot, threadId: thread.id, workerId, fingerprint, port, workspace }
  } finally { database.close() }
}

export async function verifyMobileMigration(state, { thread: claudeThread, port, fingerprint }) {
  const mobile = await loadMobileDatabase(state.file)
  try {
    const database = await mobile.getDatabase()
    assert.equal((await database.getFirstAsync('PRAGMA user_version')).user_version, 13)
    for (const [table, before] of Object.entries(state.snapshot)) {
      const rows = await database.getAllAsync(`SELECT ${before.columns.join(',')} FROM ${table} ORDER BY rowid`)
      assert.deepEqual(rows, before.rows, `${table} 的旧 ID、内容或状态发生变化`)
    }
    const identity = await database.getFirstAsync('SELECT engine,worker_id FROM connection_profiles')
    assert.equal(identity.engine, 'codex')
    assert.equal(identity.worker_id, state.workerId)
    assert.equal((await database.getFirstAsync('SELECT engine FROM control_machine_links')).engine, 'codex')
    // 同机器允许新 Claude 入口；相同提交 ID 和缓存 ID 在数据库层也必须隔离。
    await database.runAsync(`INSERT INTO connection_profiles(profile_id,kind,name,active,machine_fingerprint,
      ssh_host,ssh_port,ssh_user,ssh_key_ref,ssh_host_fingerprint,created_at,updated_at,engine,worker_id)
      VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, 'claude-profile', 'machine', 'Claude', 0, state.fingerprint,
    '127.0.0.1', port, 'developer', 'claude-key-reference', fingerprint, 'after', 'after',
    'claude-code', state.workerId)
    await database.runAsync('INSERT INTO threads VALUES (?,?,?,?,?)', 'claude-profile', claudeThread.id, 0,
      claudeThread.updatedAt, cachedThread(claudeThread))
    await database.runAsync('INSERT INTO pending_submissions VALUES (?,?,?,?,?,?,?,?,?)',
      'claude-profile', 'pending-message', claudeThread.id, 'project', '{}', 'prepared', null, 'after', 'after')
    await assert.rejects(database.runAsync("UPDATE connection_profiles SET engine='claude-code' WHERE profile_id='old-profile'"), /不可变/)
    await assert.rejects(database.runAsync(`UPDATE control_machine_links SET profile_id='claude-profile'
      WHERE profile_id='old-profile'`), /FOREIGN KEY/)
    await database.execAsync(`INSERT INTO control_machine_links
      SELECT 'claude-profile',server_id,base_url,worker_id,'claude-code',worker_name,device_id,'after','after'
      FROM control_machine_links WHERE profile_id='old-profile'`)
    assert.equal((await database.getFirstAsync('SELECT count(*) AS n FROM pending_submissions')).n, 2)
    assert.deepEqual(await database.getAllAsync('PRAGMA foreign_key_check'), [])
    await database.closeAsync()
    const reopened = await (await loadMobileDatabase(state.file)).getDatabase()
    assert.equal((await reopened.getFirstAsync('PRAGMA user_version')).user_version, 13)
    assert.equal((await reopened.getFirstAsync('SELECT count(*) AS n FROM connection_profiles')).n, 2)
    assert.equal((await reopened.getFirstAsync('SELECT count(*) AS n FROM control_machine_links')).n, 2)
    for (const [table, before] of Object.entries(state.snapshot)) {
      const where = before.columns.includes('profile_id') ? " WHERE profile_id='old-profile'" : ''
      assert.deepEqual(await reopened.getAllAsync(`SELECT ${before.columns.join(',')} FROM ${table}${where} ORDER BY rowid`), before.rows)
    }
    return { passed: true, fromVersion: 12, toVersion: 13, retainedTables: tables,
      oldThreadId: state.threadId, newClaudeThreadId: claudeThread.id, reopened: true,
      profiles: await reopened.getAllAsync(`SELECT profile_id,engine,worker_id,ssh_host,ssh_port,ssh_user,
        ssh_key_ref,ssh_host_fingerprint FROM connection_profiles ORDER BY profile_id`),
      limitations: ['真实 SQLite 执行产品迁移；Expo 原生绑定由 Node SQLite 驱动替换，不代表 Android 手工 GUI 验收'] }
  } finally { closeTestConnections() }
}
