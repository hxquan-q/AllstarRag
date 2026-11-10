package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T05 红阶段：邀请 / 建号 / 添加成员 / 改角色的 HTTP 入口只允许
// admin/viewer；owner/contributor 必须 4xx。当前 handler 多数只做
// IsValid()，因此 owner/contributor 仍会成功，预期失败。

type t05UserSvc struct {
	interfaces.UserService
	user *types.User
}

func (s *t05UserSvc) GetUserByEmail(context.Context, string) (*types.User, error) {
	return s.user, nil
}
func (s *t05UserSvc) GetUserByID(context.Context, string) (*types.User, error) {
	return nil, nil
}
func (s *t05UserSvc) UpdateUser(context.Context, *types.User) error { return nil }

type t05InvitationSvc struct {
	interfaces.TenantInvitationService
	createCalls int
	linkCalls   int
}

func (s *t05InvitationSvc) Create(
	_ context.Context, tenantID uint64, userID string, role types.TenantRole,
	_ *string, _ string,
) (*types.TenantInvitation, error) {
	s.createCalls++
	return &types.TenantInvitation{
		ID:            1,
		TenantID:      tenantID,
		InviteeUserID: userID,
		Role:          role,
		Status:        types.TenantInvitationStatusPending,
		ExpiresAt:     time.Now().Add(time.Hour),
	}, nil
}

func (s *t05InvitationSvc) CreateShareLink(
	_ context.Context, tenantID uint64, role types.TenantRole, _ *string, _ string,
) (*types.TenantInvitation, string, error) {
	s.linkCalls++
	token := "t05-share-token"
	return &types.TenantInvitation{
		ID:        2,
		TenantID:  tenantID,
		Token:     token,
		Role:      role,
		Status:    types.TenantInvitationStatusPending,
		ExpiresAt: time.Now().Add(time.Hour),
	}, token, nil
}

type t05MemberSvc struct {
	interfaces.TenantMemberService
	addCalls    int
	addedRole   types.TenantRole
	updateCalls int
	updatedRole types.TenantRole
	current     *types.TenantMember
}

func (s *t05MemberSvc) AddMember(
	_ context.Context, userID string, tenantID uint64, role types.TenantRole, _ *string,
) (*types.TenantMember, error) {
	s.addCalls++
	s.addedRole = role
	return &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusActive,
	}, nil
}

func (s *t05MemberSvc) GetMembership(
	_ context.Context, _ string, _ uint64,
) (*types.TenantMember, error) {
	return s.current, nil
}

func (s *t05MemberSvc) UpdateRole(
	_ context.Context, _ string, _ uint64, newRole types.TenantRole,
) error {
	s.updateCalls++
	s.updatedRole = newRole
	return nil
}

func (s *t05MemberSvc) UpdateRoleGuarded(
	_ context.Context, _ string, _ uint64,
	_ types.TenantRole, newRole types.TenantRole, _ bool,
) error {
	s.updateCalls++
	s.updatedRole = newRole
	return nil
}

func t05Router(inv *TenantInvitationHandler, mem *TenantMemberHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(errorCapture())
	r.POST("/tenants/:id/invitations", inv.CreateInvitation)
	r.POST("/tenants/:id/invite-links", inv.CreateInviteLink)
	r.POST("/tenants/:id/members", mem.AddMember)
	r.POST("/tenants/:id/members/create", mem.CreateMemberAccount)
	r.PUT("/tenants/:id/members/:user_id", mem.UpdateMemberRole)
	return r
}

func t05Require4xx(t *testing.T, w *httptest.ResponseRecorder, label string) {
	t.Helper()
	require.GreaterOrEqual(t, w.Code, http.StatusBadRequest, "%s must 4xx, body=%s", label, w.Body.String())
	require.Less(t, w.Code, http.StatusInternalServerError, "%s must be a client error, body=%s", label, w.Body.String())
}

func t05OwnerCtx() accountCtxOpts {
	return accountCtxOpts{userID: "u-owner", role: types.TenantRoleOwner}
}

