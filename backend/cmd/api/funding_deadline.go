package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 募資期限。標的上架後若在期限內未募滿，資金退回出借人並取消標的。
const defaultFundingDays = 14

// fundingDeadlineFor 回傳標的的募資截止時間。
func fundingDeadlineFor(createdAt time.Time) time.Time {
	return createdAt.AddDate(0, 0, defaultFundingDays)
}

// daysUntilDeadline 回傳距離截止還有幾天（以日計，當天為 0）。
// 已過期回傳負數，讓呼叫端可以區分「今天到期」與「已逾期」。
func daysUntilDeadline(deadline, now time.Time) int {
	due := truncateToDay(deadline)
	today := truncateToDay(now)
	return int(due.Sub(today).Hours() / 24)
}

// cancelListing 取消標的並把資金原額退回出借人。
//
// 退款是「原額退回」而非按比例分配：出借人投入多少就拿回多少，
// 因為標的從未撥款，本金完全沒有動用。
//
// 呼叫端必須已在 transaction 內並鎖定該標的。
func cancelListing(
	ctx context.Context,
	transaction dbExecutorQuerier,
	listingID string,
	reason string,
) (int64, int, error) {
	rows, err := transaction.Query(ctx, `
		SELECT i.id, i.investor_id, i.amount
		FROM investments i
		WHERE i.listing_id = $1
		  AND NOT EXISTS (
		    SELECT 1 FROM investment_refunds r WHERE r.investment_id = i.id
		  )
		ORDER BY i.id
	`, listingID)
	if err != nil {
		return 0, 0, fmt.Errorf("load investments to refund: %w", err)
	}

	type refund struct {
		investmentID int64
		investorID   string
		amount       int64
	}
	pending := make([]refund, 0)
	for rows.Next() {
		var item refund
		if err := rows.Scan(&item.investmentID, &item.investorID, &item.amount); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("scan investment: %w", err)
		}
		pending = append(pending, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("read investments: %w", err)
	}

	var refunded int64
	for _, item := range pending {
		// 唯一約束保證同一筆投資只退一次；重複取消時這裡會衝突
		if _, err := transaction.Exec(ctx, `
			INSERT INTO investment_refunds (investment_id, listing_id, investor_id, amount, reason)
			VALUES ($1, $2, $3, $4, $5)
		`, item.investmentID, listingID, item.investorID, item.amount, reason); err != nil {
			if isUniqueViolation(err) {
				continue
			}
			return 0, 0, fmt.Errorf("record refund for investment %d: %w", item.investmentID, err)
		}

		if _, err := transaction.Exec(ctx, `
			UPDATE users SET available_balance = available_balance + $2 WHERE id = $1
		`, item.investorID, item.amount); err != nil {
			return 0, 0, fmt.Errorf("credit investor %s: %w", item.investorID, err)
		}
		refunded += item.amount
	}

	if _, err := transaction.Exec(ctx, `
		UPDATE listings
		SET status = 'cancelled',
		    cancelled_at = COALESCE(cancelled_at, NOW()),
		    cancel_reason = $2,
		    funded_amount = 0
		WHERE id = $1
	`, listingID, reason); err != nil {
		return 0, 0, fmt.Errorf("mark listing cancelled: %w", err)
	}

	// 申請退回待補件而非婉拒：募資失敗是平台端的結果，
	// 不代表授信條件不符，借款人應該能被重新處理。
	if _, err := transaction.Exec(ctx, `
		UPDATE applications
		SET status = 'more_info_required', updated_at = NOW()
		WHERE id = (SELECT application_id FROM listings WHERE id = $1)
	`, listingID); err != nil {
		return 0, 0, fmt.Errorf("reset application status: %w", err)
	}

	return refunded, len(pending), nil
}

