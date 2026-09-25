//go:build integration

package main

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// documentFixture 建立一份 pending 狀態的申請（此時可增刪文件）。
func documentFixture(t *testing.T) (borrower, reviewer *testClient, applicationID string) {
	t.Helper()
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	borrower = newTestClient(t, server)
	reviewer = borrower.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	return borrower, reviewer, borrower.submitApplication()
}

func TestIntegrationDocumentUploadAndDownload(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	var uploaded documentRecord

	t.Run("uploading a PDF succeeds", func(t *testing.T) {
		response := borrower.upload(path, "file", "身分證正反面.pdf", samplePDF).
			expectStatus(t, http.StatusCreated, "upload pdf")
		response.decode(t, &uploaded)

		if uploaded.ID == "" {
			t.Fatal("no document id was returned")
		}
		if uploaded.ContentType != "application/pdf" {
			t.Errorf("contentType = %q, want application/pdf", uploaded.ContentType)
		}
		if uploaded.SizeBytes != int64(len(samplePDF)) {
			t.Errorf("sizeBytes = %d, want %d", uploaded.SizeBytes, len(samplePDF))
		}
		if uploaded.Checksum != checksumOf(samplePDF) {
			t.Error("the stored checksum does not match the uploaded content")
		}
		if uploaded.OriginalName != "身分證正反面.pdf" {
			t.Errorf("originalName = %q", uploaded.OriginalName)
		}
	})

	t.Run("the document is listed", func(t *testing.T) {
		var items []documentRecord
		borrower.do(http.MethodGet, path, nil).
			expectStatus(t, http.StatusOK, "list documents").decode(t, &items)
		if len(items) != 1 || items[0].ID != uploaded.ID {
			t.Fatalf("listing returned %d documents, want the one just uploaded", len(items))
		}
	})

	t.Run("downloading returns the original bytes", func(t *testing.T) {
		response := borrower.download("/v1/documents/"+uploaded.ID+"/download").
			expectStatus(t, http.StatusOK, "download")

		if !bytes.Equal(response.Body, samplePDF) {
			t.Error("the downloaded content differs from what was uploaded")
		}
		if got := response.Header.Get("Content-Type"); got != "application/pdf" {
			t.Errorf("Content-Type = %q, want application/pdf", got)
		}
		// 必須是附件下載，不可讓瀏覽器內嵌執行
		disposition := response.Header.Get("Content-Disposition")
		if !strings.HasPrefix(disposition, "attachment;") {
			t.Errorf("Content-Disposition = %q, want an attachment", disposition)
		}
		if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
		}
	})

	t.Run("JPEG and PNG are also accepted", func(t *testing.T) {
		borrower.upload(path, "file", "photo.jpg", sampleJPEG).
			expectStatus(t, http.StatusCreated, "upload jpeg")
		borrower.upload(path, "file", "scan.png", samplePNG).
			expectStatus(t, http.StatusCreated, "upload png")

		var items []documentRecord
		borrower.do(http.MethodGet, path, nil).
			expectStatus(t, http.StatusOK, "list").decode(t, &items)
		if len(items) != 3 {
			t.Errorf("%d documents, want 3", len(items))
		}
	})

	t.Run("deleting removes the document", func(t *testing.T) {
		borrower.do(http.MethodDelete, "/v1/documents/"+uploaded.ID, nil).
			expectStatus(t, http.StatusOK, "delete")

		borrower.download("/v1/documents/"+uploaded.ID+"/download").
			expectStatus(t, http.StatusNotFound, "download after delete")

		var items []documentRecord
		borrower.do(http.MethodGet, path, nil).
			expectStatus(t, http.StatusOK, "list after delete").decode(t, &items)
		if len(items) != 2 {
			t.Errorf("%d documents remain, want 2", len(items))
		}
	})
}

