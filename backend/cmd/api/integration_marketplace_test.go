//go:build integration

package main

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

const testInvestorPassword = "investor-password-1234"

// createInvestor 建立出借人並預先入金。
func createInvestor(t *testing.T, email, name string, balance int64) string {
	t.Helper()
	hash, err := hashPassword(testInvestorPassword)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	userID := fmt.Sprintf("usr_test_%s", sanitizeForID(email))
	if _, err := testPool.Exec(testContext(t), `
		INSERT INTO users (id, email, password_hash, display_name, role, credit_score, available_balance)
		VALUES ($1, $2, $3, $4, 'investor', 700, $5)
		ON CONFLICT (id) DO UPDATE SET
			password_hash = EXCLUDED.password_hash, role = 'investor',
			available_balance = EXCLUDED.available_balance
	`, userID, email, hash, name, balance); err != nil {
		t.Fatalf("create investor: %v", err)
	}
	return userID
}

// listingFixture 建立一筆已核准並上架募資的標的。
func listingFixture(t *testing.T) (borrower *testClient, reviewer *testClient, listingID string, target int64) {
	t.Helper()
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	borrower = newTestClient(t, server)
	reviewer = borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	applicationID := borrower.submitApplication()

	var payload struct {
		Status  string `json:"status"`
		Listing struct {
			ID           string `json:"id"`
			TargetAmount int64  `json:"targetAmount"`
			Status       string `json:"status"`
		} `json:"listing"`
		Loan *loan `json:"loan"`
	}
	reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
		map[string]string{"action": "approve", "reason": "integration"}).
		expectStatus(t, http.StatusOK, "approve").decode(t, &payload)

	// 核准不再直接發約
	if payload.Loan != nil {
		t.Fatal("approval returned a loan; funding must happen before disbursement")
	}
	if payload.Listing.ID == "" {
		t.Fatal("approval did not create a funding listing")
	}
	if payload.Status != "funding" {
		t.Errorf("status after approval = %q, want funding", payload.Status)
	}
	return borrower, reviewer, payload.Listing.ID, payload.Listing.TargetAmount
}

