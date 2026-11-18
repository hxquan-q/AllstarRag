import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

/**
 * admin-theme-blue T02 的 TDD 红阶段用例：品牌绿静态素材与局部样式清零。
 * 写这些用例时 ② 尚未实现：A1/A2/A4/A5 预期失败（红），实现后应转绿；
 * A3 是锚定用例（现状就该过），防 ② 顺手删档/改多彩档。
 * 期望口径来自 T02 验收条件 5 与 01-spec AC-5/AC-9，不要为实现改这里的期望。
 *
 * 绿值集合来自 2026-11-17 对 frontend/src/assets/img/ 全量 78 个 svg 的盘点
 * （05-tests.md「TDD 红阶段 T02」段有完整文件↔色值清单，即给②的映射表基础）。
 * providers/、im/ 下是第三方品牌 logo（s3 #6cae3e、duckduckgo #65bc46、
 * wechat #07c160 等），属第三方品牌色不换（T02 验收条件 2）；它们与集合值
 * 均不相同，且下面用 token 精确比对（非子串），扫描全部 svg 不会误伤。
 */

/** 品牌绿值集合（统一 6 位小写；三位写法 #183 由归一化展开为 #118833 再比对）。 */
const BRAND_GREENS: string[] = [
  // 主题旧绿阶主档：亮 #07c05f（svg 内多为大写 #07C05F）、暗 #06b04d（当前未出现在
  // 任何 svg，防御性收进集合，防②映射时误引入；带 alpha 的 #06b04d20 由去 alpha 归一覆盖）
  '#07c05f',
  '#06b04d',
  // SpaceAvatar 主绿档 to 值，也是 screenshot 系列的标题主色
  '#059669',
  // screenshot-1..4 的 emerald 色板（深→浅；-2 独有 #064e3b，-1/-4 独有三位写法 #183）
  '#10b981',
  '#34d399',
  '#6ee7b7',
  '#a7f3d0',
  '#d1fae5',
  '#ecfdf5',
  '#f0fdf4',
  '#065f46',
  '#064e3b',
  '#118833',
  // upload.svg / upload-mask.svg 插画里的绿系
  '#39cd80',
  '#2cd87e',
  '#54e89a',
  '#6ee1a5',
  '#9edebd',
  '#b5eccf',
  '#dbfae9',
  '#e4f9ee',
  '#f0fff7',
  '#f3fff7',
]

/**
 * 提取文本里的全部 hex 色 token，归一为 6 位小写：
 * 三位写法展开（#183 → #118833）；8 位带 alpha 取前 6 位（#06b04d20 → #06b04d）。
 * 用 token 精确比对而非子串 includes，避免 #183 之类的短值误命中 #183abc。
 */
function hexColorTokens(text: string): Set<string> {
  const tokens = new Set<string>()
  for (const raw of text.match(/#[0-9a-fA-F]{3,8}\b/g) ?? []) {
    const lower = raw.toLowerCase()
    let base: string
    if (lower.length === 4) {
      base =
        '#' +
        lower.charAt(1) + lower.charAt(1) +
        lower.charAt(2) + lower.charAt(2) +
        lower.charAt(3) + lower.charAt(3)
    } else if (lower.length >= 7) {
      base = lower.slice(0, 7)
    } else {
      base = lower // 5/7 位不规则写法按原样保留，不会命中集合
    }
    tokens.add(base)
  }
  return tokens
}

/** 递归列出目录下全部 svg（含子目录，返回绝对路径，排序保证输出稳定）。 */
function listSvgFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) out.push(...listSvgFiles(full))
    else if (entry.endsWith('.svg')) out.push(full)
  }
  return out.sort()
}

test('svg static assets no longer contain brand green values', () => {
  const imgDir = fileURLToPath(new URL('../assets/img', import.meta.url))
  const offenders: string[] = []
  let scanned = 0
  for (const file of listSvgFiles(imgDir)) {
    scanned += 1
    const tokens = hexColorTokens(readFileSync(file, 'utf8'))
    for (const green of BRAND_GREENS) {
      if (tokens.has(green)) {
        offenders.push(`${relative(imgDir, file).split(sep).join('/')}: ${green}`)
      }
    }
  }
  assert.equal(scanned > 0, true, 'assets/img 下应能扫到 svg（检查路径解析是否失效）')
  assert.deepEqual(
    offenders,
    [],
    `assets/img 下 ${offenders.length} 处品牌绿未清零（文件: 色值，全清单见 05-tests.md T02 段）；` +
      'providers/、im/ 第三方 logo 色值不在集合内，命中即品牌绿残留'
  )
})

