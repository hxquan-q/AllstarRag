package repository

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

// R6b F45 TDD 红阶段：TransferOwnershipTx 的锁序必须与 PurgeUserTx /
// ResetPasswordTx 一致——先锁 users 行，再锁 tenant_members 行。
// 当前实现先锁成员行、后锁 users 行，与注销/重置密码反向，PG/MySQL 下
// 并发可形成锁环（review F45）。本文件只写测试，不改实现。

// r6bSQLRecorder 记录 GORM 实际执行的 SQL 顺序（与 T41 S11 同一思路）。
type r6bSQLRecorder struct {
	logger.Interface

	mu   sync.Mutex
	sqls []string
}

func (r *r6bSQLRecorder) Trace(
	_ context.Context, _ time.Time, fc func() (string, int64), _ error,
) {
	sql, _ := fc()
	r.mu.Lock()
	r.sqls = append(r.sqls, sql)
	r.mu.Unlock()
}

func (r *r6bSQLRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sqls...)
}

func r6bFirstSQL(sqls []string, re *regexp.Regexp) int {
	for i, sql := range sqls {
		if re.MatchString(sql) {
			return i
		}
	}
	return -1
}

// TestR6BTransferOwnershipLocksUsersBeforeMembers 覆盖 F45：
// TransferOwnershipTx 必须在锁 tenant_members 之前先锁目标 users 行，
// 与 PurgeUserTx（users→members）和 ResetPasswordTx（users→members）
// 保持同一全局锁序；否则并发注销/重置密码与交接会形成锁环。
// 当前实现先锁成员行、后锁 users 行，预期红。
func TestR6BTransferOwnershipLocksUsersBeforeMembers(t *testing.T) {
	db, transfer := t40NewRepoDB(t)
	ctx := context.Background()
	const tenantID uint64 = 46

	t40SeedTenant(t, db, tenantID)
	from := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	to := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	t40SeedUser(t, db, from, true)
	t40SeedUser(t, db, to, true)
	t40SeedMember(t, db, from, tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive)
	t40SeedMember(t, db, to, tenantID, types.TenantRoleAdmin, types.TenantMemberStatusActive)

	recorder := &r6bSQLRecorder{Interface: logger.Default}
	db.Config.Logger = recorder

	require.NoError(t, transfer.TransferOwnershipTx(ctx, tenantID, from, to))

	sqls := recorder.snapshot()
	// SQLite 用反引号引用标识符（`users`），PostgreSQL 用双引号或不引用；
	// 三种形式都要能识别。
	usersStmt := r6bFirstSQL(sqls,
		regexp.MustCompile(`(?i)(from|update)\s+[\x60"]?users[\x60"]?\b`))
	membersStmt := r6bFirstSQL(sqls,
		regexp.MustCompile(`(?i)(from|update)\s+[\x60"]?tenant_members[\x60"]?\b`))

	require.NotEqual(t, -1, usersStmt,
		"F45: TransferOwnershipTx 必须读写目标 users 行；实际 SQL：%v", sqls)
	require.NotEqual(t, -1, membersStmt,
		"F45: TransferOwnershipTx 必须锁定 tenant_members 行；实际 SQL：%v", sqls)
	require.Less(t, usersStmt, membersStmt,
		"F45: 必须先锁 users 行再锁 tenant_members 行（与 PurgeUserTx/ResetPasswordTx 同序），"+
			"否则并发注销/重置密码会与交接形成锁环；实际 SQL：%v", sqls)

	// 保护网：锁序调整后交接仍必须原子生效。
	require.Equal(t, types.TenantRoleAdmin, t40LoadRole(t, db, from, tenantID),
		"F45: 交接后 from 仍应降为 admin")
	require.Equal(t, types.TenantRoleOwner, t40LoadRole(t, db, to, tenantID),
		"F45: 交接后 to 仍应升为 owner")
}
