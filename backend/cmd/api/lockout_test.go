package main

import (
	"testing"
	"time"
)

func TestLockoutDurationFor(t *testing.T) {
	tests := []struct {
		name   string
		failed int
		want   time.Duration
	}{
		// 門檻以下不鎖定：合法使用者偶爾打錯不該被鎖
		{"no failures", 0, 0},
		{"one below the first threshold", 4, 0},
		{"first threshold", 5, 5 * time.Minute},
		{"between thresholds", 7, 5 * time.Minute},
		{"second threshold", 10, 30 * time.Minute},
		{"between second and third", 12, 30 * time.Minute},
		{"third threshold", 15, 2 * time.Hour},
		// 超過最高門檻後每 5 次加倍
		{"five past the top", 20, 4 * time.Hour},
		{"ten past the top", 25, 8 * time.Hour},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := lockoutDurationFor(test.failed); got != test.want {
				t.Errorf("lockoutDurationFor(%d) = %v, want %v", test.failed, got, test.want)
			}
		})
	}
}

// 鎖定時長必須有上限：無限增長等同永久鎖定，
// 會讓攻擊者能以故意輸錯封鎖任意帳號。
func TestLockoutDurationIsCapped(t *testing.T) {
	for _, failed := range []int{50, 200, 1000, 100000} {
		got := lockoutDurationFor(failed)
		if got > maxLockoutDuration {
			t.Errorf("lockoutDurationFor(%d) = %v, exceeds the cap %v",
				failed, got, maxLockoutDuration)
		}
		if got <= 0 {
			t.Errorf("lockoutDurationFor(%d) = %v, want a positive duration", failed, got)
		}
	}
}

// 失敗次數越多，鎖定時間不可變短。
func TestLockoutDurationIsMonotonic(t *testing.T) {
	previous := time.Duration(0)
	for failed := 0; failed <= 60; failed++ {
		current := lockoutDurationFor(failed)
		if current < previous {
			t.Fatalf("lockoutDurationFor(%d) = %v is shorter than for %d (%v)",
				failed, current, failed-1, previous)
		}
		previous = current
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		lockedUntil time.Time
		want        int
	}{
		{"five minutes out", now.Add(5 * time.Minute), 300},
		{"already expired", now.Add(-time.Minute), 0},
		{"exactly now", now, 0},
		// 不足一秒也要回 1，否則客戶端會讀到 0 而立刻重試
		{"half a second", now.Add(500 * time.Millisecond), 1},
		{"rounds up", now.Add(90500 * time.Millisecond), 91},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := retryAfterSeconds(test.lockedUntil, now); got != test.want {
				t.Errorf("retryAfterSeconds = %d, want %d", got, test.want)
			}
		})
	}
}

// 門檻設定本身要合理：太低會困擾正常使用者，太高則失去防護意義。
func TestLockoutThresholdsAreSane(t *testing.T) {
	if len(lockoutThresholds) == 0 {
		t.Fatal("no lockout thresholds are defined")
	}

	first := lockoutThresholds[0]
	if first.attempts < 3 {
		t.Errorf("the first threshold at %d attempts will lock out ordinary typos", first.attempts)
	}
	if first.attempts > 10 {
		t.Errorf("the first threshold at %d attempts is too permissive", first.attempts)
	}

	// 門檻與時長都必須遞增
	for i := 1; i < len(lockoutThresholds); i++ {
		if lockoutThresholds[i].attempts <= lockoutThresholds[i-1].attempts {
			t.Errorf("threshold %d does not require more attempts than the previous one", i)
		}
		if lockoutThresholds[i].duration <= lockoutThresholds[i-1].duration {
			t.Errorf("threshold %d does not lock for longer than the previous one", i)
		}
	}

	if maxLockoutDuration <= lockoutThresholds[len(lockoutThresholds)-1].duration {
		t.Error("the cap is not above the highest configured threshold")
	}
}
