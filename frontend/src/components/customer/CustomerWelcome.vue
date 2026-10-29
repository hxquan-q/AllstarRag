<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { ProductLine } from '@/brand/types'
import { useProductLineStore } from '@/stores/productLine'

/**
 * 客户新对话欢迎区：欢迎语 + 产线卡片 + 当前产线示例问题 + 底部提示。
 *
 * 只负责展示与交互，不做发送：示例问题通过 `send` 事件交给 creatChat，
 * 由它调用 Input-field 的 triggerSend，保证和手动输入走同一条发送链路（AC-20）。
 * 产线状态仍然只落在 productLine store，切换只影响界面（AC-18 / C7）。
 */
const emit = defineEmits<{
  send: [question: string]
}>()

const { t, locale } = useI18n()
const router = useRouter()
const productLineStore = useProductLineStore()

/** 字标图片是中文的：只有中文环境替换标题文字，其他语言保留本地化文案。 */
const isZhLocale = computed(() => String(locale.value).toLowerCase().startsWith('zh'))

/** 折叠阈值：超过 4 条才出现「查看更多」（AC-12，2026-10-29 用户 E2E 反馈由 3 改 4）。 */
const COLLAPSED_LINE_COUNT = 4

const lines = computed(() => productLineStore.lines)
const currentLine = computed(() => productLineStore.currentLine)
const samples = computed(() => currentLine.value?.samples ?? [])

/** 展开状态只作用于欢迎区卡片；输入区下拉始终全量（AC-13）。 */
const expanded = ref(false)
const hasOverflow = computed(() => lines.value.length > COLLAPSED_LINE_COUNT)
const visibleLines = computed(() =>
  hasOverflow.value && !expanded.value ? lines.value.slice(0, COLLAPSED_LINE_COUNT) : lines.value,
)
/** agents 未就绪时不显示空态，避免冷启动闪一下（AC-10 / 02-plan 5.1）。 */
const showEmptyState = computed(() => productLineStore.ready && lines.value.length === 0)
/**
 * agents 拉取失败且没有任何产线时给「加载失败 + 重试」，避免永久空窗（AC-10 失败兜底）。
 * 要求 !ready：与上面的空态（ready 且 0 条）严格互斥，不出现「空态 + 失败态」双块同现。
 */
const showLoadFailed = computed(
  () => productLineStore.loadFailed && !productLineStore.ready && lines.value.length === 0,
)

function retryLoadAgents(): void {
  productLineStore.retryBootstrapAgents()
}

function toggleExpanded(): void {
  expanded.value = !expanded.value
}

function goBindAgents(): void {
  void router.push({ path: '/platform/settings', query: { section: 'agentlines' } })
}

function chooseLine(line: ProductLine): void {
  if (line.id === currentLine.value?.id) return
  productLineStore.selectLine(line.id)
}

function ask(question: string): void {
  if (!question.trim()) return
  emit('send', question)
}
</script>

<template>
  <section class="customer-welcome" aria-labelledby="customer-welcome-title">
    <!-- 字标独立成「头部区域」，在剩余空间里垂直居中；其余内容全部排在其下方 -->
    <div class="customer-welcome__head">
      <h1 id="customer-welcome-title" class="customer-welcome__title">
        <!-- 中文环境用字标图片替换标题文案；其他语言保留本地化文字（图片是中文的） -->
        <span v-if="isZhLocale" class="customer-welcome__wordmark" role="img" aria-label="选择你的 agent">
          <img src="/brand/choose-agent-wordmark.png" alt="选择你的 agent" />
        </span>
        <template v-else>{{ t('brand.welcome.chooseAgent') }}</template>
      </h1>
    </div>

    <div class="customer-welcome__content">
      <p v-if="lines.length === 0" class="customer-welcome__subtitle">{{ t('brand.welcome.subtitle') }}</p>

      <div id="customer-welcome-lines" class="customer-welcome__lines" role="group"
        :aria-label="t('brand.welcome.lineGroupLabel')">
        <button v-for="line in visibleLines" :key="line.id" type="button" class="line-card"
          :class="{ 'is-active': line.id === currentLine?.id }" :aria-pressed="line.id === currentLine?.id"
          @click="chooseLine(line)">
          <span class="line-card__name">{{ line.name }}</span>
          <span v-if="line.description" class="line-card__desc">{{ line.description }}</span>
        </button>
      </div>

      <!-- 超过 4 条时就地展开/收起，不跳页、不弹层（AC-12）；按钮沿用现有 sample-chip 样式。 -->
      <button v-if="hasOverflow" type="button" class="sample-chip customer-welcome__toggle"
        :aria-expanded="expanded" aria-controls="customer-welcome-lines" @click="toggleExpanded">
        {{ expanded ? t('brand.welcome.collapseLines') : t('brand.welcome.showMoreLines') }}
      </button>

      <!-- 未绑定：不单独提示「还没有绑定智能体」，去绑定沿用原来的 sample-chip 按钮样式，紧跟副标题 -->
      <button v-if="showEmptyState" type="button" class="sample-chip customer-welcome__bind-btn" @click="goBindAgents">
        {{ t('brand.welcome.emptyAction') }}
      </button>

      <!-- agents 拉取失败且无产线时给重试（AC-10 失败兜底）；复用空态容器与 sample-chip，不加新动效（C8）。 -->
      <div v-if="showLoadFailed" class="customer-welcome__empty" role="alert">
        <p class="customer-welcome__empty-text">{{ t('brand.welcome.loadFailed') }}</p>
        <button type="button" class="sample-chip" @click="retryLoadAgents">
          {{ t('brand.welcome.retry') }}
        </button>
      </div>

      <!-- 切产线时用 out-in 过渡让示例问题先淡出再淡入；时长走 --app-motion-instant（120ms） -->
      <Transition name="samples-swap" mode="out-in">
        <div v-if="samples.length > 0" :key="currentLine?.id" class="customer-welcome__samples"
          role="group" :aria-label="t('brand.welcome.samplesLabel')">
          <button v-for="question in samples" :key="question" type="button" class="sample-chip" @click="ask(question)">
            {{ question }}
          </button>
        </div>
      </Transition>
    </div>

    <!-- 产线 = 当前工作区绑定的 agent（无绑定时由上面的空态提示承担） -->
    <!-- 免责声明已移到输入框下方（creatChat 页面底部） -->
  </section>
