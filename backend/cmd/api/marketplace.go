package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type listingItem struct {
	ID            string     `json:"id"`
	ApplicationID string     `json:"applicationId"`
	Purpose       string     `json:"purpose"`
	Grade         string     `json:"grade"`
	Rate          float64    `json:"rate"`
	TargetAmount  int64      `json:"targetAmount"`
	FundedAmount  int64      `json:"fundedAmount"`
	Term          int        `json:"termMonths"`
	Job           string     `json:"job"`
	Years         string     `json:"employmentYears"`
	Region        string     `json:"region"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"createdAt"`
	FundedAt      *time.Time `json:"fundedAt"`
	Deadline      time.Time  `json:"fundingDeadline"`

	// 由後端計算，避免前端各自推導出不一致的數字
	FundedPercent    int   `json:"fundedPercent"`
	RemainingAmount  int64 `json:"remainingAmount"`
	MonthlyPayment   int64 `json:"monthlyPayment"`
	InvestorCount    int   `json:"investorCount"`
	MyInvestedAmount int64 `json:"myInvestedAmount"`
	// 距離募資截止的天數；負數表示已逾期（尚未被排程掃到）
	DaysRemaining int `json:"daysRemaining"`
}

type investment struct {
	ID        int64     `json:"id"`
	ListingID string    `json:"listingId"`
	Amount    int64     `json:"amount"`
	CreatedAt time.Time `json:"createdAt"`
	Purpose   string    `json:"purpose"`
	Grade     string    `json:"grade"`
	Rate      float64   `json:"rate"`
	Term      int       `json:"termMonths"`
	Status    string    `json:"status"`
	LoanID    *string   `json:"loanId"`
	// 依投標金額與年化利率估算的整筆收益
	EstimatedReturn int64 `json:"estimatedReturn"`
	// 實際已收回的金額，由借款人還款時按占比分潤累計
	PrincipalReturned int64 `json:"principalReturned"`
	InterestEarned    int64 `json:"interestEarned"`
	// 尚未收回的本金
	OutstandingPrincipal int64 `json:"outstandingPrincipal"`
}

// 單筆投標的金額限制。真實平台會依出借人資格分級，此處採固定門檻。
const (
	minInvestmentAmount = 1000
	maxInvestmentAmount = 1000000
)

// fundedPercent 計算募集進度，四捨五入到整數百分比。
// 未達目標時不回傳 100，避免前端顯示「已滿」卻仍可投標。
func fundedPercent(funded, target int64) int {
	if target <= 0 {
		return 0
	}
	if funded >= target {
		return 100
	}
	percent := int(math.Round(float64(funded) / float64(target) * 100))
	if percent >= 100 {
		return 99
	}
	return percent
}

// estimatedInvestmentReturn 估算一筆投標在整個期間的利息收益。
// 以本息平均攤還的總繳款減去本金，再依投標占比分攤。
func estimatedInvestmentReturn(amount int64, annualRate float64, months int) int64 {
	if amount <= 0 || months <= 0 {
		return 0
	}
	totalPaid := monthlyPayment(float64(amount), annualRate, months) * float64(months)
	return int64(math.Round(totalPaid - float64(amount)))
}

