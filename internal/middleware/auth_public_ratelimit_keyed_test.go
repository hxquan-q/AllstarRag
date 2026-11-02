package middleware

import (
	"testing"
	"time"
)

// auth_public_ratelimit_keyed_test.go 覆盖 KeyedRateLimiter 的滑窗语义:
// 边界(第 max 次通过、第 max+1 次拒绝)、窗口滑过恢复、不同 key 独立桶、
// 空 key 共享桶。直接调用 Allow,不依赖 gin 上下文,让窗口可以取毫秒级。

func TestKeyedRateLimiter_Boundary(t *testing.T) {
	limiter := NewKeyedRateLimiter(time.Hour, 5)
	const key = "alice@example.com|203.0.113.5"

	for i := 1; i <= 5; i++ {
		if !limiter.Allow(key) {
			t.Fatalf("request %d: want allowed", i)
		}
	}
	if limiter.Allow(key) {
		t.Fatal("6th request within window: want denied")
	}
}

func TestKeyedRateLimiter_WindowRecovery(t *testing.T) {
	// 窗口取 50ms,睡 120ms 足够让旧时间戳全部滑出窗口,同时留足余量
	// 避免 CI 抖动。
	limiter := NewKeyedRateLimiter(50*time.Millisecond, 2)
	const key = "bob@example.com|203.0.113.6"

	for i := 1; i <= 2; i++ {
		if !limiter.Allow(key) {
			t.Fatalf("request %d: want allowed", i)
		}
	}
	if limiter.Allow(key) {
		t.Fatal("3rd request within window: want denied")
	}

	time.Sleep(120 * time.Millisecond)

	if !limiter.Allow(key) {
		t.Fatal("request after window slipped: want allowed")
	}
}

func TestKeyedRateLimiter_KeysIndependent(t *testing.T) {
	limiter := NewKeyedRateLimiter(time.Hour, 2)
	const keyA = "alice@example.com|203.0.113.5"
	const keyB = "bob@example.com|203.0.113.5"

	for i := 1; i <= 2; i++ {
		if !limiter.Allow(keyA) {
			t.Fatalf("keyA request %d: want allowed", i)
		}
		if !limiter.Allow(keyB) {
			t.Fatalf("keyB request %d: want allowed", i)
		}
	}

	if limiter.Allow(keyA) {
		t.Fatal("keyA 3rd request: want denied")
	}
	if limiter.Allow(keyB) {
		t.Fatal("keyB 3rd request: want denied")
	}
}

func TestKeyedRateLimiter_EmptyKeySharesBucket(t *testing.T) {
	// 调用方漏传 key 时落到同一个共享桶,宁可略严也不静默放行。
	limiter := NewKeyedRateLimiter(time.Hour, 2)

	if !limiter.Allow("") {
		t.Fatal("1st empty-key request: want allowed")
	}
	if !limiter.Allow("") {
		t.Fatal("2nd empty-key request: want allowed")
	}
	if limiter.Allow("") {
		t.Fatal("3rd empty-key request: want denied")
	}
}
