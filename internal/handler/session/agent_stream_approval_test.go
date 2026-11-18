package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestAttachToolApprovalsMatchesTheToolCall(t *testing.T) {
	steps := []types.AgentStep{{
		ToolCalls: []types.ToolCall{{ID: "call-1"}, {ID: "call-2"}},
	}}
	records := []types.ToolApprovalRecord{
		{PendingID: "p2", ToolCallID: "call-2", Resolved: true, Approved: true},
		{PendingID: "p1", ToolCallID: "call-1", Resolved: true, Approved: false},
		{PendingID: "loose", Resolved: true},
	}

	got := attachToolApprovals(steps, records)
	if len(got) != 1 {
		t.Fatalf("steps = %d", len(got))
	}
	approvals := got[0].Approvals
	if len(approvals) != 3 {
		t.Fatalf("approvals = %#v", approvals)
	}
	if approvals[0].PendingID != "p2" || approvals[1].PendingID != "p1" || approvals[2].PendingID != "loose" {
		t.Fatalf("order = %#v", approvals)
	}
}

func TestHostApprovalSnapshotKeepsSessionRules(t *testing.T) {
	got := hostApprovalSnapshot(&event.HostApprovalPayload{
		Reason: "delete", Command: "rm -rf build", AllowSession: true,
		SessionRules: []string{"rm -f -r"},
	})
	if got == nil || len(got.SessionRules) != 1 || got.SessionRules[0] != "rm -f -r" {
		t.Fatalf("snapshot = %#v", got)
	}
}

// CP3 M1：历史回放从记录的 JSON 重建审批卡，显示服务名会随品牌配置变化，
// 所以稳定的注册工具名必须一起落进记录，并在落库/回放的 JSON 里保留。
func TestRememberToolApprovalKeepsRegisteredToolName(t *testing.T) {
	handler := &AgentStreamHandler{}
	handler.rememberToolApproval(event.ToolApprovalRequiredData{
		PendingID:          "pending-1",
		ToolCallID:         "call-1",
		ServiceName:        "小爱知识库",
		RegisteredToolName: "run_operation",
		MCPToolName:        "run_operation",
		Description:        "创建知识库\n新库",
		ArgsJSON:           `{"name":"新库"}`,
		TimeoutSeconds:     60,
		RequestedAtUnix:    1700000000,
	})

	if len(handler.toolApprovals) != 1 {
		t.Fatalf("toolApprovals = %d, want 1", len(handler.toolApprovals))
	}
	record := handler.toolApprovals[0]
	if record.RegisteredToolName != "run_operation" {
		t.Errorf("RegisteredToolName = %q, want run_operation", record.RegisteredToolName)
	}
	if record.ServiceName != "小爱知识库" {
		t.Errorf("ServiceName = %q, want 小爱知识库", record.ServiceName)
	}
	if record.PendingID != "pending-1" || record.ToolCallID != "call-1" {
		t.Errorf("copied ids = %#v", record)
	}

	// The replayed transcript reads the same JSON, so the field must survive
	// attach + marshal.
	steps := attachToolApprovals(
		[]types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "call-1"}}}},
		handler.toolApprovals,
	)
	raw, err := json.Marshal(steps)
	if err != nil {
		t.Fatalf("marshal steps: %v", err)
	}
	if !strings.Contains(string(raw), `"registered_tool_name":"run_operation"`) {
		t.Fatalf("replayed step JSON lost registered_tool_name: %s", raw)
	}
}

// CP3 M1 反例：普通 MCP 审批没有注册工具名，不能被写成 run_operation。
func TestRememberToolApprovalLeavesGenericApprovalUnmarked(t *testing.T) {
	handler := &AgentStreamHandler{}
	handler.rememberToolApproval(event.ToolApprovalRequiredData{
		PendingID:   "pending-2",
		ToolCallID:  "call-2",
		ServiceName: "docs-mcp",
		MCPToolName: "search_docs",
	})

	if len(handler.toolApprovals) != 1 {
		t.Fatalf("toolApprovals = %d, want 1", len(handler.toolApprovals))
	}
	if got := handler.toolApprovals[0].RegisteredToolName; got != "" {
		t.Errorf("RegisteredToolName = %q, want empty for a generic MCP approval", got)
	}
	raw, err := json.Marshal(handler.toolApprovals)
	if err != nil {
		t.Fatalf("marshal records: %v", err)
	}
	if strings.Contains(string(raw), "registered_tool_name") {
		t.Errorf("generic approval gained registered_tool_name: %s", raw)
	}
}