func TestIntegrationApprovalCreatesListingNotLoan(t *testing.T) {
	borrower, _, listingID, target := listingFixture(t)

	t.Run("no loan exists yet", func(t *testing.T) {
		var loanCount int
		if err := testPool.QueryRow(testContext(t), `SELECT COUNT(*) FROM loans`).Scan(&loanCount); err != nil {
			t.Fatalf("count loans: %v", err)
		}
		if loanCount != 0 {
			t.Errorf("%d loans exist before funding completed, want 0", loanCount)
		}
	})

	t.Run("no installments exist yet", func(t *testing.T) {
		var count int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM loan_installments`).Scan(&count); err != nil {
			t.Fatalf("count installments: %v", err)
		}
		if count != 0 {
			t.Errorf("%d installments exist before disbursement, want 0", count)
		}
	})

	t.Run("listing is open for funding", func(t *testing.T) {
		var status string
		var funded int64
		if err := testPool.QueryRow(testContext(t),
			`SELECT status, funded_amount FROM listings WHERE id = $1`, listingID).
			Scan(&status, &funded); err != nil {
			t.Fatalf("read listing: %v", err)
		}
		if status != "funding" {
			t.Errorf("listing status = %q, want funding", status)
		}
		if funded != 0 {
			t.Errorf("funded_amount = %d, want 0", funded)
		}
		if target <= 0 {
			t.Errorf("target_amount = %d, want positive", target)
		}
	})

	t.Run("borrower sees funding progress", func(t *testing.T) {
		items, _ := borrower.do(http.MethodGet, "/v1/applications", nil).
			expectStatus(t, http.StatusOK, "my applications").decodeApplications(t)
		if len(items) == 0 {
			t.Fatal("no applications returned")
		}
		if items[0].Status != "funding" {
			t.Errorf("application status = %q, want funding", items[0].Status)
		}
		if items[0].ListingID != listingID {
			t.Errorf("listingId = %q, want %q", items[0].ListingID, listingID)
		}
		if items[0].FundedPercent != 0 {
			t.Errorf("fundedPercent = %d, want 0", items[0].FundedPercent)
		}
	})
}

func TestIntegrationInvestmentFlow(t *testing.T) {
	borrower, _, listingID, target := listingFixture(t)
	createInvestor(t, "investor@creditflow.test", "出借人", 2000000)

	investor := borrower.fork()
	investor.loginAs("investor@creditflow.test", testInvestorPassword)

	t.Run("listing appears in the public marketplace", func(t *testing.T) {
		var items []listingItem
		investor.do(http.MethodGet, "/v1/listings", nil).
			expectStatus(t, http.StatusOK, "listings").decode(t, &items)

		found := false
		for _, item := range items {
			if item.ID == listingID {
				found = true
				if item.RemainingAmount != target {
					t.Errorf("remainingAmount = %d, want %d", item.RemainingAmount, target)
				}
				if item.MonthlyPayment <= 0 {
					t.Error("monthlyPayment was not calculated")
				}
			}
		}
		if !found {
			t.Fatalf("listing %s is not in the marketplace", listingID)
		}
	})

	t.Run("partial investment updates progress", func(t *testing.T) {
		var payload investResponse
		investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": 100000, "requestId": "first"}).
			expectStatus(t, http.StatusCreated, "invest").decode(t, &payload)

		if payload.Investment.Amount != 100000 {
			t.Errorf("invested amount = %d, want 100000", payload.Investment.Amount)
		}
		if payload.FullyFunded {
			t.Error("listing reported as fully funded after a partial investment")
		}
		if payload.DisbursedLoa != nil {
			t.Error("a loan was disbursed before the listing was fully funded")
		}
		if payload.Balance != 1900000 {
			t.Errorf("balance after investing = %d, want 1900000", payload.Balance)
		}
		if payload.Investment.EstimatedReturn <= 0 {
			t.Error("estimatedReturn was not calculated")
		}
	})

	t.Run("balance is debited in the database", func(t *testing.T) {
		var balance int64
		if err := testPool.QueryRow(testContext(t),
			`SELECT available_balance FROM users WHERE email = $1`,
			"investor@creditflow.test").Scan(&balance); err != nil {
			t.Fatalf("read balance: %v", err)
		}
		if balance != 1900000 {
			t.Errorf("stored balance = %d, want 1900000", balance)
		}
	})

	t.Run("duplicate requestId is rejected", func(t *testing.T) {
		investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": 100000, "requestId": "first"}).
			expectStatus(t, http.StatusConflict, "duplicate investment")

		var count int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM investments WHERE listing_id = $1`, listingID).Scan(&count); err != nil {
			t.Fatalf("count investments: %v", err)
		}
		if count != 1 {
			t.Errorf("%d investments recorded after a duplicate request, want 1", count)
		}
	})

	t.Run("amount below the minimum is rejected", func(t *testing.T) {
		investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": 500, "requestId": "too-small"}).
			expectStatus(t, http.StatusUnprocessableEntity, "below minimum")
	})

	t.Run("borrower cannot invest in their own listing", func(t *testing.T) {
		// borrower 角色本身就無權存取投標端點
		borrower.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": 10000, "requestId": "self"}).
			expectStatus(t, http.StatusForbidden, "borrower investing")
	})

	t.Run("funding it fully disburses the loan", func(t *testing.T) {
		remaining := target - 100000
		var payload investResponse
		investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": remaining, "requestId": "final"}).
			expectStatus(t, http.StatusCreated, "final investment").decode(t, &payload)

		if !payload.FullyFunded {
			t.Fatal("listing was not reported as fully funded")
		}
		if payload.DisbursedLoa == nil {
			t.Fatal("no loan was disbursed after full funding")
		}
		if payload.DisbursedLoa.Amount != target {
			t.Errorf("loan amount = %d, want %d", payload.DisbursedLoa.Amount, target)
		}
	})

	t.Run("loan and schedule now exist", func(t *testing.T) {
		var loanID string
		var listingRef *string
		if err := testPool.QueryRow(testContext(t),
			`SELECT id, listing_id FROM loans`).Scan(&loanID, &listingRef); err != nil {
			t.Fatalf("read loan: %v", err)
		}
		if listingRef == nil || *listingRef != listingID {
			t.Errorf("loan.listing_id = %v, want %s", listingRef, listingID)
		}

		var installments int
		var principalSum int64
		if err := testPool.QueryRow(testContext(t), `
			SELECT COUNT(*), COALESCE(SUM(principal), 0)
			FROM loan_installments WHERE loan_id = $1
		`, loanID).Scan(&installments, &principalSum); err != nil {
			t.Fatalf("read installments: %v", err)
		}
		if installments != 36 {
			t.Errorf("%d installments, want 36", installments)
		}
		if principalSum != target {
			t.Errorf("principal sum = %d, want %d", principalSum, target)
		}
	})

	t.Run("listing and application reach disbursed", func(t *testing.T) {
		var listingStatus, applicationStatus string
		if err := testPool.QueryRow(testContext(t), `
			SELECT l.status, a.status FROM listings l
			JOIN applications a ON a.id = l.application_id
			WHERE l.id = $1
		`, listingID).Scan(&listingStatus, &applicationStatus); err != nil {
			t.Fatalf("read statuses: %v", err)
		}
		if listingStatus != "disbursed" {
			t.Errorf("listing status = %q, want disbursed", listingStatus)
		}
		if applicationStatus != "disbursed" {
			t.Errorf("application status = %q, want disbursed", applicationStatus)
		}
	})

	t.Run("borrower now sees the loan", func(t *testing.T) {
		var dashboard dashboardResponse
		borrower.do(http.MethodGet, "/v1/dashboard", nil).
			expectStatus(t, http.StatusOK, "dashboard").decode(t, &dashboard)
		if len(dashboard.Loans) != 1 {
			t.Fatalf("borrower sees %d loans, want 1", len(dashboard.Loans))
		}
	})

	t.Run("further investment is rejected", func(t *testing.T) {
		investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": 10000, "requestId": "after-close"}).
			expectStatus(t, http.StatusConflict, "investing in a closed listing")
	})
}