// investmentIdempotencyKey 產生投標的冪等鍵。
//
// 與還款不同，同一人可對同一標的多次投標，因此鍵必須包含客戶端提供的
// requestID；沒有提供時退回時間戳，此時不具冪等保證（由呼叫端負責）。
func investmentIdempotencyKey(listingID, investorID, requestID string) string {
	if strings.TrimSpace(requestID) == "" {
		requestID = fmt.Sprintf("auto-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("invest:%s:%s:%s", listingID, investorID, requestID)
}

// ---------------------------------------------------------------- 市集列表

// listings 回傳募資中的標的。公開端點，但登入時會附帶「我已投入多少」。
func (s *apiServer) marketplaceListings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 未登入時以空字串比對，my_invested 一律為 0
	viewerID := ""
	if actor := currentUser(r); actor != nil {
		viewerID = actor.ID
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = "funding"
	}

	rows, err := s.db.Query(ctx, `
		SELECT l.id, l.application_id, l.purpose, l.grade, l.annual_rate,
		       l.target_amount, l.funded_amount, l.term_months,
		       l.job, l.employment_years, l.region, l.status, l.created_at, l.funded_at,
		       l.funding_deadline,
		       COALESCE(stats.investor_count, 0),
		       COALESCE(mine.amount, 0)
		FROM listings l
		LEFT JOIN (
			SELECT listing_id, COUNT(DISTINCT investor_id) AS investor_count
			FROM investments GROUP BY listing_id
		) stats ON stats.listing_id = l.id
		LEFT JOIN (
			SELECT listing_id, SUM(amount) AS amount
			FROM investments WHERE investor_id = $2 GROUP BY listing_id
		) mine ON mine.listing_id = l.id
		WHERE ($1 = 'all' OR l.status = $1)
		ORDER BY l.created_at DESC, l.id
		LIMIT 200
	`, status, viewerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load listings")
		return
	}
	defer rows.Close()

	items := make([]listingItem, 0)
	for rows.Next() {
		var item listingItem
		if err := rows.Scan(&item.ID, &item.ApplicationID, &item.Purpose, &item.Grade,
			&item.Rate, &item.TargetAmount, &item.FundedAmount, &item.Term,
			&item.Job, &item.Years, &item.Region, &item.Status, &item.CreatedAt,
			&item.FundedAt, &item.Deadline, &item.InvestorCount, &item.MyInvestedAmount); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode listings")
			return
		}
		item.FundedPercent = fundedPercent(item.FundedAmount, item.TargetAmount)
		item.RemainingAmount = item.TargetAmount - item.FundedAmount
		item.MonthlyPayment = int64(math.Round(
			monthlyPayment(float64(item.TargetAmount), item.Rate, item.Term)))
		item.DaysRemaining = daysUntilDeadline(item.Deadline, time.Now())
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read listings")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// ---------------------------------------------------------------- 投標

type investRequest struct {
	Amount    int64  `json:"amount"`
	RequestID string `json:"requestId"`
}

type investResponse struct {
	Investment   investment  `json:"investment"`
	Listing      listingItem `json:"listing"`
	Balance      int64       `json:"balance"`
	FullyFunded  bool        `json:"fullyFunded"`
	DisbursedLoa *loan       `json:"disbursedLoan"`
}

// investInListing 執行投標。
//
// 全程在單一 transaction 內完成：鎖定標的 → 檢查額度與餘額 → 寫入投標
// → 扣款 → 更新募集金額 → 滿額時撥款。任一步失敗全部回滾，
// 不會出現「扣了錢但沒投標」或「募滿但沒撥款」的中間狀態。
func (s *apiServer) investInListing(w http.ResponseWriter, r *http.Request, listingID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	var request investRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Amount < minInvestmentAmount {
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("amount must be at least %d", minInvestmentAmount))
		return
	}
	if request.Amount > maxInvestmentAmount {
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("amount must not exceed %d per investment", maxInvestmentAmount))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	transaction, err := s.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// 鎖定標的，避免併發投標造成超募
	var target listingItem
	err = transaction.QueryRow(ctx, `
		SELECT id, application_id, purpose, grade, annual_rate, target_amount,
		       funded_amount, term_months, job, employment_years, region, status, created_at
		FROM listings WHERE id = $1 FOR UPDATE
	`, listingID).Scan(&target.ID, &target.ApplicationID, &target.Purpose, &target.Grade,
		&target.Rate, &target.TargetAmount, &target.FundedAmount, &target.Term,
		&target.Job, &target.Years, &target.Region, &target.Status, &target.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "listing not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load listing")
		return
	}

	if target.Status != "funding" {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("listing is no longer accepting investments (status: %s)", target.Status))
		return
	}

	// 期限已過但排程尚未掃到時也要拒絕，否則會出現「投進即將被退款的標的」
	var deadline time.Time
	if err := transaction.QueryRow(ctx,
		`SELECT funding_deadline FROM listings WHERE id = $1`, listingID).Scan(&deadline); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load funding deadline")
		return
	}
	if !time.Now().Before(deadline) {
		writeError(w, http.StatusConflict, "the funding period for this listing has ended")
		return
	}

	// 借款人不得投資自己的標的
	var borrowerID string
	if err := transaction.QueryRow(ctx,
		`SELECT borrower_id FROM listings WHERE id = $1`, listingID).Scan(&borrowerID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load listing owner")
		return
	}
	if borrowerID == actor.ID {
		writeError(w, http.StatusForbidden, "you cannot invest in your own listing")
		return
	}

	remaining := target.TargetAmount - target.FundedAmount
	if remaining <= 0 {
		writeError(w, http.StatusConflict, "listing is already fully funded")
		return
	}
	// 超額投標只接受剩餘額度，不拒絕整筆——對出借人較友善，且避免超募
	accepted := request.Amount
	if accepted > remaining {
		accepted = remaining
	}

	// 鎖定出借人餘額，避免併發投標超支
	var balance int64
	if err := transaction.QueryRow(ctx,
		`SELECT available_balance FROM users WHERE id = $1 FOR UPDATE`, actor.ID).Scan(&balance); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load balance")
		return
	}
	if balance < accepted {
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("insufficient balance: need %d, have %d", accepted, balance))
		return
	}

	key := investmentIdempotencyKey(listingID, actor.ID, request.RequestID)
	var created investment
	err = transaction.QueryRow(ctx, `
		INSERT INTO investments (listing_id, investor_id, amount, idempotency_key)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at
	`, listingID, actor.ID, accepted, key).Scan(&created.ID, &created.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this investment was already submitted")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record investment")
		return
	}

	if _, err := transaction.Exec(ctx,
		`UPDATE users SET available_balance = available_balance - $2 WHERE id = $1`,
		actor.ID, accepted); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to debit balance")
		return
	}

	// listings_not_oversubscribed CHECK 是最後一道防線：
	// 即使上面的計算有誤，資料庫也不會讓 funded_amount 超過 target_amount
	var newFunded int64
	if err := transaction.QueryRow(ctx, `
		UPDATE listings SET funded_amount = funded_amount + $2 WHERE id = $1
		RETURNING funded_amount
	`, listingID, accepted).Scan(&newFunded); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update listing")
		return
	}

	response := investResponse{Balance: balance - accepted}
	fullyFunded := newFunded >= target.TargetAmount

	if fullyFunded {
		disbursed, err := disburseListing(ctx, transaction, listingID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to disburse loan")
			return
		}
		response.DisbursedLoa = disbursed
		response.FullyFunded = true
	}

	if err := transaction.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit investment")
		return
	}

	created.ListingID = listingID
	created.Amount = accepted
	created.Purpose = target.Purpose
	created.Grade = target.Grade
	created.Rate = target.Rate
	created.Term = target.Term
	created.EstimatedReturn = estimatedInvestmentReturn(accepted, target.Rate, target.Term)

	target.FundedAmount = newFunded
	target.FundedPercent = fundedPercent(newFunded, target.TargetAmount)
	target.RemainingAmount = target.TargetAmount - newFunded
	if fullyFunded {
		target.Status = "disbursed"
	}

	response.Investment = created
	response.Listing = target
	writeJSON(w, http.StatusCreated, response)
}

