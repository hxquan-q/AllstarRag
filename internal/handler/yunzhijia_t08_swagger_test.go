package handler

// T08 测试：Swagger 产物包含 4 个管理端操作（AC-16、C9）。
//
// 直接读仓库里的 docs/swagger.json，断言路径与方法齐全；不重新生成，
// 避免测试依赖 make docs 工具链。

import (
	"encoding/json"
	"os"
	"testing"
)

func TestYunzhijiaT08SwaggerHasAdminEndpoints(t *testing.T) {
	raw, err := os.ReadFile("../../docs/swagger.json")
	if err != nil {
		t.Fatalf("读取 docs/swagger.json 失败: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 swagger.json 失败: %v", err)
	}

	want := []struct{ path, method string }{
		{"/system/admin/yunzhijia/sync", "get"},
		{"/system/admin/yunzhijia/sync", "post"},
		{"/system/admin/yunzhijia/sync/preview", "post"},
		{"/system/admin/yunzhijia/employees/{open_id}/unblock", "post"},
	}
	for _, item := range want {
		ops, ok := doc.Paths[item.path]
		if !ok {
			t.Fatalf("swagger 缺少路径 %s", item.path)
		}
		if _, ok := ops[item.method]; !ok {
			t.Fatalf("swagger 路径 %s 缺少方法 %s", item.path, item.method)
		}
	}
}
