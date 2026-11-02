import assert from 'node:assert/strict'
import test from 'node:test'
import { resolveOnboardingSurfaces } from './workspaceOnboarding'

test('access revoked hides create and invitations, keeps only the notice', () => {
  for (const canCreate of [true, false]) {
    const surfaces = resolveOnboardingSurfaces(canCreate, true)
    assert.equal(surfaces.showCreate, false)
    assert.equal(surfaces.showInvitations, false)
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
