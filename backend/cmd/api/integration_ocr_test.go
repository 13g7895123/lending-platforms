//go:build integration

package main

import (
	"net/http"
	"testing"
)

// 上傳不可被 OCR 阻塞：OCR 需要數秒，必須在背景進行。
func TestIntegrationUploadIsNotBlockedByOCR(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	var uploaded documentRecord
	borrower.upload(path, "file", "id.png", samplePNG).
		expectStatus(t, http.StatusCreated, "upload").decode(t, &uploaded)

	// 上傳回應應立即回來，狀態為待處理
	if uploaded.OCRStatus != "pending" {
		t.Errorf("ocrStatus = %q, want pending right after upload", uploaded.OCRStatus)
	}

	var storedStatus string
	if err := testPool.QueryRow(testContext(t),
		`SELECT ocr_status FROM application_documents WHERE id = $1`,
		uploaded.ID).Scan(&storedStatus); err != nil {
		t.Fatalf("read stored status: %v", err)
	}
	if storedStatus != "pending" {
		t.Errorf("stored ocr_status = %q, want pending", storedStatus)
	}
}

// claimNextDocument 必須以原子操作取件，否則多副本會重複處理同一份文件。
func TestIntegrationOCRClaimIsAtomic(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	var uploaded documentRecord
	borrower.upload(path, "file", "id.png", samplePNG).
		expectStatus(t, http.StatusCreated, "upload").decode(t, &uploaded)

	server := newTestAPIServer(t)
	logger := discardLogger()

	first, claimed := server.claimNextDocument(testContext(t), logger)
	if !claimed {
		t.Fatal("the pending document was not claimed")
	}
	if first != uploaded.ID {
		t.Errorf("claimed %q, want %q", first, uploaded.ID)
	}

	// 已被取走的文件不可再被取一次
	if _, claimedAgain := server.claimNextDocument(testContext(t), logger); claimedAgain {
		t.Error("the same document was claimed twice; concurrent workers would duplicate work")
	}

	var status string
	if err := testPool.QueryRow(testContext(t),
		`SELECT ocr_status FROM application_documents WHERE id = $1`, first).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "processing" {
		t.Errorf("ocr_status = %q, want processing after being claimed", status)
	}
}

// OCR 失敗不可影響文件本身：檔案仍要能下載，審核流程不受阻。
func TestIntegrationOCRFailureDoesNotBreakDocument(t *testing.T) {
	borrower, reviewer, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	var uploaded documentRecord
	borrower.upload(path, "file", "id.png", samplePNG).
		expectStatus(t, http.StatusCreated, "upload").decode(t, &uploaded)

	// 模擬 OCR 失敗
	if _, err := testPool.Exec(testContext(t), `
		UPDATE application_documents
		SET ocr_status = 'failed', ocr_error = '模擬的辨識失敗', ocr_checked_at = NOW()
		WHERE id = $1
	`, uploaded.ID); err != nil {
		t.Fatalf("mark as failed: %v", err)
	}

	t.Run("the document is still downloadable", func(t *testing.T) {
		response := borrower.download("/v1/documents/"+uploaded.ID+"/download").
			expectStatus(t, http.StatusOK, "download after OCR failure")
		if len(response.Body) == 0 {
			t.Error("the downloaded document is empty")
		}
	})

	t.Run("the failure is visible to the reviewer", func(t *testing.T) {
		var items []documentRecord
		reviewer.do(http.MethodGet, path, nil).
			expectStatus(t, http.StatusOK, "reviewer listing").decode(t, &items)
		if len(items) != 1 {
			t.Fatalf("%d documents, want 1", len(items))
		}
		if items[0].OCRStatus != "failed" {
			t.Errorf("ocrStatus = %q, want failed", items[0].OCRStatus)
		}
		if items[0].OCRError == "" {
			t.Error("ocrError is empty; the reviewer cannot tell why it failed")
		}
	})

	t.Run("the application can still be reviewed", func(t *testing.T) {
		reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
			map[string]string{"action": "approve", "reason": "人工確認文件"}).
			expectStatus(t, http.StatusOK, "approve despite OCR failure")
	})
}

