import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const read = (name: string) => readFileSync(new URL(name, import.meta.url), 'utf8')

test('ToolApprovalCard keys operation detection off the stable registered tool name (T06)', () => {
  const source = read('./ToolApprovalCard.vue')
  assert.match(source, /isOperationApproval/, 'ToolApprovalCard 应调用 isOperationApproval 判别操作卡')
  assert.match(source, /registeredToolName/, 'ToolApprovalCard 应声明/使用 registeredToolName prop')
  assert.doesNotMatch(
    source,
    /serviceName\s*===\s*['"]WeKnora['"]/,
    'ToolApprovalCard 不应再用后端显示名 WeKnora 判别操作卡',
  )
})

test('every ToolApprovalCard call site forwards registered-tool-name (T06)', () => {
  const source = read('./AgentStreamDisplay.vue')
  const openings = source.match(/<ToolApprovalCard\b/g)?.length ?? 0
  const cards = [...source.matchAll(/<ToolApprovalCard\b[\s\S]*?\/>/g)].map((match) => match[0])
  assert.ok(openings >= 4, `AgentStreamDisplay 至少应有 4 个 ToolApprovalCard 调用点，实际 ${openings}`)
  assert.equal(cards.length, openings, '每个 ToolApprovalCard 调用点都应能被自闭合标签匹配')
  cards.forEach((card, index) => {
    assert.match(card, /registered-tool-name/, `第 ${index + 1} 个 ToolApprovalCard 未传 registered-tool-name`)
    assert.match(card, /registered_tool_name/, `第 ${index + 1} 个 ToolApprovalCard 未绑定 registered_tool_name`)
  })
})

test('PendingPromptDock forwards registered-tool-name to ToolApprovalCard (T06)', () => {
  const source = read('./PendingPromptDock.vue')
  const cards = [...source.matchAll(/<ToolApprovalCard\b[\s\S]*?\/>/g)].map((match) => match[0])
  assert.ok(cards.length >= 1, 'PendingPromptDock 应包含 ToolApprovalCard 调用点')
  cards.forEach((card, index) => {
    assert.match(card, /registered-tool-name/, `第 ${index + 1} 个 ToolApprovalCard 未传 registered-tool-name`)
    assert.match(card, /registered_tool_name/, `第 ${index + 1} 个 ToolApprovalCard 未绑定 registered_tool_name`)
  })
})

test('both tool_approval_required event factories keep registered_tool_name (T06)', () => {
  const source = read('../../../composables/useChatStreamHandler.ts')
  const factories = [...source.matchAll(/type:\s*['"]tool_approval_required['"]/g)]
  assert.ok(factories.length >= 2, `应有至少两条 tool_approval_required 构造，实际 ${factories.length}`)
  factories.forEach((match, index) => {
    const window = source.slice(match.index, match.index + 1000)
    assert.match(window, /registered_tool_name/, `第 ${index + 1} 条 tool_approval_required 构造未保留 registered_tool_name`)
  })
})

test('stored approval records keep registered_tool_name (T06)', () => {
  const source = read('./hostApproval.ts')
  const start = source.indexOf('export function approvalEventFromRecord')
  const end = source.indexOf('export function clearComposerApproval')
  assert.ok(start >= 0 && end > start, '应能定位 approvalEventFromRecord 函数体')
  assert.match(source.slice(start, end), /registered_tool_name/, 'approvalEventFromRecord 应保留 registered_tool_name')
})
