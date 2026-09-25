package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// OCR 在背景處理：擷取文字需要數秒，不可阻塞上傳請求。
// 上傳時文件標記為 pending，worker 逐筆取出處理。

const (
	ocrPollInterval = 5 * time.Second
	// 每輪處理的上限：避免大量上傳時單輪佔用過久
	ocrBatchSize = 5
)

// startOCRWorker 啟動背景處理迴圈。
// 找不到 OCR 工具時不啟動 worker，並把待處理文件標記為 skipped。
func (s *apiServer) startOCRWorker(logger *slog.Logger) func() {
	engine, err := newOCREngine()
	if err != nil {
		logger.Warn("OCR worker not started; documents will be marked as skipped",
			"error", err)
		s.markPendingDocumentsSkipped(logger)
		return func() {}
	}
	s.ocr = engine
	logger.Info("OCR worker started", "languages", engine.languages,
		"pdf_support", engine.pdftoppmPath != "")

	ticker := time.NewTicker(ocrPollInterval)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				s.processPendingDocuments(logger)
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// markPendingDocumentsSkipped 在缺少 OCR 工具時把待處理文件標記為 skipped。
// 用 skipped 而非 failed：缺少工具是部署環境的問題，不是文件的問題。
func (s *apiServer) markPendingDocumentsSkipped(logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tag, err := s.db.Exec(ctx, `
		UPDATE application_documents
		SET ocr_status = 'skipped', ocr_error = 'OCR 工具不可用'
		WHERE ocr_status = 'pending'
	`)
	if err != nil {
		// 欄位可能尚未由 migration 建立（部署順序：先啟容器後 migrate）
		logger.Debug("could not mark documents as skipped", "error", err)
		return
	}
	if affected := tag.RowsAffected(); affected > 0 {
		logger.Info("marked pending documents as skipped", "count", affected)
	}
}

// processPendingDocuments 處理一批待 OCR 的文件。
func (s *apiServer) processPendingDocuments(logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for i := 0; i < ocrBatchSize; i++ {
		documentID, claimed := s.claimNextDocument(ctx, logger)
		if !claimed {
			return
		}
		if err := s.runOCRForDocument(ctx, documentID); err != nil {
			logger.Warn("OCR failed", "document_id", documentID, "error", err)
			s.recordOCRFailure(ctx, documentID, err)
		}
	}
}

// claimNextDocument 以原子操作取出一份待處理文件並標記為 processing。
//
// 用 UPDATE ... RETURNING 而非先 SELECT 再 UPDATE：
// 後者在多副本部署時會讓同一份文件被處理兩次。
func (s *apiServer) claimNextDocument(ctx context.Context, logger *slog.Logger) (string, bool) {
	var documentID string
	err := s.db.QueryRow(ctx, `
		UPDATE application_documents
		SET ocr_status = 'processing'
		WHERE id = (
			SELECT id FROM application_documents
			WHERE ocr_status = 'pending'
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id
	`).Scan(&documentID)

	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// 欄位尚未建立時會走到這裡；以 Debug 記錄避免每 5 秒噪音
			logger.Debug("could not claim a document for OCR", "error", err)
		}
		return "", false
	}
	return documentID, true
}

// runOCRForDocument 擷取文字並比對申請人資料。
func (s *apiServer) runOCRForDocument(ctx context.Context, documentID string) error {
	var storagePath, contentType, applicationID string
	if err := s.db.QueryRow(ctx, `
		SELECT storage_path, content_type, application_id
		FROM application_documents WHERE id = $1
	`, documentID).Scan(&storagePath, &contentType, &applicationID); err != nil {
		return fmt.Errorf("load document: %w", err)
	}

	file, err := s.storage.open(storagePath)
	if err != nil {
		return fmt.Errorf("open stored document: %w", err)
	}
	content := make([]byte, 0, maxDocumentBytes)
	buffer := make([]byte, 32<<10)
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			content = append(content, buffer[:read]...)
		}
		if readErr != nil {
			break
		}
	}
	_ = file.Close()

	ocrCtx, cancel := context.WithTimeout(ctx, ocrTimeoutSeconds*time.Second)
	defer cancel()

	text, err := s.ocr.extractText(ocrCtx, content, contentType)
	if err != nil {
		return err
	}

	// 取出申請人填寫的姓名與身分證，用於比對
	var applicantName string
	var idNumberEnc []byte
	if err := s.db.QueryRow(ctx, `
		SELECT applicant_name, id_number_enc FROM applications WHERE id = $1
	`, applicationID).Scan(&applicantName, &idNumberEnc); err != nil {
		return fmt.Errorf("load applicant data: %w", err)
	}
	idNumber, err := s.pii.decrypt(idNumberEnc)
	if err != nil {
		// 解密失敗不讓整個 OCR 失敗：文字已擷取成功，只是無法比對身分證
		idNumber = ""
	}

	transaction, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if _, err := transaction.Exec(ctx, `
		UPDATE application_documents
		SET ocr_status = 'done', ocr_text = $2, ocr_error = '', ocr_checked_at = NOW()
		WHERE id = $1
	`, documentID, text); err != nil {
		return fmt.Errorf("store OCR text: %w", err)
	}

	checks := map[string]string{
		"applicant_name": applicantName,
		"id_number":      idNumber,
	}
	for field, expected := range checks {
		result := verifyFieldInText(text, expected)
		if _, err := transaction.Exec(ctx, `
			INSERT INTO document_verifications (document_id, field, result)
			VALUES ($1, $2, $3)
			ON CONFLICT (document_id, field) DO UPDATE SET
				result = EXCLUDED.result, created_at = NOW()
		`, documentID, field, result); err != nil {
			return fmt.Errorf("record verification for %s: %w", field, err)
		}
	}

	return transaction.Commit(ctx)
}

// recordOCRFailure 記錄失敗原因。文件本身仍可下載，只是沒有 OCR 結果。
func (s *apiServer) recordOCRFailure(ctx context.Context, documentID string, cause error) {
	message := truncate(cause.Error(), 500)
	if _, err := s.db.Exec(ctx, `
		UPDATE application_documents
		SET ocr_status = 'failed', ocr_error = $2, ocr_checked_at = NOW()
		WHERE id = $1
	`, documentID, message); err != nil {
		slog.Error("could not record OCR failure", "document_id", documentID, "error", err)
	}
}