</template>

<style lang="less" scoped>
.customer-welcome {
  width: 100%;
  max-width: 820px;
  margin: 0 auto;
  padding: 0 var(--app-space-4);
  box-sizing: border-box;
  text-align: center;
}

.customer-welcome__head {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0 0 var(--app-space-2);
}

.customer-welcome__content {
  flex: 0 0 auto;
}

.customer-welcome__title {
  margin: 0 0 var(--app-space-2);
  color: var(--td-text-color-primary);
  font-family: var(--app-font-family);
  font-size: var(--app-text-4xl);
  font-weight: 600;
  line-height: 1.35;
}

/* 字标裁切容器：原图 2048×1152 白底，裁到文字主体（约 36px 视觉高度）；
   白底用 multiply 融进浅色背景，正式素材透明化后可移除。 */
.customer-welcome__wordmark {
  position: relative;
  display: inline-block;
  overflow: hidden;
  width: 246px;
  height: 66px;
  vertical-align: middle;
}

.customer-welcome__wordmark img {
  position: absolute;
  display: block;
  width: 354px;
  left: -58px;
  top: -66px;
  mix-blend-mode: multiply;
  pointer-events: none;
}

.customer-welcome__subtitle {
  margin: 0 0 var(--app-space-6);
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-base);
}

/* 空态去绑定：沿用原 sample-chip 按钮外观，只收紧与副标题的间距 */
.customer-welcome__bind-btn {
  margin: calc(-1 * var(--app-space-3)) 0 var(--app-space-4);
}

.customer-welcome__lines {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: var(--app-space-4);
}

/* 「查看更多 / 收起」只加间距，视觉沿用上面的 sample-chip 按钮。 */
.customer-welcome__toggle {
  margin-top: var(--app-space-4);
}

/* 空态：提示 + 「去绑定」，不新增动效与图标（C8）。 */
.customer-welcome__empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--app-space-3);
  margin-top: var(--app-space-4);
}

.customer-welcome__empty-text {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-base);
}

.line-card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  width: 320px;
  max-width: 100%;
  padding: var(--app-space-4) var(--app-space-5);
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-xl);
  background: var(--td-bg-color-container);
  color: var(--td-text-color-primary);
  text-align: left;
  cursor: pointer;
  transition:
    border-color var(--app-motion-fast) ease,
    box-shadow var(--app-motion-fast) ease,
    transform var(--app-motion-fast) ease;
}

.line-card:hover {
  transform: translateY(-1px);
  box-shadow: var(--td-shadow-1);
}

.line-card.is-active {
  border-color: var(--td-brand-color);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--td-brand-color) 14%, transparent);
}

.line-card:focus-visible {
  outline: 2px solid var(--td-brand-color);
  outline-offset: 2px;
}

.line-card__name {
  font-size: var(--app-text-lg);
  font-weight: 600;
}

.line-card__desc {
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-md);
}

.customer-welcome__samples {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: var(--app-space-2);
  margin-top: var(--app-space-5);
}

.sample-chip {
  padding: 5px var(--app-space-3);
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-pill);
  background: var(--td-bg-color-container);
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-md);
  cursor: pointer;
  transition:
    color var(--app-motion-fast) ease,
    border-color var(--app-motion-fast) ease,
    background var(--app-motion-fast) ease;
}

.sample-chip:hover {
  color: var(--td-brand-color);
  border-color: var(--td-brand-color);
  background: color-mix(in srgb, var(--td-brand-color) 8%, transparent);
}

.sample-chip:focus-visible {
  outline: 2px solid var(--td-brand-color);
  outline-offset: 2px;
}

.samples-swap-enter-active,
.samples-swap-leave-active {
  transition: opacity var(--app-motion-instant) ease;
}

.samples-swap-enter-from,
.samples-swap-leave-to {
  opacity: 0;
}

/* 免责声明已移到输入框下方（creatChat 客户版底部），此文件不再渲染 */

@media (prefers-reduced-motion: reduce) {
  .line-card,
  .sample-chip,
  .samples-swap-enter-active,
  .samples-swap-leave-active {
    transition: none;
  }
}
</style>
