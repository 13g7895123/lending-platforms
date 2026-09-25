//go:build integration

package main

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// approvedLoanFixture 建立一份已撥款的合約，回傳借款人 client 與合約。
//
// 撥款需要走完募資流程：核准上架 → 出借人投滿 → 自動撥款。
func approvedLoanFixture(t *testing.T) (*testClient, loan) {
	t.Helper()
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)
	createInvestor(t, "fixture-investor@creditflow.test", "出借人", 10000000)

	server := newTestAPIServer(t)
	borrower := newTestClient(t, server)
	reviewer := borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	return borrower, approveAndDisburse(t, borrower, reviewer, "fixture-investor@creditflow.test")
}

func TestIntegrationPayInstallment(t *testing.T) {
	borrower, createdLoan := approvedLoanFixture(t)

	var initial scheduleResponse
	borrower.do(http.MethodGet, "/v1/loans/"+createdLoan.ID+"/schedule", nil).
		expectStatus(t, http.StatusOK, "schedule").decode(t, &initial)

	if initial.Schedule[0].Status != "due" {
		t.Fatalf("first installment status = %q, want due", initial.Schedule[0].Status)
	}

	t.Run("future installment cannot be paid", func(t *testing.T) {
		borrower.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/installments/2/pay", createdLoan.ID), nil).
			expectStatus(t, http.StatusUnprocessableEntity, "pay future installment")
	})

	t.Run("due installment is paid and progress advances", func(t *testing.T) {
		var payload payResponse
		borrower.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
			expectStatus(t, http.StatusCreated, "pay first installment").decode(t, &payload)

		if payload.Loan.PaidInstallments != 1 {
			t.Errorf("paidInstallments = %d, want 1", payload.Loan.PaidInstallments)
		}
		if payload.Settled {
			t.Error("loan reported as settled after a single payment")
		}
		if payload.Repayment.Amount != initial.Schedule[0].AmountDue {
			t.Errorf("recorded amount = %d, want %d",
				payload.Repayment.Amount, initial.Schedule[0].AmountDue)
		}
	})

	t.Run("next installment becomes due", func(t *testing.T) {
		var after scheduleResponse
		borrower.do(http.MethodGet, "/v1/loans/"+createdLoan.ID+"/schedule", nil).
			expectStatus(t, http.StatusOK, "schedule after payment").decode(t, &after)

		if after.Schedule[0].Status != "paid" {
			t.Errorf("installment 1 status = %q, want paid", after.Schedule[0].Status)
		}
		if after.Schedule[1].Status != "due" {
			t.Errorf("installment 2 status = %q, want due", after.Schedule[1].Status)
		}

		dueCount := 0
		for _, row := range after.Schedule {
			if row.Status == "due" {
				dueCount++
			}
		}
		if dueCount != 1 {
			t.Errorf("%d installments are due, want exactly 1", dueCount)
		}
	})

	t.Run("repayment is recorded once", func(t *testing.T) {
		var records []repayment
		borrower.do(http.MethodGet, "/v1/loans/"+createdLoan.ID+"/repayments", nil).
			expectStatus(t, http.StatusOK, "repayments").decode(t, &records)
		if len(records) != 1 {
			t.Fatalf("%d repayment records, want 1", len(records))
		}
		if records[0].Kind != "installment" {
			t.Errorf("kind = %q, want installment", records[0].Kind)
		}
	})

	t.Run("paying the same installment again is rejected", func(t *testing.T) {
		borrower.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
			expectStatus(t, http.StatusConflict, "duplicate payment")

		var count int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM repayments WHERE loan_id = $1`, createdLoan.ID).Scan(&count); err != nil {
			t.Fatalf("count repayments: %v", err)
		}
		if count != 1 {
			t.Errorf("%d repayments exist after a duplicate request, want 1", count)
		}
	})

	t.Run("unknown installment returns 404", func(t *testing.T) {
		borrower.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/installments/999/pay", createdLoan.ID), nil).
			expectStatus(t, http.StatusNotFound, "unknown installment")
	})
}

// 併發繳同一期：冪等鍵的唯一約束必須確保只落一次帳。
// 這是還款引擎最關鍵的正確性保證 —— 只靠讀狀態再判斷在併發下會漏。
func TestIntegrationConcurrentPaymentIsIdempotent(t *testing.T) {
	borrower, createdLoan := approvedLoanFixture(t)

	const attempts = 8
	clients := make([]*testClient, attempts)
	for i := range clients {
		clients[i] = borrower.fork()
		clients[i].loginAs("borrower@creditflow.test", testPassword)
	}

	var waitGroup sync.WaitGroup
	statuses := make([]int, attempts)
	path := fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID)
	for i := 0; i < attempts; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			statuses[index] = clients[index].do(http.MethodPost, path, nil).StatusCode
		}(i)
	}
	waitGroup.Wait()

	created := 0
	for index, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			// 預期：重複請求被冪等鍵擋下
		default:
			t.Errorf("attempt %d got unexpected HTTP %d", index, status)
		}
	}
	if created != 1 {
		t.Errorf("%d concurrent payments succeeded, want exactly 1", created)
	}

	var count int
	var total int64
	if err := testPool.QueryRow(testContext(t), `
		SELECT COUNT(*), COALESCE(SUM(amount), 0) FROM repayments WHERE loan_id = $1
	`, createdLoan.ID).Scan(&count, &total); err != nil {
		t.Fatalf("count repayments: %v", err)
	}
	if count != 1 {
		t.Fatalf("%d repayment rows were written, want 1 (double charge!)", count)
	}

	var paidInstallments int
	if err := testPool.QueryRow(testContext(t),
		`SELECT paid_installments FROM loans WHERE id = $1`, createdLoan.ID).Scan(&paidInstallments); err != nil {
		t.Fatalf("read loan progress: %v", err)
	}
	if paidInstallments != 1 {
		t.Errorf("paid_installments = %d, want 1", paidInstallments)
	}
}

func TestIntegrationPayAllInstallmentsSettlesLoan(t *testing.T) {
	borrower, createdLoan := approvedLoanFixture(t)

	// 讓所有期數到期，便於逐期繳完
	if _, err := testPool.Exec(testContext(t), `
		UPDATE loan_installments SET due_date = CURRENT_DATE, status = 'due' WHERE loan_id = $1
	`, createdLoan.ID); err != nil {
		t.Fatalf("mark all installments due: %v", err)
	}

	var lastPayload payResponse
	for number := 1; number <= createdLoan.TotalInstallments; number++ {
		response := borrower.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/installments/%d/pay", createdLoan.ID, number), nil)
		response.expectStatus(t, http.StatusCreated, fmt.Sprintf("pay installment %d", number))
		response.decode(t, &lastPayload)
	}

	if !lastPayload.Settled {
		t.Error("loan was not reported as settled after the final payment")
	}
	if lastPayload.Loan.Status != "已結清" {
		t.Errorf("loan status = %q, want 已結清", lastPayload.Loan.Status)
	}
	if lastPayload.Loan.PaidInstallments != createdLoan.TotalInstallments {
		t.Errorf("paidInstallments = %d, want %d",
			lastPayload.Loan.PaidInstallments, createdLoan.TotalInstallments)
	}

	var settledAt *string
	if err := testPool.QueryRow(testContext(t),
		`SELECT settled_at::text FROM loans WHERE id = $1`, createdLoan.ID).Scan(&settledAt); err != nil {
		t.Fatalf("read settled_at: %v", err)
	}
	if settledAt == nil {
		t.Error("settled_at was not written when the loan closed")
	}

	// 結清後不可再繳
	borrower.do(http.MethodPost,
		fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
		expectStatus(t, http.StatusConflict, "pay after settlement")
}

func TestIntegrationSettleLoanEarly(t *testing.T) {
	borrower, createdLoan := approvedLoanFixture(t)

	// 先繳一期，讓清償只涵蓋剩餘期數
	borrower.do(http.MethodPost,
		fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
		expectStatus(t, http.StatusCreated, "pay first installment")

	var before scheduleResponse
	borrower.do(http.MethodGet, "/v1/loans/"+createdLoan.ID+"/schedule", nil).
		expectStatus(t, http.StatusOK, "schedule").decode(t, &before)

	var expectedPrincipal, expectedWaived int64
	unpaidCount := 0
	for _, row := range before.Schedule {
		if row.Status != "paid" {
			expectedPrincipal += row.Principal
			expectedWaived += row.Interest
			unpaidCount++
		}
	}

	var payload struct {
		Loan           loan  `json:"loan"`
		Settled        bool  `json:"settled"`
		WaivedInterest int64 `json:"waivedInterest"`
		ClosedCount    int   `json:"closedCount"`
		Repayment      struct {
			Kind      string `json:"kind"`
			Amount    int64  `json:"amount"`
			Principal int64  `json:"principal"`
		} `json:"repayment"`
	}
	borrower.do(http.MethodPost, "/v1/loans/"+createdLoan.ID+"/settle", nil).
		expectStatus(t, http.StatusCreated, "settle").decode(t, &payload)

	if !payload.Settled || payload.Loan.Status != "已結清" {
		t.Errorf("settle returned settled=%v status=%q", payload.Settled, payload.Loan.Status)
	}
	if payload.ClosedCount != unpaidCount {
		t.Errorf("closedCount = %d, want %d", payload.ClosedCount, unpaidCount)
	}
	// 只收剩餘本金，未到期利息免除
	if payload.Repayment.Amount != expectedPrincipal {
		t.Errorf("settlement amount = %d, want %d (remaining principal only)",
			payload.Repayment.Amount, expectedPrincipal)
	}
	if payload.WaivedInterest != expectedWaived {
		t.Errorf("waivedInterest = %d, want %d", payload.WaivedInterest, expectedWaived)
	}
	if payload.Repayment.Kind != "prepayment" {
		t.Errorf("kind = %q, want prepayment", payload.Repayment.Kind)
	}

	t.Run("no installments remain unpaid", func(t *testing.T) {
		var unpaid int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM loan_installments WHERE loan_id = $1 AND status <> 'paid'`,
			createdLoan.ID).Scan(&unpaid); err != nil {
			t.Fatalf("count unpaid: %v", err)
		}
		if unpaid != 0 {
			t.Errorf("%d installments remain unpaid after settlement", unpaid)
		}
	})

	t.Run("settling twice is rejected", func(t *testing.T) {
		borrower.do(http.MethodPost, "/v1/loans/"+createdLoan.ID+"/settle", nil).
			expectStatus(t, http.StatusConflict, "duplicate settle")

		var prepayments int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM repayments WHERE loan_id = $1 AND kind = 'prepayment'`,
			createdLoan.ID).Scan(&prepayments); err != nil {
			t.Fatalf("count prepayments: %v", err)
		}
		if prepayments != 1 {
			t.Errorf("%d prepayment rows exist, want 1", prepayments)
		}
	})
}

func TestIntegrationOverdueDetectionAndCollection(t *testing.T) {
	borrower, createdLoan := approvedLoanFixture(t)
	reviewer := borrower.fork()
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	// 把第一期推到 45 天前 → 應判定為 M2
	if _, err := testPool.Exec(testContext(t), `
		UPDATE loan_installments SET due_date = CURRENT_DATE - 45
		WHERE loan_id = $1 AND installment_no = 1
	`, createdLoan.ID); err != nil {
		t.Fatalf("backdate installment: %v", err)
	}

	var overdue []overdueItem
	reviewer.do(http.MethodGet, "/v1/admin/overdue", nil).
		expectStatus(t, http.StatusOK, "overdue list").decode(t, &overdue)

	var target *overdueItem
	for i := range overdue {
		if overdue[i].LoanID == createdLoan.ID && overdue[i].InstallmentNo == 1 {
			target = &overdue[i]
		}
	}
	if target == nil {
		t.Fatal("backdated installment is missing from the collection list")
	}
	if target.OverdueDays != 45 {
		t.Errorf("overdueDays = %d, want 45", target.OverdueDays)
	}
	if target.Stage != "M2" {
		t.Errorf("stage = %q, want M2 for 45 days", target.Stage)
	}
	if target.Action == "" || target.Action == "—" {
		t.Errorf("action = %q, want a collection recommendation", target.Action)
	}
	if target.BorrowerName == "" {
		t.Error("borrowerName is empty")
	}

	t.Run("loan status reflects the overdue installment", func(t *testing.T) {
		var status string
		if err := testPool.QueryRow(testContext(t),
			`SELECT status FROM loans WHERE id = $1`, createdLoan.ID).Scan(&status); err != nil {
			t.Fatalf("read loan status: %v", err)
		}
		if status != "逾期" {
			t.Errorf("loan status = %q, want 逾期", status)
		}
	})

	t.Run("overdue installment can be paid", func(t *testing.T) {
		borrower.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
			expectStatus(t, http.StatusCreated, "pay overdue installment")
	})

	t.Run("paid installment leaves the collection list", func(t *testing.T) {
		var after []overdueItem
		reviewer.do(http.MethodGet, "/v1/admin/overdue", nil).
			expectStatus(t, http.StatusOK, "overdue list after payment").decode(t, &after)
		for _, row := range after {
			if row.LoanID == createdLoan.ID && row.InstallmentNo == 1 {
				t.Error("paid installment is still in the collection list")
			}
		}
	})

	t.Run("loan status recovers after clearing arrears", func(t *testing.T) {
		var status string
		if err := testPool.QueryRow(testContext(t),
			`SELECT status FROM loans WHERE id = $1`, createdLoan.ID).Scan(&status); err != nil {
			t.Fatalf("read loan status: %v", err)
		}
		if status != "正常繳款" {
			t.Errorf("loan status = %q, want 正常繳款 after arrears are cleared", status)
		}
	})
}

// 逾期掃描必須 idempotent：重複執行不可讓 overdue_days 累加或狀態亂跳。
func TestIntegrationOverdueSweepIsIdempotent(t *testing.T) {
	_, createdLoan := approvedLoanFixture(t)

	if _, err := testPool.Exec(testContext(t), `
		UPDATE loan_installments SET due_date = CURRENT_DATE - 10
		WHERE loan_id = $1 AND installment_no = 1
	`, createdLoan.ID); err != nil {
		t.Fatalf("backdate installment: %v", err)
	}

	ctx := testContext(t)
	for round := 1; round <= 3; round++ {
		if _, err := markOverdueInstallments(ctx, testPool); err != nil {
			t.Fatalf("sweep round %d: %v", round, err)
		}
		if err := syncOverdueLoanStatus(ctx, testPool); err != nil {
			t.Fatalf("sync round %d: %v", round, err)
		}

		var days int
		var status string
		if err := testPool.QueryRow(ctx, `
			SELECT overdue_days, status FROM loan_installments
			WHERE loan_id = $1 AND installment_no = 1
		`, createdLoan.ID).Scan(&days, &status); err != nil {
			t.Fatalf("read installment: %v", err)
		}
		if days != 10 {
			t.Errorf("round %d: overdue_days = %d, want 10 (not accumulating)", round, days)
		}
		if status != "overdue" {
			t.Errorf("round %d: status = %q, want overdue", round, status)
		}
	}
}

// 還款率必須由已到期期數真實計算。
func TestIntegrationRepaymentRateReflectsHistory(t *testing.T) {
	borrower, createdLoan := approvedLoanFixture(t)

	t.Run("no settled installments yields 100 percent", func(t *testing.T) {
		var dashboard dashboardResponse
		borrower.do(http.MethodGet, "/v1/dashboard", nil).
			expectStatus(t, http.StatusOK, "dashboard").decode(t, &dashboard)
		if dashboard.Summary.RepaymentRate != 100 {
			t.Errorf("repaymentRate = %v, want 100 when nothing is due yet",
				dashboard.Summary.RepaymentRate)
		}
	})

	t.Run("an overdue installment lowers the rate", func(t *testing.T) {
		if _, err := testPool.Exec(testContext(t), `
			UPDATE loan_installments SET due_date = CURRENT_DATE - 5
			WHERE loan_id = $1 AND installment_no = 2
		`, createdLoan.ID); err != nil {
			t.Fatalf("backdate installment: %v", err)
		}
		// 先繳第 1 期，再讓第 2 期逾期 → 1 paid / 1 overdue = 50%
		borrower.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
			expectStatus(t, http.StatusCreated, "pay first installment")
		if _, err := markOverdueInstallments(testContext(t), testPool); err != nil {
			t.Fatalf("sweep: %v", err)
		}

		var dashboard dashboardResponse
		borrower.do(http.MethodGet, "/v1/dashboard", nil).
			expectStatus(t, http.StatusOK, "dashboard").decode(t, &dashboard)
		if dashboard.Summary.RepaymentRate != 50 {
			t.Errorf("repaymentRate = %v, want 50 (1 paid / 1 overdue)",
				dashboard.Summary.RepaymentRate)
		}
	})
}
