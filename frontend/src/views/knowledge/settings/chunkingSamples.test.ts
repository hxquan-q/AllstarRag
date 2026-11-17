import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { CHUNKING_SAMPLES, DEFAULT_SAMPLE_ID } from './chunkingSamples'

/**
 * 分块预览示例（F6 / T07）。
 *
 * 默认示例必须是与产品无关的中性文档：不出现上游项目名/仓库 URL，也不能
 * 把品牌名写成「开源框架」。示例 id 与默认选择保持不变（UI 接线契约）。
 */

const source = readFileSync(new URL('./chunkingSamples.ts', import.meta.url), 'utf8')

const sampleById = id => CHUNKING_SAMPLES.find(sample => sample.id === id)

test('default chunking sample is a neutral document with no upstream brand or URL (F6)', () => {
  assert.equal(DEFAULT_SAMPLE_ID, 'markdown')
  const markdown = sampleById('markdown')
  assert.ok(markdown, 'markdown sample missing')
  assert.match(markdown.text, /^# 分块示例文档/)
  assert.match(markdown.text, /```yaml/)
  assert.match(markdown.text, /^\| 内容类型 \| 建议分块方式 \|$/m)
  assert.doesNotMatch(markdown.text, /Tencent|WeKnora|weknora|开源框架|https?:\/\//)
  assert.doesNotMatch(markdown.text, /小爱/)
})

test('chunking samples source has no upstream brand or "开源框架" wording (F6)', () => {
  assert.doesNotMatch(source, /Tencent/)
  assert.doesNotMatch(source, /WeKnora/)
  assert.doesNotMatch(source, /开源框架/)
  assert.deepEqual(
    CHUNKING_SAMPLES.map(sample => sample.id),
    ['markdown', 'faq', 'chapter', 'plain'],
  )
})