// 超額投標只接受剩餘額度，不可超募。
func TestIntegrationOversubscriptionIsTrimmed(t *testing.T) {
	borrower, _, listingID, target := listingFixture(t)
	createInvestor(t, "investor@creditflow.test", "出借人", 5000000)

	investor := borrower.fork()
	investor.loginAs("investor@creditflow.test", testInvestorPassword)

	// 先投一半
	half := target / 2
	investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
		map[string]any{"amount": half, "requestId": "half"}).
		expectStatus(t, http.StatusCreated, "half investment")

	// 再投超過剩餘額度：應只接受剩餘部分
	var payload investResponse
	investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
		map[string]any{"amount": target, "requestId": "oversized"}).
		expectStatus(t, http.StatusCreated, "oversized investment").decode(t, &payload)

	expected := target - half
	if payload.Investment.Amount != expected {
		t.Errorf("accepted amount = %d, want %d (trimmed to remaining)",
			payload.Investment.Amount, expected)
	}

	var funded int64
	if err := testPool.QueryRow(testContext(t),
		`SELECT funded_amount FROM listings WHERE id = $1`, listingID).Scan(&funded); err != nil {
		t.Fatalf("read funded amount: %v", err)
	}
	if funded != target {
		t.Errorf("funded_amount = %d, want exactly %d (no oversubscription)", funded, target)
	}
}

