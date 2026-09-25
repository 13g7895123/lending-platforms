//go:build integration

package main

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// 完整主流程：送申請 → 風控可見 → 核准 → 合約與攤還表生成 → 借款人可見。
func TestIntegrationApplicationToLoanWorkflow(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)
	createInvestor(t, "workflow-investor@creditflow.test", "出借人", 10000000)

	server := newTestAPIServer(t)
	borrower := newTestClient(t, server)
	reviewer := borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	applicationID := borrower.submitApplication()

	t.Run("borrower sees their own application", func(t *testing.T) {
		mine, _ := borrower.do(http.MethodGet, "/v1/applications", nil).
			expectStatus(t, http.StatusOK, "my applications").decodeApplications(t)
		if len(mine) != 1 || mine[0].ID != applicationID {
			t.Fatalf("borrower sees %d applications, want exactly %s", len(mine), applicationID)
		}
		if mine[0].Status != "pending" {
			t.Errorf("status = %q, want pending", mine[0].Status)
		}
		// 後端應已完成 DBR 與建議試算
		if mine[0].EstimatedPayment == 0 || mine[0].DBR == 0 {
			t.Error("application is missing server-side DBR calculation")
		}
	})

	t.Run("reviewer sees the same application", func(t *testing.T) {
		queue, _ := reviewer.do(http.MethodGet, "/v1/admin/applications", nil).
			expectStatus(t, http.StatusOK, "admin queue").decodeApplications(t)

		found := false
		for _, item := range queue {
			if item.ID == applicationID {
				found = true
				if item.Grade != "A" {
					t.Errorf("grade = %q, want A for a 780 score", item.Grade)
				}
				if item.Recommendation == "" {
					t.Error("recommendation is empty")
				}
			}
		}
		if !found {
			t.Fatalf("application %s is not visible to the reviewer", applicationID)
		}
	})

	t.Run("status filter narrows the queue", func(t *testing.T) {
		pending, _ := reviewer.do(http.MethodGet, "/v1/admin/applications?status=pending", nil).
			expectStatus(t, http.StatusOK, "filtered queue").decodeApplications(t)
		for _, item := range pending {
			if item.Status != "pending" {
				t.Errorf("filter returned status %q", item.Status)
			}
		}

		approved, _ := reviewer.do(http.MethodGet, "/v1/admin/applications?status=approved", nil).
			expectStatus(t, http.StatusOK, "approved filter").decodeApplications(t)
		if len(approved) != 0 {
			t.Errorf("approved filter returned %d rows before any approval", len(approved))
		}
	})

	createdListing := reviewer.approveApplication(applicationID)

	t.Run("approval creates a funding listing, not a loan", func(t *testing.T) {
		if createdListing.Status != "funding" {
			t.Errorf("listing status = %q, want funding", createdListing.Status)
		}
		var loanCount int
		if err := testPool.QueryRow(testContext(t), `SELECT COUNT(*) FROM loans`).Scan(&loanCount); err != nil {
			t.Fatalf("count loans: %v", err)
		}
		if loanCount != 0 {
			t.Errorf("%d loans exist before funding, want 0", loanCount)
		}
	})

	investor := borrower.fork()
	investor.loginAs("workflow-investor@creditflow.test", testInvestorPassword)
	createdLoan := investor.fundListingFully(createdListing.ID, createdListing.TargetAmount)

	t.Run("full funding creates a loan with a full schedule", func(t *testing.T) {
		if createdLoan.TotalInstallments != 36 {
			t.Errorf("totalInstallments = %d, want 36", createdLoan.TotalInstallments)
		}
		if createdLoan.MonthlyPayment <= 0 {
			t.Error("monthlyPayment is not set")
		}

		var installmentCount int
		var principalSum int64
		if err := testPool.QueryRow(testContext(t), `
			SELECT COUNT(*), COALESCE(SUM(principal), 0)
			FROM loan_installments WHERE loan_id = $1
		`, createdLoan.ID).Scan(&installmentCount, &principalSum); err != nil {
			t.Fatalf("read installments: %v", err)
		}
		if installmentCount != 36 {
			t.Errorf("%d installments were created, want 36", installmentCount)
		}
		// 本金加總必須精確等於核貸金額（末期吸收捨入殘差）
		if principalSum != createdLoan.Amount {
			t.Errorf("principal sum = %d, want exactly %d", principalSum, createdLoan.Amount)
		}
	})

	t.Run("approval is recorded in the audit trail", func(t *testing.T) {
		var action, fromStatus, toStatus, reason string
		if err := testPool.QueryRow(testContext(t), `
			SELECT action, from_status, to_status, reason
			FROM application_reviews WHERE application_id = $1
		`, applicationID).Scan(&action, &fromStatus, &toStatus, &reason); err != nil {
			t.Fatalf("read audit trail: %v", err)
		}
		if action != "approve" || fromStatus != "pending" || toStatus != "funding" {
			t.Errorf("audit row = (%s, %s → %s), want (approve, pending → funding)",
				action, fromStatus, toStatus)
		}
		if reason == "" {
			t.Error("audit row has no reason recorded")
		}
	})

	t.Run("loan appears on the borrower dashboard", func(t *testing.T) {
		var dashboard dashboardResponse
		borrower.do(http.MethodGet, "/v1/dashboard", nil).
			expectStatus(t, http.StatusOK, "dashboard").decode(t, &dashboard)

		found := false
		for _, item := range dashboard.Loans {
			if item.ID == createdLoan.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("loan %s is missing from the dashboard", createdLoan.ID)
		}
		if dashboard.Summary.TotalBorrowed != createdLoan.Amount {
			t.Errorf("totalBorrowed = %d, want %d",
				dashboard.Summary.TotalBorrowed, createdLoan.Amount)
		}
		// 月付合計必須是真實聚合，不是舊版寫死的 14982
		if dashboard.Summary.MonthlyPayment != createdLoan.MonthlyPayment {
			t.Errorf("summary monthlyPayment = %d, want %d",
				dashboard.Summary.MonthlyPayment, createdLoan.MonthlyPayment)
		}
	})

	t.Run("re-approving is rejected", func(t *testing.T) {
		reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
			map[string]string{"action": "approve"}).
			expectStatus(t, http.StatusConflict, "duplicate approval")

		// 不得因重複請求而產生第二份合約
		var loanCount int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM loans WHERE application_id = $1`, applicationID).Scan(&loanCount); err != nil {
			t.Fatalf("count loans: %v", err)
		}
		if loanCount != 1 {
			t.Errorf("%d loans exist for one application, want 1", loanCount)
		}
	})
}

func TestIntegrationReviewRejectionPaths(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	borrower := newTestClient(t, server)
	reviewer := borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	t.Run("request_more_info is not terminal", func(t *testing.T) {
		applicationID := borrower.submitApplication()
		reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
			map[string]string{"action": "request_more_info", "reason": "請補營業稅單"}).
			expectStatus(t, http.StatusOK, "request more info")

		// 補件後仍可做最終決定
		reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
			map[string]string{"action": "reject", "reason": "逾期未補件"}).
			expectStatus(t, http.StatusOK, "reject after more info")
	})

	t.Run("rejection creates no loan", func(t *testing.T) {
		applicationID := borrower.submitApplication()
		reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
			map[string]string{"action": "reject", "reason": "DBR 超標"}).
			expectStatus(t, http.StatusOK, "reject")

		var loanCount int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM loans WHERE application_id = $1`, applicationID).Scan(&loanCount); err != nil {
			t.Fatalf("count loans: %v", err)
		}
		if loanCount != 0 {
			t.Errorf("rejection created %d loans, want 0", loanCount)
		}
	})

	t.Run("rejected application cannot be approved later", func(t *testing.T) {
		applicationID := borrower.submitApplication()
		reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
			map[string]string{"action": "reject", "reason": "婉拒"}).
			expectStatus(t, http.StatusOK, "reject")
		reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
			map[string]string{"action": "approve"}).
			expectStatus(t, http.StatusConflict, "approve after reject")
	})

	t.Run("invalid action is rejected", func(t *testing.T) {
		applicationID := borrower.submitApplication()
		reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
			map[string]string{"action": "definitely-not-valid"}).
			expectStatus(t, http.StatusUnprocessableEntity, "invalid action")
	})

	t.Run("unknown application returns 404", func(t *testing.T) {
		reviewer.do(http.MethodPatch, "/v1/admin/applications/LN-0000-000000",
			map[string]string{"action": "approve"}).
			expectStatus(t, http.StatusNotFound, "unknown application")
	})
}