const avatarVue = readFileSync(new URL('../components/SpaceAvatar.vue', import.meta.url), 'utf8')

/** 截取源码里 `const gradients = [ ... ];` 数组块（选择器唯一）。 */
function gradientsBlock(source: string): string {
  const start = source.indexOf('const gradients = [')
  assert.notEqual(start, -1, 'SpaceAvatar 应保留 gradients 数组')
  const end = source.indexOf('];', start)
  return source.slice(start, end)
}

test('SpaceAvatar primary green gradient slot is replaced by sky-blue', () => {
  const text = avatarVue.toLowerCase()
  assert.equal(text.includes('#07c05f'), false, 'SpaceAvatar 不应再含主绿 #07c05f（主绿档 from，SpaceAvatar.vue:50）')
  assert.equal(text.includes('#059669'), false, 'SpaceAvatar 不应再含 #059669（主绿档 to）')
})

// 锚定：OQ-3 裁决只换「主绿」档，其余多彩档一律不动；档位数也不许少（防②顺手删档）。
test('SpaceAvatar keeps 12 gradient slots and the other colorful slots unchanged', () => {
  const block = gradientsBlock(avatarVue)
  const slots = block.match(/\{ from: '#[0-9a-f]{6}', to: '#[0-9a-f]{6}' \}/g) ?? []
  assert.equal(slots.length, 12, '渐变板应保持 12 档（只换主绿档，不删档）')
  const keptSlots: Array<[string, string]> = [
    ['#11998e', '#38ef7d'],
    ['#43e97b', '#38f9d7'],
    ['#02aab0', '#00cdac'],
    ['#36d1dc', '#5b86e5'],
    ['#4facfe', '#00f2fe'],
    ['#667eea', '#764ba2'],
    ['#4776e6', '#8e54e9'],
    ['#56ab2f', '#a8e063'],
    ['#00b09b', '#96c93d'],
    ['#5ee7df', '#b490ca'],
    ['#614385', '#516395'],
  ]
  for (const [from, to] of keptSlots) {
    assert.equal(
      block.includes(`{ from: '${from}', to: '${to}' }`),
      true,
      `多彩档 {${from} → ${to}} 应保持不动（OQ-3：只换主绿档）`
    )
  }
})

const dataSourceVue = readFileSync(
  new URL('../views/knowledge/settings/DataSourceSettings.vue', import.meta.url),
  'utf8'
)

/** 截取 `&__badge {` 规则块（块内只有圆括号嵌套，首个 `}` 即块尾）。 */
function badgeBlock(source: string): string {
  const start = source.indexOf('&__badge {')
  assert.notEqual(start, -1, 'DataSourceSettings 应保留 ds-card badge 规则')
  const open = source.indexOf('{', start)
  const close = source.indexOf('}', open)
  return source.slice(open + 1, close)
}

test('DataSourceSettings badge color follows brand var instead of legacy green', () => {
  const badge = badgeBlock(dataSourceVue)
  // 红点：现状 :466 是 `color: #07c05f;`（:465 的 background 已是 var，不构成本断言的干扰）。
  assert.match(
    badge,
    /color:\s*var\(--td-brand-color/,
    'badge 的 color 应改 var(--td-brand-color*)（T02 验收条件 3，DataSourceSettings.vue:466）'
  )
  assert.equal(
    dataSourceVue.toLowerCase().includes('#07c05f'),
    false,
    'DataSourceSettings.vue 不应再含 #07c05f 字面量'
  )
})

const parserVue = readFileSync(new URL('../views/settings/ParserEngineSettings.vue', import.meta.url), 'utf8')

test('ParserEngineSettings builtin engine colors follow brand var instead of legacy green', () => {
  // 红点 1：现状 :826 是 `.provider-card-badge-color(#07c05f);`（其余徽章色 #0052d9 等非绿不动）。
  assert.match(
    parserVue,
    /\.provider-card-badge-color\(\s*var\(--td-brand-color/,
    '内置引擎徽章色应改 var(--td-brand-color*)（T02 验收条件 3，ParserEngineSettings.vue:826）'
  )
  // 红点 2：全文件旧绿字面量清零——含 :826 徽章与 :1151 抽屉头图标（826 的 non-scoped 镜像）两处。
  assert.equal(
    parserVue.toLowerCase().includes('#07c05f'),
    false,
    'ParserEngineSettings.vue 不应再含 #07c05f 字面量（:826 与 :1151 两处）'
  )
})