// 副檔名與 Content-Type 都可偽造，只有內容說得準。
func TestIntegrationDocumentTypeIsValidatedByContent(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	rejected := []struct {
		name     string
		fileName string
		content  []byte
	}{
		{"plain text with a pdf extension", "fake.pdf", []byte("this is not a PDF at all")},
		{"shell script disguised as an image", "photo.png", []byte("#!/bin/sh\nrm -rf /\n")},
		{"elf binary", "doc.pdf", []byte{0x7F, 'E', 'L', 'F', 0x02, 0x01}},
		{"html", "page.png", []byte("<html><script>alert(1)</script></html>")},
		{"svg with script", "image.png", []byte(`<svg onload="alert(1)"></svg>`)},
		{"pdf marker not at the start", "late.pdf", []byte("junk%PDF-1.7")},
	}

	for _, test := range rejected {
		t.Run(test.name, func(t *testing.T) {
			response := borrower.upload(path, "file", test.fileName, test.content)
			if response.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("uploading %s got HTTP %d, want 422", test.name, response.StatusCode)
			}
		})
	}

	t.Run("nothing was stored", func(t *testing.T) {
		var items []documentRecord
		borrower.do(http.MethodGet, path, nil).
			expectStatus(t, http.StatusOK, "list").decode(t, &items)
		if len(items) != 0 {
			t.Errorf("%d documents were stored despite rejection", len(items))
		}
	})

	t.Run("empty file is rejected", func(t *testing.T) {
		borrower.upload(path, "file", "empty.pdf", []byte{}).
			expectStatus(t, http.StatusUnprocessableEntity, "empty upload")
	})

	t.Run("a missing file field is rejected", func(t *testing.T) {
		borrower.upload(path, "wrongfield", "doc.pdf", samplePDF).
			expectStatus(t, http.StatusBadRequest, "wrong field name")
	})
}

func TestIntegrationDocumentSizeLimit(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	// 合法 PDF 開頭 + 超過上限的內容
	oversized := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("A"), maxDocumentBytes)...)
	response := borrower.upload(path, "file", "huge.pdf", oversized)
	if response.StatusCode != http.StatusRequestEntityTooLarge &&
		response.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized upload got HTTP %d, want 413 or 400", response.StatusCode)
	}

	var items []documentRecord
	borrower.do(http.MethodGet, path, nil).
		expectStatus(t, http.StatusOK, "list").decode(t, &items)
	if len(items) != 0 {
		t.Error("the oversized file was stored")
	}
}

// 相同內容不可重複上傳到同一份申請。
func TestIntegrationDocumentDeduplication(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	borrower.upload(path, "file", "first.pdf", samplePDF).
		expectStatus(t, http.StatusCreated, "first upload")
	// 檔名不同但內容相同
	borrower.upload(path, "file", "second.pdf", samplePDF).
		expectStatus(t, http.StatusConflict, "duplicate content")

	var items []documentRecord
	borrower.do(http.MethodGet, path, nil).
		expectStatus(t, http.StatusOK, "list").decode(t, &items)
	if len(items) != 1 {
		t.Errorf("%d documents, want 1", len(items))
	}
}

func TestIntegrationDocumentAccessControl(t *testing.T) {
	borrower, reviewer, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	var uploaded documentRecord
	borrower.upload(path, "file", "id.pdf", samplePDF).
		expectStatus(t, http.StatusCreated, "upload").decode(t, &uploaded)

	// 另一位借款人
	createUser(t, "other@creditflow.test", "他人", roleBorrower, 780)
	other := borrower.fork()
	other.loginAs("other@creditflow.test", testPassword)

	t.Run("another borrower cannot list", func(t *testing.T) {
		other.do(http.MethodGet, path, nil).
			expectStatus(t, http.StatusNotFound, "list someone else's documents")
	})

	t.Run("another borrower cannot download", func(t *testing.T) {
		other.download("/v1/documents/"+uploaded.ID+"/download").
			expectStatus(t, http.StatusNotFound, "download someone else's document")
	})

	t.Run("another borrower cannot upload", func(t *testing.T) {
		other.upload(path, "file", "intrusion.pdf", samplePNG).
			expectStatus(t, http.StatusNotFound, "upload to someone else's application")
	})

	t.Run("another borrower cannot delete", func(t *testing.T) {
		other.do(http.MethodDelete, "/v1/documents/"+uploaded.ID, nil).
			expectStatus(t, http.StatusNotFound, "delete someone else's document")
	})

	// reviewer 需要看文件才能審核
	t.Run("reviewer can list and download", func(t *testing.T) {
		var items []documentRecord
		reviewer.do(http.MethodGet, path, nil).
			expectStatus(t, http.StatusOK, "reviewer listing").decode(t, &items)
		if len(items) != 1 {
			t.Errorf("reviewer sees %d documents, want 1", len(items))
		}

		response := reviewer.download("/v1/documents/"+uploaded.ID+"/download").
			expectStatus(t, http.StatusOK, "reviewer download")
		if !bytes.Equal(response.Body, samplePDF) {
			t.Error("reviewer downloaded different content")
		}
	})

	// 但 reviewer 不該能改動審核依據
	t.Run("reviewer cannot upload or delete", func(t *testing.T) {
		reviewer.upload(path, "file", "injected.pdf", samplePNG).
			expectStatus(t, http.StatusConflict, "reviewer uploading")
		reviewer.do(http.MethodDelete, "/v1/documents/"+uploaded.ID, nil).
			expectStatus(t, http.StatusConflict, "reviewer deleting")
	})

	t.Run("anonymous access is rejected", func(t *testing.T) {
		anonymous := borrower.fork()
		anonymous.do(http.MethodGet, path, nil).
			expectStatus(t, http.StatusUnauthorized, "anonymous listing")
		anonymous.download("/v1/documents/"+uploaded.ID+"/download").
			expectStatus(t, http.StatusUnauthorized, "anonymous download")
	})

	t.Run("unknown ids return 404", func(t *testing.T) {
		borrower.do(http.MethodGet, "/v1/applications/LN-0000-000000/documents", nil).
			expectStatus(t, http.StatusNotFound, "unknown application")
		borrower.download("/v1/documents/doc_deadbeef/download").
			expectStatus(t, http.StatusNotFound, "unknown document")
	})
}

