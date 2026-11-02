<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import { updateMyPreferences } from '@/api/auth'
import { useAuthStore } from '@/stores/auth'
import { useChatResourcesStore } from '@/stores/chatResources'
import { useProductLineStore } from '@/stores/productLine'

/**
 * 客户设置「产线」分区：绑定 / 解绑当前工作区的 agent。
 *
 * 产线 = 用户在**当前工作区**绑定的 agent（见 productLineStore.lines）。
 * 这里只负责编辑绑定列表；真正的归属、停用、内部 agent、数量上限校验在后端
 * （handler.UpdateMyPreferences），前端候选下拉只是便利，不是权限手段（C4）。
 *
 * 保存后用返回的 preferences 立即回写 authStore，productLineStore.lines 随之重算，
 * 解绑当前选中 / 解绑到 0 条的选中态由 store 的 watch(lines) 自动重解析。
 */
const INTERNAL_AGENT_IDS = new Set([
  'builtin-wiki-fixer',
  'builtin-skill-installer',
  'builtin-weknora-assistant',
])
const MAX_BOUND_AGENTS = 20

const { t } = useI18n()
const authStore = useAuthStore()
const chatResources = useChatResourcesStore()
// 实例化 store：它的 watch(lines) 负责解绑后的选中态重解析。
useProductLineStore()

const draftId = ref('')
const candidateId = ref('')
const saving = ref(false)
const loading = ref(false)
const loadFailed = ref(false)
const errorMessage = ref('')

/** 后端按活动工作区写入，这里按同一个键读取，保证显示的是当前工作区。 */
const currentTenantKey = computed(() => {
  const tenantId = authStore.effectiveTenantId
  return tenantId === null || tenantId === undefined ? '' : String(tenantId)
})

const boundIds = computed<string[]>(() => {
  if (!currentTenantKey.value) return []
  return authStore.user?.preferences?.bound_agent_ids_by_tenant?.[currentTenantKey.value] ?? []
})

const agentById = computed(() => new Map(chatResources.agents.map((agent) => [agent.id, agent])))
const disabledIds = computed(() => new Set(chatResources.disabledOwnAgentIds))

/** 已绑定列表：id 来自偏好，名字尽量从 agents 快照解析，快照没有时退回 id。 */
const boundRows = computed(() =>
  boundIds.value.map((id) => ({
    id,
    name: agentById.value.get(id)?.name || id,
  })),
)

/**
 * 失效绑定（R3-4）：已绑定但 agent 已被删除/不在本工作区，或已被本工作区停用。
 * 只在 agents 快照可信（已加载成功）时判定，避免加载中把全部绑定误判为失效。
 */
const agentsSnapshotReady = computed(() => chatResources.isLoaded('agents'))
const invalidBoundIds = computed<string[]>(() => {
  if (!agentsSnapshotReady.value) return []
  return boundIds.value.filter((id) => !agentById.value.has(id) || disabledIds.value.has(id))
})

/**
 * 清理失效绑定：逐个解绑会被后端整批校验拒绝（剩余列表里仍有非法 id），
 * 所以这里一次性提交「仅保留有效 id」的列表。后端校验不放宽。
 * 全部失效时等价于清空当前工作区绑定。
 */
async function cleanupInvalidBindings(): Promise<void> {
  if (saving.value) return
  const invalid = invalidBoundIds.value
  if (invalid.length === 0) return
  const valid = boundIds.value.filter((id) => !invalid.includes(id))
  const dialog = DialogPlugin.confirm({
    header: t('agentLineSettings.cleanupConfirmTitle'),
    body: t('agentLineSettings.cleanupConfirmBody', { ids: invalid.join('、') }),
    confirmBtn: t('agentLineSettings.cleanupInvalid', { count: invalid.length }),
    cancelBtn: t('common.cancel'),
    onConfirm: () => {
      dialog.destroy()
      void (async () => {
        const ok = await saveBoundIds(valid)
        if (ok) {
          MessagePlugin.success(t('agentLineSettings.cleanupDone', { count: invalid.length }))
        }
      })()
    },
    onCancel: () => dialog.destroy(),
  })
}

/** 候选 = 本工作区未停用 + 尚未绑定 + 不是内部 agent；7 个内建 agent 允许出现。 */
const candidateOptions = computed(() => {
  const bound = new Set(boundIds.value)
  return chatResources.agents
    .filter(
      (agent) =>
        !disabledIds.value.has(agent.id) &&
        !bound.has(agent.id) &&
        !INTERNAL_AGENT_IDS.has(agent.id),
    )
    .map((agent) => ({
      label: `${agent.name} · ${agent.id}`,
      value: agent.id,
    }))
})

