package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T35 TDD 红阶段：平台级封禁 / 解封 / 注销 handler 契约。
//
// 假定契约（详见 05-tests §54）：
//   - UserManagementHandler 新增
//     PlatformBanUser / PlatformUnbanUser / PlatformPurgeUser；
//   - 路径 POST /system/admin/users/:user_id/ban|unban|purge；
//   - ban/unban 请求体 {reason?}，响应
//     {success:true,data:{user:{id,username,email,is_active,status}}}；
//   - purge 请求体 {confirm_email, reason?}，响应
//     {success:true,data:{user_id}}；
//   - 平台接口不要求目标成员关系，因此 handler 不得依赖 memberService；
//   - 响应绝不包含 preferences / password_hash / 邮箱以外的 PII。

type t35FakePlatformService struct {
	interfaces.UserManagementService

	banFn   func(ctx context.Context, targetUserID, reason string) (*types.User, error)
	unbanFn func(ctx context.Context, targetUserID, reason string) (*types.User, error)
	purgeFn func(ctx context.Context, targetUserID, confirmEmail, reason string) error

	banCalls    int
	unbanCalls  int
	purgeCalls  int
	lastTarget  string
	lastReason  string
	lastConfirm string
	lastPurgeID string
}

func (s *t35FakePlatformService) PlatformBanUser(
	ctx context.Context, targetUserID, reason string,
) (*types.User, error) {
	s.banCalls++
	s.lastTarget, s.lastReason = targetUserID, reason
	return s.banFn(ctx, targetUserID, reason)
}

func (s *t35FakePlatformService) PlatformUnbanUser(
	ctx context.Context, targetUserID, reason string,
) (*types.User, error) {
	s.unbanCalls++
	s.lastTarget, s.lastReason = targetUserID, reason
	return s.unbanFn(ctx, targetUserID, reason)
}

func (s *t35FakePlatformService) PlatformPurgeUser(
	ctx context.Context, targetUserID, confirmEmail, reason string,
) error {
	s.purgeCalls++
	s.lastPurgeID, s.lastConfirm, s.lastReason = targetUserID, confirmEmail, reason
	return s.purgeFn(ctx, targetUserID, confirmEmail, reason)
}

func t35HandlerEngine(h *UserManagementHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.UserIDContextKey, "sysadmin")
		ctx = context.WithValue(ctx, types.TenantIDContextKey, uint64(10))
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, true)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.POST("/api/v1/system/admin/users/:user_id/ban", h.PlatformBanUser)
	r.POST("/api/v1/system/admin/users/:user_id/unban", h.PlatformUnbanUser)
	r.POST("/api/v1/system/admin/users/:user_id/purge", h.PlatformPurgeUser)
	return r
}

func t35PostHandler(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func t35TargetUser(active bool) *types.User {
	prefTenant := uint64(9)
	return &types.User{
		ID:           "u-target",
		Username:     "target",
		Email:        "target@example.com",
		PasswordHash: "secret-hash-must-not-leak",
		IsActive:     active,
		Preferences: types.UserPreferences{
			LastActiveTenantID: &prefTenant,
		},
	}
}

// ban 响应只暴露最小账号摘要，且不带 preferences / 密码哈希。
func TestT35PlatformHandlerBanReturnsSummaryWithoutPreferences(t *testing.T) {
	fake := &t35FakePlatformService{
		banFn: func(context.Context, string, string) (*types.User, error) {
			return t35TargetUser(false), nil
		},
	}
	h := NewUserManagementHandler(fake, nil) // memberService=nil 证明不依赖成员关系
	w := t35PostHandler(t, t35HandlerEngine(h),
		"/api/v1/system/admin/users/u-target/ban", `{"reason":"  spam  "}`)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, fake.banCalls)
	require.Equal(t, "u-target", fake.lastTarget)
	require.Equal(t, "spam", fake.lastReason, "reason must be trimmed")

	body := w.Body.String()
	require.Contains(t, body, `"is_active":false`)
	require.Contains(t, body, `"status":"banned"`)
	require.NotContains(t, body, "preferences")
	require.NotContains(t, body, "last_active_tenant_id")
	require.NotContains(t, body, "bound_agent_ids_by_tenant")
	require.NotContains(t, body, "password_hash")
	require.NotContains(t, body, "secret-hash-must-not-leak")
}

