// 运行时配置（本地开发默认值）。
// 注意：Docker 部署时 docker-entrypoint.sh 会整体重写这个文件，
// 改品牌配置时两处都要同步；本地开发改这里后刷新页面即可生效，不用重新构建。
window.__RUNTIME_CONFIG__ = {
  MAX_FILE_SIZE_MB: 50,
  MAX_SKILL_BUNDLE_SIZE_MB: 256,
  // Optional: serve embed on a dedicated origin, e.g. 'https://embed.example.com'
  EMBED_BASE_URL: '',
  // Optional: default UI locale for first-time visitors (zh-CN | zh-TW | en-US | ru-RU | ko-KR | ja-JP)
  DEFAULT_LOCALE: '',
  // 品牌配置：产品名、logo、favicon、颜色、登录副标题、对话提示。
  // 颜色只支持 #RGB / #RRGGBB。
  // primaryColor 一处即可带动全站：交互色、登录渐变都由它派生；
  // actionColor 是可选覆盖项，只有显式配置时才生效（H3）。
  // logoWidth 仅作用于登录页 logo 宽度；侧栏 logo 固定 108px，与管理员一致。
  // 产线不在这里配置：它由「当前工作区绑定的 agent」派生（客户在设置里绑定 agent id）。
  BRAND: {
    productName: '小爱知识库',
    logoUrl: '/brand/allstar-logo.png',
    logoWidth: 150,
    faviconUrl: '/favicon.ico',
    primaryColor: '#0EA5E9',
    loginSubtitle: '金蝶产品知识，随问随答',
    chatDisclaimer: '回答由 AI 根据知识库生成，仅供参考',
  },
};
