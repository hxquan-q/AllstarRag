package middleware

import (
	"fmt"
	"testing"
	"time"
)

// auth_public_ratelimit_lowwater_test.go 覆盖 T11 的桶淘汰低水位:
// 超限时一次淘汰到上限的 90%,之后若干次新插入不再触发全表扫描;
// 淘汰顺序仍是最久未用优先,新 key 仍有完整预算。

func TestIPRateLimiter_LowWatermarkDefault(t *testing.T) {
	limiter := &ipRateLimiter{maxBuckets: publicAuthRateLimitMaxBuckets}
	if got := limiter.bucketLowWatermark(); got != 18000 {
		t.Fatalf("low watermark = %d, want 18000 (90%% of 20000)", got)
	}
}

func TestIPRateLimiter_EvictsToLowWatermark(t *testing.T) {
	const limit = 100
	const low = 90
	limiter := &ipRateLimiter{window: time.Hour, max: 2, maxBuckets: limit}

	for i := 0; i < limit; i++ {
		ip := fmt.Sprintf("ip-%03d", i)
		if !limiter.allow(ip) {
			t.Fatalf("warmup ip %s: want allowed", ip)
		}
	}
	if got := limiter.bucketCount.Load(); got != limit {
		t.Fatalf("bucketCount = %d, want %d", got, limit)
	}

	// 第 101 个 key 触发淘汰:一次降到 90,而不是只删 1 个。
	if !limiter.allow("ip-100") {
		t.Fatal("new ip at limit+1: want allowed")
	}
	if got := limiter.bucketCount.Load(); got != low {
		t.Fatalf("bucketCount after eviction = %d, want low watermark %d", got, low)
	}

	// 最久未用优先:ip-000..ip-010(11 个)被淘汰,ip-011 仍在。
	for _, evicted := range []string{"ip-000", "ip-010"} {
		if _, ok := limiter.buckets.Load(evicted); ok {
			t.Fatalf("bucket %s should be evicted as oldest", evicted)
		}
	}
	if _, ok := limiter.buckets.Load("ip-011"); !ok {
		t.Fatal("bucket ip-011 should survive the eviction")
	}
	if _, ok := limiter.buckets.Load("ip-100"); !ok {
		t.Fatal("newly inserted bucket ip-100 should exist")
	}

	// 低水位之后有 10 个 key 的余量:继续插入不应再触发淘汰。
	for i := 101; i <= limit+10; i++ {
		ip := fmt.Sprintf("ip-%03d", i)
		if !limiter.allow(ip) {
			t.Fatalf("post-eviction ip %s: want allowed", ip)
		}
	}
	if got := limiter.bucketCount.Load(); got != limit {
		t.Fatalf("bucketCount = %d, want %d (no eviction before reaching limit)", got, limit)
	}

	// 新 key 的预算完整:max=2,第 3 次仍被拒。
	if !limiter.allow("fresh") || !limiter.allow("fresh") {
		t.Fatal("fresh key: first two requests want allowed")
	}
	if limiter.allow("fresh") {
		t.Fatal("fresh key: third request want denied")
	}
}
