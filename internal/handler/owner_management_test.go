package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T04 handler 层红阶段用例（T04-1/3/4、AC-9/10/11/13、AC-30、AC-32）。
//
// 假定契约：
//   - UserManagementHandler 增加 TransferOwner（POST /tenants/:id/transfer-owner）
//     与 CreateCoOwnerAccount（POST /tenants/:id/owners/create）；
//   - 新 OwnerManagementHandler（NewOwnerManagementHandler(userManagementService)）
//     提供 ProvisionOwnerSpace（POST /system/admin/users/:user_id/owner-space）
//     与 CreateOwnerInvite（POST /system/admin/owners/invite）；
//   - 成功响应形状 {success:true,data:{...}}；
//   - 服务层错误按 *apperrors.AppError 原样映射状态码。

type omFakeUserManagementService struct {
	interfaces.UserManagementService

	transferFn  func(ctx context.Context, tenantID uint64, fromUserID, toUserID string) error
	provisionFn func(ctx context.Context, targetUserID, spaceName string) (*types.Tenant, error)
	inviteFn    func(ctx context.Context, email, spaceName string, expiresDays int) (*interfaces.OwnerInviteResult, error)
	coOwnerFn   func(ctx context.Context, tenantID uint64, req *interfaces.CreateCoOwnerRequest) (*types.User, error)

	lastTransferTenantID uint64
	lastTransferFrom     string
	lastTransferTo       string

	lastProvisionTarget string
	lastProvisionSpace  string

	lastInviteEmail   string
	lastInviteSpace   string
	lastInviteExpires int

	lastCoOwnerTenant uint64
	lastCoOwnerReq    *interfaces.CreateCoOwnerRequest
}

func (s *omFakeUserManagementService) TransferOwner(
	ctx context.Context, tenantID uint64, fromUserID, toUserID string,
) error {
	s.lastTransferTenantID, s.lastTransferFrom, s.lastTransferTo = tenantID, fromUserID, toUserID
	if s.transferFn == nil {
		return nil
	}
	return s.transferFn(ctx, tenantID, fromUserID, toUserID)
}

func (s *omFakeUserManagementService) ProvisionOwnerSpace(
	ctx context.Context, targetUserID, spaceName string,
) (*types.Tenant, error) {
	s.lastProvisionTarget, s.lastProvisionSpace = targetUserID, spaceName
	if s.provisionFn == nil {
		return &types.Tenant{ID: 99, Name: spaceName, Status: "active"}, nil
	}
	return s.provisionFn(ctx, targetUserID, spaceName)
}

func (s *omFakeUserManagementService) CreateOwnerInvite(
	ctx context.Context, email, spaceName string, expiresDays int,
) (*interfaces.OwnerInviteResult, error) {
	s.lastInviteEmail, s.lastInviteSpace, s.lastInviteExpires = email, spaceName, expiresDays
	if s.inviteFn == nil {
		return &interfaces.OwnerInviteResult{
			TenantID: 42, InvitationID: 7, Token: "tok",
			ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
		}, nil
	}
	return s.inviteFn(ctx, email, spaceName, expiresDays)
}

func (s *omFakeUserManagementService) CreateCoOwnerAccount(
	ctx context.Context, tenantID uint64, req *interfaces.CreateCoOwnerRequest,
) (*types.User, error) {
	s.lastCoOwnerTenant, s.lastCoOwnerReq = tenantID, req
	if s.coOwnerFn == nil {
		return &types.User{ID: "coowner", Email: "coowner@example.com"}, nil
	}
	return s.coOwnerFn(ctx, tenantID, req)
}

func omNewUserManagementRouter(h *UserManagementHandler) *gin.Engine {
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
	r.POST("/api/v1/tenants/:id/transfer-owner", h.TransferOwner)
	r.POST("/api/v1/tenants/:id/owners/create", h.CreateCoOwnerAccount)
	return r
}

func omNewOwnerManagementRouter(h *OwnerManagementHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.UserIDContextKey, "sysadmin")
		ctx = context.WithValue(ctx, types.TenantIDContextKey, uint64(0))
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, true)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.POST("/api/v1/system/admin/users/:user_id/owner-space", h.ProvisionOwnerSpace)
	r.POST("/api/v1/system/admin/owners/invite", h.CreateOwnerInvite)
	return r
}

func omPostRoute(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

type omTransferResponse struct {
	Success bool `json:"success"`
}

type omCoOwnerResponse struct {
	Success bool `json:"success"`
	Data    struct {
		User struct {
			ID       string `json:"id"`
			Email    string `json:"email"`
			TenantID uint64 `json:"tenant_id"`
		} `json:"user"`
	} `json:"data"`
}

type omProvisionResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Tenant struct {
			ID     uint64 `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"tenant"`
	} `json:"data"`
}

