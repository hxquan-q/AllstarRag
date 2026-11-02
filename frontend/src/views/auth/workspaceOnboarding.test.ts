import assert from 'node:assert/strict'
import test from 'node:test'
import { resolveOnboardingSurfaces } from './workspaceOnboarding'

test('access revoked keeps the notice and create lock, but opens the invitation entry', () => {
  for (const canCreate of [true, false]) {
    const surfaces = resolveOnboardingSurfaces(canCreate, true)
    assert.equal(surfaces.showCreate, false, '被移除后不得出现自助创建空间入口')
    assert.equal(surfaces.showInvitations, true, '被移除后仍要放行「查看邀请」恢复入口')
    assert.equal(surfaces.showAccessRevoked, true)
  }
})

test('normal users keep the existing create/invite branches', () => {
  assert.deepEqual(resolveOnboardingSurfaces(true, false), {
    showCreate: true,
    showInvitations: true,
    showAccessRevoked: false,
  })
  assert.deepEqual(resolveOnboardingSurfaces(false, false), {
    showCreate: false,
    showInvitations: true,
    showAccessRevoked: false,
  })
})
