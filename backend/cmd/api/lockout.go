package main

import (
	"context"
	"fmt"
	"math"
	"time"
)

// 帳號維度的暴力破解防護。
//
// IP 限流擋不住輪替 IP 的攻擊者，因此另外按帳號記錄失敗次數。
// 採「暫時鎖定且時長遞增」而非永久鎖定：永久鎖定會讓攻擊者
// 能以故意輸錯密碼來封鎖任意帳號（把防護本身變成 DoS 工具）。

// lockoutThresholds 由小到大排列：達到 attempts 次失敗後鎖定 duration。
var lockoutThresholds = []struct {
	attempts int
	duration time.Duration
}{
	{5, 5 * time.Minute},
	{10, 30 * time.Minute},
	{15, 2 * time.Hour},
}

// maxLockoutDuration 是遞增的上限。再久就等同永久鎖定，
// 反而讓合法使用者無法自救（他們仍可透過密碼重設解鎖）。
const maxLockoutDuration = 24 * time.Hour

// lockoutDurationFor 依累計失敗次數回傳應鎖定的時長。
// 未達最低門檻時回傳 0，表示尚不鎖定。
func lockoutDurationFor(failedCount int) time.Duration {
	if failedCount < lockoutThresholds[0].attempts {
		return 0
	}

	// 取符合條件的最高門檻
	duration := lockoutThresholds[0].duration
	for _, threshold := range lockoutThresholds {
		if failedCount >= threshold.attempts {
			duration = threshold.duration
		}
	}

	// 超過最高門檻後每多 5 次加倍，直到上限
	highest := lockoutThresholds[len(lockoutThresholds)-1]
	if failedCount > highest.attempts {
		extra := (failedCount - highest.attempts) / 5
		if extra > 0 {
			// 以 float 計算避免整數溢位，再夾到上限
			scaled := float64(duration) * math.Pow(2, float64(extra))
			if scaled >= float64(maxLockoutDuration) {
				return maxLockoutDuration
			}
			duration = time.Duration(scaled)
		}
	}

	if duration > maxLockoutDuration {
		return maxLockoutDuration
	}
	return duration
}

// retryAfterSeconds 把鎖定剩餘時間換算成 Retry-After 的秒數。
// 至少回傳 1，避免客戶端讀到 0 而立刻重試。
func retryAfterSeconds(lockedUntil, now time.Time) int {
	remaining := lockedUntil.Sub(now)
	if remaining <= 0 {
		return 0
	}
	seconds := int(math.Ceil(remaining.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

// recordFailedLogin 累加失敗次數，必要時設定鎖定。
// 回傳鎖定到期時間（未鎖定時為零值）。
func (s *apiServer) recordFailedLogin(
	ctx context.Context, userID, sourceIP string,
) (time.Time, error) {
	var failedCount int
	if err := s.db.QueryRow(ctx, `
		UPDATE users
		SET failed_login_count = failed_login_count + 1,
		    last_failed_login_at = NOW(),
		    last_failed_login_ip = $2
		WHERE id = $1
		RETURNING failed_login_count
	`, userID, sourceIP).Scan(&failedCount); err != nil {
		return time.Time{}, fmt.Errorf("record failed login: %w", err)
	}

	duration := lockoutDurationFor(failedCount)
	if duration == 0 {
		return time.Time{}, nil
	}

	var lockedUntil time.Time
	if err := s.db.QueryRow(ctx, `
		UPDATE users SET locked_until = NOW() + $2::interval
		WHERE id = $1
		RETURNING locked_until
	`, userID, duration.String()).Scan(&lockedUntil); err != nil {
		return time.Time{}, fmt.Errorf("apply lockout: %w", err)
	}
	return lockedUntil, nil
}

// clearLoginFailures 在成功登入或密碼重設後清空計數與鎖定。
//
// 合法使用者偶爾打錯不該累積到被鎖；
// 密碼重設成功代表擁有者證明了身分，更不該繼續鎖著。
func (s *apiServer) clearLoginFailures(ctx context.Context, executor dbExecutor, userID string) error {
	if _, err := executor.Exec(ctx, `
		UPDATE users
		SET failed_login_count = 0, locked_until = NULL
		WHERE id = $1 AND (failed_login_count > 0 OR locked_until IS NOT NULL)
	`, userID); err != nil {
		return fmt.Errorf("clear login failures: %w", err)
	}
	return nil
}