// 孤兒申請（user_id 為 NULL，認證機制上線前建立）不得核准成合約，
// 也不可讓後台清單整份失敗。
func TestIntegrationOrphanApplicationHandling(t *testing.T) {
	resetDatabase(t)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	if _, err := testPool.Exec(testContext(t), `
		INSERT INTO applications (
			id, user_id, product, amount, term_months, purpose, applicant_name,
			id_number, phone, email, annual_income, monthly_expenses, status
		) VALUES ('LN-ORPHAN-001', NULL, '信貸', 500000, 36, '整合', '無主申請',
		          '', '', 'orphan@creditflow.test', 1000000, 20000, 'pending')
	`); err != nil {
		t.Fatalf("insert orphan application: %v", err)
	}

	reviewer := newTestClient(t, newTestAPIServer(t))
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	t.Run("orphan rows do not break the admin queue", func(t *testing.T) {
		queue, _ := reviewer.do(http.MethodGet, "/v1/admin/applications", nil).
			expectStatus(t, http.StatusOK, "admin queue with orphan row").decodeApplications(t)

		found := false
		for _, item := range queue {
			if item.ID == "LN-ORPHAN-001" {
				found = true
				if item.UserID != "" {
					t.Errorf("orphan UserID = %q, want empty", item.UserID)
				}
			}
		}
		if !found {
			t.Error("orphan application is missing from the queue")
		}
	})

	t.Run("orphan cannot be approved", func(t *testing.T) {
		reviewer.do(http.MethodPatch, "/v1/admin/applications/LN-ORPHAN-001",
			map[string]string{"action": "approve"}).
			expectStatus(t, http.StatusConflict, "approve orphan")
	})

	t.Run("orphan can still be rejected", func(t *testing.T) {
		reviewer.do(http.MethodPatch, "/v1/admin/applications/LN-ORPHAN-001",
			map[string]string{"action": "reject", "reason": "請以帳號重新送出"}).
			expectStatus(t, http.StatusOK, "reject orphan")
	})
}

