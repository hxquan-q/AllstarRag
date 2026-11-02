import type { BrandConfig } from './types'

/**
 * 内置默认品牌。
 * public/config.js 与 docker-entrypoint.sh 里的默认值要和这里保持一致；
 * 运行时配置缺字段时回落到这份数据。
 * 产线不再由品牌配置提供：它由「当前工作区绑定的 agent」派生（见 stores/productLine.ts）。
 */
export const DEFAULT_BRAND: BrandConfig = {
  productName: '小爱知识库',
  logoUrl: '/brand/allstar-logo.png',
  logoWidth: 150,
  faviconUrl: '/favicon.ico',
  primaryColor: '#0EA5E9',
  // 运行时通常不配置 actionColor，由 primaryColor 派生；这个值就是默认主色的派生结果。
  // 配置里显式写了 actionColor 才覆盖（H3）。
  actionColor: '#0284C7',
  loginSubtitle: '金蝶产品知识，随问随答',
  chatDisclaimer: '回答由 AI 根据知识库生成，仅供参考',
}
