import assert from 'node:assert/strict'
import test from 'node:test'

import {
  approvalEventFromRecord,
  buildHostResolveBody,
  canNarrowAccess,
  hostApprovalTitleKey,
  isHostApprovalEvent,
  sessionRuleLabels,
} from './hostApproval'
import { isOperationApproval } from './toolApproval'

test('only a sandbox block may be narrowed to read-only; a delete needs write', () => {
  assert.equal(canNarrowAccess({ reason: 'sandbox_denied', grant_path: '/x', grant_access: 'write' }), true)
  assert.equal(canNarrowAccess({ reason: 'delete', grant_path: '/x', grant_access: 'write' }), false)
  assert.equal(canNarrowAccess({ reason: 'dangerous', grant_path: '/x', grant_access: 'write' }), false)
  assert.equal(canNarrowAccess({ reason: 'sandbox_denied' }), false)
})

test('a stored approval becomes a resolved timeline event', () => {
  const event = approvalEventFromRecord({
    pending_id: 'pending-1',
    kind: 'host_command',
    tool_call_id: 'call-1',
    host: { reason: 'delete', command: 'rm a.txt' },
    resolved: true,
    approved: false,
    reason: 'user rejected',
    scope: 'once',
  })
  assert.equal(event.type, 'tool_approval_required')
  assert.equal(event.resolved, true)
  assert.equal(event.approved, false)
  assert.equal(event.resolve_reason, 'user rejected')
  assert.equal(event.resolve_scope, 'once')
  assert.equal(isHostApprovalEvent(event), true)
})

test('only host_command events use the host card', () => {
  assert.equal(isHostApprovalEvent({ kind: 'host_command' }), true)
  assert.equal(isHostApprovalEvent({ kind: '' }), false)
  assert.equal(isHostApprovalEvent({}), false)
  assert.equal(isHostApprovalEvent({ host: { reason: 'delete' } }), true)
})

test('titles follow the approval reason', () => {
  assert.equal(hostApprovalTitleKey('delete'), 'agentStream.hostApproval.titleDelete')
  assert.equal(hostApprovalTitleKey('dangerous'), 'agentStream.hostApproval.titleDangerous')
  assert.equal(hostApprovalTitleKey('sandbox_denied'), 'agentStream.hostApproval.titleDenied')
  assert.equal(hostApprovalTitleKey('unknown'), 'agentStream.hostApproval.titleGeneric')
})

test('session scope is only sent when the request allows it', () => {
  assert.deepEqual(buildHostResolveBody('approve', { scope: 'session', allowSession: true }), {
    decision: 'approve',
    scope: 'session',
  })
  assert.deepEqual(buildHostResolveBody('approve', { scope: 'session', allowSession: false }), {
    decision: 'approve',
    scope: 'once',
  })
})

test('access can only narrow a write proposal to read', () => {
  assert.deepEqual(
    buildHostResolveBody('approve', { scope: 'once', access: 'read', proposedAccess: 'write' }),
    { decision: 'approve', scope: 'once', access: 'read' },
  )
  assert.deepEqual(
    buildHostResolveBody('approve', { scope: 'once', access: 'write', proposedAccess: 'read' }),
    { decision: 'approve', scope: 'once' },
  )
})

test('the card lists the delete rules a session approval remembers', () => {
  assert.deepEqual(
    sessionRuleLabels({ reason: 'delete', allow_session: true, session_rules: ['rm -f -r', 'rmdir'] }),
    ['rm -f -r …', 'rmdir …'],
  )
  assert.deepEqual(sessionRuleLabels({ reason: 'delete', allow_session: false, session_rules: ['rm'] }), [])
  assert.deepEqual(sessionRuleLabels({ reason: 'sandbox_denied', allow_session: true }), [])
})

test('reject carries only the reason', () => {
  assert.deepEqual(buildHostResolveBody('reject', { reason: 'no', scope: 'session', allowSession: true }), {
    decision: 'reject',
    reason: 'no',
  })
})

