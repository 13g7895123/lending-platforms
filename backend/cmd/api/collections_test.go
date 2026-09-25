package main

import (
	"testing"
	"time"
)

func TestCollectionStage(t *testing.T) {
	tests := []struct {
		days      int
		wantStage string
		wantTone  string
	}{
		{-5, "未逾期", "success"}, // 尚未到期
		{0, "未逾期", "success"},
		{1, "M1", "warning"},
		{29, "M1", "warning"}, // M1 上界
		{30, "M2", "error"},   // M2 下界
		{59, "M2", "error"},
		{60, "M3+", "error"}, // M3 下界
		{365, "M3+", "error"},
	}

	for _, test := range tests {
		stage, tone := collectionStage(test.days)
		if stage != test.wantStage || tone != test.wantTone {
			t.Errorf("collectionStage(%d) = (%q, %q), want (%q, %q)",
				test.days, stage, tone, test.wantStage, test.wantTone)
		}
	}
}

func TestRecommendedCollectionAction(t *testing.T) {
	tests := map[int]string{
		0:   "—",
		10:  "簡訊 + 電話提醒",
		45:  "協商還款計畫",
		120: "存證信函／委外法務",
	}
	for days, want := range tests {
		if got := recommendedCollectionAction(days); got != want {
			t.Errorf("recommendedCollectionAction(%d) = %q, want %q", days, got, want)
		}
	}
}

func TestOverdueDaysBetween(t *testing.T) {
	due := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		now  time.Time
		want int
	}{
		{"before due date", time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), 0},
		// 繳款日當天不算逾期，即使已是當天深夜
		{"on due date", time.Date(2026, 9, 15, 23, 59, 0, 0, time.UTC), 0},
		{"one day late", time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC), 1},
		{"thirty days late", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), 30},
		{"crosses year boundary", time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), 122},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := overdueDaysBetween(due, test.now); got != test.want {
				t.Errorf("overdueDaysBetween(%v, %v) = %d, want %d", due, test.now, got, test.want)
			}
		})
	}
}

// 時分秒不得影響逾期天數：同一天的任何時刻結果都必須一致。
func TestOverdueDaysIgnoresTimeOfDay(t *testing.T) {
	due := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	base := overdueDaysBetween(due, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC))
	for _, hour := range []int{1, 8, 13, 23} {
		got := overdueDaysBetween(due, time.Date(2026, 9, 20, hour, 30, 45, 0, time.UTC))
		if got != base {
			t.Errorf("at %02d:30 got %d days, want %d (same day must match)", hour, got, base)
		}
	}
}

func TestInstallmentStatusFor(t *testing.T) {
	due := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		now  time.Time
		want string
	}{
		{"future installment stays scheduled", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "scheduled"},
		{"due on the due date", time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC), "due"},
		{"overdue the next day", time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC), "overdue"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := installmentStatusFor(due, test.now); got != test.want {
				t.Errorf("installmentStatusFor = %q, want %q", got, test.want)
			}
		})
	}
}

// 只有已到期或已逾期的期數可以還款，未到期不得先繳。
func TestPayableStatuses(t *testing.T) {
	if !payableStatuses["due"] || !payableStatuses["overdue"] {
		t.Error("due and overdue must be payable")
	}
	if payableStatuses["scheduled"] {
		t.Error("scheduled installments must not be payable")
	}
	if payableStatuses["paid"] {
		t.Error("paid installments must not be payable again")
	}
}

// 冪等鍵必須能唯一識別一筆還款意圖：
// 同一期重複產生相同鍵（撞唯一約束→擋下重複扣款），不同期彼此不同。
func TestIdempotencyKeys(t *testing.T) {
	first := installmentIdempotencyKey("LN-2026-000001", 1)
	if first != installmentIdempotencyKey("LN-2026-000001", 1) {
		t.Error("same loan and installment must produce a stable key")
	}
	if first == installmentIdempotencyKey("LN-2026-000001", 2) {
		t.Error("different installments must produce different keys")
	}
	if first == installmentIdempotencyKey("LN-2026-000002", 1) {
		t.Error("different loans must produce different keys")
	}

	payoff := prepaymentIdempotencyKey("LN-2026-000001")
	if payoff != prepaymentIdempotencyKey("LN-2026-000001") {
		t.Error("prepayment key must be stable per loan")
	}
	if payoff == prepaymentIdempotencyKey("LN-2026-000002") {
		t.Error("prepayment keys must differ across loans")
	}
	// 清償鍵不可與任何單期鍵碰撞
	for number := 1; number <= 84; number++ {
		if payoff == installmentIdempotencyKey("LN-2026-000001", number) {
			t.Fatalf("prepayment key collides with installment %d", number)
		}
	}
}
