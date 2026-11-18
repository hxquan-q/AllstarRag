import { brandCssVars, deriveActionColor, isValidHex } from './colors'
import type { BrandMode } from './colors'
import { DEFAULT_BRAND } from './defaults'
import type { BrandConfig, WindowWithBrandConfig } from './types'

function warn(message: string): void {
  console.warn(`[brand] ${message}`)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim() !== ''
}

/** 把配置值转成可读文本，避免对象打印成 [object Object]。 */
function describe(value: unknown): string {
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value) ?? String(value)
  } catch {
    return String(value)
  }
}

function cloneBrand(config: BrandConfig): BrandConfig {
  // 品牌字段都是原始值（产线已不再由品牌配置提供），浅拷贝即可。
  return { ...config }
}

function pickString(raw: Record<string, unknown>, key: keyof BrandConfig, fallback: string): string {
  const value = raw[key]
  if (value === undefined || value === null) {
    warn(`BRAND.${key} 缺失，使用默认值「${fallback}」`)
    return fallback
  }
  if (!isNonEmptyString(value)) {
    warn(`BRAND.${key} 不是非空字符串（收到 ${describe(value)}），使用默认值「${fallback}」`)
    return fallback
  }
  return value.trim()
}

function pickLogoWidth(raw: Record<string, unknown>, fallback: number): number {
  const value = raw.logoWidth
  if (value === undefined || value === null) {
    warn(`BRAND.logoWidth 缺失，使用默认宽度 ${fallback}px`)
    return fallback
  }
  const width =
    typeof value === 'number'
      ? value
      : typeof value === 'string' && value.trim() !== ''
        ? Number(value)
        : Number.NaN
  if (!Number.isFinite(width) || width <= 0) {
    warn(`BRAND.logoWidth 必须是大于 0 的数字（收到 ${describe(value)}），使用默认宽度 ${fallback}px`)
    return fallback
  }
  return Math.round(width)
}

function pickColor(raw: Record<string, unknown>, key: keyof BrandConfig, fallback: string): string {
  const value = raw[key]
  if (value === undefined || value === null) {
    warn(`BRAND.${key} 缺失，使用默认颜色 ${fallback}`)
    return fallback
  }
  if (!isValidHex(value)) {
    warn(`BRAND.${key} 必须是 #RGB 或 #RRGGBB 格式（收到 ${describe(value)}），使用默认颜色 ${fallback}`)
    return fallback
  }
  return value.trim()
}

/**
 * 交互色是可选项：没配就由主色自动派生（H3），配了非法值则告警后派生。
 * 派生函数会保证白字对比度，默认主色得到 #0284C7，与改造前一致。
 */
function pickActionColor(raw: Record<string, unknown>, primaryColor: string): string {
  const value = raw.actionColor
  // 没配置是正常情况：默认按主色派生，不告警，避免每次启动都刷一条“配置缺失”。
  if (value === undefined || value === null) {
    return deriveActionColor(primaryColor)
  }
  if (!isValidHex(value)) {
    warn(`BRAND.actionColor 必须是 #RGB 或 #RRGGBB 格式（收到 ${describe(value)}），按 BRAND.primaryColor 自动派生`)
    return deriveActionColor(primaryColor)
  }
  return value.trim()
}

/**
 * 读取并校验一份品牌原始配置。
 * 整体缺失、字段缺失或字段非法时都回落到默认值，不抛错。
 * 这个函数不依赖 window，方便单元测试直接传对象。
 */
export function readBrandConfig(raw: unknown): BrandConfig {
  if (!isRecord(raw)) {
    warn('未找到 window.__RUNTIME_CONFIG__.BRAND，使用内置默认品牌')
    return cloneBrand(DEFAULT_BRAND)
  }

  // 交互色依赖主色，所以先定主色再派生/覆盖 actionColor。
  const primaryColor = pickColor(raw, 'primaryColor', DEFAULT_BRAND.primaryColor)

  return {
    productName: pickString(raw, 'productName', DEFAULT_BRAND.productName),
    logoUrl: pickString(raw, 'logoUrl', DEFAULT_BRAND.logoUrl),
    logoWidth: pickLogoWidth(raw, DEFAULT_BRAND.logoWidth),
    faviconUrl: pickString(raw, 'faviconUrl', DEFAULT_BRAND.faviconUrl),
    wordmarkUrl: pickString(raw, 'wordmarkUrl', DEFAULT_BRAND.wordmarkUrl),
    primaryColor,
    actionColor: pickActionColor(raw, primaryColor),
    loginSubtitle: pickString(raw, 'loginSubtitle', DEFAULT_BRAND.loginSubtitle),
    chatDisclaimer: pickString(raw, 'chatDisclaimer', DEFAULT_BRAND.chatDisclaimer),
  }
}

