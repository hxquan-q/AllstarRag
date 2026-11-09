package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T34 owner 空间开通幂等用例（锁友好版）。
//
// 旧版用 barrier 要求 N 个请求都到达 CreateTenant 才放行，这与“目标 users
// 行正确加锁”互斥：加锁后只有 1 个请求能到 CreateTenant，其余在锁上等待，
// barrier 会超时。本版改为：
//   - 不阻塞、只计数 CreateTenant；
//   - 在 CreateTenant 内 sleep 一小段时间放大 check-then-create 窗口；
//   - 断言最终所有请求返回同一 tenant、库里只有 1 个 active owner 空间、
//     CreateTenant 调用次数 == 1。
//
// 当前“创建后收敛”实现会在 sleep 窗口内被其他请求再次进入 CreateTenant，
// 因而 calls > 1，预期红；正确加锁后只有 1 个请求进入，calls == 1。

type t34CountingTenantService struct {
	interfaces.TenantService
	calls int32
	delay time.Duration
}

func (s *t34CountingTenantService) CreateTenant(
	ctx context.Context, tenant *types.Tenant,
) (*types.Tenant, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.TenantService.CreateTenant(ctx, tenant)
}

func (s *t34CountingTenantService) Calls() int32 {
	return atomic.LoadInt32(&s.calls)
}

func t34InstallCountingTenant(t *testing.T, env *omOwnerEnv, delay time.Duration) *t34CountingTenantService {
	t.Helper()
	wrapper := &t34CountingTenantService{TenantService: env.tenantSvc, delay: delay}
	setter, ok := env.svc.(omOwnerDepsSetter)
	require.True(t, ok, "service must expose SetOwnerManagementDeps")
	setter.SetOwnerManagementDeps(wrapper, env.memberSvc, env.inviteSvc, env.userSvc)
	return wrapper
}

func t34RunConcurrentProvision(
	t *testing.T, env *omOwnerEnv, n int,
) []*types.Tenant {
	t.Helper()
	base := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	ctx, cancel := context.WithTimeout(base, 10*time.Second)
	defer cancel()

	type result struct {
		tenant *types.Tenant
		err    error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i].tenant, results[i].err = env.svc.ProvisionOwnerSpace(ctx, "target", "并发空间")
		}(i)
	}
	close(start)
	wg.Wait()

	tenants := make([]*types.Tenant, 0, n)
	for i, r := range results {
		require.NoErrorf(t, r.err, "request %d failed", i)
		require.NotNilf(t, r.tenant, "request %d returned nil tenant", i)
		tenants = append(tenants, r.tenant)
	}
	return tenants
}

func t34AssertSingleSpace(
	t *testing.T, env *omOwnerEnv, wrapper *t34CountingTenantService, tenants []*types.Tenant,
) {
	t.Helper()
	for i := 1; i < len(tenants); i++ {
		require.Equal(t, tenants[0].ID, tenants[i].ID,
			"all concurrent provisioning calls must converge on one tenant")
	}
	require.EqualValues(t, 1, wrapper.Calls(),
		"CreateTenant must be called exactly once; check-then-create race would call it multiple times")
	require.EqualValues(t, 1, omOwnerCountTenants(t, env),
		"concurrent provisioning must not leave a second tenant")
	owners, err := env.memberRepo.CountActiveOwners(context.Background(), tenants[0].ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, owners)

	audits := omOwnerAuditsByAction(t, env, types.AuditAction("system.owner_provisioned"))
	require.NotEmpty(t, audits)
	for _, a := range audits {
		require.Equal(t, tenants[0].ID, a.TenantID,
			"all provisioning audits must point at the same tenant")
	}
}

// 并发 2 次开通：必须返回同一空间，CreateTenant 只调用一次。
func TestT34ConcurrentProvisionCreatesSingleSpace(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "target", nil)
	wrapper := t34InstallCountingTenant(t, env, 120*time.Millisecond)

	tenants := t34RunConcurrentProvision(t, env, 2)
	t34AssertSingleSpace(t, env, wrapper, tenants)
}

// 并发 4 次开通：同样只能有 1 个空间、1 次 CreateTenant。
func TestT34Concurrent4ProvisionCreatesSingleSpace(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "target", nil)
	wrapper := t34InstallCountingTenant(t, env, 120*time.Millisecond)

	tenants := t34RunConcurrentProvision(t, env, 4)
	t34AssertSingleSpace(t, env, wrapper, tenants)
}

// 串行重复调用：同一空间，审计都指向同一 tenant（幂等口径）。
func TestT34SerialProvisionIdempotent(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "target", nil)

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	first, err := env.svc.ProvisionOwnerSpace(ctx, "target", "空间")
	require.NoError(t, err)
	second, err := env.svc.ProvisionOwnerSpace(ctx, "target", "空间")
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.EqualValues(t, 1, omOwnerCountTenants(t, env))

	audits := omOwnerAuditsByAction(t, env, types.AuditAction("system.owner_provisioned"))
	require.NotEmpty(t, audits)
	for _, a := range audits {
		require.Equal(t, first.ID, a.TenantID, "all provisioning audits must point at the same tenant")
	}
}

// 已有 owner 空间时复用，不新建。
func TestT34ExistingOwnerSpaceReused(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "target", nil)
	existing := omOwnerSeedTenant(t, env, "已有空间")
	omOwnerSeedMembership(t, env, "target", existing.ID, types.TenantRoleOwner)

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	first, err := env.svc.ProvisionOwnerSpace(ctx, "target", "不应新建")
	require.NoError(t, err)
	second, err := env.svc.ProvisionOwnerSpace(ctx, "target", "不应新建")
	require.NoError(t, err)
	require.Equal(t, existing.ID, first.ID)
	require.Equal(t, existing.ID, second.ID)
	require.EqualValues(t, 1, omOwnerCountTenants(t, env))

	members, err := env.memberRepo.ListByTenant(context.Background(), existing.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)
}