func TestT35PlatformHandlerUnbanReturnsActiveSummary(t *testing.T) {
	fake := &t35FakePlatformService{
		unbanFn: func(context.Context, string, string) (*types.User, error) {
			return t35TargetUser(true), nil
		},
	}
	h := NewUserManagementHandler(fake, nil)
	w := t35PostHandler(t, t35HandlerEngine(h),
		"/api/v1/system/admin/users/u-target/unban", `{"reason":"appeal accepted"}`)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, fake.unbanCalls)
	body := w.Body.String()
	require.Contains(t, body, `"is_active":true`)
	require.Contains(t, body, `"status":"active"`)
	require.NotContains(t, body, "preferences")
	require.NotContains(t, body, "secret-hash-must-not-leak")
}

// purge 必须把 confirm_email 原样（去空格）传给 service，响应只回 user_id。
func TestT35PlatformHandlerPurgeForwardsConfirmEmail(t *testing.T) {
	fake := &t35FakePlatformService{
		purgeFn: func(context.Context, string, string, string) error { return nil },
	}
	h := NewUserManagementHandler(fake, nil)
	w := t35PostHandler(t, t35HandlerEngine(h),
		"/api/v1/system/admin/users/u-target/purge",
		`{"confirm_email":"  target@example.com  ","reason":"gdpr"}`)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, fake.purgeCalls)
	require.Equal(t, "u-target", fake.lastPurgeID)
	require.Equal(t, "target@example.com", fake.lastConfirm, "confirm_email must be trimmed")
	require.Equal(t, "gdpr", fake.lastReason)

	body := w.Body.String()
	require.Contains(t, body, `"user_id":"u-target"`)
	require.NotContains(t, body, "target@example.com", "purge response must not echo PII")
	require.NotContains(t, body, "preferences")
}

// 服务层错误按 AppError 映射：邮箱不匹配 400，owner 未交接 / 特权目标 403。
func TestT35PlatformHandlerPurgeMapsServiceErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "confirm_email_mismatch", err: apperrors.NewValidationError("confirm email does not match the target account"), want: http.StatusBadRequest},
		{name: "owner_handover_required", err: apperrors.NewForbiddenError("hand over the workspace before purging an owner account"), want: http.StatusForbidden},
		{name: "privileged_target", err: apperrors.NewForbiddenError("revoke system administrator privileges before purging this account"), want: http.StatusForbidden},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fake := &t35FakePlatformService{
				purgeFn: func(context.Context, string, string, string) error { return tc.err },
			}
			h := NewUserManagementHandler(fake, nil)
			w := t35PostHandler(t, t35HandlerEngine(h),
				"/api/v1/system/admin/users/u-target/purge",
				`{"confirm_email":"target@example.com"}`)
			require.Equal(t, tc.want, w.Code)
		})
	}
}

// 非法 JSON 必须先 400，不得触达 service。
func TestT35PlatformHandlerRejectsMalformedJSON(t *testing.T) {
	fake := &t35FakePlatformService{
		banFn:   func(context.Context, string, string) (*types.User, error) { return nil, nil },
		unbanFn: func(context.Context, string, string) (*types.User, error) { return nil, nil },
		purgeFn: func(context.Context, string, string, string) error { return nil },
	}
	h := NewUserManagementHandler(fake, nil)
	r := t35HandlerEngine(h)

	for _, action := range []string{"ban", "unban", "purge"} {
		w := t35PostHandler(t, r,
			"/api/v1/system/admin/users/u-target/"+action, "not-json")
		require.Equal(t, http.StatusBadRequest, w.Code, action)
	}
	require.Zero(t, fake.banCalls+fake.unbanCalls+fake.purgeCalls,
		"malformed JSON must not reach the service")
}

// 兜底：响应整体可解析为约定的 JSON 包裹。
func TestT35PlatformHandlerResponseEnvelope(t *testing.T) {
	fake := &t35FakePlatformService{
		banFn: func(context.Context, string, string) (*types.User, error) {
			return t35TargetUser(false), nil
		},
	}
	h := NewUserManagementHandler(fake, nil)
	w := t35PostHandler(t, t35HandlerEngine(h),
		"/api/v1/system/admin/users/u-target/ban", `{}`)
	require.Equal(t, http.StatusOK, w.Code)

	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			User struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Equal(t, "u-target", payload.Data.User.ID)
	require.Equal(t, "banned", payload.Data.User.Status)
}
