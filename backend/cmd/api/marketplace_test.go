package main

import (
	"strings"
	"testing"
)

func TestFundedPercent(t *testing.T) {
	tests := []struct {
		name   string
		funded int64
		target int64
		want   int
	}{
		{"nothing raised", 0, 500000, 0},
		{"half raised", 250000, 500000, 50},
		{"fully raised", 500000, 500000, 100},
		{"over raised is capped", 600000, 500000, 100},
		{"zero target", 0, 0, 0},
		{"rounds to nearest", 333333, 1000000, 33},
		// 未達目標時不得顯示 100%，否則前端會誤判為已滿
		{"almost full stays below 100", 999999, 1000000, 99},
		{"99.5 percent stays below 100", 995000, 1000000, 99},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fundedPercent(test.funded, test.target); got != test.want {
				t.Errorf("fundedPercent(%d, %d) = %d, want %d",
					test.funded, test.target, got, test.want)
			}
		})
	}
}

func TestEstimatedInvestmentReturn(t *testing.T) {
	// 10 萬 / 4.88% / 36 期：總繳款約 107,700，收益約 7,700
	got := estimatedInvestmentReturn(100000, 4.88, 36)
	if got < 7000 || got > 8500 {
		t.Errorf("estimatedInvestmentReturn(100000, 4.88, 36) = %d, want roughly 7700", got)
	}

	// 利率越高收益越高
	low := estimatedInvestmentReturn(100000, 4.88, 36)
	high := estimatedInvestmentReturn(100000, 9.6, 36)
	if high <= low {
		t.Errorf("higher rate produced lower return: %d vs %d", high, low)
	}

	// 期數越長總利息越多
	short := estimatedInvestmentReturn(100000, 6.8, 12)
	long := estimatedInvestmentReturn(100000, 6.8, 60)
	if long <= short {
		t.Errorf("longer term produced lower return: %d vs %d", long, short)
	}
}

func TestEstimatedInvestmentReturnRejectsInvalidInput(t *testing.T) {
	if got := estimatedInvestmentReturn(0, 4.88, 36); got != 0 {
		t.Errorf("zero amount = %d, want 0", got)
	}
	if got := estimatedInvestmentReturn(-100, 4.88, 36); got != 0 {
		t.Errorf("negative amount = %d, want 0", got)
	}
	if got := estimatedInvestmentReturn(100000, 4.88, 0); got != 0 {
		t.Errorf("zero term = %d, want 0", got)
	}
}

// 冪等鍵必須能讓同一人對同一標的的不同筆投標彼此區分，
// 但同一個 requestId 重送時產生相同的鍵（撞唯一約束→擋下重複扣款）。
func TestInvestmentIdempotencyKey(t *testing.T) {
	first := investmentIdempotencyKey("LT-001", "usr_a", "req-1")
	if first != investmentIdempotencyKey("LT-001", "usr_a", "req-1") {
		t.Error("same requestId must produce a stable key")
	}
	if first == investmentIdempotencyKey("LT-001", "usr_a", "req-2") {
		t.Error("different requestIds must produce different keys")
	}
	if first == investmentIdempotencyKey("LT-002", "usr_a", "req-1") {
		t.Error("different listings must produce different keys")
	}
	if first == investmentIdempotencyKey("LT-001", "usr_b", "req-1") {
		t.Error("different investors must produce different keys")
	}

	// 沒有 requestId 時退回時間戳，兩次呼叫必須不同（不保證冪等，由呼叫端負責）
	auto := investmentIdempotencyKey("LT-001", "usr_a", "")
	if auto == investmentIdempotencyKey("LT-001", "usr_a", "") {
		t.Error("auto-generated keys must not collide")
	}
	if !strings.Contains(auto, "auto-") {
		t.Errorf("auto key %q should be marked as generated", auto)
	}
	// 空白字元視同未提供
	blank := investmentIdempotencyKey("LT-001", "usr_a", "   ")
	if !strings.Contains(blank, "auto-") {
		t.Errorf("whitespace requestId should fall back to auto, got %q", blank)
	}
}

// 核准後的目標狀態必須是 funding：核准的意義是通過授信、開始募資，
// 不是直接發約。
func TestReviewTransitionApproveLeadsToFunding(t *testing.T) {
	if got := reviewTransitions["approve"]; got != "funding" {
		t.Errorf("approve transitions to %q, want funding", got)
	}
	if got := reviewTransitions["reject"]; got != "rejected" {
		t.Errorf("reject transitions to %q, want rejected", got)
	}
	if got := reviewTransitions["request_more_info"]; got != "more_info_required" {
		t.Errorf("request_more_info transitions to %q", got)
	}
}

func TestInvestmentAmountBounds(t *testing.T) {
	if minInvestmentAmount <= 0 {
		t.Error("minimum investment must be positive")
	}
	if maxInvestmentAmount <= minInvestmentAmount {
		t.Error("maximum investment must exceed the minimum")
	}
}
