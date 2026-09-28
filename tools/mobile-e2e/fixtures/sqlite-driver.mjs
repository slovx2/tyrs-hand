import assert from 'node:assert/strict'
import { registerHooks } from 'node:module'
import { DatabaseSync } from 'node:sqlite'

// 仅替换 Expo 原生绑定；迁移仍执行产品 database.ts，SQL、文件锁与事务均由真实 SQLite 执行。
const databaseURL = new URL('../../../client/src/db/database.ts', import.meta.url).href
let path, sequence = 0
export const connections = []

registerHooks({ resolve(specifier, context, nextResolve) {
  if (specifier === 'expo-sqlite' && context.parentURL?.startsWith(databaseURL))
    return { url: import.meta.url, shortCircuit: true }
  return nextResolve(specifier, context)
} })

export async function loadMobileDatabase(filename) {
  path = filename
  return import(databaseURL + '?migration=' + ++sequence)
}

export async function openDatabaseAsync(name) {
  assert.equal(name, 'tyrs-hand.db')
  assert.ok(path)
  const database = new DatabaseSync(path)
  database.exec('PRAGMA busy_timeout=0; PRAGMA foreign_keys=ON')
  const state = { database, closed: false, transactions: 0 }
  connections.push(state)
  const adapter = {
    async execAsync(sql) { database.exec(sql) },
    async getFirstAsync(sql, ...params) { return database.prepare(sql).get(...params) ?? null },
    async getAllAsync(sql, ...params) { return database.prepare(sql).all(...params) },
    async runAsync(sql, ...params) { return database.prepare(sql).run(...params) },
    async closeAsync() { database.close(); state.closed = true },
    async withExclusiveTransactionAsync(operation) {
      database.exec('BEGIN IMMEDIATE')
      state.transactions++
      try { await operation(adapter); database.exec('COMMIT') }
      catch (error) { database.exec('ROLLBACK'); throw error }
    },
  }
  return adapter
}

export function closeTestConnections() {
  for (const state of connections) if (!state.closed) {
    state.database.close()
    state.closed = true
  }
  connections.length = 0
}
