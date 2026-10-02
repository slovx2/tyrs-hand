import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import test from 'node:test'
import { verifyArtifact } from './adapter-build.mjs'

test('仅接受固定提交、发布版本和 SHA-256 一致的适配器制品', () => {
  const bytes = Buffer.from('test artifact')
  const pin = {
    repository: 'https://github.com/slovx2/codex-harness-adapter',
    commit: 'a'.repeat(40), goModuleVersion: 'v0.2.0', artifacts: {},
  }
  const name = `codex-harness-adapter-pi_${pin.commit}_linux_amd64.tar.gz`
  pin.artifacts.pi = {
    url: `${pin.repository}/releases/download/v0.2.0/${name}`,
    sha256: createHash('sha256').update(bytes).digest('hex'),
  }
  assert.equal(verifyArtifact(pin, 'pi', bytes), name)
  assert.throws(() => verifyArtifact(pin, 'pi', Buffer.from('modified')), /SHA-256/)
  assert.throws(() => verifyArtifact({ ...pin, commit: 'b'.repeat(40) }, 'pi', bytes), /来源与版本/)
  assert.throws(() => verifyArtifact({ ...pin, goModuleVersion: 'v0.1.0' }, 'pi', bytes), /来源与版本/)
})