// 併發核准同一筆申請：FOR UPDATE 必須確保只有一次成功、只生成一份合約。
func TestIntegrationConcurrentApprovalCreatesOneListing(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	borrower := newTestClient(t, server)
	borrower.loginAs("borrower@creditflow.test", testPassword)
	applicationID := borrower.submitApplication()

	const attempts = 5
	clients := make([]*testClient, attempts)
	for i := range clients {
		clients[i] = borrower.fork()
		clients[i].loginAs("reviewer@creditflow.test", testReviewerPassword)
	}

	var waitGroup sync.WaitGroup
	statuses := make([]int, attempts)
	for i := 0; i < attempts; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			statuses[index] = clients[index].do(http.MethodPatch,
				"/v1/admin/applications/"+applicationID,
				map[string]string{"action": "approve", "reason": "concurrent"}).StatusCode
		}(i)
	}
	waitGroup.Wait()

	successes := 0
	for index, status := range statuses {
		switch status {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			// 預期：其餘請求看到已核准狀態
		default:
			t.Errorf("attempt %d got unexpected HTTP %d", index, status)
		}
	}
	if successes != 1 {
		t.Errorf("%d concurrent approvals succeeded, want exactly 1", successes)
	}

	// 核准只上架標的；一筆申請只能有一個標的（唯一索引保證）
	var listingCount int
	if err := testPool.QueryRow(testContext(t),
		`SELECT COUNT(*) FROM listings WHERE application_id = $1`, applicationID).Scan(&listingCount); err != nil {
		t.Fatalf("count listings: %v", err)
	}
	if listingCount != 1 {
		t.Fatalf("%d listings were created for one application, want 1", listingCount)
	}

	// 尚未募資，故不該有任何合約
	var loanCount int
	if err := testPool.QueryRow(testContext(t),
		`SELECT COUNT(*) FROM loans WHERE application_id = $1`, applicationID).Scan(&loanCount); err != nil {
		t.Fatalf("count loans: %v", err)
	}
	if loanCount != 0 {
		t.Errorf("%d loans exist before funding, want 0", loanCount)
	}
}

