package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// T12 TDD 红阶段：PUT /tenants/:id/members/:user_id 的 owner 保护。
//
// 契约（03-tickets/T12.md 验收条件 2/3/4/5）：
//   - 目标当前角色是 owner、新角色不是 owner -> 403，且错误信息引导
//     调用方走 transfer-owner 交接（owner 与系统管理员都一样）；
//   - 普通 owner 不能修改 owner 目标；可以把 viewer/contributor/admin
//     提升为 owner（co-owner，Q17 B）；
//   - 系统管理员可把非 owner 提升为 owner，同样不能直接降级 owner；
//   - 非 owner/超管调用 -> 403（路由守卫之外 handler 自身也兜底）；
//   - 最后一位 owner 的服务层不变式（ErrLastOwner -> 409）保留。
//
// 分层约定：目标保护在 handler 完成（T12「涉及文件」明确
// internal/handler/tenant_member.go 的 UpdateMemberRole 保护），service
// 的 UpdateRole 继续承担最后 owner 原子保护与审计，供 transfer-owner
// 内部流程复用。测试因此断言「被保护的请求不会调用 service.UpdateRole」。

// roleGuardMemberService 只实现本组用例会触达的两个方法；其余方法由
// 嵌入的接口兜底（未实现即 panic，等价于「不该走到这里」）。
type roleGuardMemberService struct {
	interfaces.TenantMemberService
	getMembership func(ctx context.Context, userID string, tenantID uint64) (*types.TenantMember, error)
	updateRole    func(ctx context.Context, userID string, tenantID uint64, newRole types.TenantRole) error
}

func (s *roleGuardMemberService) GetMembership(
	ctx context.Context,
	userID string,
	tenantID uint64,
) (*types.TenantMember, error) {
	return s.getMembership(ctx, userID, tenantID)
}

func (s *roleGuardMemberService) UpdateRole(
	ctx context.Context,
	userID string,
	tenantID uint64,
	newRole types.TenantRole,
) error {
	return s.updateRole(ctx, userID, tenantID, newRole)
}

type memberRoleSession struct {
	callerID     string
	role         types.TenantRole
	systemAdmin  bool
	activeTenant uint64
}

func activeMember(userID string, tenantID uint64, role types.TenantRole) *types.TenantMember {
	return &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusActive,
	}
}

// putMemberRole 用真实 handler + 真实租户路由中间件发出 PUT 请求。
// 请求上下文复刻 Auth 中间件种入的调用方身份（用户 id、工作区角色、
// 系统管理员标志、活动租户）。
func putMemberRole(
	t *testing.T,
	ms interfaces.TenantMemberService,
	s memberRoleSession,
	pathTenant uint64,
	targetUserID string,
	newRole types.TenantRole,
) *httptest.ResponseRecorder {
	t.Helper()
	if s.activeTenant == 0 {
		s.activeTenant = pathTenant
	}
	h := newTestMemberHandler(ms, &stubMemberUserService{})
	r := memberTestRouter(h)

	body, err := json.Marshal(map[string]string{"role": string(newRole)})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(
		http.MethodPut,
		fmt.Sprintf("/tenants/%d/members/%s", pathTenant, targetUserID),
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")

	ctx := req.Context()
	ctx = context.WithValue(ctx, types.UserIDContextKey, s.callerID)
	ctx = context.WithValue(ctx, types.TenantIDContextKey, s.activeTenant)
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, s.role)
	ctx = context.WithValue(ctx, types.SystemAdminContextKey, s.systemAdmin)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestUpdateMemberRole_SystemAdminManagesNonOwnerTargets 覆盖 AC-8：
// 超管即使在本空间只是 viewer/contributor 也要能改非 owner 成员角色，
// 包括把 viewer 提升为 admin 以及提升为 owner。
func TestUpdateMemberRole_SystemAdminManagesNonOwnerTargets(t *testing.T) {
	for _, workspaceRole := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		for _, newRole := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
			workspaceRole, newRole := workspaceRole, newRole
			t.Run(string(workspaceRole)+"_to_"+string(newRole), func(t *testing.T) {
				var gotRole types.TenantRole
				called := false
				ms := &roleGuardMemberService{
					getMembership: func(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
						if userID != "u-target" || tenantID != 1 {
							t.Fatalf("unexpected membership lookup: user=%s tenant=%d", userID, tenantID)
						}
						return activeMember("u-target", 1, types.TenantRoleViewer), nil
					},
					updateRole: func(_ context.Context, _ string, _ uint64, newRole types.TenantRole) error {
						called = true
						gotRole = newRole
						return nil
					},
				}

				w := putMemberRole(t, ms, memberRoleSession{
					callerID: "u-sysadmin", role: workspaceRole, systemAdmin: true,
				}, 1, "u-target", newRole)

				if w.Code != http.StatusOK {
					t.Fatalf("system admin with workspace role %s must be able to set %s, got %d body=%s",
						workspaceRole, newRole, w.Code, w.Body.String())
				}
				if !called {
					t.Fatalf("service.UpdateRole must be called for a permitted change")
				}
				if gotRole != newRole {
					t.Fatalf("service.UpdateRole got role %q, want %q", gotRole, newRole)
				}
			})
		}
	}
}

