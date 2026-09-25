package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// dashboard 回傳當前使用者的合約與真實聚合摘要。
func (s *apiServer) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var response dashboardResponse
	response.Summary.CreditScore = actor.CreditScore

	// 借款總額與本月應繳皆只計未結清合約，由資料庫聚合而非寫死常數
	if err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0), COALESCE(SUM(monthly_payment), 0)
		FROM loans
		WHERE user_id = $1 AND status <> '已結清'
	`, actor.ID).Scan(&response.Summary.TotalBorrowed, &response.Summary.MonthlyPayment); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load dashboard summary")
		return
	}

	// 正常還款率 = 準時繳款期數 / 已到期期數
	var paid, overdue int64
	if err := s.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE i.status = 'paid'),
			COUNT(*) FILTER (WHERE i.status = 'overdue')
		FROM loan_installments i
		JOIN loans l ON l.id = i.loan_id
		WHERE l.user_id = $1
	`, actor.ID).Scan(&paid, &overdue); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load repayment stats")
		return
	}
	if settled := paid + overdue; settled > 0 {
		response.Summary.RepaymentRate = float64(paid) / float64(settled) * 100
	} else {
		response.Summary.RepaymentRate = 100
	}

	rows, err := s.db.Query(ctx, `
		SELECT id, product, amount, annual_rate, paid_installments,
		       total_installments, status, status_tone, monthly_payment
		FROM loans
		WHERE user_id = $1
		ORDER BY created_at DESC, id DESC
	`, actor.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load loans")
		return
	}
	defer rows.Close()

	response.Loans = make([]loan, 0)
	for rows.Next() {
		var item loan
		if err := rows.Scan(&item.ID, &item.Product, &item.Amount, &item.Rate,
			&item.PaidInstallments, &item.TotalInstallments, &item.Status,
			&item.StatusTone, &item.MonthlyPayment); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode loans")
			return
		}
		response.Loans = append(response.Loans, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read loans")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type scheduleResponse struct {
	Loan     loan          `json:"loan"`
	Schedule []installment `json:"schedule"`
}

// loanSchedule 回傳指定合約的攤還明細，取代前端硬算的假資料。
// 路由：GET /v1/loans/{id}/schedule（loanID 由 loanRoutes 解析後傳入）
func (s *apiServer) loanSchedule(w http.ResponseWriter, r *http.Request, loanID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var item loan
	// 以 user_id 一併過濾：他人合約回 404 而非 403，不洩漏該 id 是否存在
	err := s.db.QueryRow(ctx, `
		SELECT id, product, amount, annual_rate, paid_installments,
		       total_installments, status, status_tone, monthly_payment
		FROM loans
		WHERE id = $1 AND user_id = $2
	`, loanID, actor.ID).Scan(&item.ID, &item.Product, &item.Amount, &item.Rate,
		&item.PaidInstallments, &item.TotalInstallments, &item.Status,
		&item.StatusTone, &item.MonthlyPayment)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "loan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load loan")
		return
	}

	rows, err := s.db.Query(ctx, `
		SELECT installment_no, due_date, amount_due, principal, interest, remaining_balance, status
		FROM loan_installments
		WHERE loan_id = $1
		ORDER BY installment_no
	`, loanID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load schedule")
		return
	}
	defer rows.Close()

	schedule := make([]installment, 0)
	for rows.Next() {
		var row installment
		var dueDate time.Time
		if err := rows.Scan(&row.Number, &dueDate, &row.AmountDue, &row.Principal,
			&row.Interest, &row.RemainingBalance, &row.Status); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode schedule")
			return
		}
		row.DueDate = dueDate
		schedule = append(schedule, row)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read schedule")
		return
	}

	writeJSON(w, http.StatusOK, scheduleResponse{Loan: item, Schedule: schedule})
}
