package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type repayment struct {
	ID            int64     `json:"id"`
	LoanID        string    `json:"loanId"`
	InstallmentNo *int      `json:"installmentNo"`
	Kind          string    `json:"kind"`
	Amount        int64     `json:"amount"`
	Principal     int64     `json:"principal"`
	Interest      int64     `json:"interest"`
	CreatedAt     time.Time `json:"createdAt"`
}

type payResponse struct {
	Repayment repayment `json:"repayment"`
	Loan      loan      `json:"loan"`
	Settled   bool      `json:"settled"`
}

// lockedLoan 是還款流程中被鎖定的合約狀態。
type lockedLoan struct {
	ID                string
	UserID            string
	Product           string
	Amount            int64
	Rate              float64
	PaidInstallments  int
	TotalInstallments int
	Status            string
	MonthlyPayment    int64
}

// loanRoutes 分派 /v1/loans/ 底下的子路由。
//
//	GET  /v1/loans/{id}/schedule
//	GET  /v1/loans/{id}/repayments
//	POST /v1/loans/{id}/installments/{no}/pay
//	POST /v1/loans/{id}/settle
func (s *apiServer) loanRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/loans/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	loanID := parts[0]

	switch {
	case len(parts) == 2 && parts[1] == "schedule":
		s.loanSchedule(w, r, loanID)
	case len(parts) == 2 && parts[1] == "repayments":
		s.loanRepayments(w, r, loanID)
	case len(parts) == 2 && parts[1] == "settle":
		s.settleLoan(w, r, loanID)
	case len(parts) == 4 && parts[1] == "installments" && parts[3] == "pay":
		number, err := strconv.Atoi(parts[2])
		if err != nil || number <= 0 {
			writeError(w, http.StatusNotFound, "installment not found")
			return
		}
		s.payInstallment(w, r, loanID, number)
	default:
		writeError(w, http.StatusNotFound, "route not found")
	}
}

// lockLoanForUpdate 取出並鎖定使用者自己的合約。
// 以 user_id 一併過濾：他人合約回 pgx.ErrNoRows，呼叫端轉為 404，不洩漏存在性。
func lockLoanForUpdate(ctx context.Context, transaction pgx.Tx, loanID, userID string) (*lockedLoan, error) {
	var found lockedLoan
	err := transaction.QueryRow(ctx, `
		SELECT id, user_id, product, amount, annual_rate,
		       paid_installments, total_installments, status, monthly_payment
		FROM loans
		WHERE id = $1 AND user_id = $2
		FOR UPDATE
	`, loanID, userID).Scan(&found.ID, &found.UserID, &found.Product, &found.Amount,
		&found.Rate, &found.PaidInstallments, &found.TotalInstallments,
		&found.Status, &found.MonthlyPayment)
	if err != nil {
		return nil, err
	}
	return &found, nil
}