// TestUpdateMemberRole_OwnerPromotesNonOwnerToCoOwner 覆盖 Q17 B / AC-12：
// owner 可以把 viewer/contributor/admin 提升为 owner。
func TestUpdateMemberRole_OwnerPromotesNonOwnerToCoOwner(t *testing.T) {
	for _, sourceRole := range []types.TenantRole{
		types.TenantRoleViewer,
		types.TenantRoleContributor,
		types.TenantRoleAdmin,
	} {
		sourceRole := sourceRole
		t.Run("from_"+string(sourceRole), func(t *testing.T) {
			called := false
			ms := &roleGuardMemberService{
				getMembership: func(context.Context, string, uint64) (*types.TenantMember, error) {
					return activeMember("u-target", 1, sourceRole), nil
				},
				updateRole: func(_ context.Context, _ string, _ uint64, newRole types.TenantRole) error {
					called = true
					if newRole != types.TenantRoleOwner {
						t.Fatalf("expected promotion to owner, got %s", newRole)
					}
					return nil
				},
			}

			w := putMemberRole(t, ms, memberRoleSession{
				callerID: "u-owner", role: types.TenantRoleOwner,
			}, 1, "u-target", types.TenantRoleOwner)

			if w.Code != http.StatusOK {
				t.Fatalf("owner must be able to promote %s to co-owner, got %d body=%s",
					sourceRole, w.Code, w.Body.String())
			}
			if !called {
				t.Fatalf("service.UpdateRole must be called for a permitted promotion")
			}
		})
	}
}

// TestUpdateMemberRole_OwnerCannotDemoteCoOwner 覆盖 AC-13/AC-20：
// 只要目标当前是 owner，任何降级都必须走 transfer-owner，直接 PUT 403。
func TestUpdateMemberRole_OwnerCannotDemoteCoOwner(t *testing.T) {
	for _, newRole := range []types.TenantRole{
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		newRole := newRole
		t.Run("to_"+string(newRole), func(t *testing.T) {
			updateCalled := false
			ms := &roleGuardMemberService{
				getMembership: func(context.Context, string, uint64) (*types.TenantMember, error) {
					return activeMember("u-other-owner", 1, types.TenantRoleOwner), nil
				},
				updateRole: func(context.Context, string, uint64, types.TenantRole) error {
					updateCalled = true
					return nil
				},
			}

			w := putMemberRole(t, ms, memberRoleSession{
				callerID: "u-owner", role: types.TenantRoleOwner,
			}, 1, "u-other-owner", newRole)

			if w.Code != http.StatusForbidden {
				t.Fatalf("owner demoting a co-owner must be 403, got %d body=%s", w.Code, w.Body.String())
			}
			if updateCalled {
				t.Fatalf("service.UpdateRole must NOT run for a forbidden owner demotion")
			}
			if !strings.Contains(w.Body.String(), "transfer-owner") {
				t.Fatalf("403 body must point callers at transfer-owner, got %s", w.Body.String())
			}
		})
	}
}

