package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// T26 红阶段：/tenants/:id/audit-log?q= 必须把 TrimSpace 后的关键词放进
// AuditLogQuery.Search，并保持 after_id / action / outcome / actor 原样。
func TestAuditLogHandlerForwardsSearchQuery(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{name: "trimmed", query: "q=%20alice%20", want: "alice"},
		{name: "keeps-case", query: "q=Alice", want: "Alice"},
		{name: "empty", query: "q=%20%20", want: ""},
		{name: "missing", query: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *interfaces.AuditLogQuery
			svc := &stubAuditService{
				list: func(_ context.Context, _ uint64, q *interfaces.AuditLogQuery) ([]*types.AuditLog, error) {
					got = q
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
			if got.Search != tc.want {
				t.Fatalf("Search = %q, want %q", got.Search, tc.want)
			}
		})
	}
}

// q 与既有过滤参数同时存在时不得互相覆盖。
func TestAuditLogHandlerSearchComposesWithExistingFilters(t *testing.T) {
	var got *interfaces.AuditLogQuery
	svc := &stubAuditService{
		list: func(_ context.Context, _ uint64, q *interfaces.AuditLogQuery) ([]*types.AuditLog, error) {
			got = q
			return nil, nil
		},
	}
	url := "/tenants/7/audit-log?q=alice&after_id=100&limit=25&action=rbac.member_banned&outcome=success&actor=u-1"
	w := httptest.NewRecorder()
	newAuditHandlerTestRouter(svc).ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got == nil {
		t.Fatalf("service was not called")
	}
	if got.Search != "alice" || got.AfterID != 100 || got.Limit != 25 ||
		got.Action != types.AuditActionMemberBanned || got.Outcome != types.AuditOutcomeSuccess ||
		got.ActorUserID != "u-1" || !got.UnscopedOnly {
		t.Fatalf("filters not composed: %+v", got)
	}
}