// sweepExpiredListings 取消所有逾期未募滿的標的。
//
// 每個標的各自一個 transaction：一個標的退款失敗不該讓其他標的也回滾。
//
// 注意部署順序：deploy.sh 先啟動容器才跑 migration，因此 API 首次啟動時
// funding_deadline 欄位可能尚未存在。此時回報「尚未就緒」而非錯誤，
// 與 migratePlaintextPII 採同一策略。
func (s *apiServer) sweepExpiredListings(ctx context.Context) (int, error) {
	ready, err := s.fundingDeadlineReady(ctx)
	if err != nil {
		return 0, err
	}
	if !ready {
		return -1, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT id FROM listings
		WHERE status = 'funding' AND funding_deadline < NOW()
		ORDER BY funding_deadline
		LIMIT 500
	`)
	if err != nil {
		return 0, fmt.Errorf("find expired listings: %w", err)
	}

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan listing id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read expired listings: %w", err)
	}

	cancelled := 0
	for _, id := range ids {
		if err := s.cancelOneExpired(ctx, id); err != nil {
			return cancelled, err
		}
		cancelled++
	}
	return cancelled, nil
}

func (s *apiServer) cancelOneExpired(ctx context.Context, listingID string) error {
	transaction, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin cancel transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// 重新確認狀態：掃描與實際取消之間可能已被投滿
	var status string
	if err := transaction.QueryRow(ctx,
		`SELECT status FROM listings WHERE id = $1 FOR UPDATE`, listingID).Scan(&status); err != nil {
		return fmt.Errorf("lock listing %s: %w", listingID, err)
	}
	if status != "funding" {
		return nil
	}

	if _, _, err := cancelListing(ctx, transaction, listingID, "募資期限屆滿未達標"); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

// startFundingDeadlineSweep 啟動時執行一次，之後每日重跑。
func (s *apiServer) startFundingDeadlineSweep(logger *slog.Logger) func() {
	sweep := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cancelled, err := s.sweepExpiredListings(ctx)
		switch {
		case err != nil:
			logger.Warn("funding deadline sweep failed", "error", err,
				"cancelled_before_failure", cancelled)
		case cancelled < 0:
			logger.Info("funding_deadline column not ready yet; sweep will run after migration")
		case cancelled > 0:
			logger.Info("cancelled expired listings", "count", cancelled)
		}
	}

	sweep()

	ticker := time.NewTicker(24 * time.Hour)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				sweep()
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// ---------------------------------------------------------------- 手動取消

// cancelListingByReviewer 讓風控在募資明顯無望時不必等到期限。
// 路由：POST /v1/admin/listings/{id}/cancel
func (s *apiServer) cancelListingByReviewer(w http.ResponseWriter, r *http.Request, listingID string) {
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
		writeError(w, http.StatusUnprocessableEntity, "reason is required to cancel a listing")
		return
	}
	if len(reason) > 500 {
		writeError(w, http.StatusUnprocessableEntity, "reason is too long")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	transaction, err := s.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var status string
	if err := transaction.QueryRow(ctx,
		`SELECT status FROM listings WHERE id = $1 FOR UPDATE`, listingID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "listing not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load listing")
		return
	}
	// 已撥款的標的資金已進入借款人手中，無法退款
	if status != "funding" {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("only listings still in funding can be cancelled (status: %s)", status))
		return
	}

	refunded, count, err := cancelListing(ctx, transaction, listingID,
		fmt.Sprintf("%s（由 %s 取消）", reason, actor.DisplayName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel the listing")
		return
	}
	if err := transaction.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit cancellation")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"listingId":      listingID,
		"status":         "cancelled",
		"refundedAmount": refunded,
		"refundedCount":  count,
		"cancelledBy":    actor.DisplayName,
	})
}

// fundingDeadlineReady 檢查 migration 009 是否已建立 funding_deadline 欄位。
func (s *apiServer) fundingDeadlineReady(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'listings' AND column_name = 'funding_deadline'
	`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check funding_deadline column: %w", err)
	}
	return count == 1, nil
}