// TestUpdateMemberRole_SystemAdminCannotDemoteOwner 覆盖 AC-8/AC-13：
// 超管也不能绕过交接直接降级 owner。
func TestUpdateMemberRole_SystemAdminCannotDemoteOwner(t *testing.T) {
	updateCalled := false
	ms := &roleGuardMemberService{
		getMembership: func(context.Context, string, uint64) (*types.TenantMember, error) {
			return activeMember("u-owner", 1, types.TenantRoleOwner), nil
		},
		updateRole: func(context.Context, string, uint64, types.TenantRole) error {
			updateCalled = true
			return nil
		},
	}

	w := putMemberRole(t, ms, memberRoleSession{
		callerID: "u-sysadmin", role: types.TenantRoleViewer, systemAdmin: true,
	}, 1, "u-owner", types.TenantRoleAdmin)

	if w.Code != http.StatusForbidden {
		t.Fatalf("system admin demoting an owner must be 403, got %d body=%s", w.Code, w.Body.String())
	}
	if updateCalled {
		t.Fatalf("service.UpdateRole must NOT run for a forbidden owner demotion")
	}
	if !strings.Contains(w.Body.String(), "transfer-owner") {
		t.Fatalf("403 body must point callers at transfer-owner, got %s", w.Body.String())
	}
}

// TestUpdateMemberRole_NonOwnerNonSystemAdminForbidden 覆盖 AC-6 / AC-38：
// 路由守卫之外 handler 自身也拒绝普通角色，避免未来路由装配回退时
// 静默 fail-open。
func TestUpdateMemberRole_NonOwnerNonSystemAdminForbidden(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			updateCalled := false
			ms := &roleGuardMemberService{
				getMembership: func(context.Context, string, uint64) (*types.TenantMember, error) {
					return activeMember("u-target", 1, types.TenantRoleViewer), nil
				},
				updateRole: func(context.Context, string, uint64, types.TenantRole) error {
					updateCalled = true
					return nil
				},
			}

			w := putMemberRole(t, ms, memberRoleSession{
				callerID: "u-ordinary", role: role,
			}, 1, "u-target", types.TenantRoleAdmin)

			if w.Code != http.StatusForbidden {
				t.Fatalf("ordinary %s must be 403 at the handler layer, got %d body=%s",
					role, w.Code, w.Body.String())
			}
			if updateCalled {
				t.Fatalf("service.UpdateRole must NOT run for an ordinary caller")
			}
		})
	}
}

// TestUpdateMemberRole_TargetMembershipMissingIs404 保护既有映射：
// 目标不是本空间 active 成员时仍返回 404。
func TestUpdateMemberRole_TargetMembershipMissingIs404(t *testing.T) {
	ms := &roleGuardMemberService{
		getMembership: func(context.Context, string, uint64) (*types.TenantMember, error) {
			return nil, nil
		},
		updateRole: func(context.Context, string, uint64, types.TenantRole) error {
			t.Fatalf("service.UpdateRole must not run when the target is not a member")
			return nil
		},
	}

	w := putMemberRole(t, ms, memberRoleSession{
		callerID: "u-owner", role: types.TenantRoleOwner,
	}, 1, "u-ghost", types.TenantRoleAdmin)

	if w.Code != http.StatusNotFound {
		t.Fatalf("missing target membership must be 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestUpdateMemberRole_TargetNotActiveIs404 覆盖 T12-5 的 "非 active 目标"
// 分支：目标在本空间有成员关系，但状态是 invited / suspended 时，按 404
// 处理且不得调用 service.UpdateRole（与"目标不存在"同一口径）。
func TestUpdateMemberRole_TargetNotActiveIs404(t *testing.T) {
	for _, status := range []types.TenantMemberStatus{
		types.TenantMemberStatusInvited,
		types.TenantMemberStatusSuspended,
	} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			ms := &roleGuardMemberService{
				getMembership: func(context.Context, string, uint64) (*types.TenantMember, error) {
					m := activeMember("u-target", 1, types.TenantRoleViewer)
					m.Status = status
					return m, nil
				},
				updateRole: func(context.Context, string, uint64, types.TenantRole) error {
					t.Fatalf("service.UpdateRole must not run when the target is not active")
					return nil
				},
			}

			w := putMemberRole(t, ms, memberRoleSession{
				callerID: "u-owner", role: types.TenantRoleOwner,
			}, 1, "u-target", types.TenantRoleAdmin)

			if w.Code != http.StatusNotFound {
				t.Fatalf("non-active target must be 404, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
