package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	port                   string
	databaseURL            string
	allowedOrigins         string
	secureCookies          bool
	trustProxy             bool
	appEnv                 string
	piiKey                 string
	storageRoot            string
	publicBaseURL          string
	rateLimitPerMinute     int
	authRateLimitPerMinute int
	connectTimeout         time.Duration
	requestTimeout         time.Duration
}

type apiServer struct {
	db             *pgxpool.Pool
	allowedOrigins string
	secureCookies  bool
	trustProxy     bool
	pii            *piiCipher
	storage        *documentStorage
	ocr            *ocrEngine
	mailer         *mailer
	// 信件中連結使用的對外網址（瀏覽器可達的位址，非容器內位址）
	publicBaseURL string
	limiter       *rateLimiter
	authLimiter   *rateLimiter
}

// dbExecutor 讓同一段邏輯可在連線池或交易中執行。
// pgxpool.Pool 與 pgx.Tx 都滿足此介面。
type dbExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// dbQuerier 是只需查詢的版本。
type dbQuerier interface {
	Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
}

// dbExecutorQuerier 同時需要讀寫，用於必須在 transaction 內完成的邏輯。
type dbExecutorQuerier interface {
	dbExecutor
	dbQuerier
}

type loan struct {
	ID                string  `json:"id"`
	Product           string  `json:"product"`
	Amount            int64   `json:"amount"`
	Rate              float64 `json:"rate"`
	PaidInstallments  int     `json:"paidInstallments"`
	TotalInstallments int     `json:"totalInstallments"`
	Status            string  `json:"status"`
	StatusTone        string  `json:"statusTone"`
	MonthlyPayment    int64   `json:"monthlyPayment"`
}

type dashboardResponse struct {
	Summary struct {
		TotalBorrowed  int64   `json:"totalBorrowed"`
		MonthlyPayment int64   `json:"monthlyPayment"`
		CreditScore    int     `json:"creditScore"`
		RepaymentRate  float64 `json:"repaymentRate"`
	} `json:"summary"`
	Loans []loan `json:"loans"`
}

type listing struct {
	ID      string  `json:"id"`
	Purpose string  `json:"purpose"`
	Grade   string  `json:"grade"`
	Rate    float64 `json:"rate"`
	Amount  int64   `json:"amount"`
	Term    int     `json:"term"`
	Funded  int     `json:"funded"`
	Job     string  `json:"job"`
	Years   string  `json:"years"`
	Region  string  `json:"region"`
}

type applicationRequest struct {
	Product         string   `json:"product"`
	Amount          int64    `json:"amount"`
	TermMonths      int      `json:"termMonths"`
	Purpose         string   `json:"purpose"`
	ApplicantName   string   `json:"applicantName"`
	IDNumber        string   `json:"idNumber"`
	Phone           string   `json:"phone"`
	Email           string   `json:"email"`
	Job             string   `json:"job"`
	EmploymentYears string   `json:"employmentYears"`
	AnnualIncome    int64    `json:"annualIncome"`
	MonthlyExpenses int64    `json:"monthlyExpenses"`
	Housing         string   `json:"housing"`
	Note            string   `json:"note"`
	Documents       []string `json:"documents"`
}

type applicationResponse struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

func main() {
	cfg := loadConfig()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	pool, err := connectDatabase(cfg)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	pii, err := loadPIICipher(cfg, logger)
	if err != nil {
		logger.Error("PII encryption setup failed", "error", err)
		os.Exit(1)
	}

	storage, err := newDocumentStorage(cfg.storageRoot)
	if err != nil {
		logger.Error("document storage setup failed", "error", err, "root", cfg.storageRoot)
		os.Exit(1)
	}

	app := &apiServer{
		db:             pool,
		allowedOrigins: cfg.allowedOrigins,
		secureCookies:  cfg.secureCookies,
		trustProxy:     cfg.trustProxy,
		pii:            pii,
		storage:        storage,
		mailer:         newMailer(logger),
		publicBaseURL:  cfg.publicBaseURL,
		limiter:        newRateLimiter(cfg.rateLimitPerMinute, cfg.rateLimitPerMinute, time.Minute),
		authLimiter:    newRateLimiter(cfg.authRateLimitPerMinute, cfg.authRateLimitPerMinute, time.Minute),
	}

	// PII 遷移失敗不阻止啟動：欄位可能尚未由 migration 建立（deploy.sh 先啟容器後 migrate），
	// 且此為一次性資料搬遷，不影響新資料的加密寫入路徑。
	switch migrated, err := app.migratePlaintextPII(context.Background()); {
	case err != nil:
		logger.Warn("PII migration skipped", "error", err)
	case migrated < 0:
		logger.Info("PII columns not ready yet; migration will run on the next start")
	case migrated > 0:
		logger.Info("encrypted existing plaintext PII", "rows", migrated)
	}

	stopCleanup := app.startSessionCleanup(logger)
	defer stopCleanup()

	stopSweep := app.startOverdueSweep(logger)
	defer stopSweep()

	stopDeadlineSweep := app.startFundingDeadlineSweep(logger)
	defer stopDeadlineSweep()

	stopOCRWorker := app.startOCRWorker(logger)
	defer stopOCRWorker()

	server := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.requestTimeout,
		WriteTimeout:      cfg.requestTimeout,
		IdleTimeout:       60 * time.Second,
	}

	logger.Info("creditflow api started", "port", cfg.port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server stopped", "error", err)
		os.Exit(1)
	}
}

