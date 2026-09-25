package main

import (
	"fmt"
	"time"
)

// 催收階段門檻（逾期天數）。與業界慣用的 M1/M2/M3 分段一致。
const (
	stageM1Days = 30
	stageM2Days = 60
)

// collectionStage 依逾期天數回傳催收階段與嚴重度色調。
// 0 天（或負數，即尚未到期）視為未逾期。
func collectionStage(overdueDays int) (string, string) {
	switch {
	case overdueDays <= 0:
		return "未逾期", "success"
	case overdueDays < stageM1Days:
		return "M1", "warning"
	case overdueDays < stageM2Days:
		return "M2", "error"
	default:
		return "M3+", "error"
	}
}

// recommendedCollectionAction 依催收階段建議處理方式。
func recommendedCollectionAction(overdueDays int) string {
	switch stage, _ := collectionStage(overdueDays); stage {
	case "未逾期":
		return "—"
	case "M1":
		return "簡訊 + 電話提醒"
	case "M2":
		return "協商還款計畫"
	default:
		return "存證信函／委外法務"
	}
}

// overdueDaysBetween 計算逾期天數：以日為單位，繳款日當天不算逾期。
// 兩個時間都會先截到當日零時，避免時分秒造成 off-by-one。
func overdueDaysBetween(dueDate, now time.Time) int {
	due := truncateToDay(dueDate)
	today := truncateToDay(now)
	if !today.After(due) {
		return 0
	}
	return int(today.Sub(due).Hours() / 24)
}

func truncateToDay(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// installmentStatusFor 依繳款日與當前時間決定未繳期數應有的狀態。
// 已繳期數不經過此函式（paid 為終局狀態）。
func installmentStatusFor(dueDate, now time.Time) string {
	if overdueDaysBetween(dueDate, now) > 0 {
		return "overdue"
	}
	if !truncateToDay(dueDate).After(truncateToDay(now)) {
		return "due"
	}
	return "scheduled"
}

// payableStatuses 是允許還款的期數狀態。
// 未到期（scheduled）不可先繳，避免與提前清償語意混淆。
var payableStatuses = map[string]bool{
	"due":     true,
	"overdue": true,
}

// installmentIdempotencyKey 產生單期還款的冪等鍵。
// 同一合約同一期只會有一個鍵，因此重送必然撞上唯一約束。
func installmentIdempotencyKey(loanID string, installmentNo int) string {
	return fmt.Sprintf("installment:%s:%d", loanID, installmentNo)
}

// prepaymentIdempotencyKey 產生提前清償的冪等鍵。
// 一份合約只能清償一次，故不含期數。
func prepaymentIdempotencyKey(loanID string) string {
	return fmt.Sprintf("prepayment:%s", loanID)
}
