//go:build integration

// 整合測試：對真實 PostgreSQL 驗證 DB 互動、middleware 與 transaction 邊界。
//
// 執行：
//
//	TEST_DATABASE_URL=postgres://... go test -tags=integration ./cmd/api/
//
// 未設定 TEST_DATABASE_URL 或連不上資料庫時，整個套件會被跳過（而非失敗），
// 讓沒有 DB 的環境仍能跑純單元測試。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr,
			"integration tests skipped: TEST_DATABASE_URL is not set")
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration tests skipped: cannot create pool: %v\n", err)
		os.Exit(0)
	}
	if err := waitForDatabase(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "integration tests skipped: cannot reach database: %v\n", err)
		pool.Close()
		os.Exit(0)
	}
	testPool = pool

	if err := resetSchema(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "failed to reset test schema: %v\n", err)
		pool.Close()
		os.Exit(1)
	}
	if err := applyMigrations(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "failed to apply migrations: %v\n", err)
		pool.Close()
		os.Exit(1)
	}

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// waitForDatabase 等待資料庫就緒。
// CI 的 service container 可能還在啟動，故需重試；
// 但本機沒有 DB 時應快速放棄，避免每次跑測試都等很久。
func waitForDatabase(ctx context.Context, pool *pgxpool.Pool) error {
	timeout := 5 * time.Second
	if os.Getenv("CI") != "" {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// resetSchema 清空 public schema，讓 migration 從乾淨狀態套用。
//
// 整合測試每次啟動都重建 schema，理由是 migration 並非全都可以對「已含資料的
// 資料庫」重複套用——收斂型的變更（例如把 CHECK 約束改窄）在既有資料列不符時
// 會失敗。正式環境由 scripts/migrate.sh 以版本表確保每支只跑一次，
// 測試則以重建取代版本記錄，兩者都得到與全新安裝相同的 schema。
func resetSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		return fmt.Errorf("reset public schema: %w", err)
	}
	return nil
}

// applyMigrations 依檔名順序套用 backend/migrations 下的所有 SQL。
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	pattern := filepath.Join("..", "..", "migrations", "*.sql")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no migrations found at %s", pattern)
	}
	sort.Strings(files)

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read %s: %w", file, err)
		}
		if _, err := pool.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("apply %s: %w", filepath.Base(file), err)
		}
	}
	return nil
}

// resetDatabase 清空所有業務資料表，讓每個測試從乾淨狀態開始。
// 依外鍵相依順序刪除；market_listings 保留（由 seed 提供，測試不修改）。
func resetDatabase(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// TRUNCATE ... CASCADE 一次清掉並重設 BIGSERIAL
	if _, err := testPool.Exec(ctx, `
		TRUNCATE TABLE pii_access_log, distributions, investments, repayments,
		               loan_installments, application_reviews, application_documents,
		               listings, loans, applications, sessions, users
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("reset database: %v", err)
	}
}

// newTestAPIServer 建立與 production 相同組態的 apiServer，指向測試資料庫。
func newTestAPIServer(t *testing.T) *apiServer {
	t.Helper()

	cipher, err := newPIICipher(developFallbackPIIKey)
	if err != nil {
		t.Fatalf("create PII cipher: %v", err)
	}

	// 每個測試使用獨立的暫存目錄，結束時由 t.TempDir 自動清除
	storage, err := newDocumentStorage(t.TempDir())
	if err != nil {
		t.Fatalf("create document storage: %v", err)
	}

	return &apiServer{
		db:             testPool,
		allowedOrigins: "http://localhost:3000",
		secureCookies:  false,
		trustProxy:     false,
		pii:            cipher,
		storage:        storage,
		// 限流設高，避免一般測試誤觸；限流本身另有專屬測試
		limiter:     newRateLimiter(10000, 10000, time.Minute),
		authLimiter: newRateLimiter(10000, 10000, time.Minute),
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testContext 回傳帶合理逾時的 context，並在測試結束時取消。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}
