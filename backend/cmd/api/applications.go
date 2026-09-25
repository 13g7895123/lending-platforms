package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 依信用等級對應的年利率，與前端 rateOptions 一致。
const (
	rateGradeA = 4.88
	rateGradeB = 6.80
	rateGradeC = 9.60
)

type applicationSummary struct {
	ID            string `json:"id"`
	UserID        string `json:"userId"`
	ApplicantName string `json:"applicantName"`
	Product       string `json:"product"`
	Purpose       string `json:"purpose"`
	Amount        int64  `json:"amount"`
	TermMonths    int    `json:"termMonths"`
	// 收入與支出不以原值回傳：前端不顯示它們，但每筆回應都帶著會擴大曝露面。
	// 風控判讀所需的是 DBR 與級距；完整值走 reveal 稽核端點。
	IncomeRange  string `json:"incomeRange"`
	ExpenseRange string `json:"expenseRange"`

	// 僅供後端計算 DBR，不序列化到回應
	annualIncome    int64
	monthlyExpenses int64
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`

	// PII 一律以遮罩形式回傳；完整值需經稽核端點取得
	IDNumberMasked string `json:"idNumberMasked"`
	PhoneMasked    string `json:"phoneMasked"`

	// 核准後的募資進度；尚未上架時 ListingID 為空
	ListingID     string `json:"listingId"`
	FundedAmount  int64  `json:"fundedAmount"`
	FundedPercent int    `json:"fundedPercent"`

	// 由後端試算，供風控後台判讀
	EstimatedPayment int64   `json:"estimatedPayment"`
	DBR              float64 `json:"dbr"`
	CreditScore      int     `json:"creditScore"`
	Grade            string  `json:"grade"`
	Recommendation   string  `json:"recommendation"`
	RecommendTone    string  `json:"recommendTone"`
}

type reviewRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// gradeForScore 以信用評分決定等級與適用利率。
func gradeForScore(score int) (string, float64) {
	switch {
	case score >= 750:
		return "A", rateGradeA
	case score >= 650:
		return "B", rateGradeB
	default:
		return "C", rateGradeC
	}
}

// debtBurdenRatio 計算負債比（月付金年化 / 年收入），與前端 applicationDbr 同義。
func debtBurdenRatio(monthlyPay float64, annualIncome int64) float64 {
	if annualIncome <= 0 {
		return 0
	}
	return monthlyPay * 12 / float64(annualIncome) * 100
}

// recommendation 依 DBR 與信用評分給出建議，門檻與前端展示一致（DBR 22% / 評分 600）。
func recommendation(dbr float64, score int) (string, string) {
	switch {
	case dbr >= 25 || score < 600:
		return "建議婉拒", "error"
	case dbr >= 22 || score < 680:
		return "需補件", "warning"
	default:
		return "建議核准", "success"
	}
}

func (s *apiServer) enrichApplication(item *applicationSummary) {
	grade, rate := gradeForScore(item.CreditScore)
	pay := monthlyPayment(float64(item.Amount), rate, item.TermMonths)
	item.Grade = grade
	item.EstimatedPayment = int64(math.Round(pay))
	item.DBR = math.Round(debtBurdenRatio(pay, item.annualIncome)*10) / 10
	item.Recommendation, item.RecommendTone = recommendation(item.DBR, item.CreditScore)
	item.IncomeRange = incomeBracket(item.annualIncome)
	item.ExpenseRange = expenseBracket(item.monthlyExpenses)
}

// ---------------------------------------------------------------- 建立申請

func (s *apiServer) createApplication(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	// 貸款申請涉及授信與撥款，必須先確認信箱屬於本人。
	// 登入不受限制，因此使用者仍可登入並要求重寄驗證信。
	if !actor.EmailVerified {
		writeError(w, http.StatusForbidden,
			"請先完成電子信箱驗證後再送出貸款申請")
		return
	}

	var request applicationRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if err := validateApplication(request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	documents, err := json.Marshal(request.Documents)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid documents")
		return
	}

	// 身分證與電話屬敏感個資，加密後才落地；明文欄位保持空字串
	idNumberEnc, err := s.pii.encrypt(strings.TrimSpace(request.IDNumber))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to protect applicant data")
		return
	}
	phoneEnc, err := s.pii.encrypt(strings.TrimSpace(request.Phone))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to protect applicant data")
		return
	}

	id := newApplicationID()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var createdAt time.Time
	err = s.db.QueryRow(ctx, `
		INSERT INTO applications (
			id, user_id, product, amount, term_months, purpose, applicant_name,
			id_number, phone, id_number_enc, phone_enc,
			email, job, employment_years, annual_income, monthly_expenses,
			housing, note, documents, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, '', '', $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, 'pending')
		RETURNING created_at
	`, id, actor.ID, request.Product, request.Amount, request.TermMonths, request.Purpose,
		request.ApplicantName, idNumberEnc, phoneEnc, request.Email, request.Job,
		request.EmploymentYears, request.AnnualIncome, request.MonthlyExpenses,
		request.Housing, request.Note, documents).Scan(&createdAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create application")
		return
	}

	writeJSON(w, http.StatusCreated, applicationResponse{ID: id, Status: "pending", CreatedAt: createdAt})
}

// ---------------------------------------------------------------- 查詢申請

const applicationSelectColumns = `
	a.id, a.user_id, a.applicant_name, a.product, a.purpose, a.amount, a.term_months,
	a.annual_income, a.monthly_expenses, a.status, a.created_at,
	COALESCE(u.credit_score, 700), a.id_number_enc, a.phone_enc,
	COALESCE(l.id, ''), COALESCE(l.funded_amount, 0)
`

// maskDecrypted 解密後套用遮罩；空值回傳空字串，解密失敗回傳固定提示。
func maskDecrypted(pii *piiCipher, payload []byte, mask func(string) string) string {
	if len(payload) == 0 {
		return ""
	}
	plain, err := pii.decrypt(payload)
	if err != nil {
		return "(無法讀取)"
	}
	return mask(plain)
}

func scanApplications(pii *piiCipher, rows pgx.Rows) ([]applicationSummary, error) {
	items := make([]applicationSummary, 0)
	for rows.Next() {
		var item applicationSummary
		// user_id 為 nullable：認證機制上線前建立的申請沒有歸屬使用者，
		// 這類孤兒案件仍須能在後台列出（否則整份清單會失敗）。
		var userID *string
		var idNumberEnc, phoneEnc []byte
		if err := rows.Scan(
			&item.ID, &userID, &item.ApplicantName, &item.Product, &item.Purpose,
			&item.Amount, &item.TermMonths, &item.annualIncome, &item.monthlyExpenses,
			&item.Status, &item.CreatedAt, &item.CreditScore, &idNumberEnc, &phoneEnc,
			&item.ListingID, &item.FundedAmount,
		); err != nil {
			return nil, err
		}
		item.FundedPercent = fundedPercent(item.FundedAmount, item.Amount)
		if userID != nil {
			item.UserID = *userID
		}
		// 解密失敗不讓整份清單失敗：該欄位以 (無法讀取) 呈現並交由稽核追查
		item.IDNumberMasked = maskDecrypted(pii, idNumberEnc, maskIDNumber)
		item.PhoneMasked = maskDecrypted(pii, phoneEnc, maskPhone)
		items = append(items, item)
	}
	return items, rows.Err()
}

// myApplications 回傳當前使用者自己的申請（借款人視角）。
func (s *apiServer) myApplications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)
	page := parsePageParams(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var total int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM applications WHERE user_id = $1`, actor.ID).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count applications")
		return
	}

	rows, err := s.db.Query(ctx, `
		SELECT `+applicationSelectColumns+`
		FROM applications a
		LEFT JOIN users u ON u.id = a.user_id
		LEFT JOIN listings l ON l.application_id = a.id
		WHERE a.user_id = $1
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $2 OFFSET $3
	`, actor.ID, page.Limit, page.Offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load applications")
		return
	}
	defer rows.Close()

	items, err := scanApplications(s.pii, rows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode applications")
		return
	}
	for i := range items {
		s.enrichApplication(&items[i])
	}
	writeJSON(w, http.StatusOK, paginatedApplications{Items: items, Page: page.meta(total)})
}

