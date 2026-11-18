package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// CP3 M1：历史回放靠记录里的稳定注册工具名判别操作卡，显示名会随品牌配置
// 变化。这里锁 JSON 字段名与往返行为，防止字段被删或改名。
func TestToolApprovalRecordKeepsRegisteredToolNameInJSON(t *testing.T) {
	record := ToolApprovalRecord{
		PendingID:          "pending-1",
		ToolCallID:         "call-1",
		ServiceName:        "小爱知识库",
		RegisteredToolName: "run_operation",
		MCPToolName:        "run_operation",
		Description:        "创建知识库\n新库",
	}

	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}

	var fields map[string]interface{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal record map: %v", err)
	}
	if got := fields["registered_tool_name"]; got != "run_operation" {
		t.Fatalf("registered_tool_name = %#v, want run_operation (json: %s)", got, raw)
	}

	var back ToolApprovalRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("round trip record: %v", err)
	}
	if back.RegisteredToolName != "run_operation" {
		t.Errorf("round-trip RegisteredToolName = %q, want run_operation", back.RegisteredToolName)
	}
	if back.ServiceName != "小爱知识库" {
		t.Errorf("round-trip ServiceName = %q, want 小爱知识库", back.ServiceName)
	}
}

// CP3 M1：改名前的历史记录没有 registered_tool_name，读出来不能报错，重新
// 序列化也不应凭空注入该字段（omitempty）。
func TestToolApprovalRecordReadsLegacyJSONWithoutRegisteredToolName(t *testing.T) {
	legacy := `{"pending_id":"pending-old","service_name":"WeKnora","resolved":true,"approved":true}`

	var record ToolApprovalRecord
	if err := json.Unmarshal([]byte(legacy), &record); err != nil {
		t.Fatalf("unmarshal legacy record: %v", err)
	}
	if record.RegisteredToolName != "" {
		t.Errorf("RegisteredToolName = %q, want empty for legacy record", record.RegisteredToolName)
	}
	if record.ServiceName != "WeKnora" {
		t.Errorf("ServiceName = %q, want WeKnora", record.ServiceName)
	}

	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("re-marshal legacy record: %v", err)
	}
	if strings.Contains(string(raw), "registered_tool_name") {
		t.Errorf("legacy record gained registered_tool_name on re-marshal: %s", raw)
	}
}
