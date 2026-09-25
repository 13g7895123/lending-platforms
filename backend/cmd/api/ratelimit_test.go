package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToCapacity(t *testing.T) {
	// 容量 3、每分鐘補 3 個：前 3 次應放行，第 4 次擋下
	limiter := newRateLimiter(3, 3, time.Minute)

	for attempt := 1; attempt <= 3; attempt++ {
		allowed, _ := limiter.allow("1.2.3.4")
		if !allowed {
			t.Fatalf("attempt %d was blocked but should be within capacity", attempt)
		}
	}
	allowed, retryAfter := limiter.allow("1.2.3.4")
	if allowed {
		t.Error("4th attempt was allowed but capacity is 3")
	}
	if retryAfter < 1 {
		t.Errorf("retryAfter = %d, want at least 1 second", retryAfter)
	}
}

// 限流必須依 key 分開計數，一個 IP 被擋不能影響其他 IP。
func TestRateLimiterIsolatesKeys(t *testing.T) {
	limiter := newRateLimiter(1, 1, time.Minute)

	if allowed, _ := limiter.allow("1.1.1.1"); !allowed {
		t.Fatal("first request for 1.1.1.1 blocked")
	}
	if allowed, _ := limiter.allow("1.1.1.1"); allowed {
		t.Error("second request for 1.1.1.1 allowed, want blocked")
	}
	if allowed, _ := limiter.allow("2.2.2.2"); !allowed {
		t.Error("request for a different IP was blocked; keys are not isolated")
	}
}

// token 應隨時間補回。
func TestRateLimiterRefillsOverTime(t *testing.T) {
	// 每秒補 10 個 token，容量 1
	limiter := newRateLimiter(1, 10, time.Second)

	if allowed, _ := limiter.allow("1.2.3.4"); !allowed {
		t.Fatal("first request blocked")
	}
	if allowed, _ := limiter.allow("1.2.3.4"); allowed {
		t.Fatal("immediate second request allowed, want blocked")
	}

	// 手動回撥 lastSeen，模擬經過 1 秒（避免測試真的 sleep）
	limiter.mu.Lock()
	limiter.buckets["1.2.3.4"].lastSeen = time.Now().Add(-time.Second)
	limiter.mu.Unlock()

	if allowed, _ := limiter.allow("1.2.3.4"); !allowed {
		t.Error("request after refill window was blocked")
	}
}

func TestRateLimiterGarbageCollection(t *testing.T) {
	limiter := newRateLimiter(5, 5, time.Second)
	limiter.allow("stale-ip")

	// 讓該 bucket 看起來很久沒用，且強制觸發 GC 條件
	limiter.mu.Lock()
	limiter.buckets["stale-ip"].lastSeen = time.Now().Add(-time.Hour)
	limiter.lastGC = time.Now().Add(-time.Hour)
	limiter.mu.Unlock()

	limiter.allow("fresh-ip")

	limiter.mu.Lock()
	_, stillThere := limiter.buckets["stale-ip"]
	limiter.mu.Unlock()
	if stillThere {
		t.Error("stale bucket was not collected; memory would grow without bound")
	}
}

func TestClientIP(t *testing.T) {
	t.Run("uses RemoteAddr when proxy is not trusted", func(t *testing.T) {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = "10.0.0.5:54321"
		request.Header.Set("X-Forwarded-For", "1.1.1.1")
		if got := clientIP(request, false); got != "10.0.0.5" {
			t.Errorf("clientIP = %q, want 10.0.0.5", got)
		}
	})

	// 取最後一段：客戶端可偽造前綴，但最後一跳由我們的 proxy 附加
	t.Run("uses last forwarded hop when proxy is trusted", func(t *testing.T) {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = "10.0.0.5:54321"
		request.Header.Set("X-Forwarded-For", "spoofed-by-client, 203.0.113.9")
		if got := clientIP(request, true); got != "203.0.113.9" {
			t.Errorf("clientIP = %q, want 203.0.113.9 (the last hop)", got)
		}
	})

	t.Run("falls back to RemoteAddr without the header", func(t *testing.T) {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = "10.0.0.5:54321"
		if got := clientIP(request, true); got != "10.0.0.5" {
			t.Errorf("clientIP = %q, want 10.0.0.5", got)
		}
	})
}

func TestSplitAndTrim(t *testing.T) {
	got := splitAndTrim("  a , b ,, c  ", ',')
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("splitAndTrim returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d = %q, want %q", i, got[i], want[i])
		}
	}
}