// 跨使用者隔離：他人的合約與申請都不可見。
func TestIntegrationCrossUserIsolation(t *testing.T) {
	resetDatabase(t)
	createUser(t, "first@creditflow.test", "使用者一", roleBorrower, 780)
	createUser(t, "second@creditflow.test", "使用者二", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)
	createInvestor(t, "isolation-investor@creditflow.test", "出借人", 10000000)

	server := newTestAPIServer(t)
	first := newTestClient(t, server)
	second := first.fork()
	reviewer := first.fork()
	first.loginAs("first@creditflow.test", testPassword)
	second.loginAs("second@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	createdLoan := approveAndDisburse(t, first, reviewer, "isolation-investor@creditflow.test")

	t.Run("other borrower sees no applications", func(t *testing.T) {
		theirs, _ := second.do(http.MethodGet, "/v1/applications", nil).
			expectStatus(t, http.StatusOK, "second user applications").decodeApplications(t)
		if len(theirs) != 0 {
			t.Errorf("second user sees %d applications, want 0", len(theirs))
		}
	})

	t.Run("other borrower sees no loans", func(t *testing.T) {
		var dashboard dashboardResponse
		second.do(http.MethodGet, "/v1/dashboard", nil).
			expectStatus(t, http.StatusOK, "second user dashboard").decode(t, &dashboard)
		if len(dashboard.Loans) != 0 {
			t.Errorf("second user sees %d loans, want 0", len(dashboard.Loans))
		}
		if dashboard.Summary.TotalBorrowed != 0 {
			t.Errorf("second user totalBorrowed = %d, want 0", dashboard.Summary.TotalBorrowed)
		}
	})

	// 回 404 而非 403：不洩漏該 id 是否存在
	t.Run("other loan returns 404 not 403", func(t *testing.T) {
		paths := []string{
			fmt.Sprintf("/v1/loans/%s/schedule", createdLoan.ID),
			fmt.Sprintf("/v1/loans/%s/repayments", createdLoan.ID),
		}
		for _, path := range paths {
			second.do(http.MethodGet, path, nil).
				expectStatus(t, http.StatusNotFound, path)
		}
	})

	t.Run("other user cannot pay someone elses installment", func(t *testing.T) {
		second.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
			expectStatus(t, http.StatusNotFound, "pay another user's installment")
	})

	t.Run("other user cannot settle someone elses loan", func(t *testing.T) {
		second.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/settle", createdLoan.ID), nil).
			expectStatus(t, http.StatusNotFound, "settle another user's loan")
	})

	// reviewer 雖有後台權限，但合約端點仍以 user_id 過濾
	t.Run("reviewer cannot read borrower loan details", func(t *testing.T) {
		reviewer.do(http.MethodGet,
			fmt.Sprintf("/v1/loans/%s/schedule", createdLoan.ID), nil).
			expectStatus(t, http.StatusNotFound, "reviewer reading loan schedule")
	})
}