func TestT05InvitationHTTPRoleWhitelist(t *testing.T) {
	allowed := []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleViewer}
	denied := []types.TenantRole{types.TenantRoleOwner, types.TenantRoleContributor}

	for _, role := range denied {
		role := role
		t.Run("invitation_deny_"+string(role), func(t *testing.T) {
			invites := &t05InvitationSvc{}
			users := &t05UserSvc{user: &types.User{ID: "u-bob", Email: "bob@x.com"}}
			members := &t05MemberSvc{}
			r := t05Router(
				NewTenantInvitationHandler(invites, users, nil, nil, nil, nil),
				NewTenantMemberHandler(members, users),
			)
			w := doAccountJSON(t, r, http.MethodPost, "/tenants/1/invitations",
				map[string]any{"email": "bob@x.com", "role": role}, t05OwnerCtx())
			t05Require4xx(t, w, "POST /invitations role="+string(role))
			require.Zero(t, invites.createCalls, "rejected role must not reach invitation service")
		})

		t.Run("invite_link_deny_"+string(role), func(t *testing.T) {
			invites := &t05InvitationSvc{}
			users := &t05UserSvc{user: &types.User{ID: "u-bob", Email: "bob@x.com"}}
			members := &t05MemberSvc{}
			r := t05Router(
				NewTenantInvitationHandler(invites, users, nil, nil, nil, nil),
				NewTenantMemberHandler(members, users),
			)
			w := doAccountJSON(t, r, http.MethodPost, "/tenants/1/invite-links",
				map[string]any{"role": role}, t05OwnerCtx())
			t05Require4xx(t, w, "POST /invite-links role="+string(role))
			require.Zero(t, invites.linkCalls, "rejected role must not reach share-link service")
		})
	}

	for _, role := range allowed {
		role := role
		t.Run("invitation_allow_"+string(role), func(t *testing.T) {
			invites := &t05InvitationSvc{}
			users := &t05UserSvc{user: &types.User{ID: "u-bob", Email: "bob@x.com"}}
			members := &t05MemberSvc{}
			r := t05Router(
				NewTenantInvitationHandler(invites, users, nil, nil, nil, nil),
				NewTenantMemberHandler(members, users),
			)
			w := doAccountJSON(t, r, http.MethodPost, "/tenants/1/invitations",
				map[string]any{"email": "bob@x.com", "role": role}, t05OwnerCtx())
			require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
			require.Equal(t, 1, invites.createCalls)
		})

		t.Run("invite_link_allow_"+string(role), func(t *testing.T) {
			invites := &t05InvitationSvc{}
			users := &t05UserSvc{user: &types.User{ID: "u-bob", Email: "bob@x.com"}}
			members := &t05MemberSvc{}
			r := t05Router(
				NewTenantInvitationHandler(invites, users, nil, nil, nil, nil),
				NewTenantMemberHandler(members, users),
			)
			w := doAccountJSON(t, r, http.MethodPost, "/tenants/1/invite-links",
				map[string]any{"role": role}, t05OwnerCtx())
			require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
			require.Equal(t, 1, invites.linkCalls)
		})
	}
}

