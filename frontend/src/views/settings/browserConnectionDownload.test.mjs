import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

/**
 * 浏览器技能包下载文件名（F5 / T07）。
 *
 * download() 依赖浏览器 API，无法在 Node 里直接执行；这里对源码做接线断言：
 * 下载文件名必须是品牌化的 browser-skill-xiaoai.zip，且不得回引旧 weknora 名。
 */

const source = readFileSync(new URL('./BrowserConnectionSettings.vue', import.meta.url), 'utf8')

test('browser skill package downloads as browser-skill-xiaoai.zip (F5)', () => {
  assert.match(source, /link\.download = 'browser-skill-xiaoai\.zip'/)
  assert.match(source, /link\.href = url; link\.download = 'browser-skill-xiaoai\.zip'; link\.click\(\)/)
  assert.doesNotMatch(source, /browser-skill-weknora/)
})
