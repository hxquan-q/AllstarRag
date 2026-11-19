package handler

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"gorm.io/gorm"
)

// T04 补环：生产装配（container.go 的 KnowledgeHandler wrapper provider）
// 依赖 WithReparseQuota 把限次闸挂上，闸为 nil 时两入口不限次（T04 前行为）。
// 本用例钉住接线契约：默认构造不带闸，挂闸后链式返回自身且闸非 nil。
func TestKnowledgeHandlerWithReparseQuotaWiresGate(t *testing.T) {
	h := NewKnowledgeHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if h.reparseQuota != nil {
		t.Fatal("默认构造不应带限次闸（既有单测直构路径保持 T04 前行为）")
	}
	if returned := h.WithReparseQuota(service.NewReparseQuotaGate(&gorm.DB{})); returned != h {
		t.Fatal("WithReparseQuota 应链式返回自身（同 WithConfig/WithGrants 先例）")
	}
	if h.reparseQuota == nil {
		t.Fatal("WithReparseQuota 后限次闸应为非 nil，缺此环则生产不限次")
	}
}
