import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { getSuggestedQuestions } from '@/api/agent'
import { productLineStorageKey, resolveLine } from '@/brand/productLines'
import type { ProductLine } from '@/brand/types'
import { useAuthStore } from '@/stores/auth'
import { useChatResourcesStore } from '@/stores/chatResources'

/**
 * 客户当前选的产线。
 *
 * 产线不再是品牌配置，而是「用户在当前工作区绑定的 agent」派生出来的：
 * 顺序 = 绑定顺序，名称/说明/示例问题都取 agent 自身（AC-8）。
 * store 只记录选中态（欢迎区、示例问题跟着变），不切向量库/检索（C7）；
 * 会话页切线自动开新会话的逻辑放在 chat 视图层 watch（customer-model-lines AC-13），store 不加副作用。
 * 这里刻意不调用 settingsStore.selectAgent，避免把产线状态和会话/知识库/文件选择绑在一起。
 */
export const useProductLineStore = defineStore('productLine', () => {
  const authStore = useAuthStore()
  const chatResources = useChatResourcesStore()

  /**
   * 当前工作区的绑定 agent id。
   * 键 = 工作区 id 的十进制字符串（T01 的 bound_agent_ids_by_tenant）；
   * 切工作区时 effectiveTenantId 变化，这里自动读到另一条，互不覆盖（AC-1 / 5.1.1）。
   */
  const boundIds = computed<string[]>(() => {
    const tenantId = authStore.effectiveTenantId
    if (tenantId === null || tenantId === undefined) return []
    const all = authStore.user?.preferences?.bound_agent_ids_by_tenant ?? {}
    return all[String(tenantId)] ?? []
  })

  /** 各 agent 的示例问题；请求失败或还没加载时是空数组，不阻塞界面（D7 / AC-8）。 */
  const samplesById = ref<Record<string, string[]>>({})
  /** 已经请求过的 agent（含失败），避免同一 agent 反复打接口。 */
  const samplesRequested = new Set<string>()

  /**
   * 产线 = 绑定 agent 派生（AC-8）。
   * 已停用（disabledOwnAgentIds）与已删除/不在本工作区（不在 agents 快照里）的自动过滤，不报错。
   * `agentId` 必须填 agent id：useCustomerAgent 靠它驱动 settingsStore.selectAgent。
   */
  const lines = computed<ProductLine[]>(() => {
    const disabled = new Set(chatResources.disabledOwnAgentIds)
    const byId = new Map(chatResources.agents.map((agent) => [agent.id, agent]))
    const seen = new Set<string>()
    const result: ProductLine[] = []
    for (const id of boundIds.value) {
      if (!id || seen.has(id)) continue
      seen.add(id)
      if (disabled.has(id)) continue
      const agent = byId.get(id)
      if (!agent) continue
      result.push({
        id: agent.id,
        agentId: agent.id,
        name: agent.name,
        description: agent.description ?? '',
        samples: samplesById.value[agent.id] ?? [],
      })
    }
    return result
  })

  const currentLineId = ref('')
  /** localStorage 里读到的「上次选中」，用来在 agents 异步到达后还原选择。 */
  const storedLineId = ref<string | null>(null)

  /** 未登录时返回空串：不读也不写共享键，避免不同账号串数据。 */
  function currentStorageKey(): string {
    const userId = authStore.currentUserId
    return userId ? productLineStorageKey(userId) : ''
  }

  function persist(lineId: string): void {
    const key = currentStorageKey()
    if (!key || typeof localStorage === 'undefined') return
    try {
      localStorage.setItem(key, lineId)
    } catch (error) {
      // 隐私模式/配额满时 setItem 会抛错；偏好保存失败不影响当前会话内的产线切换（M10）。
      console.warn('[product-line] 保存产线偏好失败，仅保留内存态:', error)
    }
  }

  function readStored(): string | null {
    const key = currentStorageKey()
    if (!key || typeof localStorage === 'undefined') return null
    try {
      return localStorage.getItem(key)
    } catch {
      // localStorage 被禁用时按「没有记录」处理，界面仍可用。
      return null
    }
  }

  /**
   * 按「当前选中（仍有效时保持）→ 上次选中（localStorage）→ 第一条」解析当前产线，
   * 列表为空时置空。抽成公共函数，既供 hydrate（账号切换/首次实例化）用，
   * 也供 watch(lines) 重解析用（AC-8.1）。
   */
  function reconcile(): void {
    const preferred = currentLineId.value || storedLineId.value
    const resolved = resolveLine(lines.value, preferred)
    currentLineId.value = resolved?.id ?? ''

    // 存储里是坏值（产线被解绑、agent 被停用/删除、手改）时回退第一条并覆盖，
    // 避免下次继续读到同一个坏值；本来就没有存储值时不主动写入。
    if (resolved && storedLineId.value !== null && storedLineId.value !== resolved.id) {
      storedLineId.value = resolved.id
      persist(resolved.id)
    }
  }

  /** 按当前用户读一次 localStorage 并解析选中态。 */
  function hydrate(): void {
    storedLineId.value = readStored()
    // 账号切换时先把上一个人的内存选中清掉，避免它在新用户的列表里「碰巧有效」。
    currentLineId.value = ''
    reconcile()
  }

  /**
   * 只改当前产线（以及按用户持久化），不碰智能体、知识库、文件、会话。
   * 传入当前产线列表里不存在的 id 时回退第一条。
   */
  function selectLine(id: string): void {
    const resolved = resolveLine(lines.value, id)
    if (!resolved) return
    currentLineId.value = resolved.id
    storedLineId.value = resolved.id
    persist(resolved.id)
  }

  const currentLine = computed<ProductLine | null>(() =>
    resolveLine(lines.value, currentLineId.value),
  )

  /** agents 是否至少成功拉取过一次：空态只在 ready 且确实 0 条时出现（AC-10）。 */
  const ready = ref(chatResources.isLoaded('agents'))
  /** 最近一次自举是否失败：失败且没有任何产线时，欢迎区显示「加载失败 + 重试」（AC-10 失败兜底）。 */
  const loadFailed = ref(false)
  let bootstrapPromise: Promise<void> | null = null

  /**
   * store 自举 agents（AC-8.2）：侧栏不经过对话页也要有数据，
   * 不能等 Input-field 挂载。ensureAgents 自身单飞，重复调用安全。
   */
  function bootstrapAgents(): void {
    if (!authStore.currentUserId) return
    if (bootstrapPromise) return
    loadFailed.value = false
    const current: Promise<void> = chatResources
      .ensureAgents()
      .then(() => {
        ready.value = true
      })
      .catch((error) => {
        // 只有这次 promise 仍是当前自举时才清引用、置 loadFailed（F14）：
        // 账号切换后旧请求迟到失败，不得清掉新 bootstrap 的 promise、
        // 也不得把 stale 失败态留在新会话上（否则与空态同现）。
        if (bootstrapPromise === current) {
          bootstrapPromise = null
          loadFailed.value = true
        }
        console.warn('[product-line] 拉取智能体列表失败，产线暂不可用:', error)
      })
    bootstrapPromise = current
  }

  /** 自举失败后的手动重试：清掉已结束的 promise 再拉一次（ensureAgents 失败不缓存，必然发新请求）。 */
  function retryBootstrapAgents(): void {
    bootstrapPromise = null
    bootstrapAgents()
  }

  /** 懒加载示例问题：失败留空、不重试，不阻塞欢迎区渲染（D7）。 */
  async function ensureAgentSamples(ids: readonly string[]): Promise<void> {
    const pending = ids.filter((id) => id && !samplesRequested.has(id))
    if (pending.length === 0) return
    pending.forEach((id) => samplesRequested.add(id))
    await Promise.all(
      pending.map(async (id) => {
        try {
          const res = await getSuggestedQuestions(id)
          const questions = res?.data?.questions ?? []
          samplesById.value = {
            ...samplesById.value,
            [id]: questions.map((item) => item.question).filter((question) => !!question),
          }
        } catch {
          samplesById.value = { ...samplesById.value, [id]: [] }
        }
      }),
    )
  }

  hydrate()
  bootstrapAgents()

  // 产物线列表变化（agents 异步到达、停用/解绑、切工作区）时重解析选中态（AC-8.1），
  // 并给新增的 agent 补示例问题。不能只在初始化与 currentUserId 变化时 hydrate。
  // immediate：store 创建时 agents 可能已在快照里（lines 非空），也要补一次示例问题。
  watch(
    lines,
    (next) => {
      reconcile()
      void ensureAgentSamples(next.map((line) => line.agentId))
    },
    { immediate: true },
  )

  // 账号切换（登出 → 另一个用户登录）时按新用户 id 重新读，两个账号的偏好互不影响。
  watch(
    () => authStore.currentUserId,
    () => {
      ready.value = chatResources.isLoaded('agents')
      bootstrapPromise = null
      loadFailed.value = false
      hydrate()
      bootstrapAgents()
    },
  )

  return {
    lines,
    ready,
    loadFailed,
    currentLineId,
    currentLine,
    selectLine,
    hydrate,
    retryBootstrapAgents,
  }
})
