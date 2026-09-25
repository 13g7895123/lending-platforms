package main

import (
	"testing"
	"time"
)

func TestFundingDeadlineFor(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	deadline := fundingDeadlineFor(created)

	want := time.Date(2026, 9, 15, 10, 30, 0, 0, time.UTC)
	if !deadline.Equal(want) {
		t.Errorf("fundingDeadlineFor(%v) = %v, want %v", created, deadline, want)
	}

	// 跨月與跨年都要正確
	if got := fundingDeadlineFor(time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)); got.Year() != 2027 {
		t.Errorf("deadline for a late-December listing = %v, want it to roll into 2027", got)
	}
}

func TestDaysUntilDeadline(t *testing.T) {
	deadline := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		now  time.Time
		want int
	}{
		{"two weeks out", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 14},
		{"one day left", time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), 1},
		// 當天到期回 0，讓前端能顯示「今天截止」
		{"due today", time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC), 0},
		{"due today late at night", time.Date(2026, 9, 15, 23, 59, 0, 0, time.UTC), 0},
		// 逾期回負數，與「今天到期」區分開
		{"one day overdue", time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC), -1},
		{"long overdue", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), -30},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := daysUntilDeadline(deadline, test.now); got != test.want {
				t.Errorf("daysUntilDeadline = %d, want %d", got, test.want)
			}
		})
	}
}

// 時分秒不可影響天數計算，否則同一天會看到不同的倒數。
func TestDaysUntilDeadlineIgnoresTimeOfDay(t *testing.T) {
	deadline := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	base := daysUntilDeadline(deadline, time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC))

	for _, hour := range []int{1, 7, 13, 23} {
		got := daysUntilDeadline(deadline, time.Date(2026, 9, 10, hour, 45, 30, 0, time.UTC))
		if got != base {
			t.Errorf("at %02d:45 got %d days, want %d (same day must agree)", hour, got, base)
		}
	}
}

func TestDefaultFundingDays(t *testing.T) {
	if defaultFundingDays < 1 {
		t.Error("the funding window must be at least one day")
	}
	// 過長的募資期會讓借款人與出借人的資金都被長時間綁住
	if defaultFundingDays > 60 {
		t.Errorf("defaultFundingDays %d is long enough to trap funds", defaultFundingDays)
	}
}