// 申請一旦進入募資或決議，文件就不可再增刪——審核依據不該事後被改動。
func TestIntegrationDocumentsLockedAfterDecision(t *testing.T) {
	borrower, reviewer, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	var uploaded documentRecord
	borrower.upload(path, "file", "id.pdf", samplePDF).
		expectStatus(t, http.StatusCreated, "upload before decision").decode(t, &uploaded)

	// 要求補件時仍可上傳
	reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
		map[string]string{"action": "request_more_info", "reason": "請補營業稅單"}).
		expectStatus(t, http.StatusOK, "request more info")

	t.Run("more_info_required still allows uploads", func(t *testing.T) {
		borrower.upload(path, "file", "tax.png", samplePNG).
			expectStatus(t, http.StatusCreated, "upload while more info is required")
	})

	// 核准後進入 funding，此時鎖定
	reviewer.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
		map[string]string{"action": "approve", "reason": "資料齊備"}).
		expectStatus(t, http.StatusOK, "approve")

	t.Run("funding blocks uploads", func(t *testing.T) {
		borrower.upload(path, "file", "late.jpg", sampleJPEG).
			expectStatus(t, http.StatusConflict, "upload after approval")
	})

	t.Run("funding blocks deletes", func(t *testing.T) {
		borrower.do(http.MethodDelete, "/v1/documents/"+uploaded.ID, nil).
			expectStatus(t, http.StatusConflict, "delete after approval")
	})

	t.Run("reading is still allowed", func(t *testing.T) {
		borrower.do(http.MethodGet, path, nil).
			expectStatus(t, http.StatusOK, "list after approval")
		borrower.download("/v1/documents/"+uploaded.ID+"/download").
			expectStatus(t, http.StatusOK, "download after approval")
	})
}

func TestIntegrationDocumentCountLimit(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	// 每次上傳都用不同內容，避免撞到去重
	for i := 0; i < maxDocumentsPerApp; i++ {
		content := append([]byte("%PDF-1.7\n"), []byte(fmt.Sprintf("document number %d", i))...)
		borrower.upload(path, "file", fmt.Sprintf("doc%d.pdf", i), content).
			expectStatus(t, http.StatusCreated, fmt.Sprintf("upload %d", i))
	}

	overflow := append([]byte("%PDF-1.7\n"), []byte("one too many")...)
	borrower.upload(path, "file", "overflow.pdf", overflow).
		expectStatus(t, http.StatusConflict, "exceeding the document limit")
}

// 上傳需要 CSRF token（multipart 也是變更狀態的請求）。
func TestIntegrationDocumentUploadRequiresCSRF(t *testing.T) {
	borrower, _, applicationID := documentFixture(t)
	path := "/v1/applications/" + applicationID + "/documents"

	// 手動送出不帶 CSRF header 的 multipart 請求
	request, body := buildMultipartRequest(t, borrower.server.URL+path, "file", "id.pdf", samplePDF)
	_ = body
	for _, cookie := range borrower.http.Jar.Cookies(mustParseURL(t, borrower.server.URL)) {
		request.AddCookie(cookie)
	}

	response, err := borrower.http.Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusForbidden {
		t.Errorf("upload without a CSRF token got HTTP %d, want 403", response.StatusCode)
	}
}
