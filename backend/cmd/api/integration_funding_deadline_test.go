//go:build integration

package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// expireListing 把標的的募資期限推到過去，模擬逾期。
func expireListing(t *testing.T, listingID string) {
	t.Helper()
	if _, err := testPool.Exec(testContext(t), `
		UPDATE listings SET funding_deadline = NOW() - INTERVAL '1 day' WHERE id = $1
	`, listingID); err != nil {
		t.Fatalf("expire listing: %v", err)
	}
}

func TestIntegrationListingHasFundingDeadline(t *testing.T) {
	_, _, listingID, _ := listingFixture(t)

	var daysAhead int
	if err := testPool.QueryRow(testContext(t), `
		SELECT (funding_deadline::date - CURRENT_DATE) FROM listings WHERE id = $1
	`, listingID).Scan(&daysAhead); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	if daysAhead != defaultFundingDays {
		t.Errorf("deadline is %d days out, want %d", daysAhead, defaultFundingDays)
	}

	// 市集回應要帶剩餘天數，前端才能顯示倒數
	createInvestor(t, "investor@creditflow.test", "出借人", 1000000)
	server := newTestAPIServer(t)
	investor := newTestClient(t, server)
	investor.loginAs("investor@creditflow.test", testInvestorPassword)

	var items []listingItem
	investor.do(http.MethodGet, "/v1/listings", nil).
		expectStatus(t, http.StatusOK, "listings").decode(t, &items)

	for _, item := range items {
		if item.ID == listingID {
			if item.DaysRemaining != defaultFundingDays {
				t.Errorf("daysRemaining = %d, want %d", item.DaysRemaining, defaultFundingDays)
			}
			if item.Deadline.IsZero() {
				t.Error("fundingDeadline was not returned")
			}
		}
	}
}

