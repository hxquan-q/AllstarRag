package middleware

import (
	"fmt"
	"testing"
	"time"
)

// auth_public_ratelimit_t12_eviction_test.go 是 T12 对 R1-2 的独立复验:
// 桶数超上限时必须一次性淘汰到 90% 低水位(而不是每个新 key 只删一个),
// 淘汰顺序是最久未用优先,新 key 仍能放行且预算完整。
// 用显式 lastSeen 构造桶,避免依赖 time.Now() 的时钟精度导致淘汰顺序抖动。

func t12StoreBucket(l *ipRateLimiter, key string, lastSeen time.Time) {
	l.buckets.Store(key, &ipBucket{lastSeen: lastSeen})
	l.bucketCount.Add(1)
}

func TestT12_R1_2_SingleEvictionToLowWatermarkLRU(t *testing.T) {
	const limit = 100
	const low = 90
	limiter := &ipRateLimiter{window: time.Hour, max: 2, maxBuckets: limit}

	now := time.Now()
	// i=0 最旧(now-100s),i=99 最新(now-1s);时间严格递增,无并列。
	for i := 0; i < limit; i++ {
		key := fmt.Sprintf("t12-ip-%03d", i)
		t12StoreBucket(limiter, key, now.Add(-time.Duration(limit-i)*time.Second))
	}
	if got := limiter.bucketCount.Load(); got != limit {
		t.Fatalf("warmup bucketCount = %d, want %d", got, limit)
	}

	// 第 101 个 key:触发一次淘汰,直接降到低水位 90(删 11 个)。
	if !limiter.allow("t12-new") {
		t.Fatal("new key at limit+1: want allowed")
	}
	if got := limiter.bucketCount.Load(); got != low {
		t.Fatalf("bucketCount after one eviction = %d, want single drop to low watermark %d", got, low)
	}

	// 最久未用优先:最早 lastSeen 的 000..010 共 11 个被淘汰,011 保留。
	for i := 0; i <= 10; i++ {
		key := fmt.Sprintf("t12-ip-%03d", i)
		if _, ok := limiter.buckets.Load(key); ok {
			t.Fatalf("bucket %s should be evicted (11 oldest)", key)
		}
	}
	if _, ok := limiter.buckets.Load("t12-ip-011"); !ok {
		t.Fatal("bucket t12-ip-011 is the 12th oldest and must survive")
	}
	if _, ok := limiter.buckets.Load("t12-ip-099"); !ok {
		t.Fatal("newest warmup bucket t12-ip-099 must survive")
	}
	if _, ok := limiter.buckets.Load("t12-new"); !ok {
		t.Fatal("newly inserted bucket must exist after eviction")
	}

	// 新 key 预算完整:max=2,首次已计入;第二次放行,第三次拒绝。
	if !limiter.allow("t12-new") {
		t.Fatal("new key 2nd request: want allowed")
	}
	if limiter.allow("t12-new") {
		t.Fatal("new key 3rd request: want denied (max=2)")
	}

	// 低水位之后有 10 个 key 的余量:连续插入 10 个不应触发任何淘汰。
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("t12-fill-%02d", i)
		if !limiter.allow(key) {
			t.Fatalf("fill key %s: want allowed", key)
		}
	}
	if got := limiter.bucketCount.Load(); got != limit {
		t.Fatalf("after 10 fills bucketCount = %d, want %d (no eviction before limit)", got, limit)
	}
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("t12-fill-%02d", i)
		if _, ok := limiter.buckets.Load(key); !ok {
			t.Fatalf("fill bucket %s must not be evicted while under limit", key)
		}
	}

	// 再次达到上限+1:仍然一次降到低水位(而不是只删一个)。
	if !limiter.allow("t12-over") {
		t.Fatal("key at second limit+1: want allowed")
	}
	if got := limiter.bucketCount.Load(); got != low {
		t.Fatalf("bucketCount after second eviction = %d, want %d", got, low)
	}
}

func TestT12_R1_2_LowWatermarkBoundaries(t *testing.T) {
	tests := []struct {
		limit int
		want  int64
	}{
		{publicAuthRateLimitMaxBuckets, 18000}, // 生产默认 20000 -> 90%
		{1000, 900},
		{10, 9},
		{5, 5}, // 小上限整数除法为 0,保持旧的「淘汰到上限」行为
		{1, 1},
	}
	for _, tt := range tests {
		limiter := &ipRateLimiter{maxBuckets: tt.limit}
		if got := limiter.bucketLowWatermark(); got != tt.want {
			t.Fatalf("limit %d: low watermark = %d, want %d", tt.limit, got, tt.want)
		}
	}
}
