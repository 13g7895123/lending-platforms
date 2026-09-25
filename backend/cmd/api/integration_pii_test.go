//go:build integration

package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestIntegrationPIIIsEncryptedAtRest(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	borrower := newTestClient(t, newTestAPIServer(t))
	borrower.loginAs("borrower@creditflow.test", testPassword)
	applicationID := borrower.submitApplication()

	const plainIDNumber = "A123456789"
	const plainPhone = "0912-345-678"

	t.Run("plaintext columns are left empty", func(t *testing.T) {
		var idNumber, phone string
		if err := testPool.QueryRow(testContext(t), `
			SELECT COALESCE(id_number, ''), COALESCE(phone, '')
			FROM applications WHERE id = $1
		`, applicationID).Scan(&idNumber, &phone); err != nil {
			t.Fatalf("read plaintext columns: %v", err)
		}
		if idNumber != "" || phone != "" {
			t.Errorf("plaintext columns hold data: id_number=%q phone=%q", idNumber, phone)
		}
	})

	t.Run("ciphertext is stored and contains no plaintext", func(t *testing.T) {
		var idEnc, phoneEnc []byte
		if err := testPool.QueryRow(testContext(t), `
			SELECT id_number_enc, phone_enc FROM applications WHERE id = $1
		`, applicationID).Scan(&idEnc, &phoneEnc); err != nil {
			t.Fatalf("read ciphertext: %v", err)
		}
		if len(idEnc) == 0 || len(phoneEnc) == 0 {
			t.Fatal("ciphertext columns are empty; PII was not stored")
		}
		if bytes.Contains(idEnc, []byte(plainIDNumber)) {
			t.Error("id_number_enc contains the plaintext ID number")
		}
		if bytes.Contains(phoneEnc, []byte("0912")) {
			t.Error("phone_enc contains a plaintext phone fragment")
		}
	})

	t.Run("ciphertext decrypts back to the original", func(t *testing.T) {
		cipher, err := newPIICipher(developFallbackPIIKey)
		if err != nil {
			t.Fatalf("create cipher: %v", err)
		}
		var idEnc []byte
		if err := testPool.QueryRow(testContext(t),
			`SELECT id_number_enc FROM applications WHERE id = $1`, applicationID).Scan(&idEnc); err != nil {
			t.Fatalf("read ciphertext: %v", err)
		}
		decrypted, err := cipher.decrypt(idEnc)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if decrypted != plainIDNumber {
			t.Errorf("decrypted = %q, want %q", decrypted, plainIDNumber)
		}
	})

	// 同一明文的兩筆申請必須有不同密文，否則可用密文比對推測是否同一人
	t.Run("two applications with the same PII have different ciphertext", func(t *testing.T) {
		secondID := borrower.submitApplication()

		var first, second []byte
		if err := testPool.QueryRow(testContext(t),
			`SELECT id_number_enc FROM applications WHERE id = $1`, applicationID).Scan(&first); err != nil {
			t.Fatalf("read first: %v", err)
		}
		if err := testPool.QueryRow(testContext(t),
			`SELECT id_number_enc FROM applications WHERE id = $1`, secondID).Scan(&second); err != nil {
			t.Fatalf("read second: %v", err)
		}
		if bytes.Equal(first, second) {
			t.Error("identical PII produced identical ciphertext; the nonce is not random")
		}
	})
}

func TestIntegrationPIIIsMaskedInResponses(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	borrower := newTestClient(t, server)
	reviewer := borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)
	borrower.submitApplication()

	endpoints := map[string]*testClient{
		"/v1/applications":       borrower,
		"/v1/admin/applications": reviewer,
	}

	for path, client := range endpoints {
		t.Run("masked on "+path, func(t *testing.T) {
			response := client.do(http.MethodGet, path, nil).
				expectStatus(t, http.StatusOK, path)

			// 原始回應內容不得含完整身分證
			if strings.Contains(string(response.Body), "A123456789") {
				t.Error("response body leaks the full ID number")
			}

			items, _ := response.decodeApplications(t)
			if len(items) == 0 {
				t.Fatal("no applications returned")
			}
			if items[0].IDNumberMasked != "A12****789" {
				t.Errorf("idNumberMasked = %q, want A12****789", items[0].IDNumberMasked)
			}
			if !strings.HasPrefix(items[0].PhoneMasked, "****") {
				t.Errorf("phoneMasked = %q, want a masked value", items[0].PhoneMasked)
			}
		})
	}
}