// 併發投標：總募集金額不可超過目標，且只能撥款一次。
// 這是撮合引擎最關鍵的正確性保證。
func TestIntegrationConcurrentInvestmentsDoNotOversubscribe(t *testing.T) {
	borrower, _, listingID, target := listingFixture(t)

	const investorCount = 8
	// 每人投標額都夠大，合計遠超目標，逼出競爭條件
	perInvestor := target/3 + 1000

	clients := make([]*testClient, investorCount)
	for i := 0; i < investorCount; i++ {
		email := fmt.Sprintf("investor%d@creditflow.test", i)
		createInvestor(t, email, fmt.Sprintf("出借人%d", i), target*2)
		clients[i] = borrower.fork()
		clients[i].loginAs(email, testInvestorPassword)
	}

	var waitGroup sync.WaitGroup
	statuses := make([]int, investorCount)
	for i := 0; i < investorCount; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			statuses[index] = clients[index].do(http.MethodPost,
				"/v1/listings/"+listingID+"/invest",
				map[string]any{"amount": perInvestor, "requestId": fmt.Sprintf("concurrent-%d", index)},
			).StatusCode
		}(i)
	}
	waitGroup.Wait()

	accepted := 0
	for index, status := range statuses {
		switch status {
		case http.StatusCreated:
			accepted++
		case http.StatusConflict:
			// 預期：標的已募滿
		default:
			t.Errorf("investor %d got unexpected HTTP %d", index, status)
		}
	}
	if accepted == 0 {
		t.Fatal("no investment succeeded")
	}

	var funded int64
	var status string
	if err := testPool.QueryRow(testContext(t),
		`SELECT funded_amount, status FROM listings WHERE id = $1`, listingID).
		Scan(&funded, &status); err != nil {
		t.Fatalf("read listing: %v", err)
	}
	if funded > target {
		t.Fatalf("listing is oversubscribed: funded %d > target %d", funded, target)
	}
	if funded != target {
		t.Errorf("funded_amount = %d, want %d (should reach the target)", funded, target)
	}

	// 投標紀錄的總和必須與 funded_amount 一致
	var investmentSum int64
	if err := testPool.QueryRow(testContext(t),
		`SELECT COALESCE(SUM(amount), 0) FROM investments WHERE listing_id = $1`,
		listingID).Scan(&investmentSum); err != nil {
		t.Fatalf("sum investments: %v", err)
	}
	if investmentSum != funded {
		t.Errorf("investment sum %d does not match funded_amount %d", investmentSum, funded)
	}

	// 只能撥款一次
	var loanCount int
	if err := testPool.QueryRow(testContext(t),
		`SELECT COUNT(*) FROM loans WHERE listing_id = $1`, listingID).Scan(&loanCount); err != nil {
		t.Fatalf("count loans: %v", err)
	}
	if loanCount != 1 {
		t.Fatalf("%d loans were created for one listing, want 1", loanCount)
	}
}

func TestIntegrationInvestorBalanceAndPortfolio(t *testing.T) {
	borrower, _, listingID, _ := listingFixture(t)
	createInvestor(t, "investor@creditflow.test", "出借人", 500000)

	investor := borrower.fork()
	investor.loginAs("investor@creditflow.test", testInvestorPassword)

	t.Run("investment beyond balance is rejected", func(t *testing.T) {
		investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": 600000, "requestId": "too-rich"}).
			expectStatus(t, http.StatusUnprocessableEntity, "insufficient balance")
	})

	t.Run("top up increases the balance", func(t *testing.T) {
		var payload struct {
			Balance int64 `json:"balance"`
		}
		investor.do(http.MethodPost, "/v1/investments/top-up",
			map[string]any{"amount": 500000}).
			expectStatus(t, http.StatusOK, "top up").decode(t, &payload)
		if payload.Balance != 1000000 {
			t.Errorf("balance = %d, want 1000000", payload.Balance)
		}
	})

	t.Run("invalid top up amounts are rejected", func(t *testing.T) {
		investor.do(http.MethodPost, "/v1/investments/top-up",
			map[string]any{"amount": 0}).
			expectStatus(t, http.StatusUnprocessableEntity, "zero top up")
		investor.do(http.MethodPost, "/v1/investments/top-up",
			map[string]any{"amount": -100}).
			expectStatus(t, http.StatusUnprocessableEntity, "negative top up")
	})

	t.Run("portfolio reflects investments", func(t *testing.T) {
		investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": 200000, "requestId": "portfolio-test"}).
			expectStatus(t, http.StatusCreated, "invest")

		var portfolio portfolioResponse
		investor.do(http.MethodGet, "/v1/investments", nil).
			expectStatus(t, http.StatusOK, "portfolio").decode(t, &portfolio)

		if portfolio.TotalInvested != 200000 {
			t.Errorf("totalInvested = %d, want 200000", portfolio.TotalInvested)
		}
		if portfolio.Balance != 800000 {
			t.Errorf("balance = %d, want 800000", portfolio.Balance)
		}
		if len(portfolio.Investments) != 1 {
			t.Fatalf("%d investments, want 1", len(portfolio.Investments))
		}
		if portfolio.WeightedRate <= 0 {
			t.Error("weightedRate was not calculated")
		}
		if portfolio.EstimatedReturn <= 0 {
			t.Error("estimatedReturn was not calculated")
		}
	})
}