// 比對結果要能隨清單回傳，風控才看得到。
func TestIntegrationVerificationResultsAreReturned(t *testing.T) {
	borrower, reviewer, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	var uploaded documentRecord
	borrower.upload(path, "file", "id.png", samplePNG).
		expectStatus(t, http.StatusCreated, "upload").decode(t, &uploaded)

	// 直接寫入比對結果，不依賴 tesseract 是否安裝
	for field, result := range map[string]string{
		"applicant_name": verificationMatched,
		"id_number":      verificationNotFound,
	} {
		if _, err := testPool.Exec(testContext(t), `
			INSERT INTO document_verifications (document_id, field, result)
			VALUES ($1, $2, $3)
		`, uploaded.ID, field, result); err != nil {
			t.Fatalf("insert verification: %v", err)
		}
	}
	if _, err := testPool.Exec(testContext(t), `
		UPDATE application_documents SET ocr_status = 'done', ocr_checked_at = NOW() WHERE id = $1
	`, uploaded.ID); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	var items []documentRecord
	reviewer.do(http.MethodGet, path, nil).
		expectStatus(t, http.StatusOK, "reviewer listing").decode(t, &items)

	if len(items) != 1 {
		t.Fatalf("%d documents, want 1", len(items))
	}
	if items[0].OCRStatus != "done" {
		t.Errorf("ocrStatus = %q, want done", items[0].OCRStatus)
	}
	if got := items[0].Verifications["applicant_name"]; got != verificationMatched {
		t.Errorf("applicant_name = %q, want matched", got)
	}
	if got := items[0].Verifications["id_number"]; got != verificationNotFound {
		t.Errorf("id_number = %q, want not_found", got)
	}
}

// 同一份文件重跑 OCR 時，比對結果應更新而非累積。
func TestIntegrationVerificationIsUpsertedNotDuplicated(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	var uploaded documentRecord
	borrower.upload(path, "file", "id.png", samplePNG).
		expectStatus(t, http.StatusCreated, "upload").decode(t, &uploaded)

	for _, result := range []string{verificationNotFound, verificationMatched} {
		if _, err := testPool.Exec(testContext(t), `
			INSERT INTO document_verifications (document_id, field, result)
			VALUES ($1, 'applicant_name', $2)
			ON CONFLICT (document_id, field) DO UPDATE SET result = EXCLUDED.result
		`, uploaded.ID, result); err != nil {
			t.Fatalf("upsert verification: %v", err)
		}
	}

	var count int
	var result string
	if err := testPool.QueryRow(testContext(t), `
		SELECT COUNT(*), MAX(result) FROM document_verifications
		WHERE document_id = $1 AND field = 'applicant_name'
	`, uploaded.ID).Scan(&count, &result); err != nil {
		t.Fatalf("read verifications: %v", err)
	}
	if count != 1 {
		t.Errorf("%d rows for one field, want 1 (results must be upserted)", count)
	}
	if result != verificationMatched {
		t.Errorf("result = %q, want the latest value (matched)", result)
	}
}

// OCR 工具不可用時文件標記為 skipped 而非 failed：
// 缺少工具是部署環境的問題，不是文件的問題。
func TestIntegrationPendingDocumentsAreSkippedWhenOCRUnavailable(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	var uploaded documentRecord
	borrower.upload(path, "file", "id.png", samplePNG).
		expectStatus(t, http.StatusCreated, "upload").decode(t, &uploaded)

	server := newTestAPIServer(t)
	server.markPendingDocumentsSkipped(discardLogger())

	var status, message string
	if err := testPool.QueryRow(testContext(t), `
		SELECT ocr_status, ocr_error FROM application_documents WHERE id = $1
	`, uploaded.ID).Scan(&status, &message); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "skipped" {
		t.Errorf("ocr_status = %q, want skipped", status)
	}
	if message == "" {
		t.Error("ocr_error is empty; the reason for skipping is not recorded")
	}
}