/**
 * 后端校验错误 → 本地文案。优先按 Details 里的稳定 code 映射（F5，不受后端措辞调整影响），
 * code 缺失（旧后端/非校验错误）时退回英文 message 子串匹配兜底。
 */
function localizeServerError(code: string | undefined, message?: string): string {
  const stableCode = typeof code === 'string' ? code : ''
  switch (stableCode) {
    case 'bound_agent_empty':
      return t('agentLineSettings.errorEmpty')
    case 'bound_agent_internal':
      return t('agentLineSettings.errorInternal')
    case 'bound_agent_disabled':
      return t('agentLineSettings.errorDisabled')
    case 'bound_agent_limit':
      return t('agentLineSettings.errorLimit', { max: MAX_BOUND_AGENTS })
    case 'bound_agent_not_found':
      return t('agentLineSettings.errorNotFound')
    default:
      return localizeByMessage(message)
  }
}

function localizeByMessage(message?: string): string {
  const raw = (message || '').toLowerCase()
  if (raw.includes('cannot be empty')) return t('agentLineSettings.errorEmpty')
  if (raw.includes('is internal')) return t('agentLineSettings.errorInternal')
  if (raw.includes('is disabled')) return t('agentLineSettings.errorDisabled')
  if (raw.includes('more than')) return t('agentLineSettings.errorLimit', { max: MAX_BOUND_AGENTS })
  if (raw.includes('does not exist') || raw.includes('cannot be bound')) {
    return t('agentLineSettings.errorNotFound')
  }
  return t('agentLineSettings.errorUnknown')
}

/** 保存整份绑定列表（覆盖当前工作区），成功后立即回写 authStore。 */
async function saveBoundIds(nextIds: string[]): Promise<boolean> {
  const user = authStore.user
  if (!user) return false
  saving.value = true
  errorMessage.value = ''
  try {
    const result = await updateMyPreferences({ bound_agent_ids: nextIds })
    if (!result.success || !result.data) {
      errorMessage.value = localizeServerError(result.errorCode, result.message)
      return false
    }
    authStore.setUser({ ...user, preferences: result.data })
    return true
  } catch (error: any) {
    // 兜底路径：updateMyPreferences 已捕获常规错误，这里只防意外抛出。
    errorMessage.value = localizeServerError(error?.error?.details, error?.message)
    return false
  } finally {
    saving.value = false
  }
}

async function loadAgents(): Promise<void> {
  loading.value = true
  loadFailed.value = false
  try {
    await chatResources.ensureAgents()
  } catch {
    loadFailed.value = true
  } finally {
    loading.value = false
  }
}

async function bindAgent(): Promise<void> {
  if (saving.value) return
  const id = draftId.value.trim()
  // 失败时保留输入框内容，方便用户修正后重试（AC-6）。
  if (!id) {
    errorMessage.value = t('agentLineSettings.errorEmpty')
    return
  }
  if (boundIds.value.includes(id)) {
    errorMessage.value = t('agentLineSettings.errorDuplicate')
    return
  }
  if (INTERNAL_AGENT_IDS.has(id)) {
    errorMessage.value = t('agentLineSettings.errorInternal')
    return
  }
  if (boundIds.value.length >= MAX_BOUND_AGENTS) {
    errorMessage.value = t('agentLineSettings.errorLimit', { max: MAX_BOUND_AGENTS })
    return
  }
  const ok = await saveBoundIds([...boundIds.value, id])
  if (ok) {
    draftId.value = ''
    candidateId.value = ''
    MessagePlugin.success(t('agentLineSettings.successBound', { id }))
  }
}

async function unbindAgent(id: string): Promise<void> {
  if (saving.value) return
  const ok = await saveBoundIds(boundIds.value.filter((bound) => bound !== id))
  if (ok) {
    MessagePlugin.success(t('agentLineSettings.successUnbound', { id }))
  }
}

/** 候选下拉只负责填入输入框，是否保存由「绑定」按钮决定。 */
function handleCandidateChange(value: unknown): void {
  if (typeof value === 'string' && value) draftId.value = value
}

onMounted(() => {
  void loadAgents()
})
</script>

