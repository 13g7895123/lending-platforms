//go:build integration

package main

import (
	"fmt"
	"net/http"
	"testing"
)

// multiInvestorLoanFixture 建立一份由多位出借人共同出資的已撥款合約。
// 回傳借款人 client、合約、以及各出借人的 email 與投資金額。
func multiInvestorLoanFixture(t *testing.T, contributions map[string]int64) (*testClient, loan) {
	t.Helper()
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	borrower := newTestClient(t, server)
	reviewer := borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	applicationID := borrower.submitApplication()
	created := reviewer.approveApplication(applicationID)

	var total int64
	for _, amount := range contributions {
		total += amount
	}
	if total != created.TargetAmount {
		t.Fatalf("contributions total %d does not match the target %d", total, created.TargetAmount)
	}

	// 依 email 排序以確保投標順序固定，讓測試可重現
	emails := make([]string, 0, len(contributions))
	for email := range contributions {
		emails = append(emails, email)
	}
	sortStrings(emails)

	var disbursed *loan
	for _, email := range emails {
		amount := contributions[email]
		createInvestor(t, email, email, amount*2)
		investor := borrower.fork()
		investor.loginAs(email, testInvestorPassword)

		response := investor.do(http.MethodPost, "/v1/listings/"+created.ID+"/invest",
			map[string]any{"amount": amount, "requestId": "fixture-" + email})
		response.expectStatus(t, http.StatusCreated, "invest "+email)

		var payload investResponse
		response.decode(t, &payload)
		if payload.DisbursedLoa != nil {
			disbursed = payload.DisbursedLoa
		}
	}

	if disbursed == nil {
		t.Fatal("the listing was not fully funded by the fixture contributions")
	}
	return borrower, *disbursed
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// 單期還款的分潤總額必須精確等於該期實收金額。
func TestIntegrationRepaymentIsDistributedToInvestors(t *testing.T) {
	contributions := map[string]int64{
		"inv-a@creditflow.test": 400000,
		"inv-b@creditflow.test": 150000,
		"inv-c@creditflow.test": 50000,
	}
	borrower, createdLoan := multiInvestorLoanFixture(t, contributions)

	// 記下分潤前的餘額
	balancesBefore := make(map[string]int64)
	for email := range contributions {
		balancesBefore[email] = investorBalance(t, email)
	}

	var schedule scheduleResponse
	borrower.do(http.MethodGet, "/v1/loans/"+createdLoan.ID+"/schedule", nil).
		expectStatus(t, http.StatusOK, "schedule").decode(t, &schedule)
	firstRow := schedule.Schedule[0]

	borrower.do(http.MethodPost,
		fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
		expectStatus(t, http.StatusCreated, "pay first installment")

	t.Run("distribution total equals the amount paid", func(t *testing.T) {
		var principalSum, interestSum int64
		if err := testPool.QueryRow(testContext(t), `
			SELECT COALESCE(SUM(principal), 0), COALESCE(SUM(interest), 0)
			FROM distributions WHERE loan_id = $1
		`, createdLoan.ID).Scan(&principalSum, &interestSum); err != nil {
			t.Fatalf("sum distributions: %v", err)
		}
		if principalSum != firstRow.Principal {
			t.Errorf("distributed principal = %d, want exactly %d", principalSum, firstRow.Principal)
		}
		if interestSum != firstRow.Interest {
			t.Errorf("distributed interest = %d, want exactly %d", interestSum, firstRow.Interest)
		}
	})

	t.Run("each investor is credited proportionally", func(t *testing.T) {
		var creditedTotal int64
		for email, invested := range contributions {
			gained := investorBalance(t, email) - balancesBefore[email]
			creditedTotal += gained
			if gained <= 0 {
				t.Errorf("%s (invested %d) gained nothing", email, invested)
			}
		}
		// 出借人餘額增加總額必須等於借款人繳款金額
		if creditedTotal != firstRow.AmountDue {
			t.Errorf("investors gained %d in total, want exactly %d (the amount paid)",
				creditedTotal, firstRow.AmountDue)
		}
	})

	t.Run("larger investors receive larger shares", func(t *testing.T) {
		shareA := distributionTotalFor(t, "inv-a@creditflow.test")
		shareB := distributionTotalFor(t, "inv-b@creditflow.test")
		shareC := distributionTotalFor(t, "inv-c@creditflow.test")
		if !(shareA > shareB && shareB > shareC) {
			t.Errorf("shares are not ordered by investment size: A=%d B=%d C=%d",
				shareA, shareB, shareC)
		}
	})

	t.Run("investment totals are updated", func(t *testing.T) {
		var returned, earned int64
		if err := testPool.QueryRow(testContext(t), `
			SELECT COALESCE(SUM(principal_returned), 0), COALESCE(SUM(interest_earned), 0)
			FROM investments
		`).Scan(&returned, &earned); err != nil {
			t.Fatalf("read investment totals: %v", err)
		}
		if returned != firstRow.Principal {
			t.Errorf("principal_returned sum = %d, want %d", returned, firstRow.Principal)
		}
		if earned != firstRow.Interest {
			t.Errorf("interest_earned sum = %d, want %d", earned, firstRow.Interest)
		}
	})
}

func investorBalance(t *testing.T, email string) int64 {
	t.Helper()
	var balance int64
	if err := testPool.QueryRow(testContext(t),
		`SELECT available_balance FROM users WHERE email = $1`, email).Scan(&balance); err != nil {
		t.Fatalf("read balance for %s: %v", email, err)
	}
	return balance
}

func distributionTotalFor(t *testing.T, email string) int64 {
	t.Helper()
	var total int64
	if err := testPool.QueryRow(testContext(t), `
		SELECT COALESCE(SUM(d.principal + d.interest), 0)
		FROM distributions d
		JOIN users u ON u.id = d.investor_id
		WHERE u.email = $1
	`, email).Scan(&total); err != nil {
		t.Fatalf("sum distributions for %s: %v", email, err)
	}
	return total
}

// 全期繳畢後，出借人收回的本金總額必須等於原始投資總額。
// 這是資金閉環最重要的不變量。
func TestIntegrationFullRepaymentReturnsAllPrincipal(t *testing.T) {
	contributions := map[string]int64{
		"inv-a@creditflow.test": 350000,
		"inv-b@creditflow.test": 250000,
	}
	borrower, createdLoan := multiInvestorLoanFixture(t, contributions)

	// 讓所有期數到期，便於逐期繳完
	if _, err := testPool.Exec(testContext(t), `
		UPDATE loan_installments SET due_date = CURRENT_DATE, status = 'due' WHERE loan_id = $1
	`, createdLoan.ID); err != nil {
		t.Fatalf("mark all installments due: %v", err)
	}

	var totalPaid int64
	for number := 1; number <= createdLoan.TotalInstallments; number++ {
		response := borrower.do(http.MethodPost,
			fmt.Sprintf("/v1/loans/%s/installments/%d/pay", createdLoan.ID, number), nil)
		response.expectStatus(t, http.StatusCreated, fmt.Sprintf("pay installment %d", number))

		var payload payResponse
		response.decode(t, &payload)
		totalPaid += payload.Repayment.Amount
	}

	t.Run("all principal is returned", func(t *testing.T) {
		var returned int64
		if err := testPool.QueryRow(testContext(t),
			`SELECT COALESCE(SUM(principal_returned), 0) FROM investments`).Scan(&returned); err != nil {
			t.Fatalf("sum principal returned: %v", err)
		}
		if returned != createdLoan.Amount {
			t.Errorf("returned principal = %d, want exactly %d (the full loan amount)",
				returned, createdLoan.Amount)
		}
	})

	t.Run("each investor recovers their own principal", func(t *testing.T) {
		for email, invested := range contributions {
			var returned int64
			if err := testPool.QueryRow(testContext(t), `
				SELECT COALESCE(SUM(i.principal_returned), 0)
				FROM investments i JOIN users u ON u.id = i.investor_id
				WHERE u.email = $1
			`, email).Scan(&returned); err != nil {
				t.Fatalf("read returned principal for %s: %v", email, err)
			}
			// 逐期捨入會造成小幅偏差，容許每期最多 1 元
			tolerance := int64(createdLoan.TotalInstallments)
			if returned < invested-tolerance || returned > invested+tolerance {
				t.Errorf("%s invested %d but recovered %d (outside tolerance %d)",
					email, invested, returned, tolerance)
			}
		}
	})

	t.Run("distributions match the total paid", func(t *testing.T) {
		var distributed int64
		if err := testPool.QueryRow(testContext(t),
			`SELECT COALESCE(SUM(principal + interest), 0) FROM distributions
			 WHERE loan_id = $1`, createdLoan.ID).Scan(&distributed); err != nil {
			t.Fatalf("sum distributions: %v", err)
		}
		if distributed != totalPaid {
			t.Errorf("distributed %d but borrower paid %d", distributed, totalPaid)
		}
	})

	t.Run("investors earned interest", func(t *testing.T) {
		var earned int64
		if err := testPool.QueryRow(testContext(t),
			`SELECT COALESCE(SUM(interest_earned), 0) FROM investments`).Scan(&earned); err != nil {
			t.Fatalf("sum interest: %v", err)
		}
		if earned <= 0 {
			t.Error("investors earned no interest after a full repayment cycle")
		}
		// 總利息應等於借款人多付的部分
		if earned != totalPaid-createdLoan.Amount {
			t.Errorf("interest earned %d, want %d (total paid minus principal)",
				earned, totalPaid-createdLoan.Amount)
		}
	})
}

// 提前清償只分配本金：免除的未到期利息不該分給出借人。
func TestIntegrationPrepaymentDistributesPrincipalOnly(t *testing.T) {
	contributions := map[string]int64{
		"inv-a@creditflow.test": 300000,
		"inv-b@creditflow.test": 300000,
	}
	borrower, createdLoan := multiInvestorLoanFixture(t, contributions)

	// 先繳一期，確認之後的清償只涵蓋剩餘本金
	borrower.do(http.MethodPost,
		fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
		expectStatus(t, http.StatusCreated, "pay first installment")

	var interestBeforePayoff int64
	if err := testPool.QueryRow(testContext(t),
		`SELECT COALESCE(SUM(interest_earned), 0) FROM investments`).Scan(&interestBeforePayoff); err != nil {
		t.Fatalf("read interest: %v", err)
	}

	var payload struct {
		WaivedInterest int64 `json:"waivedInterest"`
		Repayment      struct {
			ID        int64 `json:"id"`
			Principal int64 `json:"principal"`
		} `json:"repayment"`
	}
	borrower.do(http.MethodPost, "/v1/loans/"+createdLoan.ID+"/settle", nil).
		expectStatus(t, http.StatusCreated, "settle").decode(t, &payload)

	t.Run("payoff principal is distributed in full", func(t *testing.T) {
		var principal, interest int64
		if err := testPool.QueryRow(testContext(t), `
			SELECT COALESCE(SUM(principal), 0), COALESCE(SUM(interest), 0)
			FROM distributions WHERE repayment_id = $1
		`, payload.Repayment.ID).Scan(&principal, &interest); err != nil {
			t.Fatalf("sum payoff distributions: %v", err)
		}
		if principal != payload.Repayment.Principal {
			t.Errorf("distributed payoff principal = %d, want %d",
				principal, payload.Repayment.Principal)
		}
		// 免除的利息不分配
		if interest != 0 {
			t.Errorf("payoff distributed %d in interest, want 0 (waived interest is not paid)", interest)
		}
	})

	t.Run("waived interest is not credited to investors", func(t *testing.T) {
		var interestAfter int64
		if err := testPool.QueryRow(testContext(t),
			`SELECT COALESCE(SUM(interest_earned), 0) FROM investments`).Scan(&interestAfter); err != nil {
			t.Fatalf("read interest: %v", err)
		}
		if interestAfter != interestBeforePayoff {
			t.Errorf("interest earned changed from %d to %d during payoff; waived interest leaked",
				interestBeforePayoff, interestAfter)
		}
		if payload.WaivedInterest <= 0 {
			t.Error("no interest was waived, so this test proves nothing")
		}
	})

	t.Run("all principal is recovered", func(t *testing.T) {
		var returned int64
		if err := testPool.QueryRow(testContext(t),
			`SELECT COALESCE(SUM(principal_returned), 0) FROM investments`).Scan(&returned); err != nil {
			t.Fatalf("sum principal: %v", err)
		}
		if returned != createdLoan.Amount {
			t.Errorf("recovered principal = %d, want %d", returned, createdLoan.Amount)
		}
	})
}

// 重複還款被擋下時不可重複分潤。
func TestIntegrationDuplicatePaymentDoesNotDoubleDistribute(t *testing.T) {
	contributions := map[string]int64{"inv-a@creditflow.test": 600000}
	borrower, createdLoan := multiInvestorLoanFixture(t, contributions)

	path := fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID)
	borrower.do(http.MethodPost, path, nil).
		expectStatus(t, http.StatusCreated, "first payment")

	balanceAfterFirst := investorBalance(t, "inv-a@creditflow.test")

	borrower.do(http.MethodPost, path, nil).
		expectStatus(t, http.StatusConflict, "duplicate payment")

	if balanceAfterFirst != investorBalance(t, "inv-a@creditflow.test") {
		t.Error("the investor was credited twice for one installment")
	}

	var count int
	if err := testPool.QueryRow(testContext(t),
		`SELECT COUNT(*) FROM distributions WHERE loan_id = $1`, createdLoan.ID).Scan(&count); err != nil {
		t.Fatalf("count distributions: %v", err)
	}
	if count != 1 {
		t.Errorf("%d distribution rows exist for one payment to one investor, want 1", count)
	}
}

func TestIntegrationInvestorSeesActualReturns(t *testing.T) {
	contributions := map[string]int64{
		"inv-a@creditflow.test": 400000,
		"inv-b@creditflow.test": 200000,
	}
	borrower, createdLoan := multiInvestorLoanFixture(t, contributions)

	investor := borrower.fork()
	investor.loginAs("inv-a@creditflow.test", testInvestorPassword)

	t.Run("nothing received before any repayment", func(t *testing.T) {
		var portfolio portfolioResponse
		investor.do(http.MethodGet, "/v1/investments", nil).
			expectStatus(t, http.StatusOK, "portfolio").decode(t, &portfolio)

		if portfolio.TotalPrincipalReturned != 0 || portfolio.TotalInterestEarned != 0 {
			t.Errorf("received %d principal and %d interest before any repayment",
				portfolio.TotalPrincipalReturned, portfolio.TotalInterestEarned)
		}
		if portfolio.OutstandingPrincipal != 400000 {
			t.Errorf("outstandingPrincipal = %d, want 400000", portfolio.OutstandingPrincipal)
		}
	})

	borrower.do(http.MethodPost,
		fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
		expectStatus(t, http.StatusCreated, "pay")

	t.Run("actual returns appear after repayment", func(t *testing.T) {
		var portfolio portfolioResponse
		investor.do(http.MethodGet, "/v1/investments", nil).
			expectStatus(t, http.StatusOK, "portfolio").decode(t, &portfolio)

		if portfolio.TotalPrincipalReturned <= 0 {
			t.Error("no principal was reported as returned")
		}
		if portfolio.TotalInterestEarned <= 0 {
			t.Error("no interest was reported as earned")
		}
		// 未收本金應相應減少
		expected := int64(400000) - portfolio.TotalPrincipalReturned
		if portfolio.OutstandingPrincipal != expected {
			t.Errorf("outstandingPrincipal = %d, want %d", portfolio.OutstandingPrincipal, expected)
		}
		if len(portfolio.Investments) != 1 {
			t.Fatalf("%d investments, want 1", len(portfolio.Investments))
		}
		if portfolio.Investments[0].PrincipalReturned != portfolio.TotalPrincipalReturned {
			t.Error("per-investment and total principal returned disagree")
		}
	})

	t.Run("distribution records are listed", func(t *testing.T) {
		var records []distributionRecord
		investor.do(http.MethodGet, "/v1/investments/distributions", nil).
			expectStatus(t, http.StatusOK, "distributions").decode(t, &records)

		if len(records) != 1 {
			t.Fatalf("%d distribution records, want 1", len(records))
		}
		if records[0].Total != records[0].Principal+records[0].Interest {
			t.Error("total does not equal principal plus interest")
		}
		if records[0].LoanID != createdLoan.ID {
			t.Errorf("loanId = %q, want %q", records[0].LoanID, createdLoan.ID)
		}
	})

	t.Run("other investors cannot see these records", func(t *testing.T) {
		other := borrower.fork()
		other.loginAs("inv-b@creditflow.test", testInvestorPassword)

		var records []distributionRecord
		other.do(http.MethodGet, "/v1/investments/distributions", nil).
			expectStatus(t, http.StatusOK, "distributions").decode(t, &records)

		for _, record := range records {
			var owner string
			if err := testPool.QueryRow(testContext(t),
				`SELECT u.email FROM distributions d JOIN users u ON u.id = d.investor_id
				 WHERE d.id = $1`, record.ID).Scan(&owner); err != nil {
				t.Fatalf("read distribution owner: %v", err)
			}
			if owner != "inv-b@creditflow.test" {
				t.Errorf("investor B can see a distribution belonging to %s", owner)
			}
		}
	})

	t.Run("borrower cannot read distributions", func(t *testing.T) {
		borrower.do(http.MethodGet, "/v1/investments/distributions", nil).
			expectStatus(t, http.StatusForbidden, "borrower reading distributions")
	})
}

// seed 或舊資料建立的合約背後沒有投資紀錄，還款時不該失敗。
func TestIntegrationRepaymentWithoutInvestorsStillSucceeds(t *testing.T) {
	resetDatabase(t)
	userID := createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	// 直接建立一份沒有 listing 的合約（模擬 seed 資料）
	if _, err := testPool.Exec(testContext(t), `
		INSERT INTO loans (id, user_id, product, amount, annual_rate,
		                   paid_installments, total_installments, status, status_tone, monthly_payment)
		VALUES ('LN-LEGACY-LOAN', $1, '信貸', 120000, 4.88, 0, 12, '正常繳款', 'success', 10000)
	`, userID); err != nil {
		t.Fatalf("insert legacy loan: %v", err)
	}
	if _, err := testPool.Exec(testContext(t), `
		INSERT INTO loan_installments (loan_id, installment_no, due_date, amount_due,
		                               principal, interest, remaining_balance, status)
		VALUES ('LN-LEGACY-LOAN', 1, CURRENT_DATE, 10000, 9500, 500, 110500, 'due')
	`); err != nil {
		t.Fatalf("insert installment: %v", err)
	}

	borrower := newTestClient(t, newTestAPIServer(t))
	borrower.loginAs("borrower@creditflow.test", testPassword)

	borrower.do(http.MethodPost, "/v1/loans/LN-LEGACY-LOAN/installments/1/pay", nil).
		expectStatus(t, http.StatusCreated, "pay on a loan with no investors")

	var count int
	if err := testPool.QueryRow(testContext(t),
		`SELECT COUNT(*) FROM distributions WHERE loan_id = 'LN-LEGACY-LOAN'`).Scan(&count); err != nil {
		t.Fatalf("count distributions: %v", err)
	}
	if count != 0 {
		t.Errorf("%d distributions were created for a loan with no investors, want 0", count)
	}
}