func loadConfig() config {
	connectSeconds := envInt("DB_CONNECT_TIMEOUT_SECONDS", 60)
	requestSeconds := envInt("REQUEST_TIMEOUT_SECONDS", 15)
	return config{
		port:           env("PORT", "8080"),
		databaseURL:    env("DATABASE_URL", "postgres://creditflow:creditflow@localhost:5432/creditflow?sslmode=disable"),
		allowedOrigins: env("CORS_ALLOWED_ORIGINS", "http://localhost:3000"),
		secureCookies:  strings.EqualFold(env("SECURE_COOKIES", "false"), "true"),
		// 本服務只經由 Nuxt proxy 對外，故預設信任 X-Forwarded-For
		trustProxy:             strings.EqualFold(env("TRUST_PROXY", "true"), "true"),
		appEnv:                 env("APP_ENV", "develop"),
		piiKey:                 os.Getenv("PII_ENCRYPTION_KEY"),
		storageRoot:            env("DOCUMENT_STORAGE_ROOT", "/var/lib/creditflow/documents"),
		publicBaseURL:          env("PUBLIC_BASE_URL", "http://localhost:3000"),
		rateLimitPerMinute:     envInt("RATE_LIMIT_PER_MINUTE", 300),
		authRateLimitPerMinute: envInt("AUTH_RATE_LIMIT_PER_MINUTE", 60),
		connectTimeout:         time.Duration(connectSeconds) * time.Second,
		requestTimeout:         time.Duration(requestSeconds) * time.Second,
	}
}

func connectDatabase(cfg config) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 1

	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}

	deadline := time.Now().Add(cfg.connectTimeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = pool.Ping(ctx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("ping database: %w", err)
		}
		time.Sleep(time.Second)
	}
}

