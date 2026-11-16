package service

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// R6b F46 TDD 红阶段：automation 运行与会话委托这两条路径不得再用
// db.Session(&gorm.Session{SkipHooks: true}).Clauses(OnConflict DoNothing)
// .Create(session) 直接建会话——那样会绕过 SessionRepository 的
// lockLiveWebUser，注销与 automation/委托运行交错时可能留下孤儿会话与
// 消息（review F46）。两条路径必须改调 SessionRepository 新增的
// CreateIdempotent（事务内锁 user 行 + OnConflict DoNothing）。
// 当前两处仍是直建，预期红。
//
// 这里用源码断言锚定两条调用路径，属于派单允许的「源码断言」方式；
// CreateIdempotent 的数据库语义由 repository 包用例覆盖。
func TestR6BSessionCreationPathsUseIdempotentCreate(t *testing.T) {
	for _, file := range []string{"automation_runner.go", "agent_collaboration.go"} {
		t.Run(file, func(t *testing.T) {
			src, err := os.ReadFile(file)
			require.NoError(t, err, "读取服务实现文件失败")
			text := string(src)

			if strings.Contains(text, "SkipHooks") {
				t.Errorf("F46: %s 仍含 SkipHooks，不得再直建会话绕过 live-user 锁", file)
			}
			if !strings.Contains(text, "CreateIdempotent") {
				t.Errorf("F46: %s 未调用 CreateIdempotent"+
					"（事务内 lockLiveWebUser + OnConflict DoNothing）", file)
			}
		})
	}
}