func TestIntegrationInvestorRoleBoundaries(t *testing.T) {
	borrower, reviewer, listingID, _ := listingFixture(t)
	createInvestor(t, "investor@creditflow.test", "出借人", 1000000)

	investor := borrower.fork()
	investor.loginAs("investor@creditflow.test", testInvestorPassword)

	investorOnly := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/v1/investments", nil},
		{http.MethodPost, "/v1/investments/top-up", map[string]any{"amount": 1000}},
		{http.MethodPost, "/v1/listings/" + listingID + "/invest", map[string]any{"amount": 10000}},
	}

	for _, endpoint := range investorOnly {
		t.Run("borrower is forbidden from "+endpoint.path, func(t *testing.T) {
			borrower.do(endpoint.method, endpoint.path, endpoint.body).
				expectStatus(t, http.StatusForbidden, endpoint.path)
		})
		t.Run("reviewer is forbidden from "+endpoint.path, func(t *testing.T) {
			reviewer.do(endpoint.method, endpoint.path, endpoint.body).
				expectStatus(t, http.StatusForbidden, endpoint.path)
		})
	}

	t.Run("investor cannot access the admin queue", func(t *testing.T) {
		investor.do(http.MethodGet, "/v1/admin/applications", nil).
			expectStatus(t, http.StatusForbidden, "admin queue")
	})

	t.Run("marketplace stays public", func(t *testing.T) {
		anonymous := borrower.fork()
		anonymous.do(http.MethodGet, "/v1/listings", nil).
			expectStatus(t, http.StatusOK, "public listings")
	})
}

// 註冊可選擇 investor，但不可自封 reviewer。
func TestIntegrationRegisterAsInvestor(t *testing.T) {
	resetDatabase(t)
	client := newTestClient(t, newTestAPIServer(t))

	t.Run("investor role is accepted", func(t *testing.T) {
		var created user
		client.do(http.MethodPost, "/v1/auth/register", map[string]string{
			"email": "newinvestor@creditflow.test", "password": "a-good-password",
			"displayName": "新出借人", "role": "investor",
		}).expectStatus(t, http.StatusCreated, "register as investor").decode(t, &created)

		if created.Role != roleInvestor {
			t.Errorf("role = %q, want investor", created.Role)
		}
		client.do(http.MethodGet, "/v1/investments", nil).
			expectStatus(t, http.StatusOK, "investor endpoint")
	})

	t.Run("reviewer role is still refused", func(t *testing.T) {
		fresh := client.fork()
		var created user
		fresh.do(http.MethodPost, "/v1/auth/register", map[string]string{
			"email": "sneaky2@creditflow.test", "password": "a-good-password",
			"displayName": "試圖提權", "role": "reviewer",
		}).expectStatus(t, http.StatusCreated, "register as reviewer").decode(t, &created)

		if created.Role != roleBorrower {
			t.Fatalf("role = %q, want borrower; privilege escalation is possible", created.Role)
		}
	})

	t.Run("unknown role falls back to borrower", func(t *testing.T) {
		fresh := client.fork()
		var created user
		fresh.do(http.MethodPost, "/v1/auth/register", map[string]string{
			"email": "weird@creditflow.test", "password": "a-good-password",
			"displayName": "怪角色", "role": "administrator",
		}).expectStatus(t, http.StatusCreated, "register with unknown role").decode(t, &created)

		if created.Role != roleBorrower {
			t.Errorf("role = %q, want borrower", created.Role)
		}
	})
}