type omInviteResponse struct {
	Success bool `json:"success"`
	Data    struct {
		InviteURL string    `json:"invite_url"`
		TenantID  uint64    `json:"tenant_id"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"data"`
}

// ---------------------------------------------------------------------------
// TransferOwner
// ---------------------------------------------------------------------------

func TestOwnerTransferMemberResponse(t *testing.T) {
	svc := &omFakeUserManagementService{}
	h := NewUserManagementHandler(svc, &umFakeMembershipService{})
	r := omNewUserManagementRouter(h)

	w := omPostRoute(t, r, "/api/v1/tenants/10/transfer-owner",
		`{"from_user_id":"actor","to_user_id":"successor"}`)

	require.Equal(t, http.StatusOK, w.Code)
	var payload omTransferResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.EqualValues(t, 10, svc.lastTransferTenantID)
	require.Equal(t, "actor", svc.lastTransferFrom)
	require.Equal(t, "successor", svc.lastTransferTo)
}

// 02-plan §3.1 body 的 from_user_id 可选；缺省时由服务层按调用者处理。
func TestOwnerTransferMemberFromIsOptional(t *testing.T) {
	svc := &omFakeUserManagementService{}
	h := NewUserManagementHandler(svc, &umFakeMembershipService{})
	r := omNewUserManagementRouter(h)

	w := omPostRoute(t, r, "/api/v1/tenants/10/transfer-owner", `{"to_user_id":"successor"}`)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, svc.lastTransferFrom, "omitted from_user_id must be forwarded as empty")
	require.Equal(t, "successor", svc.lastTransferTo)
}

func TestOwnerTransferMemberMissingTarget(t *testing.T) {
	svc := &omFakeUserManagementService{}
	h := NewUserManagementHandler(svc, &umFakeMembershipService{})
	r := omNewUserManagementRouter(h)

	w := omPostRoute(t, r, "/api/v1/tenants/10/transfer-owner", `{}`)
	require.Equal(t, http.StatusBadRequest, w.Code,
		"to_user_id is required; the service must not be called")
}

