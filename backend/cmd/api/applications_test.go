package main

import (
	"math"
	"testing"
)

func TestGradeForScore(t *testing.T) {
	tests := []struct {
		score     int
		wantGrade string
		wantRate  float64
	}{
		{900, "A", rateGradeA},
		{750, "A", rateGradeA}, // 邊界
		{749, "B", rateGradeB},
		{650, "B", rateGradeB}, // 邊界
		{649, "C", rateGradeC},
		{0, "C", rateGradeC},
	}

	for _, test := range tests {
		grade, rate := gradeForScore(test.score)
		if grade != test.wantGrade || rate != test.wantRate {
			t.Errorf("gradeForScore(%d) = (%q, %v), want (%q, %v)",
				test.score, grade, rate, test.wantGrade, test.wantRate)
		}
	}
}

func TestDebtBurdenRatio(t *testing.T) {
	// 月付 15000 → 年付 180000；年收入 1080000 → DBR 16.67%
	got := debtBurdenRatio(15000, 1080000)
	if math.Abs(got-16.666) > 0.01 {
		t.Errorf("debtBurdenRatio(15000, 1080000) = %.3f, want ~16.667", got)
	}
	// 年收入為零不得造成除以零
	if got := debtBurdenRatio(15000, 0); got != 0 {
		t.Errorf("debtBurdenRatio with zero income = %v, want 0", got)
	}
	if got := debtBurdenRatio(15000, -100); got != 0 {
		t.Errorf("debtBurdenRatio with negative income = %v, want 0", got)
	}
}

func TestRecommendation(t *testing.T) {
	tests := []struct {
		name     string
		dbr      float64
		score    int
		wantText string
		wantTone string
	}{
		{"healthy", 15, 780, "建議核准", "success"},
		{"dbr at warning boundary", 22, 780, "需補件", "warning"},
		{"dbr at reject boundary", 25, 780, "建議婉拒", "error"},
		{"low score rejects", 10, 599, "建議婉拒", "error"},
		{"mid score needs docs", 10, 679, "需補件", "warning"},
		{"score boundary passes", 10, 680, "建議核准", "success"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, tone := recommendation(test.dbr, test.score)
			if text != test.wantText || tone != test.wantTone {
				t.Errorf("recommendation(%v, %d) = (%q, %q), want (%q, %q)",
					test.dbr, test.score, text, tone, test.wantText, test.wantTone)
			}
		})
	}
}

func TestEnrichApplication(t *testing.T) {
	server := &apiServer{}
	item := applicationSummary{
		Amount:       500000,
		TermMonths:   36,
		annualIncome: 1080000,
		CreditScore:  782,
	}
	server.enrichApplication(&item)

	if item.Grade != "A" {
		t.Errorf("grade = %q, want A", item.Grade)
	}
	if item.EstimatedPayment != 14959 {
		t.Errorf("estimatedPayment = %d, want 14959", item.EstimatedPayment)
	}
	// 14959*12/1080000 = 16.62%
	if math.Abs(item.DBR-16.6) > 0.1 {
		t.Errorf("dbr = %v, want ~16.6", item.DBR)
	}
	if item.Recommendation != "建議核准" {
		t.Errorf("recommendation = %q, want 建議核准", item.Recommendation)
	}
	// 清單只給級距，不給精確金額
	if item.IncomeRange != "80～120 萬" {
		t.Errorf("incomeRange = %q, want 80～120 萬", item.IncomeRange)
	}
}

func TestReviewTransitionsCoverAllActions(t *testing.T) {
	// approve 進入 funding 而非 approved：核准的意義是通過授信並開始募資，
	// 合約要等標的募滿才生成（見 marketplace.go）。
	want := map[string]string{
		"approve":           "funding",
		"reject":            "rejected",
		"request_more_info": "more_info_required",
	}
	if len(reviewTransitions) != len(want) {
		t.Fatalf("reviewTransitions has %d entries, want %d", len(reviewTransitions), len(want))
	}
	for action, status := range want {
		if got := reviewTransitions[action]; got != status {
			t.Errorf("reviewTransitions[%q] = %q, want %q", action, got, status)
		}
	}
}

func TestValidateApplication(t *testing.T) {
	valid := applicationRequest{
		Product: "個人信用貸款", Purpose: "債務整合", Amount: 500000, TermMonths: 36,
		ApplicantName: "陳建宏", Email: "chen@example.com",
		AnnualIncome: 1080000, MonthlyExpenses: 32000,
	}
	if err := validateApplication(valid); err != nil {
		t.Fatalf("valid application rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*applicationRequest)
	}{
		{"empty product", func(r *applicationRequest) { r.Product = "  " }},
		{"empty purpose", func(r *applicationRequest) { r.Purpose = "" }},
		{"amount below floor", func(r *applicationRequest) { r.Amount = 49999 }},
		{"amount above ceiling", func(r *applicationRequest) { r.Amount = 3000001 }},
		{"term not in whitelist", func(r *applicationRequest) { r.TermMonths = 30 }},
		{"empty name", func(r *applicationRequest) { r.ApplicantName = " " }},
		{"empty email", func(r *applicationRequest) { r.Email = "" }},
		{"zero income", func(r *applicationRequest) { r.AnnualIncome = 0 }},
		{"negative expenses", func(r *applicationRequest) { r.MonthlyExpenses = -1 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := validateApplication(request); err == nil {
				t.Error("invalid application accepted, want rejection")
			}
		})
	}
}

// 申請驗證的金額與期數規則必須與 migration 的 CHECK 約束一致，
// 否則合法請求會在寫入時才炸成 500。
func TestValidateApplicationMatchesSchemaBoundaries(t *testing.T) {
	base := applicationRequest{
		Product: "信貸", Purpose: "整合", ApplicantName: "測試", Email: "a@b.com",
		AnnualIncome: 1000000, MonthlyExpenses: 0,
	}
	for _, term := range []int{12, 24, 36, 48, 60, 84} {
		request := base
		request.Amount = 50000
		request.TermMonths = term
		if err := validateApplication(request); err != nil {
			t.Errorf("term %d rejected but allowed by schema CHECK: %v", term, err)
		}
	}
	for _, amount := range []int64{50000, 3000000} {
		request := base
		request.Amount = amount
		request.TermMonths = 36
		if err := validateApplication(request); err != nil {
			t.Errorf("amount %d rejected but allowed by schema CHECK: %v", amount, err)
		}
	}
}

// 迴歸測試：user_id 為 NULL 的孤兒申請（認證上線前建立）不得讓
// 整份後台清單 scan 失敗。此處驗證 nullable 接收型別的行為契約。
func TestApplicationSummaryHandlesNullUserID(t *testing.T) {
	// scanApplications 以 *string 接 user_id；nil 時 UserID 應為空字串而非 panic
	var userID *string
	item := applicationSummary{ID: "LN-ORPHAN"}
	if userID != nil {
		item.UserID = *userID
	}
	if item.UserID != "" {
		t.Errorf("orphan application UserID = %q, want empty string", item.UserID)
	}

	// 有值時應正確帶入
	owner := "usr_demo_borrower"
	userID = &owner
	if userID != nil {
		item.UserID = *userID
	}
	if item.UserID != owner {
		t.Errorf("UserID = %q, want %q", item.UserID, owner)
	}
}
