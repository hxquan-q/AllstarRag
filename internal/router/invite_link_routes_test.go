package router

// 整体回归（AC-12 / AC-18）：共享链接的创建/撤销路由接线。
//
// 路由权限由 g.AdminOrSystemAdmin() 守卫（T06 起普通管理员可邀请/管理链接）；
// 「创建后不可修改」体现为没有 PUT/PATCH 路由。
// 这里直接读路由表源码做接线断言——handler 单测不覆盖 RBAC 守卫，而启动完整
// 路由需要大量依赖；同类源码级接线断言在仓库已有先例。

import (
	"os"
	"strings"
	"testing"
)

func TestInviteLinkRoutesAreAdminManagedAndImmutable(t *testing.T) {
	source, err := os.ReadFile("routes_auth_tenant.go")
	if err != nil {
		t.Fatalf("read routes_auth_tenant.go: %v", err)
	}
	text := string(source)

	createLine := `g.apiKeyRoute(tenantByID, http.MethodPost, "/invite-links",`
	if !strings.Contains(text, createLine) {
		t.Fatal("缺少 POST /tenants/:id/invite-links 路由")
	}
	if !strings.Contains(text, "invitationHandler.CreateInviteLink)") {
		t.Fatal("POST /invite-links 未接到 CreateInviteLink")
	}
	// 创建路由必须由普通管理员或系统管理员守卫（AC-5 / F23：权限以后端为准）。
	createStart := strings.Index(text, createLine)
	createEnd := strings.Index(text[createStart:], "\n")
	if createEnd < 0 {
		t.Fatal("无法定位创建路由行尾")
	}
	if !strings.Contains(text[createStart:createStart+createEnd], "g.AdminOrSystemAdmin()") {
		t.Fatal("POST /invite-links 必须由 g.AdminOrSystemAdmin() 守卫")
	}

	if count := strings.Count(text, `"/invite-links"`); count != 1 {
		t.Fatalf(`"/invite-links" 出现 %d 次, want 1（不得新增修改路由）`, count)
	}
	for _, method := range []string{"http.MethodPut", "http.MethodPatch"} {
		if strings.Contains(text, method+`, "/invite-links"`) {
			t.Fatalf("共享链接不应提供 %s 修改路由（创建后不可改）", method)
		}
	}

	revokeLine := `http.MethodDelete, "/invitations/:inv_id"`
	if !strings.Contains(text, revokeLine) || !strings.Contains(text, "invitationHandler.RevokeInvitation)") {
		t.Fatal("既有撤销路由 DELETE /tenants/:id/invitations/:inv_id 必须保留")
	}
	revokeStart := strings.Index(text, revokeLine)
	if !strings.Contains(text[revokeStart:revokeStart+220], "g.AdminOrSystemAdmin()") {
		t.Fatal("撤销路由必须由 g.AdminOrSystemAdmin() 守卫（F23 / T06）")
	}
}