// paginatedApplications 是分頁後的申請清單。
type paginatedApplications struct {
	Items []applicationSummary `json:"items"`
	Page  pageMeta             `json:"page"`
}

// adminApplications 回傳待審清單（風控視角），可用 ?status= 篩選。
func (s *apiServer) adminApplications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	page := parsePageParams(r)

	var total int
	if err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM applications
		WHERE ($1::text = '' OR status = $1::text)
	`, status).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count applications")
		return
	}

	rows, err := s.db.Query(ctx, `
		SELECT `+applicationSelectColumns+`
		FROM applications a
		LEFT JOIN users u ON u.id = a.user_id
		LEFT JOIN listings l ON l.application_id = a.id
		WHERE ($1::text = '' OR a.status = $1::text)
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $2 OFFSET $3
	`, status, page.Limit, page.Offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load applications")
		return
	}
	defer rows.Close()

	items, err := scanApplications(s.pii, rows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode applications")
		return
	}
	for i := range items {
		s.enrichApplication(&items[i])
	}
	writeJSON(w, http.StatusOK, paginatedApplications{Items: items, Page: page.meta(total)})
}

// ---------------------------------------------------------------- 審核

// approve 後直接進入 funding：核准的意義是「通過授信、開始募資」，
// approved 只是一個瞬間狀態，不需要讓它停留在資料庫中。
var reviewTransitions = map[string]string{
	"approve":           "funding",
	"reject":            "rejected",
	"request_more_info": "more_info_required",
}

// reviewApplication 執行審核動作。核准時於同一 transaction 生成貸款合約與攤還表，
// 確保「已核准但沒有合約」的中間狀態不可能出現。
func (s *apiServer) reviewApplication(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	applicationID := strings.TrimPrefix(r.URL.Path, "/v1/admin/applications/")
	applicationID = strings.Trim(applicationID, "/")
	if applicationID == "" {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	// {id}/reveal 走 PII 稽核揭露；其餘不接受多層路徑
	if id, suffix, found := strings.Cut(applicationID, "/"); found {
		if suffix != "reveal" || id == "" {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		s.revealApplicationPII(w, r, id)
		return
	}

	var request reviewRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	toStatus, ok := reviewTransitions[request.Action]
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "action must be approve, reject, or request_more_info")
		return
	}
	if len(request.Reason) > 1000 {
		writeError(w, http.StatusUnprocessableEntity, "reason is too long")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	transaction, err := s.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// 鎖定該筆申請，避免兩位風控人員同時審核造成重複發約
	var current applicationSummary
	var currentUserID *string
	err = transaction.QueryRow(ctx, `
		SELECT a.id, a.user_id, a.applicant_name, a.product, a.purpose, a.amount, a.term_months,
		       a.annual_income, a.monthly_expenses, a.status, a.created_at, COALESCE(u.credit_score, 700)
		FROM applications a
		LEFT JOIN users u ON u.id = a.user_id
		WHERE a.id = $1
		FOR UPDATE OF a
	`, applicationID).Scan(
		&current.ID, &currentUserID, &current.ApplicantName, &current.Product, &current.Purpose,
		&current.Amount, &current.TermMonths, &current.annualIncome, &current.monthlyExpenses,
		&current.Status, &current.CreatedAt, &current.CreditScore)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load application")
		return
	}

	// 已做出最終決定或已進入募資流程的案件不可再審
	switch current.Status {
	case "approved", "funding", "funded", "disbursed", "rejected":
		writeError(w, http.StatusConflict, fmt.Sprintf("application is already %s", current.Status))
		return
	}
	if currentUserID == nil {
		// 認證機制上線前留下的孤兒申請：無從判斷合約歸屬，不得核准成合約。
		// 這類案件只能婉拒或要求補件（由申請人重新以帳號送出）。
		if request.Action == "approve" {
			writeError(w, http.StatusConflict,
				"application has no owning user and cannot be approved; ask the applicant to resubmit")
			return
		}
	} else {
		current.UserID = *currentUserID
	}

	if _, err := transaction.Exec(ctx,
		`UPDATE applications SET status = $2, updated_at = NOW() WHERE id = $1`,
		applicationID, toStatus); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update application")
		return
	}

	if _, err := transaction.Exec(ctx, `
		INSERT INTO application_reviews (application_id, reviewer_id, action, from_status, to_status, reason)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, applicationID, actor.ID, request.Action, current.Status, toStatus, request.Reason); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record review")
		return
	}

	response := map[string]any{
		"id":         applicationID,
		"status":     toStatus,
		"reviewedBy": actor.DisplayName,
	}

	if request.Action == "approve" {
		// 核准不再直接發約：先上架募資標的，募滿後才撥款生成合約。
		// 這讓資金來源成為流程的一部分，而非憑空生成合約。
		createdListing, err := createListingFromApplication(ctx, transaction, current)
		if err != nil {
			writeInternalError(w, "failed to create funding listing", err)
			return
		}
		response["listing"] = createdListing
		response["status"] = "funding"
	}

	if err := transaction.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit review")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// createLoanFromApplication 由已核准的申請生成貸款合約與完整攤還表。
