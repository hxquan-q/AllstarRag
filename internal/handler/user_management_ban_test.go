package handler

import (
	"context"
	"encoding/json"
	"fmt"
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

// T02 handler 层红阶段用例（T02-1/2、AC-16、AC-17、AC-32、AC-33）。
//
// 假定契约：
//   - internal/handler/user_management.go 提供 UserManagementHandler 与
//     NewUserManagementHandler(userManagementService, memberService)；
//   - POST /tenants/:id/members/:user_id/ban|unban 返回 200
//     {success:true,data:{user,member}}；
//   - 服务层拒绝以 *apperrors.AppError 透传（403 保护目标 / 404 非本空间成员）；
//   - 响应不得包含密码哈希、token 等敏感字段。

type umFakeUserManagementService struct {
	// T04 扩展 UserManagementService（owner 空间 / 交接 / co-owner）。这里嵌入
	// 接口让 T02/T03 的测试替身无需逐个实现新方法即可继续编译；本轮只有在
	// 调用到未覆盖方法时才会 nil panic，而 T02/T03 用例不会调用它们。
	interfaces.UserManagementService

	banFn   func(ctx context.Context, tenantID uint64, targetUserID, reason string) (*types.User, error)
	unbanFn func(ctx context.Context, tenantID uint64, targetUserID, reason string) (*types.User, error)
	purgeFn func(ctx context.Context, tenantID uint64, targetUserID, confirmEmail, reason string) error

	lastTenantID uint64
	lastTargetID string
	lastReason   string

	lastPurgeTenantID     uint64
	lastPurgeTargetID     string
	lastPurgeConfirmEmail string
	lastPurgeReason       string
}

func (s *umFakeUserManagementService) BanUser(
	ctx context.Context, tenantID uint64, targetUserID, reason string,
) (*types.User, error) {
	s.lastTenantID, s.lastTargetID, s.lastReason = tenantID, targetUserID, reason
	return s.banFn(ctx, tenantID, targetUserID, reason)
}

func (s *umFakeUserManagementService) UnbanUser(
	ctx context.Context, tenantID uint64, targetUserID, reason string,
) (*types.User, error) {
	s.lastTenantID, s.lastTargetID, s.lastReason = tenantID, targetUserID, reason
	return s.unbanFn(ctx, tenantID, targetUserID, reason)
}

func (s *umFakeUserManagementService) PurgeUser(
	ctx context.Context, tenantID uint64, targetUserID, confirmEmail, reason string,
) error {
	s.lastPurgeTenantID = tenantID
	s.lastPurgeTargetID = targetUserID
	s.lastPurgeConfirmEmail = confirmEmail
	s.lastPurgeReason = reason
	return s.purgeFn(ctx, tenantID, targetUserID, confirmEmail, reason)
}

type umFakeMembershipService struct {
	interfaces.TenantMemberService
	member *types.TenantMember
	err    error
}

func (s *umFakeMembershipService) GetMembership(
	_ context.Context, _ string, _ uint64,
) (*types.TenantMember, error) {
	return s.member, s.err
}

type umHandlerPayload struct {
	Success bool `json:"success"`
	Data    struct {
		User struct {
			ID       string `json:"id"`
			Email    string `json:"email"`
			IsActive bool   `json:"is_active"`
		} `json:"user"`
		Member struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
			Status string `json:"status"`
		} `json:"member"`
	} `json:"data"`
}

func newUMHandlerRouter(h *UserManagementHandler) *gin.Engine {
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
	r.POST("/api/v1/tenants/:id/members/:user_id/ban", h.BanMember)
	r.POST("/api/v1/tenants/:id/members/:user_id/unban", h.UnbanMember)
	return r
}

