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

	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	port           string
	databaseURL    string
	allowedOrigins string
	connectTimeout time.Duration
	requestTimeout time.Duration
}

type apiServer struct {
	db             *pgxpool.Pool
	allowedOrigins string
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

	app := &apiServer{db: pool, allowedOrigins: cfg.allowedOrigins}
	handler := app.routes()
	server := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           handler,
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
		allowedOrigins: env("CORS_ALLOWED_ORIGINS", "*"),
		connectTimeout: time.Duration(connectSeconds) * time.Second,
		requestTimeout: time.Duration(requestSeconds) * time.Second,
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

func (s *apiServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/v1/dashboard", s.dashboard)
	mux.HandleFunc("/v1/market/listings", s.marketListings)
	mux.HandleFunc("/v1/applications", s.applications)
	return s.withCORS(s.withRequestID(mux))
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

func (s *apiServer) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var response dashboardResponse
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0) FROM loans WHERE status <> '已結清'`).Scan(&response.Summary.TotalBorrowed); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load dashboard summary")
		return
	}
	response.Summary.MonthlyPayment = 14982
	response.Summary.CreditScore = 782
	response.Summary.RepaymentRate = 97.6

	rows, err := s.db.Query(ctx, `
		SELECT id, product, amount, annual_rate, paid_installments, total_installments, status, status_tone
		FROM loans
		ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load loans")
		return
	}
	defer rows.Close()
	response.Loans = make([]loan, 0)
	for rows.Next() {
		var item loan
		if err := rows.Scan(&item.ID, &item.Product, &item.Amount, &item.Rate, &item.PaidInstallments, &item.TotalInstallments, &item.Status, &item.StatusTone); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode loans")
			return
		}
		response.Loans = append(response.Loans, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read loans")
		return
	}
	writeJSON(w, http.StatusOK, response)
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
	writeJSON(w, http.StatusOK, items)
}

func (s *apiServer) applications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var request applicationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
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
	id := newApplicationID()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var createdAt time.Time
	err = s.db.QueryRow(ctx, `
		INSERT INTO applications (
			id, product, amount, term_months, purpose, applicant_name, id_number,
			phone, email, job, employment_years, annual_income, monthly_expenses,
			housing, note, documents, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 'pending')
		RETURNING created_at
	`, id, request.Product, request.Amount, request.TermMonths, request.Purpose, request.ApplicantName,
		request.IDNumber, request.Phone, request.Email, request.Job, request.EmploymentYears,
		request.AnnualIncome, request.MonthlyExpenses, request.Housing, request.Note, documents).Scan(&createdAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create application")
		return
	}

	writeJSON(w, http.StatusCreated, applicationResponse{ID: id, Status: "pending", CreatedAt: createdAt})
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
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
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