func TestT05AddMemberHTTPRoleWhitelist(t *testing.T) {
	denied := []types.TenantRole{types.TenantRoleOwner, types.TenantRoleContributor}
	allowed := []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleViewer}

	for _, role := range denied {
		role := role
		t.Run("deny_"+string(role), func(t *testing.T) {
			members := &t05MemberSvc{}
			users := &t05UserSvc{user: &types.User{ID: "u-bob", Email: "bob@x.com"}}
			r := t05Router(
				NewTenantInvitationHandler(&t05InvitationSvc{}, users, nil, nil, nil, nil),
				NewTenantMemberHandler(members, users),
			)
			w := doAccountJSON(t, r, http.MethodPost, "/tenants/1/members",
				map[string]any{"email": "bob@x.com", "role": role}, t05OwnerCtx())
			t05Require4xx(t, w, "POST /members role="+string(role))
			require.Zero(t, members.addCalls, "rejected role must not reach AddMember")
		})
	}

	for _, role := range allowed {
		role := role
		t.Run("allow_"+string(role), func(t *testing.T) {
			members := &t05MemberSvc{}
			users := &t05UserSvc{user: &types.User{ID: "u-bob", Email: "bob@x.com"}}
			r := t05Router(
				NewTenantInvitationHandler(&t05InvitationSvc{}, users, nil, nil, nil, nil),
				NewTenantMemberHandler(members, users),
			)
			w := doAccountJSON(t, r, http.MethodPost, "/tenants/1/members",
				map[string]any{"email": "bob@x.com", "role": role}, t05OwnerCtx())
			require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
			require.Equal(t, 1, members.addCalls)
			require.Equal(t, role, members.addedRole)
		})
	}
}

func TestT05CreateMemberAccountHTTPRoleWhitelist(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleOwner, types.TenantRoleContributor} {
		role := role
		t.Run("deny_"+string(role), func(t *testing.T) {
			users := &accountTestUserService{}
			members := &accountTestMemberService{}
			h := NewTenantMemberHandler(members, users)
			w := doAccountJSON(t, accountTestRouter(h), http.MethodPost, "/tenants/1/members/create",
				validCreateBody(role), t05OwnerCtx())
			t05Require4xx(t, w, "POST /members/create role="+string(role))
			require.Zero(t, users.createCalls)
			require.Zero(t, members.addCalls)
		})
	}

	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleViewer} {
		role := role
		t.Run("allow_"+string(role), func(t *testing.T) {
			users := &accountTestUserService{}
			members := &accountTestMemberService{}
			h := NewTenantMemberHandler(members, users)
			w := doAccountJSON(t, accountTestRouter(h), http.MethodPost, "/tenants/1/members/create",
				validCreateBody(role), t05OwnerCtx())
			require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
			require.Equal(t, 1, users.createCalls)
			require.Equal(t, 1, members.addCalls)
			require.Equal(t, role, members.addedRole)
		})
	}
}

func TestT05UpdateMemberRoleHTTPRoleWhitelist(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleOwner, types.TenantRoleContributor} {
		role := role
		t.Run("deny_"+string(role), func(t *testing.T) {
			members := &t05MemberSvc{current: &types.TenantMember{
				UserID:   "u-target",
				TenantID: 1,
				Role:     types.TenantRoleViewer,
				Status:   types.TenantMemberStatusActive,
			}}
			users := &t05UserSvc{}
			r := t05Router(
				NewTenantInvitationHandler(&t05InvitationSvc{}, users, nil, nil, nil, nil),
				NewTenantMemberHandler(members, users),
			)
			w := doAccountJSON(t, r, http.MethodPut, "/tenants/1/members/u-target",
				map[string]any{"role": role}, t05OwnerCtx())
			t05Require4xx(t, w, "PUT /members/:id role="+string(role))
			require.Zero(t, members.updateCalls, "rejected role must not reach UpdateRole")
		})
	}

	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleViewer} {
		role := role
		t.Run("allow_"+string(role), func(t *testing.T) {
			members := &t05MemberSvc{current: &types.TenantMember{
				UserID:   "u-target",
				TenantID: 1,
				Role:     types.TenantRoleViewer,
				Status:   types.TenantMemberStatusActive,
			}}
			users := &t05UserSvc{}
			r := t05Router(
				NewTenantInvitationHandler(&t05InvitationSvc{}, users, nil, nil, nil, nil),
				NewTenantMemberHandler(members, users),
			)
			w := doAccountJSON(t, r, http.MethodPut, "/tenants/1/members/u-target",
				map[string]any{"role": role}, t05OwnerCtx())
			require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
			require.Equal(t, 1, members.updateCalls)
			require.Equal(t, role, members.updatedRole)
		})
	}
}
