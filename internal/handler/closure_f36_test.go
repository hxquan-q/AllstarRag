package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func listRouterWithContext(h *TenantInvitationHandler, role types.TenantRole, sysadmin bool) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantRoleContextKey, role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, sysadmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}, errorCapture())
	r.GET("/tenants/:id/invitations", h.ListTenantInvitations)
	return r
}

// F36：纯系统管理员（空间角色仅为 viewer）也能看到共享链接的 invite_url/token；
// 非 owner 的普通成员仍只看到脱敏元数据。
func TestListInvitationsSystemAdminSeesShareLinkToken(t *testing.T) {
	env := newShareLinkTestEnv(t)
	if w := env.do(t, http.MethodPost, shareLinkPath, `{"role":"viewer"}`); w.Code != http.StatusCreated {
		t.Fatalf("create share link status=%d, want 201 (body=%s)", w.Code, w.Body.String())
	}

	do := func(r *gin.Engine) []types.TenantInvitationResponse {
		req := httptest.NewRequest(http.MethodGet, "/tenants/901/invitations", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("list status=%d, want 200 (body=%s)", w.Code, w.Body.String())
		}
		var out struct {
			Data struct {
				Invitations []types.TenantInvitationResponse `json:"invitations"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode list response: %v", err)
		}
		return out.Data.Invitations
	}

	// 系统管理员 + viewer 角色：应看到 invite_url。
	adminRows := do(listRouterWithContext(env.handler, types.TenantRoleViewer, true))
	if len(adminRows) == 0 {
		t.Fatalf("system admin list returned no rows")
	}
	foundLink := false
	for _, row := range adminRows {
		if row.IsShareLink {
			foundLink = true
			if row.InviteURL == "" {
				t.Fatalf("system admin 共享链接 invite_url 为空: %+v", row)
			}
		}
	}
	if !foundLink {
		t.Fatalf("system admin list missing share link row: %+v", adminRows)
	}

	// 对照：非系统管理员的 viewer 仍脱敏。
	for _, row := range do(listRouterWithContext(env.handler, types.TenantRoleViewer, false)) {
		if row.IsShareLink && row.InviteURL != "" {
			t.Fatalf("plain viewer 不应看到 invite_url: %+v", row)
		}
	}
}
