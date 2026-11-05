package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T03 handler 层红阶段用例（T03-1/2/7、AC-23、AC-32、AC-33）。
//
// 假定契约：
//   - UserManagementHandler 增加 PurgeMember(c *gin.Context)；
//   - POST /tenants/:id/members/:user_id/purge，body {confirm_email, reason?}；
//   - 成功 200 {success:true,data:{user_id}}（成员关系已软删，不再回填 member）；
//   - 服务层 *AppError 原样映射：400 邮箱不匹配 / 403 目标保护 / 404 非本空间成员；
//   - 响应不得包含邮箱、密码哈希、token 等敏感字段。

type umPurgeHandlerPayload struct {
	Success bool `json:"success"`
	Data    struct {
		UserID string `json:"user_id"`
	} `json:"data"`
}

func newUMPurgeHandlerRouter(h *UserManagementHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.UserIDContextKey, "actor")
		ctx = context.WithValue(ctx, types.TenantIDContextKey, uint64(10))
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleOwner)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, false)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.POST("/api/v1/tenants/:id/members/:user_id/purge", h.PurgeMember)
	return r
}

// TestUserManagementPurgeMemberResponse：成功返回 {success:true,data:{user_id}}，
// 且不泄露 PII；请求体参数原样交给服务层。
func TestUserManagementPurgeMemberResponse(t *testing.T) {
	svc := &umFakeUserManagementService{
		purgeFn: func(context.Context, uint64, string, string, string) error { return nil },
	}
	h := NewUserManagementHandler(svc, &umFakeMembershipService{})
	r := newUMPurgeHandlerRouter(h)

	w := postUMRoute(t, r, "/api/v1/tenants/10/members/target/purge",
		`{"confirm_email":"target@example.com","reason":"offboarding"}`)

	require.Equal(t, http.StatusOK, w.Code)
	var payload umPurgeHandlerPayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Equal(t, "target", payload.Data.UserID)

	require.EqualValues(t, 10, svc.lastPurgeTenantID)
	require.Equal(t, "target", svc.lastPurgeTargetID)
	require.Equal(t, "target@example.com", svc.lastPurgeConfirmEmail)
	require.Equal(t, "offboarding", svc.lastPurgeReason)

	body := w.Body.String()
	require.NotContains(t, body, "password_hash")
	require.NotContains(t, body, "token")
	require.NotContains(t, body, "target@example.com",
		"the confirmed email must not be echoed back")
}

// TestUserManagementPurgeMemberMapsServiceErrors：400/403/404 原样透传。
func TestUserManagementPurgeMemberMapsServiceErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{
			name:   "confirm_email_mismatch",
			err:    apperrors.NewValidationError("confirm email does not match"),
			status: http.StatusBadRequest,
		},
		{
			name:   "protected_target",
			err:    apperrors.NewForbiddenError("target is protected"),
			status: http.StatusForbidden,
		},
		{
			name:   "not_a_member",
			err:    apperrors.NewNotFoundError("membership not found"),
			status: http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			svc := &umFakeUserManagementService{
				purgeFn: func(context.Context, uint64, string, string, string) error { return tc.err },
			}
			h := NewUserManagementHandler(svc, &umFakeMembershipService{})
			r := newUMPurgeHandlerRouter(h)

			w := postUMRoute(t, r, "/api/v1/tenants/10/members/target/purge",
				`{"confirm_email":"target@example.com"}`)
			require.Equal(t, tc.status, w.Code)
		})
	}
}

// TestUserManagementPurgeMemberMissingConfirmEmail：缺少 confirm_email → 400。
func TestUserManagementPurgeMemberMissingConfirmEmail(t *testing.T) {
	svc := &umFakeUserManagementService{
		purgeFn: func(_ context.Context, _ uint64, _, confirmEmail, _ string) error {
			if confirmEmail == "" {
				return apperrors.NewValidationError("confirm email is required")
			}
			return nil
		},
	}
	h := NewUserManagementHandler(svc, &umFakeMembershipService{})
	r := newUMPurgeHandlerRouter(h)

	w := postUMRoute(t, r, "/api/v1/tenants/10/members/target/purge", `{"reason":"x"}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestUserManagementPurgeMemberInvalidTenantID：:id 非正整数 → 400。
func TestUserManagementPurgeMemberInvalidTenantID(t *testing.T) {
	svc := &umFakeUserManagementService{
		purgeFn: func(context.Context, uint64, string, string, string) error { return nil },
	}
	h := NewUserManagementHandler(svc, &umFakeMembershipService{})
	r := newUMPurgeHandlerRouter(h)

	w := postUMRoute(t, r, "/api/v1/tenants/0/members/target/purge",
		`{"confirm_email":"target@example.com"}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
}
