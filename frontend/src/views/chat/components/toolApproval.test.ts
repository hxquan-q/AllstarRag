import assert from 'node:assert/strict'
import test from 'node:test'

import { isOperationApproval } from './toolApproval'

test('isOperationApproval identifies run_operation from the stable registered tool name (T06)', () => {
  assert.equal(isOperationApproval('run_operation'), true)
})

test('isOperationApproval rejects missing, empty, and other registered tool names (T06)', () => {
  assert.equal(isOperationApproval(''), false)
  assert.equal(isOperationApproval(undefined), false)
  assert.equal(isOperationApproval(null), false)
  assert.equal(isOperationApproval('mcp_x'), false)
  assert.equal(isOperationApproval('search_weknora_docs'), false)
})
