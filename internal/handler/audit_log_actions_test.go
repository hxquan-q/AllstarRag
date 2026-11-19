package handler

// CP3 修复轮 1 复验（FS3 后端半）：ListTenantAuditLog 的 actions 多选过滤
// 参数解析——逗号分隔、trim、去重、空项忽略、默认不过滤、50 项上限，
// 并与单值 action 叠加传给仓储（IN 与 = 同时生效）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func captureAuditQuery(t *testing.T, rawQuery string) *interfaces.AuditLogQuery {
	t.Helper()
	var got *interfaces.AuditLogQuery
	svc := &stubAuditService{list: func(
		_ context.Context, _ uint64, q *interfaces.AuditLogQuery,
	) ([]*types.AuditLog, error) {
		got = q
		return nil, nil
	}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tenants/7/audit-log?"+rawQuery, nil)
	newAuditHandlerTestRouter(svc).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got == nil {
		t.Fatalf("service.List 未被调用")
	}
	return got
}

func actionStrings(actions []types.AuditAction) []string {
	out := make([]string, 0, len(actions))
	for _, action := range actions {
		out = append(out, string(action))
	}
	return out
}

func TestCP3R1_AuditLogActionsCSVParsesTrimDedupe(t *testing.T) {
	q := captureAuditQuery(t,
		"actions="+"knowledge.created%2C+knowledge.deleted+%2C%2Ckb.created%2Cknowledge.created%2C%20%20%2Ckb.updated")
	want := []string{"knowledge.created", "knowledge.deleted", "kb.created", "kb.updated"}
	if got := actionStrings(q.Actions); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("actions=%v, want %v（trim/去重/空项忽略）", got, want)
	}
}

func TestCP3R1_AuditLogActionsDefaultsToNoFilter(t *testing.T) {
	if q := captureAuditQuery(t, ""); len(q.Actions) != 0 {
		t.Fatalf("缺省 actions 不得过滤，got %v", actionStrings(q.Actions))
	}
	if q := captureAuditQuery(t, "actions=,,%20,"); len(q.Actions) != 0 {
		t.Fatalf("全空项 actions 不得过滤，got %v", actionStrings(q.Actions))
	}
}

func TestCP3R1_AuditLogActionsCapAtFifty(t *testing.T) {
	parts := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		parts = append(parts, fmt.Sprintf("action.%02d", i))
	}
	q := captureAuditQuery(t, "actions="+strings.Join(parts, ","))
	if len(q.Actions) != 50 {
		t.Fatalf("actions len=%d, want 50（超长 IN 防呆）", len(q.Actions))
	}
	if q.Actions[0] != "action.00" || q.Actions[49] != "action.49" {
		t.Fatalf("截断应保留前 50 项，got first=%q last=%q", q.Actions[0], q.Actions[49])
	}
}

func TestCP3R1_AuditLogActionsStacksWithSingleAction(t *testing.T) {
	q := captureAuditQuery(t, "action=kb.created&actions=knowledge.created,kb.created")
	if q.Action != types.AuditAction("kb.created") {
		t.Fatalf("action=%q, want kb.created", q.Action)
	}
	if got := actionStrings(q.Actions); fmt.Sprint(got) != fmt.Sprint([]string{"knowledge.created", "kb.created"}) {
		t.Fatalf("actions=%v, want 两项叠加", got)
	}
	// 参数确实进入 handler→service 的查询对象（序列化可见）。
	raw, _ := json.Marshal(q.Actions)
	if !strings.Contains(string(raw), "knowledge.created") {
		t.Fatalf("actions 未进入查询对象: %s", raw)
	}
}