// 逾期未募滿 → 取消並全額退款。退款總額必須等於已募集金額。
func TestIntegrationExpiredListingIsCancelledAndRefunded(t *testing.T) {
	borrower, _, listingID, target := listingFixture(t)

	contributions := map[string]int64{
		"inv-a@creditflow.test": 100000,
		"inv-b@creditflow.test": 50000,
	}
	balancesBefore := make(map[string]int64)
	var totalInvested int64

	for email, amount := range contributions {
		createInvestor(t, email, email, 1000000)
		balancesBefore[email] = investorBalance(t, email)

		investor := borrower.fork()
		investor.loginAs(email, testInvestorPassword)
		investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": amount, "requestId": "partial-" + email}).
			expectStatus(t, http.StatusCreated, "invest "+email)
		totalInvested += amount
	}

	// 確認尚未募滿（否則測不到取消路徑）
	if totalInvested >= target {
		t.Fatalf("contributions %d already reach the target %d", totalInvested, target)
	}

	expireListing(t, listingID)

	server := newTestAPIServer(t)
	cancelled, err := server.sweepExpiredListings(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if cancelled != 1 {
		t.Errorf("swept %d listings, want 1", cancelled)
	}

	t.Run("listing is cancelled", func(t *testing.T) {
		var status string
		var reason string
		var funded int64
		if err := testPool.QueryRow(testContext(t), `
			SELECT status, cancel_reason, funded_amount FROM listings WHERE id = $1
		`, listingID).Scan(&status, &reason, &funded); err != nil {
			t.Fatalf("read listing: %v", err)
		}
		if status != "cancelled" {
			t.Errorf("status = %q, want cancelled", status)
		}
		if reason == "" {
			t.Error("cancel_reason is empty")
		}
		if funded != 0 {
			t.Errorf("funded_amount = %d, want 0 after refunding", funded)
		}
	})

	t.Run("refund total equals the amount raised", func(t *testing.T) {
		var refunded int64
		if err := testPool.QueryRow(testContext(t), `
			SELECT COALESCE(SUM(amount), 0) FROM investment_refunds WHERE listing_id = $1
		`, listingID).Scan(&refunded); err != nil {
			t.Fatalf("sum refunds: %v", err)
		}
		if refunded != totalInvested {
			t.Errorf("refunded %d, want exactly %d (the amount raised)", refunded, totalInvested)
		}
	})

	// 退款是原額退回，不是按比例分配——標的從未撥款，本金完全沒動用
	t.Run("each investor gets back exactly what they put in", func(t *testing.T) {
		for email, invested := range contributions {
			gained := investorBalance(t, email) - balancesBefore[email]
			if gained != 0 {
				t.Errorf("%s: balance changed by %d overall, want 0 "+
					"(invested %d then refunded %d)", email, gained, invested, invested)
			}
		}
	})

	t.Run("application returns to a reviewable state", func(t *testing.T) {
		var status string
		if err := testPool.QueryRow(testContext(t), `
			SELECT a.status FROM applications a
			JOIN listings l ON l.application_id = a.id WHERE l.id = $1
		`, listingID).Scan(&status); err != nil {
			t.Fatalf("read application status: %v", err)
		}
		// 募資失敗是平台端的結果，不該讓借款人被婉拒
		if status != "more_info_required" {
			t.Errorf("application status = %q, want more_info_required", status)
		}
	})

	t.Run("cancelled listing rejects further investment", func(t *testing.T) {
		investor := borrower.fork()
		investor.loginAs("inv-a@creditflow.test", testInvestorPassword)
		investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
			map[string]any{"amount": 10000, "requestId": "after-cancel"}).
			expectStatus(t, http.StatusConflict, "investing in a cancelled listing")
	})

	// 重複取消不可重複退款
	t.Run("sweeping again refunds nothing more", func(t *testing.T) {
		balancesAfterFirst := make(map[string]int64)
		for email := range contributions {
			balancesAfterFirst[email] = investorBalance(t, email)
		}

		if _, err := server.sweepExpiredListings(context.Background()); err != nil {
			t.Fatalf("second sweep: %v", err)
		}

		for email := range contributions {
			if investorBalance(t, email) != balancesAfterFirst[email] {
				t.Errorf("%s was refunded twice", email)
			}
		}

		var refundRows int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM investment_refunds WHERE listing_id = $1`,
			listingID).Scan(&refundRows); err != nil {
			t.Fatalf("count refunds: %v", err)
		}
		if refundRows != len(contributions) {
			t.Errorf("%d refund rows, want %d", refundRows, len(contributions))
		}
	})
}

// 已募滿並撥款的標的不受期限影響：資金已進入借款人手中，不能退款。
func TestIntegrationDisbursedListingIsNotCancelled(t *testing.T) {
	borrower, _, listingID, target := listingFixture(t)
	createInvestor(t, "investor@creditflow.test", "出借人", target*2)

	investor := borrower.fork()
	investor.loginAs("investor@creditflow.test", testInvestorPassword)
	createdLoan := investor.fundListingFully(listingID, target)

	// 即使把期限推到過去，已撥款的標的也不該被取消
	expireListing(t, listingID)

	server := newTestAPIServer(t)
	cancelled, err := server.sweepExpiredListings(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if cancelled != 0 {
		t.Errorf("swept %d listings, want 0 (the listing was already disbursed)", cancelled)
	}

	var status string
	if err := testPool.QueryRow(testContext(t),
		`SELECT status FROM listings WHERE id = $1`, listingID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "disbursed" {
		t.Errorf("status = %q, want disbursed", status)
	}

	var refunds int
	if err := testPool.QueryRow(testContext(t),
		`SELECT COUNT(*) FROM investment_refunds WHERE listing_id = $1`, listingID).Scan(&refunds); err != nil {
		t.Fatalf("count refunds: %v", err)
	}
	if refunds != 0 {
		t.Errorf("%d refunds were issued for a disbursed listing", refunds)
	}

	// 合約仍然存在且可還款
	if createdLoan.ID == "" {
		t.Fatal("no loan was created")
	}
	borrower.do(http.MethodPost,
		fmt.Sprintf("/v1/loans/%s/installments/1/pay", createdLoan.ID), nil).
		expectStatus(t, http.StatusCreated, "paying a disbursed loan after the deadline passed")
}

// 期限已過但排程尚未掃到時，投標也要被拒絕。
func TestIntegrationExpiredListingRejectsInvestmentBeforeSweep(t *testing.T) {
	borrower, _, listingID, _ := listingFixture(t)
	createInvestor(t, "investor@creditflow.test", "出借人", 1000000)

	expireListing(t, listingID)

	investor := borrower.fork()
	investor.loginAs("investor@creditflow.test", testInvestorPassword)

	response := investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
		map[string]any{"amount": 10000, "requestId": "past-deadline"})
	response.expectStatus(t, http.StatusConflict, "investing past the deadline")

	if message := response.errorMessage(t); message == "" {
		t.Error("no error message explaining the rejection")
	}

	var count int
	if err := testPool.QueryRow(testContext(t),
		`SELECT COUNT(*) FROM investments WHERE listing_id = $1`, listingID).Scan(&count); err != nil {
		t.Fatalf("count investments: %v", err)
	}
	if count != 0 {
		t.Errorf("%d investments were accepted past the deadline", count)
	}
}

func TestIntegrationReviewerCanCancelListing(t *testing.T) {
	borrower, reviewer, listingID, _ := listingFixture(t)
	createInvestor(t, "investor@creditflow.test", "出借人", 1000000)

	investor := borrower.fork()
	investor.loginAs("investor@creditflow.test", testInvestorPassword)
	investor.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
		map[string]any{"amount": 80000, "requestId": "before-manual-cancel"}).
		expectStatus(t, http.StatusCreated, "invest")

	balanceBefore := investorBalance(t, "investor@creditflow.test")

	t.Run("reason is required", func(t *testing.T) {
		reviewer.do(http.MethodPost, "/v1/admin/listings/"+listingID+"/cancel",
			map[string]string{"reason": ""}).
			expectStatus(t, http.StatusUnprocessableEntity, "cancel without reason")
	})

	t.Run("borrower cannot cancel", func(t *testing.T) {
		borrower.do(http.MethodPost, "/v1/admin/listings/"+listingID+"/cancel",
			map[string]string{"reason": "我不想借了"}).
			expectStatus(t, http.StatusForbidden, "borrower cancelling")
	})

	t.Run("investor cannot cancel", func(t *testing.T) {
		investor.do(http.MethodPost, "/v1/admin/listings/"+listingID+"/cancel",
			map[string]string{"reason": "想拿回錢"}).
			expectStatus(t, http.StatusForbidden, "investor cancelling")
	})

	t.Run("reviewer cancels and funds are returned", func(t *testing.T) {
		var payload struct {
			Status         string `json:"status"`
			RefundedAmount int64  `json:"refundedAmount"`
			RefundedCount  int    `json:"refundedCount"`
		}
		reviewer.do(http.MethodPost, "/v1/admin/listings/"+listingID+"/cancel",
			map[string]string{"reason": "借款人資格複核未通過"}).
			expectStatus(t, http.StatusOK, "reviewer cancel").decode(t, &payload)

		if payload.Status != "cancelled" {
			t.Errorf("status = %q, want cancelled", payload.Status)
		}
		if payload.RefundedAmount != 80000 {
			t.Errorf("refundedAmount = %d, want 80000", payload.RefundedAmount)
		}
		if payload.RefundedCount != 1 {
			t.Errorf("refundedCount = %d, want 1", payload.RefundedCount)
		}
		if investorBalance(t, "investor@creditflow.test") != balanceBefore+80000 {
			t.Error("the investor's balance was not restored")
		}
	})

	t.Run("cancelling twice is rejected", func(t *testing.T) {
		reviewer.do(http.MethodPost, "/v1/admin/listings/"+listingID+"/cancel",
			map[string]string{"reason": "再試一次"}).
			expectStatus(t, http.StatusConflict, "second cancel")
	})

	t.Run("unknown listing returns 404", func(t *testing.T) {
		reviewer.do(http.MethodPost, "/v1/admin/listings/LT-0000-000000/cancel",
			map[string]string{"reason": "查無此標的"}).
			expectStatus(t, http.StatusNotFound, "unknown listing")
	})
}

// 沒有任何投標的標的逾期時也要能取消（沒有錢要退，但狀態要推進）。
func TestIntegrationExpiredListingWithNoInvestors(t *testing.T) {
	_, _, listingID, _ := listingFixture(t)
	expireListing(t, listingID)

	server := newTestAPIServer(t)
	cancelled, err := server.sweepExpiredListings(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if cancelled != 1 {
		t.Errorf("swept %d listings, want 1", cancelled)
	}

	var status string
	if err := testPool.QueryRow(testContext(t),
		`SELECT status FROM listings WHERE id = $1`, listingID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "cancelled" {
		t.Errorf("status = %q, want cancelled", status)
	}

	var refunds int
	if err := testPool.QueryRow(testContext(t),
		`SELECT COUNT(*) FROM investment_refunds WHERE listing_id = $1`, listingID).Scan(&refunds); err != nil {
		t.Fatalf("count refunds: %v", err)
	}
	if refunds != 0 {
		t.Errorf("%d refunds for a listing with no investors", refunds)
	}
}