// CP3 M1：历史回放读的是后端落库的审批记录。记录带了稳定注册工具名时原样
// 透传，审批卡据此判断是否操作卡（显示名随品牌配置变化，不能参与判别）。
test('replay keeps the stored registered_tool_name from the record (CP3 M1)', () => {
  const event = approvalEventFromRecord({
    pending_id: 'pending-new',
    service_name: '小爱知识库',
    registered_tool_name: 'run_operation',
    mcp_tool_name: 'run_operation',
    resolved: true,
    approved: true,
  })
  assert.equal(event.registered_tool_name, 'run_operation')
  assert.equal(isOperationApproval(event.registered_tool_name), true)
})

// CP3 M1：改名前的旧记录没有 registered_tool_name，当时的 service_name 是
// WeKnora，且当时就是按操作卡显示的；回放要继续走操作卡。
test('legacy WeKnora records still render as operation cards (CP3 M1)', () => {
  const withoutField = approvalEventFromRecord({
    pending_id: 'pending-legacy',
    service_name: 'WeKnora',
    mcp_tool_name: 'run_operation',
    resolved: true,
  })
  assert.equal(withoutField.registered_tool_name, 'run_operation')
  assert.equal(isOperationApproval(withoutField.registered_tool_name), true)

  const explicitUndefined = approvalEventFromRecord({
    pending_id: 'pending-legacy-2',
    service_name: 'WeKnora',
    registered_tool_name: undefined,
  })
  assert.equal(explicitUndefined.registered_tool_name, 'run_operation')
  assert.equal(isOperationApproval(explicitUndefined.registered_tool_name), true)
})

// CP3 M1：其它服务（MCP/改名后的新显示名）不能被 legacy 映射误判成操作卡。
test('other services are not mistaken for operations (CP3 M1)', () => {
  const docsMcp = approvalEventFromRecord({
    pending_id: 'pending-mcp',
    service_name: 'docs-mcp',
    mcp_tool_name: 'search_docs',
  })
  assert.equal(docsMcp.registered_tool_name, undefined)
  assert.equal(isOperationApproval(docsMcp.registered_tool_name), false)

  const renamedGeneric = approvalEventFromRecord({
    pending_id: 'pending-brand',
    service_name: '小爱知识库',
    mcp_tool_name: 'wiki_write_page',
  })
  assert.equal(renamedGeneric.registered_tool_name, undefined)
  assert.equal(isOperationApproval(renamedGeneric.registered_tool_name), false)
})

// CP3 M1：新字段优先，不能被 legacy 映射覆盖；非 run_operation 的新字段也
// 不应被改写成操作卡。
test('an explicit registered_tool_name wins over the legacy mapping (CP3 M1)', () => {
  const explicit = approvalEventFromRecord({
    pending_id: 'pending-explicit',
    service_name: 'WeKnora',
    registered_tool_name: 'mcp_other',
  })
  assert.equal(explicit.registered_tool_name, 'mcp_other')
  assert.equal(isOperationApproval(explicit.registered_tool_name), false)
})

// CP3 M1 跨端：后端 AgentStep.approvals 的 JSON（新旧两种形状）经
// approvalEventFromRecord 后仍能给出正确的操作卡判别。
test('backend approval record JSON round-trips to the right card kind (CP3 M1)', () => {
  const fresh = JSON.parse(
    '{"pending_id":"pending-1","service_name":"小爱知识库","registered_tool_name":"run_operation","resolved":true,"approved":true}',
  )
  assert.equal(isOperationApproval(approvalEventFromRecord(fresh).registered_tool_name), true)

  const legacy = JSON.parse('{"pending_id":"pending-0","service_name":"WeKnora","resolved":true}')
  assert.equal(isOperationApproval(approvalEventFromRecord(legacy).registered_tool_name), true)

  const legacyMcp = JSON.parse('{"pending_id":"pending-mcp","service_name":"docs-mcp","resolved":true}')
  assert.equal(isOperationApproval(approvalEventFromRecord(legacyMcp).registered_tool_name), false)
})
