package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// T02 票级验证：include_scoped 参数到 AuditLogQuery 的翻译口径。
// 旧口径 = UnscopedOnly=true（仅成员治理行，即 scope_type 为空串的行）；
// include_scoped=true 把它放开为 false，知识库行为行随成员治理行一并返回。
// handler 层只验翻译与透传（stub 服务）；KB 行真正进入返回由 repository 层
// 用例实证（audit_log_scope_test.go）。
func TestAuditLogHandlerIncludeScopedControlsUnscopedOnly(t *testing.T) {
	cases := []struct {
		name         string
		query        string
		wantUnscoped bool
	}{
		{name: "缺省按旧口径", query: "", wantUnscoped: true},
		{name: "显式false", query: "include_scoped=false", wantUnscoped: true},
		{name: "true放开scope过滤", query: "include_scoped=true", wantUnscoped: false},
		{name: "1等价true", query: "include_scoped=1", wantUnscoped: false},
		{name: "TRUE等价true", query: "include_scoped=TRUE", wantUnscoped: false},
		{name: "垃圾值abc按false", query: "include_scoped=abc", wantUnscoped: true},
		{name: "空值按false", query: "include_scoped=", wantUnscoped: true},
		{name: "yes不识别按false", query: "include_scoped=yes", wantUnscoped: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *interfaces.AuditLogQuery
			var gotTenant uint64
			svc := &stubAuditService{
				list: func(_ context.Context, tenantID uint64, q *interfaces.AuditLogQuery) ([]*types.AuditLog, error) {
					got = q
					gotTenant = tenantID
					return nil, nil
				},
			}
			url := "/tenants/7/audit-log"
			if tc.query != "" {
				url += "?" + tc.query
			}
			w := httptest.NewRecorder()
			newAuditHandlerTestRouter(svc).ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
			if got == nil {
				t.Fatalf("service was not called")
			}
			// 隔离半：租户恒取自 URL 路径（跨空间拦截在路由层 PathTenantMatch，
			// 由 audit_log_route_guard_test.go 既有用例覆盖）。
			if gotTenant != 7 {
				t.Fatalf("tenant must come from URL path, got %d", gotTenant)
			}
			if got.UnscopedOnly != tc.wantUnscoped {
				t.Fatalf("UnscopedOnly = %v, want %v (query=%q)", got.UnscopedOnly, tc.wantUnscoped, tc.query)
			}
			// 放开语义是"不限 scope"，不是"只看 KB"：本 handler 任何分支都不得设置 scope 过滤。
			if got.ScopeType != "" || got.ScopeID != "" {
				t.Fatalf("include_scoped must not set scope filters, got type=%q id=%q", got.ScopeType, got.ScopeID)
			}
		})
	}
}

// 平台审计接口（/system/admin/audit-log）不接受 include_scoped：
// 无论传什么值，查询口径不变（tenant_id=0、不设 scope 维度）。
func TestSystemAuditLogHandlerIgnoresIncludeScoped(t *testing.T) {
	for _, raw := range []string{"include_scoped=true", "include_scoped=abc", "include_scoped="} {
		t.Run(raw, func(t *testing.T) {
			var got *interfaces.AuditLogQuery
			var gotTenant uint64
			svc := &stubAuditService{
				list: func(_ context.Context, tenantID uint64, q *interfaces.AuditLogQuery) ([]*types.AuditLog, error) {
					got = q
					gotTenant = tenantID
					return nil, nil
				},
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/system/admin/audit-log?"+raw, nil)
			newSystemAuditHandlerTestRouter(svc).ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
			if got == nil {
				t.Fatalf("service was not called")
			}
			if gotTenant != 0 {
				t.Fatalf("platform feed must stay tenant_id=0, got %d", gotTenant)
			}
			if got.UnscopedOnly {
				t.Fatalf("platform feed never sets UnscopedOnly, got true (query=%q)", raw)
			}
			if got.ScopeType != "" || got.ScopeID != "" {
				t.Fatalf("platform feed never sets scope filters, got type=%q id=%q", got.ScopeType, got.ScopeID)
			}
		})
	}
}
