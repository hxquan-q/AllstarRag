package handler

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// maxBoundAgentIDsPerTenant 是单个工作区能绑定的产线上限。
const maxBoundAgentIDsPerTenant = 20

// internalAgentIDs 是三个内部 agent：它们只在特定入口打开（Wiki 编辑器、
// skill 安装、全局助手抽屉），不面向客户的产线选择，因此禁止绑定。
// 其余 7 个内建 agent 在 ListAgents 里可见、可被停用，允许当作产线绑定。
var internalAgentIDs = map[string]struct{}{
	types.BuiltinWikiFixerID:        {},
	types.BuiltinSkillInstallerID:   {},
	types.BuiltinWeKnoraAssistantID: {},
}

// validateBoundAgentIDs 校验一批要绑定的 agent id，返回去重后的结果。
//
// 规则（整批全有或全无，任何一条不合法就整批拒绝）：
//  1. 重复 id 去重，保持首次出现的顺序；空串直接拒绝；
//  2. 去重后超过 20 个报错；
//  3. 内部 agent（wiki fixer / skill installer / weknora assistant）报错；
//     另外 7 个内建 agent 允许绑定；
//  4. 在当前活动工作区查不到（不存在，或属于别的工作区）报错；
//  5. 在本工作区的停用列表里报错。
//
// 只认「当前活动工作区」，不认用户 home 工作区，也不认别的工作区；
// 错误消息带上具体 id，但「不存在」与「别的租户的」给同一句话，
// 不泄露其他工作区是否存在这个 agent。
//
// 返回的切片已去重且保序；空输入返回空切片（调用方据此清空当前工作区）。
func (h *AuthHandler) validateBoundAgentIDs(
	ctx context.Context,
	tenantID uint64,
	ids []string,
) ([]string, error) {
	// 去重保序，不 trim：id 按客户端送来的原样校验，避免悄悄改写绑定值。
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, errors.NewValidationError("agent id cannot be empty")
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return unique, nil
	}
	if len(unique) > maxBoundAgentIDsPerTenant {
		return nil, errors.NewValidationError(fmt.Sprintf(
			"cannot bind more than %d agents in one workspace", maxBoundAgentIDsPerTenant))
	}
	if h.customAgentService == nil {
		return nil, errors.NewInternalServerError("Failed to validate bound agents")
	}

	// agent 查询走 ctx 里的租户，所以先确保 ctx 的租户就是活动工作区，
	// 否则会校验到错误的工作区（例如用户 home 租户）。
	agentCtx := ctx
	if ctxTenant, ok := types.TenantIDFromContext(ctx); !ok || ctxTenant != tenantID {
		agentCtx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	}

	// 停用列表按工作区取一次。repo 缺失（构造路径/测试装配漏注）时 fail-closed 返回 500，
	// 与上面 customAgentService 缺失的语义一致：宁可拒绝绑定，也不放过被停用的 agent。
	if h.disabledAgentRepo == nil {
		return nil, errors.NewInternalServerError("Failed to validate bound agents")
	}
	disabled, err := h.disabledAgentRepo.ListDisabledOwnAgentIDs(ctx, tenantID)
	if err != nil {
		logger.Errorf(ctx, "Failed to list disabled agent IDs for workspace %d: %v", tenantID, err)
		return nil, errors.NewInternalServerError("Failed to validate bound agents")
	}
	disabledIDs := make(map[string]struct{}, len(disabled))
	for _, id := range disabled {
		disabledIDs[id] = struct{}{}
	}

	for _, id := range unique {
		if _, internal := internalAgentIDs[id]; internal {
			return nil, errors.NewValidationError(fmt.Sprintf(
				"agent %q is internal and cannot be bound", id))
		}
		if _, err := h.customAgentService.GetAgentByID(agentCtx, id); err != nil {
			// 不存在与跨工作区给同一句话，不泄露别的租户有没有这个 agent。
			return nil, errors.NewValidationError(fmt.Sprintf(
				"agent %q does not exist or cannot be bound in this workspace", id))
		}
		if _, disabled := disabledIDs[id]; disabled {
			return nil, errors.NewValidationError(fmt.Sprintf(
				"agent %q is disabled in this workspace", id))
		}
	}

	return unique, nil
}
