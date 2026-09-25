//go:build integration

package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 收入與支出不可以原值出現在任何清單回應中。
func TestIntegrationIncomeIsNotDisclosedInListings(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	borrower := newTestClient(t, server)
	reviewer := borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	// validApplicationBody 的年收入為 1,200,000、月支出 30,000
	applicationID := borrower.submitApplication()

	endpoints := map[string]*testClient{
		"/v1/applications":       borrower,
		"/v1/admin/applications": reviewer,
	}

	for path, client := range endpoints {
		t.Run("raw amounts are absent from "+path, func(t *testing.T) {
			response := client.do(http.MethodGet, path, nil).
				expectStatus(t, http.StatusOK, path)

			body := string(response.Body)
			for _, leaked := range []string{"1200000", "annualIncome", "monthlyExpenses"} {
				if strings.Contains(body, leaked) {
					t.Errorf("response from %s contains %q", path, leaked)
				}
			}
		})

		t.Run("brackets are provided by "+path, func(t *testing.T) {
			items, _ := client.do(http.MethodGet, path, nil).
				expectStatus(t, http.StatusOK, path).decodeApplications(t)
			if len(items) == 0 {
				t.Fatal("no applications returned")
			}
			if items[0].IncomeRange != "120～200 萬" {
				t.Errorf("incomeRange = %q, want 120～200 萬", items[0].IncomeRange)
			}
			if items[0].ExpenseRange != "3～5 萬" {
				t.Errorf("expenseRange = %q, want 3～5 萬", items[0].ExpenseRange)
			}
		})
	}

	// DBR 仍須正確——風控的判讀依據不可因為隱藏原值而失真
	t.Run("DBR is still calculated correctly", func(t *testing.T) {
		items, _ := reviewer.do(http.MethodGet, "/v1/admin/applications", nil).
			expectStatus(t, http.StatusOK, "admin queue").decodeApplications(t)
		if items[0].DBR <= 0 {
			t.Error("DBR was not calculated; hiding the raw amount broke the computation")
		}
		if items[0].Recommendation == "" {
			t.Error("recommendation is empty")
		}
	})

	// 完整金額只在稽核端點揭露
	t.Run("reveal returns the exact amounts", func(t *testing.T) {
		var revealed revealResponse
		reviewer.do(http.MethodPost, "/v1/admin/applications/"+applicationID+"/reveal",
			map[string]string{"reason": "人工覆核收入證明"}).
			expectStatus(t, http.StatusOK, "reveal").decode(t, &revealed)

		if revealed.AnnualIncome != 1200000 {
			t.Errorf("annualIncome = %d, want 1200000", revealed.AnnualIncome)
		}
		if revealed.MonthlyExpenses != 30000 {
			t.Errorf("monthlyExpenses = %d, want 30000", revealed.MonthlyExpenses)
		}
	})

	t.Run("revealing income is recorded in the audit log", func(t *testing.T) {
		var field string
		if err := testPool.QueryRow(testContext(t), `
			SELECT field FROM pii_access_log
			WHERE application_id = $1 ORDER BY created_at DESC LIMIT 1
		`, applicationID).Scan(&field); err != nil {
			t.Fatalf("read audit log: %v", err)
		}
		if !strings.Contains(field, "annual_income") {
			t.Errorf("audit field = %q, want it to mention annual_income", field)
		}
	})

	t.Run("borrower cannot reveal their own exact amounts", func(t *testing.T) {
		// 借款人本就知道自己的收入，但仍須走稽核端點（且該端點限 reviewer）
		borrower.do(http.MethodPost, "/v1/admin/applications/"+applicationID+"/reveal",
			map[string]string{"reason": "查看自己的"}).
			expectStatus(t, http.StatusForbidden, "borrower reveal")
	})
}