<template>
  <div class="agent-line-settings">
    <div class="section-header">
      <h2>{{ $t('agentLineSettings.title') }}</h2>
      <p class="section-description">{{ $t('agentLineSettings.description') }}</p>
    </div>

    <div class="settings-group">
      <div class="group-title">
        <span>{{ $t('agentLineSettings.boundTitle') }}</span>
        <span class="bound-count">
          {{ $t('agentLineSettings.count', { count: boundRows.length, max: MAX_BOUND_AGENTS }) }}
        </span>
      </div>

      <p v-if="boundRows.length === 0" class="empty-hint">{{ $t('agentLineSettings.empty') }}</p>
      <ul v-else class="bound-list">
        <li v-for="row in boundRows" :key="row.id" class="bound-row">
          <div class="bound-meta">
            <span class="bound-name">{{ row.name }}</span>
            <code class="bound-id">{{ row.id }}</code>
          </div>
          <t-button
            size="small"
            variant="outline"
            theme="default"
            :disabled="saving"
            @click="unbindAgent(row.id)"
          >
            {{ $t('agentLineSettings.unbind') }}
          </t-button>
        </li>
      </ul>

      <!-- R3-4：失效绑定（agent 已删除或已停用）无法逐个解绑——后端会整批拒绝，
           这里提供一次性清理入口；只提交仍然有效的 id，不绕过后端校验。 -->
      <p v-if="invalidBoundIds.length > 0" class="invalid-hint">
        <t-button
          size="small"
          variant="text"
          theme="danger"
          :disabled="saving"
          @click="cleanupInvalidBindings"
        >
          {{ $t('agentLineSettings.cleanupInvalid', { count: invalidBoundIds.length }) }}
        </t-button>
      </p>

      <div class="bind-row">
        <t-input
          v-model="draftId"
          class="bind-input"
          clearable
          :disabled="saving"
          :placeholder="$t('agentLineSettings.inputPlaceholder')"
          @enter="bindAgent"
        />
        <t-button theme="primary" :loading="saving" @click="bindAgent">
          {{ $t('agentLineSettings.bind') }}
        </t-button>
      </div>

      <p v-if="errorMessage" class="error-text" role="alert">{{ errorMessage }}</p>

      <div class="candidate-row">
        <div class="candidate-label">{{ $t('agentLineSettings.candidateTitle') }}</div>
        <p class="candidate-hint">{{ $t('agentLineSettings.candidateHint') }}</p>
        <p v-if="loading" class="candidate-status">{{ $t('agentLineSettings.loading') }}</p>
        <p v-else-if="loadFailed" class="candidate-status error-text">
          {{ $t('agentLineSettings.loadFailed') }}
        </p>
        <p v-else-if="candidateOptions.length === 0" class="candidate-status">
          {{ $t('agentLineSettings.candidateEmpty') }}
        </p>
        <t-select
          v-else
          v-model="candidateId"
          class="candidate-select"
          :options="candidateOptions"
          :placeholder="$t('agentLineSettings.candidatePlaceholder')"
          :disabled="saving"
          @change="handleCandidateChange"
        />
      </div>
    </div>
  </div>
</template>

<style scoped lang="less">
.agent-line-settings {
  max-width: 720px;

  .section-header h2 {
    margin: 0 0 6px;
    font-size: var(--app-text-xl);
    font-weight: 600;
    color: var(--td-text-color-primary);
  }

  .section-description {
    margin: 0 0 24px;
    color: var(--td-text-color-secondary);
    font-size: var(--app-text-md);
    line-height: 1.6;
  }

  .settings-group {
    border: 1px solid var(--td-component-stroke);
    border-radius: 10px;
    padding: 20px 24px;
  }

  .group-title {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    font-size: var(--app-text-lg);
    font-weight: 600;
    color: var(--td-text-color-primary);
  }

  .bound-count {
    font-size: var(--app-text-sm);
    font-weight: 400;
    color: var(--td-text-color-secondary);
  }

  .empty-hint {
    margin: 12px 0 0;
    color: var(--td-text-color-secondary);
    font-size: var(--app-text-md);
    line-height: 1.6;
  }

  .bound-list {
    margin: 12px 0 0;
    padding: 0;
    list-style: none;
  }

  .bound-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 0;
    border-bottom: 1px solid var(--td-component-stroke);

    &:last-child {
      border-bottom: none;
    }
  }

  .bound-meta {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .bound-name {
    font-size: var(--app-text-md);
    color: var(--td-text-color-primary);
  }

  .bound-id {
    font-size: var(--app-text-xs);
    color: var(--td-text-color-secondary);
    word-break: break-all;
  }

  .bind-row {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-top: 16px;
  }

  .bind-input {
    flex: 1;
  }

  .error-text {
    margin: 10px 0 0;
    color: var(--td-error-color);
    font-size: var(--app-text-md);
  }

  .invalid-hint {
    margin: 6px 0 0;
    display: flex;
    justify-content: flex-end;
  }

  .candidate-row {
    margin-top: 20px;
    padding-top: 16px;
    border-top: 1px solid var(--td-component-stroke);
  }

  .candidate-label {
    font-size: var(--app-text-md);
    font-weight: 600;
    color: var(--td-text-color-primary);
  }

  .candidate-hint,
  .candidate-status {
    margin: 6px 0 0;
    color: var(--td-text-color-secondary);
    font-size: var(--app-text-sm);
    line-height: 1.5;
  }

  .candidate-select {
    margin-top: 10px;
    max-width: 420px;
  }
}
</style>