func TestOwnerTransferMemberInvalidTenantID(t *testing.T) {
	svc := &omFakeUserManagementService{}
	h := NewUserManagementHandler(svc, &umFakeMembershipService{})
	r := omNewUserManagementRouter(h)

	w := omPostRoute(t, r, "/api/v1/tenants/not-a-number/transfer-owner", `{"to_user_id":"successor"}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestOwnerTransferMemberMapsServiceErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"forbidden": {apperrors.NewForbiddenError("not allowed"), http.StatusForbidden},
		"not_found": {apperrors.NewNotFoundError("membership not found"), http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &omFakeUserManagementService{
				transferFn: func(context.Context, uint64, string, string) error { return tc.err },
			}
			h := NewUserManagementHandler(svc, &umFakeMembershipService{})
			r := omNewUserManagementRouter(h)
			w := omPostRoute(t, r, "/api/v1/tenants/10/transfer-owner", `{"to_user_id":"successor"}`)
			require.Equal(t, tc.code, w.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// CreateCoOwnerAccount
// ---------------------------------------------------------------------------

func TestOwnerCreateCoOwnerAccountResponse(t *testing.T) {
	svc := &omFakeUserManagementService{}
	h := NewUserManagementHandler(svc, &umFakeMembershipService{})
	r := omNewUserManagementRouter(h)

	w := omPostRoute(t, r, "/api/v1/tenants/10/owners/create",
		`{"username":"coowner","email":"coowner@example.com","password":"Passw0rd!123"}`)

	require.Equal(t, http.StatusCreated, w.Code)
	var payload omCoOwnerResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Equal(t, "coowner", payload.Data.User.ID)
	require.Equal(t, "coowner@example.com", payload.Data.User.Email)
	require.EqualValues(t, 10, svc.lastCoOwnerTenant)
	require.NotNil(t, svc.lastCoOwnerReq)
	require.Equal(t, "coowner", svc.lastCoOwnerReq.Username)
	require.NotNil(t, svc.lastCoOwnerReq.Password)
	require.Equal(t, "Passw0rd!123", *svc.lastCoOwnerReq.Password)

	body := w.Body.String()
	require.NotContains(t, body, "Passw0rd!123", "AC-32: the password must not be echoed back")
	require.NotContains(t, body, "password_hash", "AC-32: password hashes must never be returned")
}

func TestOwnerCreateCoOwnerAccountMapsServiceErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"duplicate_identity": {apperrors.NewConflictError("email already registered"), http.StatusConflict},
		"forbidden":          {apperrors.NewForbiddenError("only owners can create co-owners"), http.StatusForbidden},
		"invalid_request":    {apperrors.NewValidationError("username is required"), http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &omFakeUserManagementService{
				coOwnerFn: func(context.Context, uint64, *interfaces.CreateCoOwnerRequest) (*types.User, error) {
					return nil, tc.err
				},
			}
			h := NewUserManagementHandler(svc, &umFakeMembershipService{})
			r := omNewUserManagementRouter(h)
			w := omPostRoute(t, r, "/api/v1/tenants/10/owners/create",
				`{"username":"coowner","email":"coowner@example.com","password":"Passw0rd!123"}`)
			require.Equal(t, tc.code, w.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// OwnerManagementHandler.ProvisionOwnerSpace
// ---------------------------------------------------------------------------

func TestOwnerProvisionOwnerSpaceResponse(t *testing.T) {
	svc := &omFakeUserManagementService{}
	h := NewOwnerManagementHandler(svc)
	r := omNewOwnerManagementRouter(h)

	w := omPostRoute(t, r, "/api/v1/system/admin/users/target/owner-space", `{"space_name":"目标空间"}`)

	require.Equal(t, http.StatusOK, w.Code)
	var payload omProvisionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.EqualValues(t, 99, payload.Data.Tenant.ID)
	require.Equal(t, "目标空间", payload.Data.Tenant.Name)
	require.Equal(t, "target", svc.lastProvisionTarget)
	require.Equal(t, "目标空间", svc.lastProvisionSpace)
}

func TestOwnerProvisionOwnerSpaceMapsServiceErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"not_found": {apperrors.NewNotFoundError("user not found"), http.StatusNotFound},
		"forbidden": {apperrors.NewForbiddenError("system administrator required"), http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &omFakeUserManagementService{
				provisionFn: func(context.Context, string, string) (*types.Tenant, error) {
					return nil, tc.err
				},
			}
			h := NewOwnerManagementHandler(svc)
			r := omNewOwnerManagementRouter(h)
			w := omPostRoute(t, r, "/api/v1/system/admin/users/target/owner-space", `{}`)
			require.Equal(t, tc.code, w.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// OwnerManagementHandler.CreateOwnerInvite
// ---------------------------------------------------------------------------

func TestOwnerCreateOwnerInviteResponse(t *testing.T) {
	svc := &omFakeUserManagementService{}
	h := NewOwnerManagementHandler(svc)
	r := omNewOwnerManagementRouter(h)

	w := omPostRoute(t, r, "/api/v1/system/admin/owners/invite",
		`{"email":"invitee@example.com","space_name":"邀请空间","expires_in_days":3}`)

	require.Equal(t, http.StatusCreated, w.Code)
	var payload omInviteResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.NotEmpty(t, payload.Data.InviteURL, "response must contain invite_url")
	require.EqualValues(t, 42, payload.Data.TenantID, "response must contain tenant_id")
	require.False(t, payload.Data.ExpiresAt.IsZero(), "response must contain expires_at")

	require.Equal(t, "invitee@example.com", svc.lastInviteEmail)
	require.Equal(t, "邀请空间", svc.lastInviteSpace)
	require.Equal(t, 3, svc.lastInviteExpires)
}

// email/space_name/expires_in_days 都可省略；缺省有效期交给服务层按 7 天处理。
func TestOwnerCreateOwnerInviteAllFieldsOptional(t *testing.T) {
	svc := &omFakeUserManagementService{}
	h := NewOwnerManagementHandler(svc)
	r := omNewOwnerManagementRouter(h)

	w := omPostRoute(t, r, "/api/v1/system/admin/owners/invite", `{}`)
	require.Equal(t, http.StatusCreated, w.Code)
	require.Empty(t, svc.lastInviteEmail)
	require.Empty(t, svc.lastInviteSpace)
	require.Equal(t, 0, svc.lastInviteExpires,
		"an omitted expires_in_days must be forwarded as 0 for the service default")
}

func TestOwnerCreateOwnerInviteMapsServiceErrors(t *testing.T) {
	svc := &omFakeUserManagementService{
		inviteFn: func(context.Context, string, string, int) (*interfaces.OwnerInviteResult, error) {
			return nil, apperrors.NewForbiddenError("system administrator required")
		},
	}
	h := NewOwnerManagementHandler(svc)
	r := omNewOwnerManagementRouter(h)

	w := omPostRoute(t, r, "/api/v1/system/admin/owners/invite", `{}`)
	require.Equal(t, http.StatusForbidden, w.Code)
}
