/**
 * T46 测试基建：完整编译并挂载 SFC（含模板渲染）。
 *
 * 既有 vueFixture 只执行 script（render 被置 null），无法断言模板渲染分支；
 * 这里补三件事：
 * 1. compileScript + compileTemplate 组装出带真实 render 的组件；
 * 2. 结构保留 renderer：host 节点记 tag / props / 文本，挂载后可遍历断言
 *    「哪个分支被渲染」（未注册的模板组件会退化为同名 host 元素，tag 即组件名）；
 * 3. KeepAlive 宿主挂载：模拟 UserManagement 里 Tab 的 v-if 切走/切回，
 *    用于触发 deactivated / activated 生命周期。
 */
import { readFileSync } from 'node:fs'
import type { TestContext } from 'node:test'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

/** 结构保留 renderer 的 host 节点。 */
export interface T46Node {
  tag: string
  text?: string
  props: Record<string, unknown>
  children: T46Node[]
  parent?: T46Node
}

function node(tag: string, text?: string): T46Node {
  return { tag, text, props: {}, children: [] }
}

/** 深度遍历收集全部节点。 */
export function allNodes(root: T46Node): T46Node[] {
  const out: T46Node[] = []
  const walk = (n: T46Node) => {
    out.push(n)
    n.children.forEach(walk)
  }
  walk(root)
  return out
}

/** 按标签名查找节点。 */
export function findByTag(root: T46Node, tag: string): T46Node[] {
  return allNodes(root).filter((n) => n.tag === tag)
}

/** 节点子树内全部文本拼接。 */
export function textOf(n: T46Node): string {
  return allNodes(n)
    .map((x) => x.text ?? '')
    .join('')
}

/** 让挂载 / 请求 / 写回的 Promise 链走完。 */
export async function settle(times = 4): Promise<void> {
  for (let i = 0; i < times; i += 1) await flush()
}

type Fixture = ReturnType<typeof fixture>

/** 编译 SFC 的 script + template 并组装为可挂载组件（依赖打桩沿用 fixture）。 */
export function compileSfcComponent(
  f: Fixture,
  file: URL,
  dependencies: Record<string, unknown>,
): vue.Component {
  const { descriptor } = parse(readFileSync(file, 'utf8'), { filename: file.pathname })
  const script = compileScript(descriptor, { id: 't46-mount' })
  const template = descriptor.template
  if (!template) throw new Error(`SFC 无模板：${file.pathname}`)
  const compiled = compileTemplate({
    source: template.content,
    filename: file.pathname,
    id: 't46-mount',
    compilerOptions: { bindingMetadata: script.bindings },
  })
  const scriptExports = f.evaluate(
    script.content
      .replace('__expose();', '')
      .replace('return __returned__', '__expose(__returned__); return __returned__'),
    { vue, ...dependencies },
  )
  const renderExports = f.evaluate(compiled.code, { vue })
  const component = scriptExports.default as Record<string, unknown>
  component.render = renderExports.render
  return component as unknown as vue.Component
}

/** 结构保留 renderer：patchProp 把 props 记到 host 元素上。 */
function createStructRenderer(root: T46Node) {
  return vue.createRenderer<T46Node, T46Node>({
    createElement: (tag: string) => node(tag),
    createText: (text: string) => node('#text', text),
    createComment: () => node('#comment'),
    setText: (n: T46Node, text: string) => {
      n.text = text
    },
    setElementText: (el: T46Node, text: string) => {
      el.children = [node('#text', text)]
    },
    parentNode: (n: T46Node) => n.parent ?? null,
    nextSibling: (n: T46Node) => {
      const parent = n.parent
      if (!parent) return null
      const index = parent.children.indexOf(n)
      return index >= 0 ? (parent.children[index + 1] ?? null) : null
    },
    insert: (child: T46Node, parent: T46Node | null) => {
      const container = parent ?? root
      if (child.parent) {
        const old = child.parent.children.indexOf(child)
        if (old >= 0) child.parent.children.splice(old, 1)
      }
      child.parent = container
      container.children.push(child)
    },
    remove: (child: T46Node) => {
      if (!child.parent) return
      const idx = child.parent.children.indexOf(child)
      if (idx >= 0) child.parent.children.splice(idx, 1)
      child.parent = undefined
    },
    patchProp: (el: T46Node, key: string, _prev: unknown, next: unknown) => {
      el.props[key] = next
    },
  } as never)
}

interface MountOptions {
  component: URL
  dependencies: Record<string, unknown>
  props?: Record<string, unknown>
}

/** 卸载前先排干调度队列：避免断言失败提前结束后，残留的微任务在卸载后继续 patch。 */
async function unmountQuietly(app: { unmount: () => void }): Promise<void> {
  try {
    await flush()
    app.unmount()
    await flush()
  } catch {
    // 结构 renderer 不具备完整 DOM 语义；卸载清理的内部报错不掩盖断言结果。
  }
}

/** 直接挂载单个 SFC（完整渲染），返回根 host 树与组件实例。 */
export function mountRendered(t: TestContext, options: MountOptions) {
  const f = fixture(t)
  const comp = compileSfcComponent(f, options.component, options.dependencies)
  const root = node('root')
  const app = createStructRenderer(root).createApp(comp, options.props ?? {})
  app.config.warnHandler = () => {}
  const vm = app.mount(root)
  t.after(() => unmountQuietly(app))
  return { root, vm: vm as any }
}

/**
 * 用 KeepAlive 宿主挂载 SFC：模拟 UserManagement 中 Tab 组件
 * `<KeepAlive><Comp v-if="active"/></KeepAlive>` 的切走/切回。
 */
export function mountInKeepAlive(t: TestContext, options: MountOptions) {
  const f = fixture(t)
  const comp = compileSfcComponent(f, options.component, options.dependencies)
  const root = node('root')
  const tab = vue.ref(true)
  const target = vue.ref<any>(null)
  const dummy = { name: 't46-dummy', render: () => null }
  const host: vue.Component = {
    name: 't46-host',
    setup() {
      return () =>
        vue.h(vue.KeepAlive, null, {
          default: () =>
            tab.value
              ? vue.h(comp as any, { ref: target, ...options.props })
              : vue.h(dummy),
        })
    },
  }
  const app = createStructRenderer(root).createApp(host)
  app.config.warnHandler = () => {}
  app.mount(root)
  t.after(() => unmountQuietly(app))
  const switchTab = async (next: boolean) => {
    tab.value = next
    await settle()
  }
  return {
    root,
    vm: () => target.value as any,
    away: () => switchTab(false),
    back: () => switchTab(true),
  }
}
