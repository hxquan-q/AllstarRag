<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useUIStore } from '@/stores/ui'

/**
 * 客户未绑定 agent 提示弹窗（W5–W8）。
 *
 * 只读 uiStore 的可见状态：进入欢迎页或点发送时由对应入口置 true。
 * 关闭（X / Esc / 遮罩 / 关闭按钮）只关掉当前这一次，不改变下次进入/提问再弹；
 * 「去绑定」跳设置页的产线分区后同样关闭。
 */
const { t } = useI18n()
const router = useRouter()
const uiStore = useUIStore()

function close(): void {
  uiStore.closeCustomerNoAgentDialog()
}

async function goBind(): Promise<void> {
  await router.push({ path: '/platform/settings', query: { section: 'agentlines' } })
  close()
}
</script>

<template>
  <t-dialog
    :visible="uiStore.customerNoAgentDialogVisible"
    :header="t('brand.noAgent.title')"
    :close-btn="true"
    :close-on-overlay-click="true"
    :close-on-esc-keydown="true"
    width="480px"
    placement="center"
    @close="close"
  >
    <p class="customer-no-agent-dialog__desc">{{ t('brand.noAgent.desc') }}</p>
    <template #footer>
      <t-button theme="default" variant="outline" @click="close">
        {{ t('common.close') }}
      </t-button>
      <t-button theme="primary" @click="goBind">
        {{ t('brand.welcome.emptyAction') }}
      </t-button>
    </template>
  </t-dialog>
</template>

<style lang="less" scoped>
.customer-no-agent-dialog__desc {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-base);
  line-height: 1.6;
}
</style>