func TestIntegrationApplicationPagination(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	borrower := newTestClient(t, server)
	reviewer := borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	const total = 12
	for i := 0; i < total; i++ {
		borrower.submitApplication()
	}

	t.Run("default page reports the full total", func(t *testing.T) {
		items, page := borrower.do(http.MethodGet, "/v1/applications", nil).
			expectStatus(t, http.StatusOK, "default page").decodeApplications(t)

		if page.Total != total {
			t.Errorf("total = %d, want %d", page.Total, total)
		}
		if len(items) != total {
			t.Errorf("%d items on the default page, want all %d", len(items), total)
		}
		if page.HasMore {
			t.Error("hasMore is true although every row fits on one page")
		}
	})

	t.Run("limit splits the result", func(t *testing.T) {
		items, page := borrower.do(http.MethodGet, "/v1/applications?limit=5", nil).
			expectStatus(t, http.StatusOK, "first page").decodeApplications(t)

		if len(items) != 5 {
			t.Errorf("%d items, want 5", len(items))
		}
		if page.Total != total {
			t.Errorf("total = %d, want %d", page.Total, total)
		}
		if !page.HasMore {
			t.Error("hasMore is false although more rows remain")
		}
	})

	t.Run("offset walks through every row exactly once", func(t *testing.T) {
		seen := make(map[string]int)
		for offset := 0; offset < total; offset += 5 {
			items, _ := borrower.do(http.MethodGet,
				fmt.Sprintf("/v1/applications?limit=5&offset=%d", offset), nil).
				expectStatus(t, http.StatusOK, "page").decodeApplications(t)
			for _, item := range items {
				seen[item.ID]++
			}
		}
		if len(seen) != total {
			t.Errorf("walked %d distinct applications, want %d", len(seen), total)
		}
		for id, count := range seen {
			if count != 1 {
				t.Errorf("application %s appeared %d times across pages", id, count)
			}
		}
	})

	t.Run("last page reports no more", func(t *testing.T) {
		_, page := borrower.do(http.MethodGet, "/v1/applications?limit=5&offset=10", nil).
			expectStatus(t, http.StatusOK, "last page").decodeApplications(t)
		if page.HasMore {
			t.Error("hasMore is true on the final page")
		}
	})

	t.Run("offset beyond the end returns nothing", func(t *testing.T) {
		items, page := borrower.do(http.MethodGet, "/v1/applications?limit=5&offset=999", nil).
			expectStatus(t, http.StatusOK, "past the end").decodeApplications(t)
		if len(items) != 0 {
			t.Errorf("%d items past the end, want 0", len(items))
		}
		if page.Total != total {
			t.Errorf("total = %d, want %d even when the page is empty", page.Total, total)
		}
	})

	// 無效參數不該讓請求失敗——分頁是呈現層細節
	t.Run("invalid params fall back to defaults", func(t *testing.T) {
		for _, query := range []string{"?limit=abc", "?limit=-1", "?limit=0", "?offset=-5", "?offset=xyz"} {
			items, page := borrower.do(http.MethodGet, "/v1/applications"+query, nil).
				expectStatus(t, http.StatusOK, "invalid "+query).decodeApplications(t)
			if page.Limit != defaultPageLimit {
				t.Errorf("%s: limit = %d, want the default %d", query, page.Limit, defaultPageLimit)
			}
			if len(items) != total {
				t.Errorf("%s: %d items, want %d", query, len(items), total)
			}
		}
	})

	t.Run("limit is capped", func(t *testing.T) {
		_, page := borrower.do(http.MethodGet, "/v1/applications?limit=99999", nil).
			expectStatus(t, http.StatusOK, "oversized limit").decodeApplications(t)
		if page.Limit != maxPageLimit {
			t.Errorf("limit = %d, want it capped at %d", page.Limit, maxPageLimit)
		}
	})

	t.Run("admin queue paginates too", func(t *testing.T) {
		items, page := reviewer.do(http.MethodGet, "/v1/admin/applications?limit=4", nil).
			expectStatus(t, http.StatusOK, "admin page").decodeApplications(t)
		if len(items) != 4 {
			t.Errorf("%d items, want 4", len(items))
		}
		if page.Total != total {
			t.Errorf("total = %d, want %d", page.Total, total)
		}
		if !page.HasMore {
			t.Error("hasMore is false although more rows remain")
		}
	})

	// 分頁與狀態篩選必須能同時運作
	t.Run("status filter is counted correctly", func(t *testing.T) {
		_, page := reviewer.do(http.MethodGet, "/v1/admin/applications?status=pending&limit=3", nil).
			expectStatus(t, http.StatusOK, "filtered page").decodeApplications(t)
		if page.Total != total {
			t.Errorf("total = %d, want %d (all are pending)", page.Total, total)
		}

		_, empty := reviewer.do(http.MethodGet, "/v1/admin/applications?status=approved", nil).
			expectStatus(t, http.StatusOK, "approved filter").decodeApplications(t)
		if empty.Total != 0 {
			t.Errorf("approved total = %d, want 0", empty.Total)
		}
	})
}