// disburseListing 在標的募滿時撥款：生成合約與攤還表，並推進所有相關狀態。
// 呼叫端必須已鎖定該標的。
func disburseListing(ctx context.Context, transaction pgx.Tx, listingID string) (*loan, error) {
	var application applicationSummary
	var listingRate float64
	err := transaction.QueryRow(ctx, `
		SELECT a.id, a.user_id, a.applicant_name, a.product, a.purpose, a.amount,
		       a.term_months, a.annual_income, a.monthly_expenses, a.status, a.created_at,
		       COALESCE(u.credit_score, 700), l.annual_rate
		FROM listings l
		JOIN applications a ON a.id = l.application_id
		LEFT JOIN users u ON u.id = a.user_id
		WHERE l.id = $1
	`, listingID).Scan(&application.ID, &application.UserID, &application.ApplicantName,
		&application.Product, &application.Purpose, &application.Amount,
		&application.TermMonths, &application.annualIncome, &application.monthlyExpenses,
		&application.Status, &application.CreatedAt, &application.CreditScore, &listingRate)
	if err != nil {
		return nil, fmt.Errorf("load application for listing: %w", err)
	}

	// 沿用既有的合約生成邏輯，確保攤還表計算只有一處實作
	created, err := createLoanFromApplication(ctx, transaction, application)
	if err != nil {
		return nil, err
	}

	if _, err := transaction.Exec(ctx, `
		UPDATE listings
		SET status = 'disbursed', funded_at = COALESCE(funded_at, NOW()), disbursed_at = NOW()
		WHERE id = $1
	`, listingID); err != nil {
		return nil, fmt.Errorf("mark listing disbursed: %w", err)
	}

	if _, err := transaction.Exec(ctx,
		`UPDATE loans SET listing_id = $2 WHERE id = $1`, created.ID, listingID); err != nil {
		return nil, fmt.Errorf("link loan to listing: %w", err)
	}

	if _, err := transaction.Exec(ctx,
		`UPDATE applications SET status = 'disbursed', updated_at = NOW() WHERE id = $1`,
		application.ID); err != nil {
		return nil, fmt.Errorf("mark application disbursed: %w", err)
	}

	return created, nil
}

