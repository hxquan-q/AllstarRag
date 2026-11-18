import { fileURLToPath, URL } from 'node:url'
import { resolve, dirname } from 'node:path'
import { existsSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { copyPdfJsWasm } from './scripts/copy-pdfjs-wasm.mjs'
import { createRequire } from 'node:module'
import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import vueJsx from '@vitejs/plugin-vue-jsx'

const __dirname = dirname(fileURLToPath(import.meta.url))
const require = createRequire(import.meta.url)

const pkg = require('./package.json') as { version?: string }
const FRONTEND_VERSION = pkg.version ?? 'unknown'

function resolveFrontendCommit(): string {
  const fromEnv = process.env.VITE_FRONTEND_COMMIT || process.env.GITHUB_SHA
  if (fromEnv) {
    return fromEnv.slice(0, 7)
  }
  try {
    return execSync('git rev-parse --short HEAD', { stdio: ['ignore', 'pipe', 'ignore'] })
      .toString()
      .trim()
  } catch {
    return 'unknown'
  }
}

const FRONTEND_COMMIT = resolveFrontendCommit()

/** Dev parity with nginx: serve embed.html for /embed/:channelId (not the main SPA). */
function embedHtmlDevFallback(): Plugin {
  return {
    name: 'embed-html-dev-fallback',
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        const raw = req.url ?? ''
        const qIdx = raw.indexOf('?')
        const path = qIdx >= 0 ? raw.slice(0, qIdx) : raw
        const qs = qIdx >= 0 ? raw.slice(qIdx) : ''
        if (path.startsWith('/embed/') && path !== '/embed.html' && !path.includes('.')) {
          req.url = `/embed.html${qs}`
        }
        next()
      })
    },
  }
}
/**
 * demo 环境由 `vite preview` 提供，默认所有静态文件都带 `Cache-Control: no-cache`。
 * 浏览器仍可能复用旧的 index.html（旧 dist 的 title 是 WeKnora），升级后标签页会先显示旧品牌。
 * 这里只对 HTML / SPA fallback 强制 `no-store`；带 hash 的 /assets/* 保持原缓存策略。
 */
function noStoreHtmlPreview(): Plugin {
  const skipPrefixes = ['/api', '/files', '/mcp']

  return {
    name: 'no-store-html-preview',
    configurePreviewServer(server) {
      server.middlewares.use((req, res, next) => {
        const rawUrl = req.url ?? '/'
        const path = rawUrl.split('?')[0]
        const isProxyPath = skipPrefixes.some(
          (prefix) => path === prefix || path.startsWith(`${prefix}/`),
        )
        const lastSegment = path.slice(path.lastIndexOf('/') + 1)
        const isHtmlOrSpaFallback =
          path.endsWith('/') || lastSegment.endsWith('.html') || !lastSegment.includes('.')
        if (isProxyPath || !isHtmlOrSpaFallback) {
          next()
          return
        }

        // Vite 的静态中间件与 SPA fallback 都会在响应阶段重写 Cache-Control，
        // 所以在写出响应头之前统一改回 no-store。
        const originalWriteHead = res.writeHead.bind(res) as (...args: unknown[]) => unknown
        res.writeHead = ((...args: unknown[]) => {
          res.setHeader('Cache-Control', 'no-store')
          // sirv 只传对象形式的 headers，改对象即可；res.setHeader 已兜底。
          const headersArg = args.find((arg) => arg !== null && typeof arg === 'object')
          if (headersArg && !Array.isArray(headersArg)) {
            (headersArg as Record<string, unknown>)['Cache-Control'] = 'no-store'
          }
          return originalWriteHead(...args)
        }) as unknown as typeof res.writeHead
        next()
      })
    },
  }
}

const DEV_PROXY_TARGET =
  process.env.VITE_DEV_PROXY_TARGET ||
  process.env.FRONTEND_BACKEND_URL ||
  'http://localhost:8080'

function resolveVueOfficePptxEntry(): string {
  try {
    const pkgDir = dirname(require.resolve('@vue-office/pptx/package.json'))
    const candidates = [
      // Use ESM so setup hooks and render helpers share the application's Vue
      // runtime. The UMD entry's require('vue') can produce a second runtime in
      // Vite's dev optimizer, leaving template refs null and PPTX previews blank.
      resolve(pkgDir, 'lib/v3/vue-office-pptx.mjs'),
      resolve(pkgDir, 'lib/v3/index.js'),
      resolve(pkgDir, 'lib/index.js'),
    ]
    const matched = candidates.find((candidate) => existsSync(candidate))
    return matched ?? '@vue-office/pptx'
  } catch {
    return '@vue-office/pptx'
  }
}

