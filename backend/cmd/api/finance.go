package main

import (
	"math"
	"time"
)

// monthlyPayment 以本息平均攤還（annuity）計算月付金。
// 與前端 frontend/pages/index.vue 的 pmt() 使用同一公式，兩側結果必須一致。
func monthlyPayment(principal float64, annualRatePercent float64, months int) float64 {
	if months <= 0 {
		return 0
	}
	monthlyRate := annualRatePercent / 100 / 12
	if monthlyRate == 0 {
		return principal / float64(months)
	}
	return principal * monthlyRate / (1 - math.Pow(1+monthlyRate, -float64(months)))
}

type installment struct {
	Number           int       `json:"installmentNo"`
	DueDate          time.Time `json:"dueDate"`
	AmountDue        int64     `json:"amountDue"`
	Principal        int64     `json:"principal"`
	Interest         int64     `json:"interest"`
	RemainingBalance int64     `json:"remainingBalance"`
	Status           string    `json:"status"`
}

// buildAmortizationSchedule 產生整份攤還表。
//
// 以整數「元」為單位結算，避免浮點誤差累積：每期利息四捨五入到元，
// 本金 = 應繳 - 利息，最後一期吸收所有捨入殘差，確保
//
//	sum(principal) == principal 且 最後一期 remainingBalance == 0。
func buildAmortizationSchedule(loanID string, principal int64, annualRatePercent float64, months int, firstDue time.Time) []installment {
	if months <= 0 || principal <= 0 {
		return nil
	}

	payment := int64(math.Round(monthlyPayment(float64(principal), annualRatePercent, months)))
	monthlyRate := annualRatePercent / 100 / 12

	schedule := make([]installment, 0, months)
	balance := principal

	for i := 1; i <= months; i++ {
		interest := int64(math.Round(float64(balance) * monthlyRate))
		principalPart := payment - interest
		amountDue := payment

		if i == months || principalPart >= balance {
			// 末期（或提前還清）：結清剩餘本金，吸收所有捨入殘差
			principalPart = balance
			amountDue = principalPart + interest
		}

		balance -= principalPart

		schedule = append(schedule, installment{
			Number:           i,
			DueDate:          firstDue.AddDate(0, i-1, 0),
			AmountDue:        amountDue,
			Principal:        principalPart,
			Interest:         interest,
			RemainingBalance: balance,
			Status:           "scheduled",
		})

		if balance == 0 && i < months {
			break
		}
	}

	return schedule
}

// nextFirstDueDate 回傳撥款後第一期的繳款日：次月的同一日（固定 15 日扣款）。
func nextFirstDueDate(from time.Time) time.Time {
	year, month, _ := from.Date()
	first := time.Date(year, month, 15, 0, 0, 0, 0, time.UTC)
	if !first.After(from) {
		first = first.AddDate(0, 1, 0)
	}
	return first
}