func TestIntegrationPIIRevealRequiresReasonAndIsAudited(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	reviewerID := createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	borrower := newTestClient(t, server)
	reviewer := borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)
	applicationID := borrower.submitApplication()

	path := "/v1/admin/applications/" + applicationID + "/reveal"

	t.Run("reason is required", func(t *testing.T) {
		reviewer.do(http.MethodPost, path, map[string]string{"reason": ""}).
			expectStatus(t, http.StatusUnprocessableEntity, "reveal without reason")
		reviewer.do(http.MethodPost, path, map[string]string{"reason": "   "}).
			expectStatus(t, http.StatusUnprocessableEntity, "reveal with blank reason")
	})

	t.Run("borrower cannot reveal", func(t *testing.T) {
		borrower.do(http.MethodPost, path, map[string]string{"reason": "試圖越權"}).
			expectStatus(t, http.StatusForbidden, "borrower reveal")
	})

	t.Run("reviewer with a reason gets the full value", func(t *testing.T) {
		var revealed revealResponse
		reviewer.do(http.MethodPost, path, map[string]string{"reason": "人工覆核身分"}).
			expectStatus(t, http.StatusOK, "reveal").decode(t, &revealed)

		if revealed.IDNumber != "A123456789" {
			t.Errorf("idNumber = %q, want A123456789", revealed.IDNumber)
		}
		if revealed.Phone != "0912-345-678" {
			t.Errorf("phone = %q, want 0912-345-678", revealed.Phone)
		}
	})

	t.Run("access is written to the audit log", func(t *testing.T) {
		var actor, reason string
		if err := testPool.QueryRow(testContext(t), `
			SELECT accessed_by, reason FROM pii_access_log
			WHERE application_id = $1 ORDER BY created_at DESC LIMIT 1
		`, applicationID).Scan(&actor, &reason); err != nil {
			t.Fatalf("read audit log: %v", err)
		}
		if actor != reviewerID {
			t.Errorf("accessed_by = %q, want %q", actor, reviewerID)
		}
		if reason != "人工覆核身分" {
			t.Errorf("reason = %q", reason)
		}
	})

	// 失敗的揭露嘗試不該留下稽核紀錄（沒有真的揭露）
	t.Run("rejected attempts are not logged as access", func(t *testing.T) {
		var before int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM pii_access_log`).Scan(&before); err != nil {
			t.Fatalf("count log: %v", err)
		}
		reviewer.do(http.MethodPost, path, map[string]string{"reason": ""}).
			expectStatus(t, http.StatusUnprocessableEntity, "reveal without reason")

		var after int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM pii_access_log`).Scan(&after); err != nil {
			t.Fatalf("count log: %v", err)
		}
		if after != before {
			t.Errorf("a rejected reveal added %d audit rows", after-before)
		}
	})

	t.Run("unknown application returns 404", func(t *testing.T) {
		reviewer.do(http.MethodPost, "/v1/admin/applications/LN-0000-000000/reveal",
			map[string]string{"reason": "查無此案"}).
			expectStatus(t, http.StatusNotFound, "reveal unknown application")
	})
}

// 舊資料的明文搬遷：migratePlaintextPII 必須加密既有明文並清空原欄位，且可重複執行。
func TestIntegrationPlaintextPIIMigration(t *testing.T) {
	resetDatabase(t)
	userID := createUser(t, "legacy@creditflow.test", "舊資料", roleBorrower, 700)

	if _, err := testPool.Exec(testContext(t), `
		INSERT INTO applications (
			id, user_id, product, amount, term_months, purpose, applicant_name,
			id_number, phone, email, annual_income, monthly_expenses, status
		) VALUES ('LN-LEGACY-001', $1, '信貸', 500000, 36, '整合', '舊資料',
		          'B987654321', '0988-111-222', 'legacy@creditflow.test', 1000000, 20000, 'pending')
	`, userID); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	server := newTestAPIServer(t)
	migrated, err := server.migratePlaintextPII(testContext(t))
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migrated < 1 {
		t.Fatalf("migrated %d rows, want at least 1", migrated)
	}

	var idNumber, phone string
	var idEnc []byte
	if err := testPool.QueryRow(testContext(t), `
		SELECT COALESCE(id_number, ''), COALESCE(phone, ''), id_number_enc
		FROM applications WHERE id = 'LN-LEGACY-001'
	`).Scan(&idNumber, &phone, &idEnc); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if idNumber != "" || phone != "" {
		t.Errorf("plaintext was not cleared: id_number=%q phone=%q", idNumber, phone)
	}
	if len(idEnc) == 0 {
		t.Fatal("ciphertext was not written")
	}

	decrypted, err := server.pii.decrypt(idEnc)
	if err != nil {
		t.Fatalf("decrypt migrated value: %v", err)
	}
	if decrypted != "B987654321" {
		t.Errorf("decrypted = %q, want B987654321", decrypted)
	}

	// 重複執行必須無害且不再處理任何列
	second, err := server.migratePlaintextPII(testContext(t))
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if second != 0 {
		t.Errorf("second migration processed %d rows, want 0 (not idempotent)", second)
	}
}

// 加密欄位尚未建立時（migration 未跑），搬遷必須回報未就緒而非錯誤，
// 否則 API 會在 migration 有機會執行前就進入重啟迴圈。
func TestIntegrationPIIColumnsReadinessCheck(t *testing.T) {
	server := newTestAPIServer(t)
	ready, err := server.piiColumnsReady(testContext(t))
	if err != nil {
		t.Fatalf("piiColumnsReady: %v", err)
	}
	if !ready {
		t.Error("columns should be ready after migrations were applied")
	}
}
