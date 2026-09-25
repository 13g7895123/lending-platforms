package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// markOverdueInstallments 推進所有合約的期數狀態：
//   - due_date 已到但仍 scheduled → due
//   - due_date 已過且未繳 → overdue，並更新逾期天數
//
// 以資料庫的 CURRENT_DATE 為基準，避免應用伺服器時區造成誤判。
// 本函式為 idempotent：重複執行只會讓 overdue_days 反映當下事實，不會累加。
func markOverdueInstallments(ctx context.Context, db dbExecutor) (int64, error) {
	// 已到期但仍標記 scheduled 的期數轉為 due
	if _, err := db.Exec(ctx, `
		UPDATE loan_installments
		SET status = 'due'
		WHERE status = 'scheduled' AND due_date <= CURRENT_DATE
	`); err != nil {
		return 0, fmt.Errorf("mark due installments: %w", err)
	}

	// 逾期判定：繳款日當天不算逾期，隔日起計
	tag, err := db.Exec(ctx, `
		UPDATE loan_installments
		SET status = 'overdue',
		    overdue_days = (CURRENT_DATE - due_date)
		WHERE status IN ('due', 'overdue')
		  AND due_date < CURRENT_DATE
		  AND (status <> 'overdue' OR overdue_days <> (CURRENT_DATE - due_date))
	`)
	if err != nil {
		return 0, fmt.Errorf("mark overdue installments: %w", err)
	}
	return tag.RowsAffected(), nil
}

// syncOverdueLoanStatus 讓有逾期期數的合約在列表上顯示為逾期，
// 逾期期數全部繳清後回復正常繳款（已結清的合約不動）。
func syncOverdueLoanStatus(ctx context.Context, db dbExecutor) error {
	if _, err := db.Exec(ctx, `
		UPDATE loans l
		SET status = '逾期', status_tone = 'error'
		WHERE l.status <> '已結清'
		  AND EXISTS (
		    SELECT 1 FROM loan_installments i
		    WHERE i.loan_id = l.id AND i.status = 'overdue'
		  )
	`); err != nil {
		return fmt.Errorf("mark overdue loans: %w", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE loans l
		SET status = '正常繳款', status_tone = 'success'
		WHERE l.status = '逾期'
		  AND NOT EXISTS (
		    SELECT 1 FROM loan_installments i
		    WHERE i.loan_id = l.id AND i.status = 'overdue'
		  )
	`); err != nil {
		return fmt.Errorf("clear overdue loans: %w", err)
	}
	return nil
}

// startOverdueSweep 啟動時執行一次，之後每日重跑，讓逾期狀態隨日期推進。
func (s *apiServer) startOverdueSweep(logger *slog.Logger) func() {
	sweep := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		affected, err := markOverdueInstallments(ctx, s.db)
		if err != nil {
			logger.Warn("overdue sweep failed", "error", err)
			return
		}
		if err := syncOverdueLoanStatus(ctx, s.db); err != nil {
			logger.Warn("overdue loan status sync failed", "error", err)
			return
		}
		if affected > 0 {
			logger.Info("overdue sweep completed", "installments_updated", affected)
		}
	}

	sweep()

	ticker := time.NewTicker(24 * time.Hour)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				sweep()
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// ---------------------------------------------------------------- 催收清單

type overdueItem struct {
	LoanID           string    `json:"loanId"`
	InstallmentNo    int       `json:"installmentNo"`
	BorrowerName     string    `json:"borrowerName"`
	Product          string    `json:"product"`
	DueDate          time.Time `json:"dueDate"`
	OverdueDays      int       `json:"overdueDays"`
	AmountDue        int64     `json:"amountDue"`
	RemainingBalance int64     `json:"remainingBalance"`
	Stage            string    `json:"stage"`
	StageTone        string    `json:"stageTone"`
	Action           string    `json:"action"`
}

// adminOverdue 回傳逾期催收清單（reviewer only）。
func (s *apiServer) adminOverdue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 先確保逾期資料反映當下日期，避免清單顯示過時狀態
	if _, err := markOverdueInstallments(ctx, s.db); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh overdue state")
		return
	}
	if err := syncOverdueLoanStatus(ctx, s.db); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync loan status")
		return
	}

	rows, err := s.db.Query(ctx, `
		SELECT i.loan_id, i.installment_no, COALESCE(u.display_name, '(未指派)'),
		       l.product, i.due_date, i.overdue_days, i.amount_due, i.remaining_balance
		FROM loan_installments i
		JOIN loans l ON l.id = i.loan_id
		LEFT JOIN users u ON u.id = l.user_id
		WHERE i.status = 'overdue'
		ORDER BY i.overdue_days DESC, i.loan_id, i.installment_no
		LIMIT 200
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load overdue list")
		return
	}
	defer rows.Close()

	items := make([]overdueItem, 0)
	for rows.Next() {
		var item overdueItem
		if err := rows.Scan(&item.LoanID, &item.InstallmentNo, &item.BorrowerName,
			&item.Product, &item.DueDate, &item.OverdueDays,
			&item.AmountDue, &item.RemainingBalance); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode overdue list")
			return
		}
		item.Stage, item.StageTone = collectionStage(item.OverdueDays)
		item.Action = recommendedCollectionAction(item.OverdueDays)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read overdue list")
		return
	}
	writeJSON(w, http.StatusOK, items)
}
