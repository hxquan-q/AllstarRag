package middleware

import (
	"fmt"
	"testing"
	"time"
)

// auth_public_ratelimit_buckets_test.go 覆盖 T05 新增的桶数上限:
// 达到上限后不再无限增长、超限时优先清理过期桶、仍有存活桶时淘汰
// 最久未用(而不是最新的)桶,以及被淘汰/新建的 IP 仍按正常预算放行。
// 这些用例直接构造 ipRateLimiter(与既有测试同包),不启动清理协程。

func TestIPRateLimiter_BucketLimitStopsGrowth(t *testing.T) {
	const limit = 10
	limiter := &ipRateLimiter{window: time.Hour, max: 2, maxBuckets: limit}

	// 连续 100 个互不相同的 IP,桶数必须始终受上限约束。
	for i := 0; i < 100; i++ {
		ip := fmt.Sprintf("203.0.113.%d", i)
		if !limiter.allow(ip) {
			t.Fatalf("fresh IP %s (request %d): want allowed", ip, i+1)
		}
		if got := limiter.bucketCount.Load(); got > limit {
			t.Fatalf("after %d IPs: bucketCount = %d, want <= %d", i+1, got, limit)
		}
	}
	if got := limiter.bucketCount.Load(); got != limit {
		t.Fatalf("bucketCount = %d, want %d", got, limit)
	}
}

func TestIPRateLimiter_EvictsOldestBucket(t *testing.T) {
	limiter := &ipRateLimiter{window: time.Hour, max: 5, maxBuckets: 3}

	for _, ip := range []string{"a", "b", "c"} {
		if !limiter.allow(ip) {
			t.Fatalf("ip %s: want allowed", ip)
		}
		// 拉开 lastSeen,让淘汰顺序确定。
		time.Sleep(5 * time.Millisecond)
	}
	// 再次命中 a,lastSeen 刷新为最新;此后最久未用的是 b。
	if !limiter.allow("a") {
		t.Fatal("ip a refresh: want allowed")
	}
	time.Sleep(5 * time.Millisecond)

	if !limiter.allow("d") {
		t.Fatal("new ip d after eviction: want allowed")
	}

	if _, ok := limiter.buckets.Load("b"); ok {
		t.Fatal("bucket b should be evicted as the oldest")
	}
	for _, ip := range []string{"a", "c", "d"} {
		if _, ok := limiter.buckets.Load(ip); !ok {
			t.Fatalf("bucket %s should still exist", ip)
		}
	}
	if got := limiter.bucketCount.Load(); got != 3 {
		t.Fatalf("bucketCount = %d, want 3", got)
	}
}

func TestIPRateLimiter_EvictsExpiredBeforeOldest(t *testing.T) {
	limiter := &ipRateLimiter{window: 50 * time.Millisecond, max: 5, maxBuckets: 3}
	for _, ip := range []string{"x", "y", "z"} {
		if !limiter.allow(ip) {
			t.Fatalf("ip %s: want allowed", ip)
		}
	}
	// 等 x/y/z 都滑出窗口后刷新 z,于是 z 是唯一活跃的旧桶,x/y 已过期。
	time.Sleep(120 * time.Millisecond)
	if !limiter.allow("z") {
		t.Fatal("ip z refresh: want allowed")
	}
	// 插入新桶触发超限:先清理过期桶,活跃的 z 与新插入的 a 都应保留。
	if !limiter.allow("a") {
		t.Fatal("fresh ip a: want allowed")
	}
	if got := limiter.bucketCount.Load(); got != 3 {
		t.Fatalf("bucketCount = %d, want 3 (limit)", got)
	}
	for _, ip := range []string{"z", "a"} {
		if _, ok := limiter.buckets.Load(ip); !ok {
			t.Fatalf("bucket %s should still exist", ip)
		}
	}
	removed := 0
	for _, ip := range []string{"x", "y"} {
		if _, ok := limiter.buckets.Load(ip); !ok {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("expired buckets removed = %d, want exactly 1 (up to the limit)", removed)
	}
}

// TestIPRateLimiter_EvictedIPStillHasBudget 验证淘汰不会破坏新 IP 的
// 正常预算:被新插入的 IP 能放行 max 次,第 max+1 次仍然被拒。
func TestIPRateLimiter_EvictedIPStillHasBudget(t *testing.T) {
	limiter := &ipRateLimiter{window: time.Hour, max: 2, maxBuckets: 2}

	for i := 0; i < 2; i++ {
		if !limiter.allow("old-1") || !limiter.allow("old-2") {
			t.Fatalf("warmup %d: want allowed", i+1)
		}
	}
	// old-1/old-2 都已满预算,新 IP 触发淘汰;新桶本身预算完整。
	if !limiter.allow("new-ip") {
		t.Fatal("new ip after eviction: 1st request want allowed")
	}
	if !limiter.allow("new-ip") {
		t.Fatal("new ip after eviction: 2nd request want allowed")
	}
	if limiter.allow("new-ip") {
		t.Fatal("new ip after eviction: 3rd request want denied")
	}
}