// ---------------------------------------------------------------- 出借人持倉

type portfolioResponse struct {
	Balance         int64   `json:"balance"`
	TotalInvested   int64   `json:"totalInvested"`
	ActiveCount     int     `json:"activeCount"`
	EstimatedReturn int64   `json:"estimatedReturn"`
	WeightedRate    float64 `json:"weightedRate"`

	// 實際已收回的金額（與 EstimatedReturn 的差距即為尚未實現的部分）
	TotalPrincipalReturned int64 `json:"totalPrincipalReturned"`
	TotalInterestEarned    int64 `json:"totalInterestEarned"`
	OutstandingPrincipal   int64 `json:"outstandingPrincipal"`

	Investments []investment `json:"investments"`
}

// myInvestments 回傳出借人的投標紀錄與彙總。
func (s *apiServer) myInvestments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT i.id, i.listing_id, i.amount, i.created_at,
		       l.purpose, l.grade, l.annual_rate, l.term_months, l.status,
		       loans.id, i.principal_returned, i.interest_earned
		FROM investments i
		JOIN listings l ON l.id = i.listing_id
		LEFT JOIN loans ON loans.listing_id = l.id
		WHERE i.investor_id = $1
		ORDER BY i.created_at DESC, i.id DESC
	`, actor.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load investments")
		return
	}
	defer rows.Close()

	response := portfolioResponse{Investments: make([]investment, 0)}
	var weightedRateSum float64

	for rows.Next() {
		var item investment
		if err := rows.Scan(&item.ID, &item.ListingID, &item.Amount, &item.CreatedAt,
			&item.Purpose, &item.Grade, &item.Rate, &item.Term, &item.Status,
			&item.LoanID, &item.PrincipalReturned, &item.InterestEarned); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode investments")
			return
		}
		item.EstimatedReturn = estimatedInvestmentReturn(item.Amount, item.Rate, item.Term)
		item.OutstandingPrincipal = item.Amount - item.PrincipalReturned
		if item.OutstandingPrincipal < 0 {
			item.OutstandingPrincipal = 0
		}

		response.TotalInvested += item.Amount
		response.EstimatedReturn += item.EstimatedReturn
		response.TotalPrincipalReturned += item.PrincipalReturned
		response.TotalInterestEarned += item.InterestEarned
		response.OutstandingPrincipal += item.OutstandingPrincipal
		weightedRateSum += item.Rate * float64(item.Amount)
		if item.Status != "cancelled" {
			response.ActiveCount++
		}
		response.Investments = append(response.Investments, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read investments")
		return
	}

	// 以投標金額加權的平均年化，單純平均會讓小額標的過度影響結果
	if response.TotalInvested > 0 {
		response.WeightedRate = math.Round(
			weightedRateSum/float64(response.TotalInvested)*100) / 100
	}

	if err := s.db.QueryRow(ctx,
		`SELECT available_balance FROM users WHERE id = $1`, actor.ID).Scan(&response.Balance); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load balance")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

// ---------------------------------------------------------------- 入金

const maxTopUpAmount = 10000000

// topUpBalance 為出借人帳戶入金。
//
// 展示用端點：真實平台必須接金流閘道並核對入帳，此處直接增加餘額。
// 已在 README 標註為未接金流。
func (s *apiServer) topUpBalance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	var request struct {
		Amount int64 `json:"amount"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Amount <= 0 || request.Amount > maxTopUpAmount {
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("amount must be between 1 and %d", maxTopUpAmount))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var balance int64
	if err := s.db.QueryRow(ctx, `
		UPDATE users SET available_balance = available_balance + $2, updated_at = NOW()
		WHERE id = $1 RETURNING available_balance
	`, actor.ID, request.Amount).Scan(&balance); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to top up balance")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"balance": balance,
		"note":    "展示用入金，未接金流閘道",
	})
}