function pdfJsWasmAssets(): Plugin {
  let wasmSource = ''
  let wasmOutput = ''
  return {
    name: 'pdfjs-wasm-assets',
    apply: 'build',
    configResolved(config) {
      const pdfjsBuild = require.resolve('pdfjs-dist/legacy/build/pdf.mjs')
      wasmSource = resolve(dirname(pdfjsBuild), '../../wasm')
      wasmOutput = resolve(config.root, config.build.outDir, 'pdfjs', 'wasm')
    },
    closeBundle() {
      copyPdfJsWasm(wasmSource, wasmOutput)
    },
  }
}

export default defineConfig({
  define: {
    __FRONTEND_VERSION__: JSON.stringify(FRONTEND_VERSION),
    __FRONTEND_COMMIT__: JSON.stringify(FRONTEND_COMMIT),
  },
  build: {
    modulePreload: {
      resolveDependencies(_filename, deps, { hostId }) {
        // Embed iframe bootstraps with token exchange only; defer heavy chat chunks.
        if (hostId?.includes('embed')) {
          return deps.filter((dep) => !(
            dep.includes('vendor-mermaid')
            || dep.includes('vendor-highlight')
            || dep.includes('vendor-markdown')
            || dep.includes('vendor-tdesign')
            || dep.includes('botmsg')
            || dep.includes('usermsg')
            || dep.includes('EmbedBotMessage')
            || dep.includes('EmbedUserMessage')
            || dep.includes('AgentStreamDisplay')
            || dep.includes('EmbedChatCore')
            || dep.includes('vendor-markdown')
            || dep.includes('fonts-')
          ))
        }
        return deps
      },
    },
    rollupOptions: {
      input: {
        main: resolve(__dirname, 'index.html'),
        embed: resolve(__dirname, 'embed.html'),
      },
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('mermaid') || id.includes('/dagre') || id.includes('cytoscape')) {
            return 'vendor-mermaid'
          }
          if (id.includes('marked') || id.includes('katex')) {
            return 'vendor-markdown'
          }
          if (id.includes('highlight.js')) {
            return 'vendor-highlight'
          }
        },
      },
    },
  },
  plugins: [
    vue(),
    vueJsx(),
    embedHtmlDevFallback(),
    noStoreHtmlPreview(),
    pdfJsWasmAssets(),
  ],
  resolve: {
    dedupe: ['vue', 'vue-demi'],
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      '@vue-office/pptx': resolveVueOfficePptxEntry(),
    },
  },
  server: {
    port: 5173,
    host: true,
    // 代理配置，用于开发环境
    // 各条代理都开 xfwd：把真实客户端 IP 写进 X-Forwarded-For 传给后端。
    // 不传的话后端看到的永远是 127.0.0.1，登录/注册限速会把所有公网
    // 用户当成同一个来源，一个人触发 429 就锁死全体。
    proxy: {
      '/mcp/': {
        target: DEV_PROXY_TARGET,
        changeOrigin: true,
        secure: false,
        xfwd: true,
        // Streamable HTTP may keep an SSE response open for long-running tools.
        timeout: 3_600_000,
        proxyTimeout: 3_600_000,
      },
      '/api': {
        target: DEV_PROXY_TARGET,
        changeOrigin: true,
        secure: false,
        xfwd: true,
        // 沙箱终端等 WebSocket 升级请求也走 /api，必须开启 WS 转发，
        // 否则浏览器侧握手失败、前端表现为"一直正在连接"。
        ws: true,
        // Cube fork snapshots pause a live MicroVM; 30s axios/proxy defaults
        // abort the POST and the backend then 500s on a canceled persist.
        timeout: 180_000,
        proxyTimeout: 180_000,
      },
      '/files': {
        target: DEV_PROXY_TARGET,
        changeOrigin: true,
        secure: false,
        xfwd: true,
      }
    }
  },
  // `vite preview` 用生产构建产物(dist)本地起服务，是最接近 release 镜像的环境：
  // 同样的压缩 / 拆包 / CSS 加载顺序，可提前暴露只在生产构建出现的问题
  // （如主题变量被打包顺序覆盖）。用法：npm run build && npm run preview
  preview: {
    port: 4173,
    host: true,
    // 同 server.proxy：必须转发真实客户端 IP，否则后端限速把所有公网
    // 用户当成 127.0.0.1 的一个桶。
    proxy: {
      '/mcp/': {
        target: DEV_PROXY_TARGET,
        changeOrigin: true,
        secure: false,
        xfwd: true,
        timeout: 3_600_000,
        proxyTimeout: 3_600_000,
      },
      '/api': {
        target: DEV_PROXY_TARGET,
        changeOrigin: true,
        secure: false,
        xfwd: true,
        ws: true,
        timeout: 180_000,
        proxyTimeout: 180_000,
      },
      '/files': {
        target: DEV_PROXY_TARGET,
        changeOrigin: true,
        secure: false,
        xfwd: true,
      }
    }
  }
})
