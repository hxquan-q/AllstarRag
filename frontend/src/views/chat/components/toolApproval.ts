/**
 * 审批卡是否是后端内置操作（run_operation）。
 * 用稳定的注册工具名判别，不依赖会随品牌改名的显示名。
 */
export function isOperationApproval(registeredToolName?: string | null): boolean {
  return registeredToolName === 'run_operation'
}
