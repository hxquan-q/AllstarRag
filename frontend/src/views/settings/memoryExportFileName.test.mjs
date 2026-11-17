import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

/**
 * 记忆导出的文件名派生（L1 / C5 / F3 / F5 / T07）。
 *
 * memoryExportFileName 不是导出函数，这里从 MemorySettings.vue 源码切片出
 * 函数本体，用桩替掉品牌依赖后真实执行（不重写被测逻辑）：
 * - T07/F5：管理员与客户统一按品牌产品名派生 slug + 当天日期（本地时区），
 *   不再有管理员专属的 weknora-memories.json 特例；
 * - slug 不写死拼音之类罗马化字面量（C5，品牌值运行时可配）；
 * - 超长产品名截断到 60 字符并剪掉尾部连字符（F3）。
 */

const source = readFileSync(new URL('./MemorySettings.vue', import.meta.url), 'utf8')

const fnMatch = source.match(/const memoryExportFileName = \(\): string => \{[\s\S]*?\n\}/)
assert.ok(fnMatch, 'MemorySettings.vue must define memoryExportFileName')
// 源码是 TS，new Function 只认 JS：剥掉返回类型标注，其余逐字保留。
const fnSource = fnMatch[0].replace('const memoryExportFileName = (): string => {', 'const memoryExportFileName = () => {')
assert.ok(fnSource !== fnMatch[0], 'memoryExportFileName header changed; update this harness')
// 日期来源固定为注入值，保证断言不随跑测时刻漂移；实现若改了这行，这里立刻报错。
assert.ok(fnSource.includes('const now = new Date()'), 'memoryExportFileName must read the date via new Date()')
const fnWithFixedNow = fnSource.replace('const now = new Date()', 'const now = fixedNow')

const makeFileName = new Function(
  'productName',
  'fixedNow',
  `
  const brandConfig = { productName };
  ${fnWithFixedNow}
  return memoryExportFileName();
  `,
)

const fileNameFor = (productName, fixedNow = new Date(2026, 9, 27)) =>
  makeFileName(productName, fixedNow)

test('admin and customer exports share the brand-derived file name (F5/T07)', () => {
  // T07/F5：管理员不再返回 weknora-memories.json，两个角色统一走品牌派生。
  assert.equal(fileNameFor('小爱知识库'), '小爱知识库-memories-2026-10-27.json')
  assert.equal(fileNameFor('XiaoAi Knowledge_Base!'), 'xiaoai-knowledge-base-memories-2026-10-27.json')
})

test('export file name derives from the brand product name (L1)', () => {
  // 中文产品名保留原字符，仅拉丁部分转小写。
  assert.equal(fileNameFor('小爱知识库'), '小爱知识库-memories-2026-10-27.json')
  // 空白/下划线转连字符，非法字符剔除，连续连字符折叠。
  assert.equal(fileNameFor('XiaoAi Knowledge_Base!'), 'xiaoai-knowledge-base-memories-2026-10-27.json')
  // 拿不到有效字符时回退 memories，保证文件名始终可用。
  assert.equal(fileNameFor('!!!'), 'memories-memories-2026-10-27.json')
  assert.equal(fileNameFor('   '), 'memories-memories-2026-10-27.json')
})

test('date stamp follows the local calendar day', () => {
  // 本地日历日（1 月 9 日 → 01-09），月/日两位补零。
  assert.equal(fileNameFor('小爱知识库', new Date(2027, 0, 9)), '小爱知识库-memories-2027-01-09.json')
})

test('long product names are truncated to 60 slug characters (F3)', () => {
  // 截断后正好 60 个字符；超长名称不得超过文件系统友好的长度。
  const long = fileNameFor('a'.repeat(80))
  assert.equal(long, `${'a'.repeat(60)}-memories-2026-10-27.json`)
  // 截断落在连字符上时剪掉尾部连字符，不产生 “--memories”。
  const edge = fileNameFor(`${'a'.repeat(59)}-bb`)
  assert.equal(edge, `${'a'.repeat(59)}-memories-2026-10-27.json`)
})

test('handleExport wires the derived name into the download link', () => {
  assert.match(source, /link\.download = memoryExportFileName\(\)/)
  // T07/F5：源码不得再出现管理员专属的 weknora 文件名，也不得再按角色分支。
  assert.doesNotMatch(source, /weknora-memories/)
  assert.doesNotMatch(source, /isCustomerViewer/)
  assert.doesNotMatch(source, /useAuthStore/)
  assert.doesNotMatch(source, /xiaoi/)
  assert.match(source, /import \{ brandConfig \} from '@\/brand'/)
})
