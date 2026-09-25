package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 測試用的最小合法檔案內容（magic bytes 正確即可）。
var (
	samplePDF  = []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n")
	sampleJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("JFIF sample")...)
	samplePNG  = append([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, []byte("IHDR")...)
)

func TestDetectContentType(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
		wantErr bool
	}{
		{"pdf", samplePDF, "application/pdf", false},
		{"jpeg", sampleJPEG, "image/jpeg", false},
		{"png", samplePNG, "image/png", false},
		{"plain text", []byte("just some text"), "", true},
		{"empty", nil, "", true},
		// 副檔名與 Content-Type 都可偽造，只有內容說得準
		{"text pretending to be pdf", []byte("this is not really a PDF"), "", true},
		{"truncated jpeg header", []byte{0xFF, 0xD8}, "", true},
		{"truncated png header", []byte{0x89, 0x50, 0x4E}, "", true},
		// 可執行檔必須被拒
		{"elf binary", []byte{0x7F, 'E', 'L', 'F', 0x02}, "", true},
		{"shell script", []byte("#!/bin/sh\nrm -rf /\n"), "", true},
		// 內容裡有 magic bytes 但不在開頭
		{"pdf marker not at the start", []byte("prefix%PDF-1.7"), "", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := detectContentType(test.content)
			if test.wantErr {
				if err == nil {
					t.Errorf("detectContentType accepted %q as %q, want rejection", test.name, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("detectContentType(%s) returned error: %v", test.name, err)
			}
			if got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestExtensionForContentType(t *testing.T) {
	want := map[string]string{
		"application/pdf": ".pdf",
		"image/jpeg":      ".jpg",
		"image/png":       ".png",
		"application/zip": ".bin", // 未知型別的保守預設
	}
	for contentType, extension := range want {
		if got := extensionForContentType(contentType); got != extension {
			t.Errorf("extensionForContentType(%q) = %q, want %q", contentType, got, extension)
		}
	}
}

// 使用者提供的檔名只用於顯示，但仍須清掉路徑成分與會破壞標頭的字元。
func TestSanitizeOriginalName(t *testing.T) {
	tests := map[string]string{
		"身分證正反面.jpg":                 "身分證正反面.jpg",
		"report.pdf":                 "report.pdf",
		"../../../etc/passwd":        "passwd",
		"/absolute/path/file.pdf":    "file.pdf",
		`..\..\windows\system32.dll`: "system32.dll",
		"":                           "document",
		"   ":                        "document",
		".":                          "document",
		"..":                         "document",
		// 這些字元會破壞 Content-Disposition
		"evil\"name.pdf":    "evilname.pdf",
		"line\nbreak.pdf":   "linebreak.pdf",
		"carriage\rret.pdf": "carriageret.pdf",
	}

	for input, want := range tests {
		if got := sanitizeOriginalName(input); got != want {
			t.Errorf("sanitizeOriginalName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSanitizeOriginalNameTruncatesLongNames(t *testing.T) {
	long := strings.Repeat("あ", 500) + ".pdf"
	got := sanitizeOriginalName(long)
	if count := len([]rune(got)); count > maxOriginalNameRune {
		t.Errorf("sanitized name has %d runes, want at most %d", count, maxOriginalNameRune)
	}
}

func TestNewDocumentID(t *testing.T) {
	first, err := newDocumentID()
	if err != nil {
		t.Fatalf("newDocumentID: %v", err)
	}
	if !strings.HasPrefix(first, "doc_") {
		t.Errorf("id %q should start with doc_", first)
	}
	second, _ := newDocumentID()
	if first == second {
		t.Error("newDocumentID produced a duplicate id")
	}
}

// 儲存路徑完全由伺服器生成，且必須依實際型別決定副檔名。
func TestGeneratePath(t *testing.T) {
	path, err := generatePath("application/pdf")
	if err != nil {
		t.Fatalf("generatePath: %v", err)
	}
	if !strings.HasSuffix(path, ".pdf") {
		t.Errorf("path %q should end with .pdf", path)
	}
	if strings.Contains(path, "..") {
		t.Errorf("path %q contains a parent reference", path)
	}
	if filepath.IsAbs(path) {
		t.Errorf("path %q should be relative", path)
	}

	other, _ := generatePath("application/pdf")
	if path == other {
		t.Error("generatePath produced a duplicate path")
	}
}

func newTestStorage(t *testing.T) *documentStorage {
	t.Helper()
	storage, err := newDocumentStorage(t.TempDir())
	if err != nil {
		t.Fatalf("newDocumentStorage: %v", err)
	}
	return storage
}

func TestDocumentStorageRoundTrip(t *testing.T) {
	storage := newTestStorage(t)

	path, err := generatePath("application/pdf")
	if err != nil {
		t.Fatalf("generatePath: %v", err)
	}
	if err := storage.write(path, samplePDF); err != nil {
		t.Fatalf("write: %v", err)
	}

	file, err := storage.open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	content := make([]byte, len(samplePDF))
	if _, err := file.Read(content); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(content, samplePDF) {
		t.Error("the stored content does not match what was written")
	}

	if err := storage.remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := storage.open(path); err == nil {
		t.Error("the document is still readable after removal")
	}
	// 刪除不存在的檔案應視為成功（幂等）
	if err := storage.remove(path); err != nil {
		t.Errorf("removing a missing file returned an error: %v", err)
	}
}

// 即使 storage_path 來自資料庫，也不可讓讀寫跑出儲存根目錄。
func TestDocumentStorageRejectsPathTraversal(t *testing.T) {
	storage := newTestStorage(t)

	malicious := []string{
		"../escaped.pdf",
		"../../etc/passwd",
		"2026/../../escaped.pdf",
		"/etc/passwd",
		"",
	}

	for _, path := range malicious {
		t.Run(path, func(t *testing.T) {
			if err := storage.write(path, samplePDF); err == nil {
				t.Errorf("write accepted the traversal path %q", path)
			}
			if _, err := storage.open(path); err == nil {
				t.Errorf("open accepted the traversal path %q", path)
			}
			if err := storage.remove(path); err == nil {
				t.Errorf("remove accepted the traversal path %q", path)
			}
		})
	}
}

// 巢狀但合法的相對路徑要能正常運作（確認防護沒有過度嚴格）。
func TestDocumentStorageAllowsNestedPaths(t *testing.T) {
	storage := newTestStorage(t)
	if err := storage.write("2026/09/abc.pdf", samplePDF); err != nil {
		t.Fatalf("write nested path: %v", err)
	}
	file, err := storage.open("2026/09/abc.pdf")
	if err != nil {
		t.Fatalf("open nested path: %v", err)
	}
	_ = file.Close()
}

// 路徑由隨機值生成，若已存在表示有問題，不可覆蓋既有檔案。
func TestDocumentStorageDoesNotOverwrite(t *testing.T) {
	storage := newTestStorage(t)
	const path = "2026/09/fixed.pdf"

	if err := storage.write(path, samplePDF); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := storage.write(path, samplePNG); err == nil {
		t.Error("write overwrote an existing file; storage paths must be unique")
	}

	// 確認原內容未被破壞
	file, err := storage.open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	content := make([]byte, len(samplePDF))
	_, _ = file.Read(content)
	if !bytes.Equal(content, samplePDF) {
		t.Error("the original content was modified by the failed write")
	}
}

func TestNewDocumentStorageRejectsEmptyRoot(t *testing.T) {
	if _, err := newDocumentStorage(""); err == nil {
		t.Error("newDocumentStorage accepted an empty root")
	}
	if _, err := newDocumentStorage("   "); err == nil {
		t.Error("newDocumentStorage accepted a whitespace root")
	}
}

func TestNewDocumentStorageCreatesRoot(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "documents")

	if _, err := newDocumentStorage(nested); err != nil {
		t.Fatalf("newDocumentStorage: %v", err)
	}
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("the storage root was not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("the storage root is not a directory")
	}
}

func TestChecksumOf(t *testing.T) {
	first := checksumOf(samplePDF)
	if len(first) != 64 {
		t.Errorf("checksum length %d, want 64 (hex sha256)", len(first))
	}
	if first != checksumOf(samplePDF) {
		t.Error("checksumOf is not deterministic")
	}
	if first == checksumOf(samplePNG) {
		t.Error("different content produced the same checksum")
	}
}

func TestDocumentEditableStatuses(t *testing.T) {
	// 仍可編輯：尚未做出決議
	for _, status := range []string{"pending", "reviewing", "more_info_required"} {
		if !documentEditableStatuses[status] {
			t.Errorf("status %q should allow document changes", status)
		}
	}
	// 不可編輯：審核依據不應在決議後被改動
	for _, status := range []string{"approved", "rejected", "funding", "funded", "disbursed"} {
		if documentEditableStatuses[status] {
			t.Errorf("status %q must not allow document changes", status)
		}
	}
}

// HTTP 標頭只能放 ASCII，非 ASCII 檔名必須以 RFC 5987 編碼，
// 否則中文檔名會變成亂碼。
func TestContentDispositionFor(t *testing.T) {
	tests := []struct {
		name         string
		fileName     string
		wantContains []string
	}{
		{
			"ascii name",
			"report.pdf",
			[]string{`filename="report.pdf"`, "filename*=UTF-8''report.pdf"},
		},
		{
			"chinese name",
			"測試文件.pdf",
			// ASCII fallback 保留副檔名；filename* 帶百分比編碼
			[]string{`filename="____.pdf"`, "filename*=UTF-8''%E6%B8%AC"},
		},
		{
			"mixed name",
			"ID-身分證.jpg",
			[]string{`filename="ID-___.jpg"`, "filename*=UTF-8''ID-"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := contentDispositionFor(test.fileName)

			if !strings.HasPrefix(got, "attachment;") {
				t.Errorf("header %q should start with attachment;", got)
			}
			for _, want := range test.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("header %q does not contain %q", got, want)
				}
			}
			// 標頭本身必須全為 ASCII
			for _, char := range got {
				if char > 127 {
					t.Errorf("header contains a non-ASCII rune %q: %s", char, got)
					break
				}
			}
			// 不可含會截斷標頭的字元
			if strings.ContainsAny(got, "\r\n") {
				t.Errorf("header contains a line break: %q", got)
			}
		})
	}
}

func TestAsciiFallbackName(t *testing.T) {
	tests := map[string]string{
		"report.pdf": "report.pdf",
		// 非 ASCII 逐字元換成底線；副檔名是 ASCII 故予以保留
		"測試文件.pdf":   "____.pdf",
		"ID-身分證.jpg": "ID-___.jpg",
		// 完全沒有可辨識的 ASCII 內容時才退回通用名稱
		"身分證":            "document",
		"中文":             "document",
		`quote"name.pdf`: "quote_name.pdf",
	}
	for input, want := range tests {
		if got := asciiFallbackName(input); got != want {
			t.Errorf("asciiFallbackName(%q) = %q, want %q", input, got, want)
		}
	}
}
