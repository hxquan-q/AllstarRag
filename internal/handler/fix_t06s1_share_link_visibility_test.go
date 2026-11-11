package handler

// T06 补充票（T06S1，口径 A）TDD 红阶段：邀请列表 invite_url 可见性矩阵。
//
// 缺陷（STATE 2026-11-11 14:05/14:20，Codex 发现）：T06 已把共享链接创建路由
// 放宽到普通管理员，但 ListTenantInvitations 的 showShareLinks 仍按
// HasPermission(Owner)||IsSystemAdmin 过滤——普通管理员能建链接，列表却拿不到
// invite_url，前端复制按钮失效（InviteManagement.vue:283 拿到空串）。
//
// 口径 A（用户拍板）：普通管理员可见本空间全部共享链接明文；owner / 系统管理
// 员行为不变；viewer 仍只看脱敏元数据。
//
// 链路复用 tenant_invite_link_options_test.go 的真实 sqlite 环境
// （newShareLinkTestEnv）与 closure_f36_test.go 的 listRouterWithContext
// 上下文注入（角色 + 系统管理员标志写进 request context，与生产 auth
// 中间件同键）。数据直接落库：一条共享链接行（Token 非空、InviteeUserID
// 空）+ 一条普通按用户邀请行（无 token——service.CreateInvitation 不为
// per-user 邀请生成 token），断言只看 invite_url 有无，不比对完整 URL。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// seedT06S1Rows 落两条 pending 行：共享链接 + 普通按用户邀请。
func seedT06S1Rows(t *testing.T, env *shareLinkTestEnv) {
	t.Helper()
	rows := []*types.TenantInvitation{
		{
			// 共享链接行：Token 非空，投影据此判 IsShareLink 并拼 invite_url。
			TenantID:  shareLinkTestTenantID,
			Token:     "t06s1-share-token",
			Role:      types.TenantRoleViewer,
			Status:    types.TenantInvitationStatusPending,
			ExpiresAt: time.Now().Add(time.Hour),
		},
		{
			// 普通按用户邀请行：InviteeUserID 非空、无 token，任何角色都不带 invite_url。
			TenantID:      shareLinkTestTenantID,
			InviteeUserID: "t06s1-invitee",
			Role:          types.TenantRoleViewer,
			Status:        types.TenantInvitationStatusPending,
			ExpiresAt:     time.Now().Add(time.Hour),
		},
	}
	for _, row := range rows {
		if err := env.db.Create(row).Error; err != nil {
			t.Fatalf("seed invitation row: %v", err)
		}
	}
}

// listT06S1Rows 以指定身份拉取邀请列表。
func listT06S1Rows(
	t *testing.T, env *shareLinkTestEnv, role types.TenantRole, sysadmin bool,
) []types.TenantInvitationResponse {
	t.Helper()
	r := listRouterWithContext(env.handler, role, sysadmin)
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
		t.Fatalf("decode list response: %v (body=%s)", err, w.Body.String())
	}
	return out.Data.Invitations
}

// TestT06S1ShareLinkInviteURLVisibility：共享链接 invite_url 的可见性矩阵。
// 红：普通管理员子用例（现状 owner-only，拿不到 invite_url）；
// 绿：owner / 系统管理员 / viewer 三个回归子用例维持现状。
func TestT06S1ShareLinkInviteURLVisibility(t *testing.T) {
	cases := []struct {
		name        string
		role        types.TenantRole
		sysadmin    bool
		wantVisible bool
	}{
		// 口径 A：普通管理员可见本空间全部共享链接明文（现状 FAIL，待 ② 放宽门）。
		{name: "普通管理员可见共享链接invite_url", role: types.TenantRoleAdmin, sysadmin: false, wantVisible: true},
		// 回归：owner 可见。
		{name: "owner可见共享链接invite_url", role: types.TenantRoleOwner, sysadmin: false, wantVisible: true},
		// 回归：系统管理员可见（纯系统管理员空间角色仅为 viewer，与 F36 用例同构）。
		{name: "系统管理员可见共享链接invite_url", role: types.TenantRoleViewer, sysadmin: true, wantVisible: true},
		// 回归：viewer 仍脱敏。
		{name: "viewer不可见共享链接invite_url", role: types.TenantRoleViewer, sysadmin: false, wantVisible: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newShareLinkTestEnv(t)
			seedT06S1Rows(t, env)

			var shareRow, perUserRow *types.TenantInvitationResponse
			rows := listT06S1Rows(t, env, tc.role, tc.sysadmin)
			for i := range rows {
				if rows[i].IsShareLink {
					shareRow = &rows[i]
				} else if rows[i].InviteeUserID != "" {
					perUserRow = &rows[i]
				}
			}
			if shareRow == nil {
				t.Fatalf("列表缺少共享链接行: %+v", rows)
			}
			if perUserRow == nil {
				t.Fatalf("列表缺少普通按用户邀请行: %+v", rows)
			}

			if tc.wantVisible && shareRow.InviteURL == "" {
				t.Errorf("共享链接行应带 invite_url（口径 A）, 实际为空: %+v", *shareRow)
			}
			if !tc.wantVisible && shareRow.InviteURL != "" {
				t.Errorf("该角色不应看到共享链接 invite_url, 实际=%q", shareRow.InviteURL)
			}
			if perUserRow.InviteURL != "" {
				t.Errorf("普通按用户邀请行任何角色都不应带 invite_url, 实际=%q", perUserRow.InviteURL)
			}
		})
	}
}
