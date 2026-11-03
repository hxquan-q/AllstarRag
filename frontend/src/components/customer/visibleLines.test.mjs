import assert from 'node:assert/strict'
import test from 'node:test'
import { computeVisibleLines } from './visibleLines.ts'

/**
 * 欢迎区可见产线排序（T02 排序修复的行为回归）。
 * 直接测纯函数 computeVisibleLines，覆盖用户要求的两组场景：
 * 移动端 2/3 张、桌面 4/5 张；重点验证未超限选第二张不换位、
 * 超限选原本不可见的助手后首页能显示当前项且数量不超限。
 */

/** 造 n 条产线：id=id1..idn，name=L1..Ln。 */
function makeLines(n) {
  return Array.from({ length: n }, (_, i) => ({
    id: `id${i + 1}`,
    name: `L${i + 1}`,
    description: '',
    agentId: `id${i + 1}`,
    samples: [],
  }))
}
const ids = (arr) => arr.map((line) => line.id)

test('移动端未超限（2 张）：保留绑定顺序，选第二张不换位', () => {
  const lines = makeLines(2)
  assert.deepEqual(ids(computeVisibleLines(lines, undefined, 2)), ['id1', 'id2'])
  assert.deepEqual(ids(computeVisibleLines(lines, 'id1', 2)), ['id1', 'id2'])
  // 核心回归：选第二张后顺序不变（修复前会变成 [id2, id1]）
  assert.deepEqual(ids(computeVisibleLines(lines, 'id2', 2)), ['id1', 'id2'])
})

test('移动端超限（3 张）：当前选中置顶，其余按顺序补足 2 张，数量不超限', () => {
  const lines = makeLines(3)
  assert.deepEqual(ids(computeVisibleLines(lines, 'id1', 2)), ['id1', 'id2'])
  // 选原本不可见的第三张：首页显示当前项，且仍只有 2 张
  const picked = computeVisibleLines(lines, 'id3', 2)
  assert.deepEqual(ids(picked), ['id3', 'id1'])
  assert.equal(picked.length, 2)
  // 选第二张：置顶
  assert.deepEqual(ids(computeVisibleLines(lines, 'id2', 2)), ['id2', 'id1'])
})

test('桌面未超限（4 张）：保留绑定顺序，选第二/三张不换位', () => {
  const lines = makeLines(4)
  assert.deepEqual(ids(computeVisibleLines(lines, undefined, 4)), ['id1', 'id2', 'id3', 'id4'])
  // 核心回归：选第二张、第三张后顺序都不变（修复前会按选中重排）
  assert.deepEqual(ids(computeVisibleLines(lines, 'id2', 4)), ['id1', 'id2', 'id3', 'id4'])
  assert.deepEqual(ids(computeVisibleLines(lines, 'id3', 4)), ['id1', 'id2', 'id3', 'id4'])
})

test('桌面超限（5 张）：当前选中置顶，其余按顺序补足 4 张，数量不超限', () => {
  const lines = makeLines(5)
  assert.deepEqual(ids(computeVisibleLines(lines, 'id1', 4)), ['id1', 'id2', 'id3', 'id4'])
  // 选原本不可见的第五张：首页显示当前项，且仍是 4 张
  const picked = computeVisibleLines(lines, 'id5', 4)
  assert.deepEqual(ids(picked), ['id5', 'id1', 'id2', 'id3'])
  assert.equal(picked.length, 4)
  // 选第三张：置顶，其余按绑定顺序
  assert.deepEqual(ids(computeVisibleLines(lines, 'id3', 4)), ['id3', 'id1', 'id2', 'id4'])
})

test('超限且未传 currentId：按绑定顺序截断，不报错', () => {
  const lines = makeLines(5)
  assert.deepEqual(ids(computeVisibleLines(lines, undefined, 4)), ['id1', 'id2', 'id3', 'id4'])
})