// refreshLoanProgress 依 loan_installments 的實際狀態同步合約進度。
// 全期繳畢時自動轉為已結清，並寫入 settled_at。
// 回傳同步後的合約與「本次是否完成結清」。
func refreshLoanProgress(ctx context.Context, transaction pgx.Tx, loanID string) (loan, bool, error) {
	var paidCount, totalCount int
	if err := transaction.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE status = 'paid'), COUNT(*)
		FROM loan_installments
		WHERE loan_id = $1
	`, loanID).Scan(&paidCount, &totalCount); err != nil {
		return loan{}, false, fmt.Errorf("count installments: %w", err)
	}

	settled := totalCount > 0 && paidCount == totalCount
	status, tone := "正常繳款", "success"
	if settled {
		status, tone = "已結清", "info"
	} else {
		// 尚有逾期期數時，合約狀態反映風險
		var overdueCount int
		if err := transaction.QueryRow(ctx,
			`SELECT COUNT(*) FROM loan_installments WHERE loan_id = $1 AND status = 'overdue'`,
			loanID).Scan(&overdueCount); err != nil {
			return loan{}, false, fmt.Errorf("count overdue: %w", err)
		}
		if overdueCount > 0 {
			status, tone = "逾期", "error"
		}
	}

	var updated loan
	// settled_at 只在首次結清時寫入（COALESCE 保留原值，避免重跑改動時間）
	err := transaction.QueryRow(ctx, `
		UPDATE loans
		SET paid_installments = $2,
		    status = $3,
		    status_tone = $4,
		    settled_at = CASE WHEN $5 THEN COALESCE(settled_at, NOW()) ELSE NULL END
		WHERE id = $1
		RETURNING id, product, amount, annual_rate, paid_installments,
		          total_installments, status, status_tone, monthly_payment
	`, loanID, paidCount, status, tone, settled).Scan(
		&updated.ID, &updated.Product, &updated.Amount, &updated.Rate,
		&updated.PaidInstallments, &updated.TotalInstallments,
		&updated.Status, &updated.StatusTone, &updated.MonthlyPayment)
	if err != nil {
		return loan{}, false, fmt.Errorf("update loan progress: %w", err)
	}
	return updated, settled, nil
}

// ---------------------------------------------------------------- 單期還款

func (s *apiServer) payInstallment(w http.ResponseWriter, r *http.Request, loanID string, number int) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	transaction, err := s.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if _, err := lockLoanForUpdate(ctx, transaction, loanID, actor.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "loan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load loan")
		return
	}

	// 鎖定該期，避免同一期被併發重複扣款
	var status string
	var amountDue, principal, interest int64
	err = transaction.QueryRow(ctx, `
		SELECT status, amount_due, principal, interest
		FROM loan_installments
		WHERE loan_id = $1 AND installment_no = $2
		FOR UPDATE
	`, loanID, number).Scan(&status, &amountDue, &principal, &interest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "installment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installment")
		return
	}

	if status == "paid" {
		writeError(w, http.StatusConflict, "installment is already paid")
		return
	}
	if !payableStatuses[status] {
		writeError(w, http.StatusUnprocessableEntity,
			"only due or overdue installments can be paid; this one is not due yet")
		return
	}

	key := installmentIdempotencyKey(loanID, number)
	var created repayment
	err = transaction.QueryRow(ctx, `
		INSERT INTO repayments (loan_id, installment_no, kind, amount, principal, interest, paid_by, idempotency_key)
		VALUES ($1, $2, 'installment', $3, $4, $5, $6, $7)
		RETURNING id, created_at
	`, loanID, number, amountDue, principal, interest, actor.ID, key).Scan(&created.ID, &created.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			// 冪等鍵已存在：這期先前已落帳，視為重複請求
			writeError(w, http.StatusConflict, "installment is already paid")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record repayment")
		return
	}

	if _, err := transaction.Exec(ctx, `
		UPDATE loan_installments
		SET status = 'paid', paid_at = NOW(), paid_amount = $3, overdue_days = 0
		WHERE loan_id = $1 AND installment_no = $2
	`, loanID, number, amountDue); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update installment")
		return
	}

	// 在同一 transaction 內把本金與利息分給出借人：
	// 不可出現「借款人繳了錢但出借人沒收到」的狀態
	if err := distributeRepayment(ctx, transaction, loanID, created.ID, principal, interest); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to distribute repayment")
		return
	}

	updatedLoan, settled, err := refreshLoanProgress(ctx, transaction, loanID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update loan progress")
		return
	}

	// 結清前把下一期由 scheduled 推進為 due，讓借款人看得到下一個繳款目標
	if !settled {
		if err := advanceNextInstallment(ctx, transaction, loanID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to advance next installment")
			return
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit repayment")
		return
	}

	created.LoanID = loanID
	created.InstallmentNo = &number
	created.Kind = "installment"
	created.Amount = amountDue
	created.Principal = principal
	created.Interest = interest
	writeJSON(w, http.StatusCreated, payResponse{Repayment: created, Loan: updatedLoan, Settled: settled})
}

// advanceNextInstallment 把最早一期未繳且仍為 scheduled 的期數轉為 due。
func advanceNextInstallment(ctx context.Context, transaction pgx.Tx, loanID string) error {
	_, err := transaction.Exec(ctx, `
		UPDATE loan_installments
		SET status = 'due'
		WHERE loan_id = $1
		  AND installment_no = (
		    SELECT MIN(installment_no) FROM loan_installments
		    WHERE loan_id = $1 AND status = 'scheduled'
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM loan_installments
		    WHERE loan_id = $1 AND status IN ('due', 'overdue')
		  )
	`, loanID)
	return err
}

// ---------------------------------------------------------------- 提前清償

func (s *apiServer) settleLoan(w http.ResponseWriter, r *http.Request, loanID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	transaction, err := s.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	existing, err := lockLoanForUpdate(ctx, transaction, loanID, actor.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "loan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load loan")
		return
	}
	if existing.Status == "已結清" {
		writeError(w, http.StatusConflict, "loan is already settled")
		return
	}

	// 未繳期數的本金總和即為應清償本金；未到期利息予以免除
	var remainingPrincipal, waivedInterest int64
	var unpaidCount int
	if err := transaction.QueryRow(ctx, `
		SELECT COALESCE(SUM(principal), 0), COALESCE(SUM(interest), 0), COUNT(*)
		FROM loan_installments
		WHERE loan_id = $1 AND status <> 'paid'
	`, loanID).Scan(&remainingPrincipal, &waivedInterest, &unpaidCount); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute payoff amount")
		return
	}
	if unpaidCount == 0 {
		writeError(w, http.StatusConflict, "loan has no outstanding installments")
		return
	}

	var created repayment
	err = transaction.QueryRow(ctx, `
		INSERT INTO repayments (loan_id, installment_no, kind, amount, principal, interest, paid_by, idempotency_key)
		VALUES ($1, NULL, 'prepayment', $2, $2, 0, $3, $4)
		RETURNING id, created_at
	`, loanID, remainingPrincipal, actor.ID, prepaymentIdempotencyKey(loanID)).Scan(&created.ID, &created.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "loan is already settled")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record prepayment")
		return
	}

	// 剩餘各期一次結清：利息免除，故 paid_amount 只記本金
	if _, err := transaction.Exec(ctx, `
		UPDATE loan_installments
		SET status = 'paid', paid_at = NOW(), paid_amount = principal, overdue_days = 0
		WHERE loan_id = $1 AND status <> 'paid'
	`, loanID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to settle installments")
		return
	}

	// 清償只分配本金：未到期利息已免除，出借人不會收到這部分
	if err := distributeRepayment(ctx, transaction, loanID, created.ID, remainingPrincipal, 0); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to distribute prepayment")
		return
	}

	updatedLoan, settled, err := refreshLoanProgress(ctx, transaction, loanID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update loan progress")
		return
	}
	if err := transaction.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prepayment")
		return
	}

	created.LoanID = loanID
	created.Kind = "prepayment"
	created.Amount = remainingPrincipal
	created.Principal = remainingPrincipal
	writeJSON(w, http.StatusCreated, map[string]any{
		"repayment":      created,
		"loan":           updatedLoan,
		"settled":        settled,
		"waivedInterest": waivedInterest,
		"closedCount":    unpaidCount,
	})
}

// ---------------------------------------------------------------- 繳款紀錄

func (s *apiServer) loanRepayments(w http.ResponseWriter, r *http.Request, loanID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 先確認合約歸屬，避免以 loan_id 猜測他人紀錄
	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM loans WHERE id = $1 AND user_id = $2)`,
		loanID, actor.ID).Scan(&exists); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify loan")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "loan not found")
		return
	}

	rows, err := s.db.Query(ctx, `
		SELECT id, loan_id, installment_no, kind, amount, principal, interest, created_at
		FROM repayments
		WHERE loan_id = $1
		ORDER BY created_at DESC, id DESC
	`, loanID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load repayments")
		return
	}
	defer rows.Close()

	items := make([]repayment, 0)
	for rows.Next() {
		var item repayment
		if err := rows.Scan(&item.ID, &item.LoanID, &item.InstallmentNo, &item.Kind,
			&item.Amount, &item.Principal, &item.Interest, &item.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode repayments")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read repayments")
		return
	}
	writeJSON(w, http.StatusOK, items)
}
