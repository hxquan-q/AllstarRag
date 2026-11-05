package service

import (
	"context"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ownerInviteDefaultDays 是 owner 邀请链接的默认有效期（AC-30b）。
const ownerInviteDefaultDays = 7

// CreateOwnerInvite 由系统管理员创建 pending 空间 + role=owner 的分享链接。
//
// 关键口径（02-plan §4.6 / T04-2）：
//   - 只建租户与邀请 token，不预建 tenant_members；成员关系在邀请被
//     接受时由既有 AcceptByToken 流程创建；
//   - TenantService.CreateTenant 会强制 status=active，这里显式改为
//     pending，接受邀请后再由既有流程激活（本票只负责邀请侧）；
//   - expiresDays <= 0 回落到 7 天。
func (s *userManagementService) CreateOwnerInvite(
	ctx context.Context,
	email, spaceName string,
	expiresDays int,
) (*interfaces.OwnerInviteResult, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.ownerDepsReady(); err != nil {
		return nil, err
	}
	email = strings.TrimSpace(email)
	name := strings.TrimSpace(spaceName)
	if name == "" {
		name = defaultInviteSpaceName(email)
	}

	tenant, err := s.tenantService.CreateTenant(ctx, &types.Tenant{Name: name})
	if err != nil {
		return nil, err
	}
	tenant.Status = "pending"
	if _, err := s.tenantService.UpdateTenant(ctx, tenant); err != nil {
		// 空间还没有成员/邀请，删除不会留下悬挂引用。
		_ = s.tenantService.DeleteTenant(ctx, tenant.ID)
		return nil, err
	}

	inv, token, err := s.invitationService.CreateShareLink(
		ctx, tenant.ID, types.TenantRoleOwner, invitedByFromContext(ctx), "",
	)
	if err != nil {
		_ = s.tenantService.DeleteTenant(ctx, tenant.ID)
		return nil, err
	}

	expiresAt := time.Now().Add(time.Duration(normalizeInviteDays(expiresDays)) * 24 * time.Hour)
	if err := s.invitationService.ApplyShareLinkOptions(ctx, inv.ID, nil, &expiresAt); err != nil {
		return nil, err
	}

	s.emitOwnerAudit(ctx, tenant.ID, types.AuditActionOwnerInviteCreated, "",
		map[string]string{"space_name": tenant.Name})
	return &interfaces.OwnerInviteResult{
		TenantID:     tenant.ID,
		InvitationID: inv.ID,
		Token:        token,
		ExpiresAt:    expiresAt,
	}, nil
}

// normalizeInviteDays 把非正数有效期回落到默认 7 天。
func normalizeInviteDays(expiresDays int) int {
	if expiresDays <= 0 {
		return ownerInviteDefaultDays
	}
	return expiresDays
}

// defaultInviteSpaceName 为未命名邀请生成非空空间名。刻意不把邮箱写进
// 空间名：空间名会出现在成员可见的列表里，邮箱属于个人信息。
func defaultInviteSpaceName(string) string {
	return "独立空间"
}