// listingRoutes 分派 /v1/listings/ 底下的子路由。
func (s *apiServer) listingRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/listings/"), "/")
	listingID, suffix, found := strings.Cut(rest, "/")
	if !found || listingID == "" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	if suffix != "invest" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	s.investInListing(w, r, listingID)
}

// createListingFromApplication 由已核准的申請建立募資標的。
// 利率依申請人信用評分決定，與 createLoanFromApplication 使用同一套規則。
func createListingFromApplication(ctx context.Context, transaction pgx.Tx, application applicationSummary) (*listingItem, error) {
	grade, rate := gradeForScore(application.CreditScore)

	var job, years, region string
	if err := transaction.QueryRow(ctx, `
		SELECT COALESCE(job, ''), COALESCE(employment_years, ''), COALESCE(housing, '')
		FROM applications WHERE id = $1
	`, application.ID).Scan(&job, &years, &region); err != nil {
		return nil, fmt.Errorf("load applicant profile: %w", err)
	}

	listingID := "LT-" + strings.TrimPrefix(newApplicationID(), "LN-")
	created := listingItem{
		ID:            listingID,
		ApplicationID: application.ID,
		Purpose:       application.Purpose,
		Grade:         grade,
		Rate:          rate,
		TargetAmount:  application.Amount,
		Term:          application.TermMonths,
		Job:           job,
		Years:         years,
		Region:        region,
		Status:        "funding",
	}

	if err := transaction.QueryRow(ctx, `
		INSERT INTO listings (
			id, application_id, borrower_id, purpose, grade, annual_rate,
			target_amount, funded_amount, term_months, job, employment_years, region,
			status, funding_deadline
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, $9, $10, $11, 'funding',
		          -- 以天數乘上 interval，避免把整數參數塞進字串拼接
		          NOW() + ($12::int * INTERVAL '1 day'))
		RETURNING created_at, funding_deadline
	`, listingID, application.ID, application.UserID, application.Purpose, grade, rate,
		application.Amount, application.TermMonths, job, years, region,
		defaultFundingDays).Scan(&created.CreatedAt, &created.Deadline); err != nil {
		return nil, fmt.Errorf("insert listing: %w", err)
	}

	created.RemainingAmount = created.TargetAmount
	created.MonthlyPayment = int64(math.Round(
		monthlyPayment(float64(created.TargetAmount), rate, created.Term)))
	created.DaysRemaining = daysUntilDeadline(created.Deadline, time.Now())
	return &created, nil
}

// ---------------------------------------------------------------- 收款明細

type distributionRecord struct {
	ID        int64     `json:"id"`
	LoanID    string    `json:"loanId"`
	ListingID string    `json:"listingId"`
	Purpose   string    `json:"purpose"`
	Principal int64     `json:"principal"`
	Interest  int64     `json:"interest"`
	Total     int64     `json:"total"`
	CreatedAt time.Time `json:"createdAt"`
}

// myDistributions 回傳出借人的收款明細（借款人還款時按占比分得的金額）。
func (s *apiServer) myDistributions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT d.id, d.loan_id, l.id, l.purpose, d.principal, d.interest, d.created_at
		FROM distributions d
		JOIN investments i ON i.id = d.investment_id
		JOIN listings l ON l.id = i.listing_id
		WHERE d.investor_id = $1
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT 200
	`, actor.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load distributions")
		return
	}
	defer rows.Close()

	items := make([]distributionRecord, 0)
	for rows.Next() {
		var item distributionRecord
		if err := rows.Scan(&item.ID, &item.LoanID, &item.ListingID, &item.Purpose,
			&item.Principal, &item.Interest, &item.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode distributions")
			return
		}
		item.Total = item.Principal + item.Interest
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read distributions")
		return
	}
	writeJSON(w, http.StatusOK, items)
}