func postUMRoute(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func umDecodeHandlerPayload(t *testing.T, w *httptest.ResponseRecorder) umHandlerPayload {
	t.Helper()
	var payload umHandlerPayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	return payload
}

func umSuccessService(target *types.User) *umFakeUserManagementService {
	return &umFakeUserManagementService{
		banFn:   func(context.Context, uint64, string, string) (*types.User, error) { return target, nil },
		unbanFn: func(context.Context, uint64, string, string) (*types.User, error) { return target, nil },
	}
}

// TestUserManagementBanMemberResponse：ban 成功返回 {user,member}，且不含敏感字段。
func TestUserManagementBanMemberResponse(t *testing.T) {
	svc := umSuccessService(&types.User{
		ID: "target", Email: "target@example.com", IsActive: false,
		PasswordHash: "hash-must-not-leak",
	})
	members := &umFakeMembershipService{member: &types.TenantMember{
		UserID: "target", TenantID: 10, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive,
	}}
	h := NewUserManagementHandler(svc, members)
	r := newUMHandlerRouter(h)

	w := postUMRoute(t, r, "/api/v1/tenants/10/members/target/ban", `{"reason":"policy violation"}`)

	require.Equal(t, http.StatusOK, w.Code)
	payload := umDecodeHandlerPayload(t, w)
	require.True(t, payload.Success)
	require.Equal(t, "target", payload.Data.User.ID)
	require.False(t, payload.Data.User.IsActive)
	require.Equal(t, "target", payload.Data.Member.UserID)
	require.Equal(t, string(types.TenantRoleViewer), payload.Data.Member.Role)
	require.Equal(t, string(types.TenantMemberStatusActive), payload.Data.Member.Status)

	require.EqualValues(t, 10, svc.lastTenantID)
	require.Equal(t, "target", svc.lastTargetID)
	require.Equal(t, "policy violation", svc.lastReason)

	body := w.Body.String()
	require.NotContains(t, body, "password_hash")
	require.NotContains(t, body, "hash-must-not-leak")
	require.NotContains(t, body, "token")
}

// TestUserManagementBanMemberMapsServiceErrors：服务层 403/404 原样映射为 HTTP 状态码。
func TestUserManagementBanMemberMapsServiceErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{name: "protected_target", err: apperrors.NewForbiddenError("target is protected"), status: http.StatusForbidden},
		{name: "not_a_member", err: apperrors.NewNotFoundError("membership not found"), status: http.StatusNotFound},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			svc := &umFakeUserManagementService{
				banFn: func(context.Context, uint64, string, string) (*types.User, error) {
					return nil, tc.err
				},
			}
			h := NewUserManagementHandler(svc, &umFakeMembershipService{})
			r := newUMHandlerRouter(h)

			w := postUMRoute(t, r, "/api/v1/tenants/10/members/target/ban", `{}`)
			require.Equal(t, tc.status, w.Code)
		})
	}
}

// TestUserManagementBanMemberInvalidTenantID：:id 非正整数 → 400。
func TestUserManagementBanMemberInvalidTenantID(t *testing.T) {
	h := NewUserManagementHandler(umSuccessService(&types.User{ID: "target"}), &umFakeMembershipService{})
	r := newUMHandlerRouter(h)

	w := postUMRoute(t, r, "/api/v1/tenants/0/members/target/ban", `{}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestUserManagementUnbanMemberResponse：unban 成功返回 {user,member}。
func TestUserManagementUnbanMemberResponse(t *testing.T) {
	svc := umSuccessService(&types.User{ID: "target", Email: "target@example.com", IsActive: true})
	members := &umFakeMembershipService{member: &types.TenantMember{
		UserID: "target", TenantID: 10, Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive,
	}}
	h := NewUserManagementHandler(svc, members)
	r := newUMHandlerRouter(h)

	w := postUMRoute(t, r, "/api/v1/tenants/10/members/target/unban", `{"reason":"appeal accepted"}`)

	require.Equal(t, http.StatusOK, w.Code)
	payload := umDecodeHandlerPayload(t, w)
	require.True(t, payload.Success)
	require.Equal(t, "target", payload.Data.User.ID)
	require.True(t, payload.Data.User.IsActive)
	require.Equal(t, string(types.TenantRoleAdmin), payload.Data.Member.Role)
	require.Equal(t, "appeal accepted", svc.lastReason)
}

// TestUserManagementUnbanMemberCrossTenantForbidden：跨租户解封 403 透传。
func TestUserManagementUnbanMemberCrossTenantForbidden(t *testing.T) {
	svc := &umFakeUserManagementService{
		unbanFn: func(context.Context, uint64, string, string) (*types.User, error) {
			return nil, apperrors.NewForbiddenError("this account was banned in another workspace")
		},
	}
	h := NewUserManagementHandler(svc, &umFakeMembershipService{})
	r := newUMHandlerRouter(h)

	w := postUMRoute(t, r, "/api/v1/tenants/10/members/target/unban", `{}`)
	require.Equal(t, http.StatusForbidden, w.Code)
}

// TestUserManagementMemberLookupFailureIsNotSilent：成员信息查询失败不能被吞掉
// 后返回 200 空 member（避免前端误显示"已解封/已封禁"）。
func TestUserManagementMemberLookupFailureIsNotSilent(t *testing.T) {
	svc := umSuccessService(&types.User{ID: "target", IsActive: false})
	members := &umFakeMembershipService{err: fmt.Errorf("transient lookup failure")}
	h := NewUserManagementHandler(svc, members)
	r := newUMHandlerRouter(h)

	w := postUMRoute(t, r, "/api/v1/tenants/10/members/target/ban", `{}`)
	require.GreaterOrEqual(t, w.Code, http.StatusInternalServerError)
}