// startSessionCleanup 定期清掉過期 session，避免 sessions 表無限成長。
func (s *apiServer) startSessionCleanup(logger *slog.Logger) func() {
	ticker := time.NewTicker(time.Hour)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if _, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < NOW()`); err != nil {
					logger.Warn("session cleanup failed", "error", err)
				}
				cancel()
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

func (s *apiServer) routes() http.Handler {
	mux := http.NewServeMux()

	// 公開端點
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/v1/auth/csrf", s.issueCSRFToken)
	mux.HandleFunc("/v1/auth/register", s.rateLimitAuth("register", s.register))
	mux.HandleFunc("/v1/auth/login", s.rateLimitAuth("login", s.login))
	mux.HandleFunc("/v1/auth/logout", s.logout)
	mux.HandleFunc("/v1/auth/verify-email", s.verifyEmail)
	mux.HandleFunc("/v1/auth/forgot-password", s.rateLimitAuth("forgot", s.forgotPassword))
	mux.HandleFunc("/v1/auth/reset-password", s.rateLimitAuth("reset", s.resetPassword))
	mux.HandleFunc("/v1/market/listings", s.marketListings)
	mux.HandleFunc("/v1/listings", s.optionalAuth(s.marketplaceListings))

	// 需登入
	mux.HandleFunc("/v1/auth/me", s.requireAuth(s.me))
	mux.HandleFunc("/v1/auth/resend-verification", s.requireAuth(s.resendVerification))
	mux.HandleFunc("/v1/dashboard", s.requireAuth(s.dashboard))
	mux.HandleFunc("/v1/applications", s.requireAuth(s.applications))
	mux.HandleFunc("/v1/applications/", s.requireAuth(s.applicationSubroutes))
	mux.HandleFunc("/v1/documents/", s.requireAuth(s.documentRoutes))
	mux.HandleFunc("/v1/loans/", s.requireAuth(s.loanRoutes))
	mux.HandleFunc("/v1/listings/", s.requireRole(roleInvestor, s.listingRoutes))
	mux.HandleFunc("/v1/investments", s.requireRole(roleInvestor, s.myInvestments))
	mux.HandleFunc("/v1/investments/top-up", s.requireRole(roleInvestor, s.topUpBalance))
	mux.HandleFunc("/v1/investments/distributions", s.requireRole(roleInvestor, s.myDistributions))

	// 需 reviewer 角色
	mux.HandleFunc("/v1/admin/applications", s.requireRole(roleReviewer, s.adminApplications))
	mux.HandleFunc("/v1/admin/applications/", s.requireRole(roleReviewer, s.reviewApplication))
	mux.HandleFunc("/v1/admin/overdue", s.requireRole(roleReviewer, s.adminOverdue))
	mux.HandleFunc("/v1/admin/listings/", s.requireRole(roleReviewer, s.adminListingRoutes))

	// 由外而內：限流 → CORS（preflight 需先回應）→ CSRF → request ID
	return s.withRateLimit(s.limiter, s.withCORS(s.withCSRF(s.withRequestID(mux))))
}

// applicationSubroutes 分派 /v1/applications/{id}/... 的請求。
func (s *apiServer) applicationSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/applications/"), "/")
	applicationID, suffix, found := strings.Cut(rest, "/")
	if !found || applicationID == "" || suffix != "documents" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.listDocuments(w, r, applicationID)
	case http.MethodPost:
		s.uploadDocument(w, r, applicationID)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// applications 依方法分派：GET 查自己的申請，POST 建立新申請。
func (s *apiServer) applications(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.myApplications(w, r)
	case http.MethodPost:
		s.createApplication(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *apiServer) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "degraded",
			"database": "unavailable",
			"error":    "database is not ready",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "database": "ready"})
}

func (s *apiServer) marketListings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT id, purpose, grade, annual_rate, amount, term_months, funded_percent, job, employment_years, region
		FROM market_listings
		WHERE active = TRUE
		ORDER BY grade, annual_rate, id
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load market listings")
		return
	}
	defer rows.Close()

	items := make([]listing, 0)
	for rows.Next() {
		var item listing
		if err := rows.Scan(&item.ID, &item.Purpose, &item.Grade, &item.Rate, &item.Amount, &item.Term, &item.Funded, &item.Job, &item.Years, &item.Region); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode market listings")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read market listings")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func validateApplication(request applicationRequest) error {
	if strings.TrimSpace(request.Product) == "" || strings.TrimSpace(request.Purpose) == "" {
		return errors.New("product and purpose are required")
	}
	if request.Amount < 50000 || request.Amount > 3000000 {
		return errors.New("amount must be between 50,000 and 3,000,000")
	}
	switch request.TermMonths {
	case 12, 24, 36, 48, 60, 84:
	default:
		return errors.New("termMonths must be one of 12, 24, 36, 48, 60, or 84")
	}
	if strings.TrimSpace(request.ApplicantName) == "" || strings.TrimSpace(request.Email) == "" {
		return errors.New("applicantName and email are required")
	}
	if request.AnnualIncome <= 0 || request.MonthlyExpenses < 0 {
		return errors.New("income and expenses must be valid amounts")
	}
	return nil
}

func newApplicationID() string {
	buffer := make([]byte, 4)
	if _, err := cryptorand.Read(buffer); err != nil {
		return fmt.Sprintf("LN-%d-%06d", time.Now().Year(), time.Now().UnixNano()%1000000)
	}
	number := uint32(buffer[0])<<24 | uint32(buffer[1])<<16 | uint32(buffer[2])<<8 | uint32(buffer[3])
	return fmt.Sprintf("LN-%d-%06d", time.Now().Year(), number%1000000)
}

func (s *apiServer) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := s.allowedOrigins
		if origin == "" {
			origin = "http://localhost:3000"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID, "+csrfHeaderName)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		// 帶 cookie 的跨來源請求要求 origin 不得為萬用字元
		if origin != "*" {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *apiServer) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = fmt.Sprintf("cf-%d", time.Now().UnixNano())
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

// decodeJSON 解析請求主體，失敗時已回應錯誤並回傳 false。
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// isUniqueViolation 判斷是否為 PostgreSQL 唯一鍵衝突（23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeInternalError 回傳通用訊息給客戶端，同時把真正的原因寫進日誌。
//
// 500 回應刻意不揭露內部細節，但若伺服器端也沒有紀錄，任何故障都只能靠猜測排查。
func writeInternalError(w http.ResponseWriter, message string, cause error) {
	slog.Error("request failed", "message", message, "error", cause)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": message})
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(env(key, strconv.Itoa(fallback)))
	if err != nil || value < 1 || value > math.MaxInt32 {
		return fallback
	}
	return value
}

// adminListingRoutes 分派 /v1/admin/listings/{id}/... 的請求。
func (s *apiServer) adminListingRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/listings/"), "/")
	listingID, suffix, found := strings.Cut(rest, "/")
	if !found || listingID == "" || suffix != "cancel" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	s.cancelListingByReviewer(w, r, listingID)
}