function readRawBrandFromWindow(): unknown {
  if (typeof window === 'undefined') return undefined
  return (window as WindowWithBrandConfig).__RUNTIME_CONFIG__?.BRAND
}

/** 启动时读取一次的品牌配置单例。 */
const startupRawBrand = readRawBrandFromWindow()

/** 品牌配置运行时只读：BrandConfig 全扁平，单层冻结足够，防止调用方改到单例。 */
export const brandConfig: Readonly<BrandConfig> = Object.freeze(readBrandConfig(startupRawBrand))

/**
 * 浅色、深色各算一份品牌变量。
 * 配置只在启动时读取一次，两份结果提前算好，主题切换时直接套用，不重复计算。
 */
const brandVarsByMode: Record<BrandMode, Record<string, string>> = {
  light: brandCssVars(brandConfig, 'light'),
  dark: brandCssVars(brandConfig, 'dark'),
}

/** 当前生效的深浅模式以 theme.css 读取的 html[theme-mode] 为准。 */
function effectiveThemeMode(): BrandMode {
  return document.documentElement.getAttribute('theme-mode') === 'dark' ? 'dark' : 'light'
}

function writeBrandCssVars(mode: BrandMode): void {
  const style = document.documentElement.style
  for (const [name, value] of Object.entries(brandVarsByMode[mode])) {
    style.setProperty(name, value)
  }
}

/**
 * 把品牌主题应用到文档，供登录页（T03）和登录后的角色判定（T04）调用。
 * 管理端主色同为品牌色（C6 2026-11-18 修订）：亮/暗品牌变量对任何角色都按当前
 * 深浅模式写入；enabled 只控制 data-customer 标记的挂/摘，客户版专属视觉
 * （圆角/阴影/线条）锁在该标记的作用域内，管理端不挂即不命中。
 */
export function applyCustomerTheme(enabled: boolean): void {
  if (typeof document === 'undefined') return

  writeBrandCssVars(effectiveThemeMode())
  if (enabled) {
    document.documentElement.dataset.customer = 'true'
  } else {
    document.documentElement.removeAttribute('data-customer')
  }
}

let themeObserverInstalled = false

/**
 * 启动时调用一次：监听 theme-mode 与 data-customer 变化，按当前模式重写品牌变量。
 * 这里只跟随 useTheme / 调用方已经维护的属性，不另建主题系统。
 */
export function initCustomerBrandTheme(): void {
  if (typeof document === 'undefined' || themeObserverInstalled) return
  themeObserverInstalled = true

  // 页面在 main.ts 执行前就带上 data-customer 时（例如服务端注入），先补一次。
  if (document.documentElement.dataset.customer === 'true') {
    writeBrandCssVars(effectiveThemeMode())
  }

  new MutationObserver(() => {
    // theme-mode 切换时跟随重写；data-customer 被外部移除只影响客户版专属视觉，
    // 品牌色变量对任何角色都在场，不再随之清空。
    writeBrandCssVars(effectiveThemeMode())
  }).observe(document.documentElement, {
    attributes: true,
    attributeFilter: ['theme-mode', 'data-customer'],
  })
}

export interface ApplyDocumentBrandOptions {
  /** 是否应用客户版主题：客户版传 true，非客户传 false（缺省按 false 处理） */
  customer?: boolean
  /** 是否覆盖浏览器标题：默认覆盖为品牌名，仅显式传 false 时跳过（当前无调用方使用） */
  title?: boolean
}

function applyFavicon(url: string): void {
  let link = document.querySelector<HTMLLinkElement>('link[rel~="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }
  link.href = url
}

/**
 * 把品牌应用到文档：标题、favicon、客户版标记与品牌色变量。
 * 品牌色变量对任何角色都写入，customer 选项只控制客户版标记的挂/摘。
 */
export function applyDocumentBrand(options: ApplyDocumentBrandOptions = {}): void {
  if (typeof document === 'undefined') return

  applyCustomerTheme(options.customer === true)

  if (options.title !== false) {
    document.title = brandConfig.productName
  }
  applyFavicon(brandConfig.faviconUrl)
}
