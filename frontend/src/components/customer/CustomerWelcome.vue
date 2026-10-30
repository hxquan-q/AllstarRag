<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ProductLine } from '@/brand/types'
import { useProductLineStore } from '@/stores/productLine'
import { useUIStore } from '@/stores/ui'

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
const productLineStore = useProductLineStore()
const uiStore = useUIStore()

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
 * 未绑定 agent 时进入欢迎页自动弹提示（W5）。
 * 只在 agents 已就绪且确实 0 条时弹；加载失败走既有重试，不弹。
 * 关闭后 store 不变不会重开；重新进入页面组件重挂载会再弹（W6）。
 */
watch(
  () => [productLineStore.ready, productLineStore.loadFailed, lines.value.length] as const,
  ([ready, failed, len]) => {
    if (ready && !failed && len === 0) uiStore.openCustomerNoAgentDialog()
  },
  { immediate: true },
)
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

      <!-- 未绑定时不再在欢迎区显示副标题/去绑定按钮，改由可关闭弹窗承接（W5–W9）。 -->

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
  /* 等高：固定高度并算进内边距/边框，名称或描述为空时也不塌陷 */
  height: 104px;
  box-sizing: border-box;
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
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  font-size: var(--app-text-lg);
  font-weight: 600;
}

.line-card__desc {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
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

/* —— T03：客户移动态窄屏。只补尺寸/换行，不动 script 与模板结构。 —— */
@media (max-width: 1023px) {
  .customer-welcome {
    max-width: 100%;
    padding-left: 12px;
    padding-right: 12px;
  }

  /* 字标按比例缩放：容器宽度不足 246px 时等比缩小，裁切参数同步换成百分比。 */
  .customer-welcome__wordmark {
    width: min(246px, 100%);
    height: auto;
    aspect-ratio: 246 / 66;
  }

  .customer-welcome__wordmark img {
    width: 143.9%; // 354 / 246
    left: -23.58%; // -58 / 246
    top: -100%; // -66 / 66
  }

  .customer-welcome__empty-text {
    max-width: 100%;
    overflow-wrap: anywhere;
  }

  /* 产线卡片占满一行，描述长文本允许换行。 */
  .line-card {
    width: 100%;
  }

  .line-card__name,
  .line-card__desc {
    overflow-wrap: anywhere;
  }

  /* 示例问题/折叠/重试按钮：可换行，触控目标 ≥44px。 */
  /* CP3 F7：加载失败态的「重试」也复用 sample-chip，一并纳入 44px 规则。 */
  .customer-welcome__samples .sample-chip,
  .customer-welcome__empty .sample-chip,
  .customer-welcome__toggle {
    max-width: 100%;
    min-height: 44px;
    white-space: normal;
    overflow-wrap: anywhere;
  }
}

@media (prefers-reduced-motion: reduce) {
  .line-card,
  .sample-chip,
  .samples-swap-enter-active,
  .samples-swap-leave-active {
    transition: none;
  }
}
</style>
