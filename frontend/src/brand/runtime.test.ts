import assert from 'node:assert/strict'
import test from 'node:test'
import { deriveActionColor } from './colors.ts'
import { DEFAULT_BRAND } from './defaults.ts'
import { brandConfig, readBrandConfig } from './runtime.ts'

/**
 * 品牌配置解析是客户版所有运行时可配置能力的底座（AC-17 / AC-21 / C5）。
 * 这里只测纯函数与启动单例，不碰 DOM。
 * 产线已按 CP1 裁决移出品牌配置（C5），因此本文件不再有产线用例。
 */

function captureWarnings(): { lines: string[]; restore: () => void } {
  const lines: string[] = []
  const original = console.warn
  console.warn = (...args: unknown[]) => {
    lines.push(args.map((arg) => String(arg)).join(' '))
  }
  return {
    lines,
    restore: () => {
      console.warn = original
    },
  }
}

function withoutWarnings<T>(run: () => T): T {
  const capture = captureWarnings()
  try {
    return run()
  } finally {
    capture.restore()
  }
}

const GOOD_RAW = {
  productName: '测试品牌',
  logoUrl: '/brand/test.png',
  logoWidth: 180,
  faviconUrl: '/test.ico',
  primaryColor: '#123456',
  actionColor: '#234567',
  loginSubtitle: '测试副标题',
  chatDisclaimer: '测试提示',
}

test('readBrandConfig(undefined) falls back to the complete built-in brand', () => {
  const capture = captureWarnings()
  let config
  try {
    config = readBrandConfig(undefined)
  } finally {
    capture.restore()
  }
  assert.deepEqual(config, DEFAULT_BRAND)
  assert.ok(capture.lines.some((line) => line.includes('未找到') && line.includes('BRAND')))
})

test('readBrandConfig accepts a valid runtime config unchanged', () => {
  const config = withoutWarnings(() => readBrandConfig(GOOD_RAW))
  assert.equal(config.productName, '测试品牌')
  assert.equal(config.logoUrl, '/brand/test.png')
  assert.equal(config.logoWidth, 180)
  assert.equal(config.primaryColor, '#123456')
  assert.equal(config.actionColor, '#234567')
})

test('missing fields fall back one by one and the warning names the field', () => {
  const capture = captureWarnings()
  let config
  try {
    config = readBrandConfig({ productName: '只有名字' })
  } finally {
    capture.restore()
  }
  assert.equal(config.productName, '只有名字')
  assert.equal(config.logoUrl, DEFAULT_BRAND.logoUrl)
  assert.equal(config.primaryColor, DEFAULT_BRAND.primaryColor)
  for (const key of ['logoUrl', 'logoWidth', 'faviconUrl', 'primaryColor', 'loginSubtitle', 'chatDisclaimer']) {
    assert.ok(
      capture.lines.some((line) => line.includes(`BRAND.${key}`)),
      `warning should mention BRAND.${key}`,
    )
  }
  // actionColor 是可选覆盖项（H3）：不配不告警，按主色自动派生。
  assert.equal(config.actionColor, '#0284C7', 'default primary must derive the historical action color')
  assert.equal(
    capture.lines.some((line) => line.includes('BRAND.actionColor')),
    false,
    'omitting actionColor is normal and must not warn',
  )
})

test('invalid colors fall back to defaults with a locatable warning', () => {
  const capture = captureWarnings()
  let config
  try {
    config = readBrandConfig({ primaryColor: 'red', actionColor: '#12' })
  } finally {
    capture.restore()
  }
  assert.equal(config.primaryColor, DEFAULT_BRAND.primaryColor)
  assert.equal(config.actionColor, DEFAULT_BRAND.actionColor)
  assert.ok(capture.lines.some((line) => line.includes('BRAND.primaryColor') && line.includes('#RGB')))
  assert.ok(capture.lines.some((line) => line.includes('BRAND.actionColor') && line.includes('派生')))
})

test('actionColor derives from primaryColor and an explicit value still wins (H3)', () => {
  const derived = withoutWarnings(() => readBrandConfig({ primaryColor: '#7C3AED' }))
  assert.equal(derived.actionColor, deriveActionColor('#7C3AED'))
  assert.notEqual(derived.actionColor, DEFAULT_BRAND.actionColor)

  const explicit = withoutWarnings(() => readBrandConfig({ primaryColor: '#7C3AED', actionColor: '#111111' }))
  assert.equal(explicit.actionColor, '#111111')

  // 非法 actionColor 时按当前主色派生，而不是退回默认主色。
  const capture = captureWarnings()
  let invalid
  try {
    invalid = readBrandConfig({ primaryColor: '#7C3AED', actionColor: '#12' })
  } finally {
    capture.restore()
  }
  assert.equal(invalid.actionColor, deriveActionColor('#7C3AED'))
  assert.ok(capture.lines.some((line) => line.includes('BRAND.actionColor') && line.includes('派生')))
})

test('3-digit colors are accepted and logoWidth accepts numeric strings', () => {
  const config = withoutWarnings(() => readBrandConfig({ primaryColor: '#abc', actionColor: '#def', logoWidth: '168' }))
  assert.equal(config.primaryColor, '#abc')
  assert.equal(config.actionColor, '#def')
  assert.equal(config.logoWidth, 168)
})

test('invalid logoWidth falls back and warns', () => {
  const capture = captureWarnings()
  let config
  try {
    config = readBrandConfig({ logoWidth: -3 })
  } finally {
    capture.restore()
  }
  assert.equal(config.logoWidth, DEFAULT_BRAND.logoWidth)
  assert.ok(capture.lines.some((line) => line.includes('BRAND.logoWidth')))
})

test('non-object brand input falls back to the default brand', () => {
  withoutWarnings(() => {
    assert.deepEqual(readBrandConfig('oops'), DEFAULT_BRAND)
    assert.deepEqual(readBrandConfig([]), DEFAULT_BRAND)
  })
})

test('brand config no longer carries product lines (C5: lines come from bound agents)', () => {
  withoutWarnings(() => {
    // 旧版 config.js 残留 productLines 时也应被忽略，不再进入品牌配置。
    const legacy = readBrandConfig({
      ...GOOD_RAW,
      productLines: [{ id: 'legacy', name: '旧产线', agentId: 'agent-a' }],
    })
    for (const config of [DEFAULT_BRAND, brandConfig, legacy]) {
      assert.equal('productLines' in config, false, 'product lines must not live in brand config')
    }
  })
})

test('the returned config is a clone: mutating it never touches DEFAULT_BRAND', () => {
  const config = withoutWarnings(() => readBrandConfig(undefined))
  config.productName = '改了'
  assert.equal(DEFAULT_BRAND.productName, '小爱知识库')
})

test('startup brandConfig is frozen at the top level', () => {
  assert.equal(brandConfig.productName, DEFAULT_BRAND.productName)
  assert.ok(Object.isFrozen(brandConfig))
  assert.throws(() => {
    ;(brandConfig as { productName: string }).productName = 'x'
  }, TypeError)
})
