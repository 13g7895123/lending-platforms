package main

import (
	"context"
	"fmt"
	"sort"
)

// investorShare 是一位出借人在某標的的投資與應得分潤。
type investorShare struct {
	InvestmentID int64
	InvestorID   string
	// 投資金額，決定分潤占比
	Amount int64
	// 本次分得的金額
	Principal int64
	Interest  int64
}

// allocateProportionally 按投資金額占比分配一筆金額。
//
// 以整數「元」為單位結算：先依占比向下取整分給每個人，
// 再把捨入殘差逐一補給投資金額最大者。這樣保證
//
//	sum(分配結果) == total
//
// 無論占比如何都成立——金額不可憑空產生或消失。
// 殘差給最大投資者而非平均攤，是因為前者的相對誤差最小。
func allocateProportionally(shares []investorShare, total int64, basis int64) []int64 {
	result := make([]int64, len(shares))
	if len(shares) == 0 || total <= 0 || basis <= 0 {
		return result
	}

	var distributed int64
	for i, share := range shares {
		// 用 int64 相乘再除，避免浮點誤差；amount 與 total 都在 int64 安全範圍內
		portion := share.Amount * total / basis
		result[i] = portion
		distributed += portion
	}

	remainder := total - distributed
	if remainder == 0 {
		return result
	}

	// 依投資金額由大到小補殘差（金額相同時依 InvestmentID 保持決定性）
	order := make([]int, len(shares))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		left, right := shares[order[a]], shares[order[b]]
		if left.Amount != right.Amount {
			return left.Amount > right.Amount
		}
		return left.InvestmentID < right.InvestmentID
	})

	// remainder 必然小於 len(shares)（每人最多少分到 1 元），
	// 但仍以迴圈處理以防 basis 與 amount 加總不一致的情況
	for remainder > 0 {
		for _, index := range order {
			if remainder == 0 {
				break
			}
			result[index]++
			remainder--
		}
	}
	return result
}

// loadInvestorShares 取出某合約背後的所有投資，供分潤計算使用。
// 回傳的 basis 是投資金額總和（即標的募資總額）。
func loadInvestorShares(ctx context.Context, transaction dbQuerier, loanID string) ([]investorShare, int64, error) {
	rows, err := transaction.Query(ctx, `
		SELECT i.id, i.investor_id, i.amount
		FROM investments i
		JOIN listings l ON l.id = i.listing_id
		JOIN loans ln ON ln.listing_id = l.id
		WHERE ln.id = $1
		ORDER BY i.id
	`, loanID)
	if err != nil {
		return nil, 0, fmt.Errorf("load investor shares: %w", err)
	}
	defer rows.Close()

	shares := make([]investorShare, 0)
	var basis int64
	for rows.Next() {
		var share investorShare
		if err := rows.Scan(&share.InvestmentID, &share.InvestorID, &share.Amount); err != nil {
			return nil, 0, fmt.Errorf("scan investor share: %w", err)
		}
		shares = append(shares, share)
		basis += share.Amount
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read investor shares: %w", err)
	}
	return shares, basis, nil
}

// distributeRepayment 把一筆還款的本金與利息按占比分給出借人。
//
// 必須在還款的同一個 transaction 內呼叫：分潤與扣款要嘛一起成立、
// 要嘛一起回滾，不可出現「借款人繳了錢但出借人沒收到」的狀態。
//
// 由 seed 或舊資料建立、背後沒有投資紀錄的合約（basis 為 0）不分潤，
// 直接回傳 nil——這類合約的資金來源不明，無從分配。
func distributeRepayment(
	ctx context.Context,
	transaction dbExecutorQuerier,
	loanID string,
	repaymentID int64,
	principal int64,
	interest int64,
) error {
	shares, basis, err := loadInvestorShares(ctx, transaction, loanID)
	if err != nil {
		return err
	}
	if basis <= 0 || len(shares) == 0 {
		return nil
	}

	principalParts := allocateProportionally(shares, principal, basis)
	interestParts := allocateProportionally(shares, interest, basis)

	for i, share := range shares {
		principalPart := principalParts[i]
		interestPart := interestParts[i]
		if principalPart == 0 && interestPart == 0 {
			continue
		}

		if _, err := transaction.Exec(ctx, `
			INSERT INTO distributions (
				repayment_id, investment_id, investor_id, loan_id, principal, interest
			) VALUES ($1, $2, $3, $4, $5, $6)
		`, repaymentID, share.InvestmentID, share.InvestorID, loanID,
			principalPart, interestPart); err != nil {
			return fmt.Errorf("insert distribution for investment %d: %w", share.InvestmentID, err)
		}

		if _, err := transaction.Exec(ctx, `
			UPDATE investments
			SET principal_returned = principal_returned + $2,
			    interest_earned = interest_earned + $3
			WHERE id = $1
		`, share.InvestmentID, principalPart, interestPart); err != nil {
			return fmt.Errorf("update investment %d totals: %w", share.InvestmentID, err)
		}

		// 本金與利息一併回到出借人的可用餘額，可再投入其他標的
		if _, err := transaction.Exec(ctx, `
			UPDATE users SET available_balance = available_balance + $2 WHERE id = $1
		`, share.InvestorID, principalPart+interestPart); err != nil {
			return fmt.Errorf("credit investor %s: %w", share.InvestorID, err)
		}
	}
	return nil
}
