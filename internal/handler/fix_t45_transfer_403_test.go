package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/stretchr/testify/require"
)

// T45 红阶段配套（handler 层）：交接路由必须把目标保护哨兵映射为真实
// HTTP 403。renderManagementError（owner_management.go）已包含
// ErrTargetPrivileged / ErrTargetOwnsWorkspace → 403 的映射，本用例锁定
// 交接路径上的 403 语义，供 service 层恢复判定后直接生效；预期绿。
func TestT45TransferOwnerMapsTargetGuardErrorsToForbidden(t *testing.T) {
	cases := map[string]error{
		"target_privileged":     service.ErrTargetPrivileged,
		"target_owns_workspace": service.ErrTargetOwnsWorkspace,
	}
	for name, sentinel := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &omFakeUserManagementService{
				transferFn: func(context.Context, uint64, string, string) error {
					return sentinel
				},
			}
			h := NewUserManagementHandler(svc, &umFakeMembershipService{})
			r := omNewUserManagementRouter(h)

			w := omPostRoute(t, r, "/api/v1/tenants/10/transfer-owner",
				`{"from_user_id":"actor","to_user_id":"target"}`)

			require.Equal(t, http.StatusForbidden, w.Code,
				"目标保护哨兵必须映射为 403，got %d body=%s", w.Code, w.Body.String())
		})
	}
}