func createLoanFromApplication(ctx context.Context, transaction pgx.Tx, application applicationSummary) (*loan, error) {
	_, rate := gradeForScore(application.CreditScore)
	pay := int64(math.Round(monthlyPayment(float64(application.Amount), rate, application.TermMonths)))

	loanID := newApplicationID()
	created := loan{
		ID:                loanID,
		Product:           application.Product,
		Amount:            application.Amount,
		Rate:              rate,
		PaidInstallments:  0,
		TotalInstallments: application.TermMonths,
		Status:            "正常繳款",
		StatusTone:        "success",
		MonthlyPayment:    pay,
	}

	if _, err := transaction.Exec(ctx, `
		INSERT INTO loans (
			id, user_id, application_id, product, amount, annual_rate,
			paid_installments, total_installments, status, status_tone, monthly_payment
		) VALUES ($1, $2, $3, $4, $5, $6, 0, $7, '正常繳款', 'success', $8)
	`, loanID, application.UserID, application.ID, application.Product,
		application.Amount, rate, application.TermMonths, pay); err != nil {
		return nil, fmt.Errorf("insert loan: %w", err)
	}

	schedule := buildAmortizationSchedule(loanID, application.Amount, rate, application.TermMonths, nextFirstDueDate(time.Now().UTC()))
	for _, row := range schedule {
		status := row.Status
		if row.Number == 1 {
			status = "due"
		}
		if _, err := transaction.Exec(ctx, `
			INSERT INTO loan_installments (
				loan_id, installment_no, due_date, amount_due, principal, interest, remaining_balance, status
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, loanID, row.Number, row.DueDate, row.AmountDue, row.Principal,
			row.Interest, row.RemainingBalance, status); err != nil {
			return nil, fmt.Errorf("insert installment %d: %w", row.Number, err)
		}
	}

	return &created, nil
}

// ---------------------------------------------------------------- PII 稽核揭露

type revealResponse struct {
	ApplicationID string `json:"applicationId"`
	IDNumber      string `json:"idNumber"`
	Phone         string `json:"phone"`
	// 清單只給級距，完整金額僅在此揭露並記入稽核
	AnnualIncome    int64 `json:"annualIncome"`
	MonthlyExpenses int64 `json:"monthlyExpenses"`
}

// revealApplicationPII 供風控人員在必要時取得完整 PII，每次呼叫都寫入稽核紀錄。
// 路由：POST /v1/admin/applications/{id}/reveal（reviewer only）
//
// 要求帶上 reason，讓稽核紀錄具備可追查的業務理由。
func (s *apiServer) revealApplicationPII(w http.ResponseWriter, r *http.Request, applicationID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	var request struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		writeError(w, http.StatusUnprocessableEntity, "reason is required to reveal personal data")
		return
	}
	if len(reason) > 500 {
		writeError(w, http.StatusUnprocessableEntity, "reason is too long")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var idNumberEnc, phoneEnc []byte
	var annualIncome, monthlyExpenses int64
	err := s.db.QueryRow(ctx, `
		SELECT id_number_enc, phone_enc, annual_income, monthly_expenses
		FROM applications WHERE id = $1
	`, applicationID).Scan(&idNumberEnc, &phoneEnc, &annualIncome, &monthlyExpenses)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load application")
		return
	}

	idNumber, err := s.pii.decrypt(idNumberEnc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt personal data")
		return
	}
	phone, err := s.pii.decrypt(phoneEnc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt personal data")
		return
	}

	// 稽核先寫入再回傳：寧可留下未被使用的紀錄，也不可揭露而無紀錄
	if _, err := s.db.Exec(ctx, `
		INSERT INTO pii_access_log (application_id, accessed_by, field, reason)
		VALUES ($1, $2, 'id_number,phone,annual_income,monthly_expenses', $3)
	`, applicationID, actor.ID, reason); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record access log")
		return
	}

	writeJSON(w, http.StatusOK, revealResponse{
		ApplicationID:   applicationID,
		IDNumber:        idNumber,
		Phone:           phone,
		AnnualIncome:    annualIncome,
		MonthlyExpenses: monthlyExpenses,
	})
}
