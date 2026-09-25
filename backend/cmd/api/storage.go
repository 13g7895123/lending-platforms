package main

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// 檔案上傳限制。
const (
	maxDocumentBytes    = 10 << 20 // 10 MiB
	maxDocumentsPerApp  = 20
	maxOriginalNameRune = 200
)

var errUnsupportedFileType = errors.New("unsupported file type")

// detectContentType 以檔案開頭的 magic bytes 判定型別。
//
// 刻意不看副檔名與 Content-Type：兩者都由客戶端提供，可以任意偽造。
// 只接受這三種格式，其餘一律拒絕。
func detectContentType(content []byte) (string, error) {
	switch {
	case bytes.HasPrefix(content, []byte("%PDF-")):
		return "application/pdf", nil
	// JPEG: FF D8 FF
	case len(content) >= 3 && content[0] == 0xFF && content[1] == 0xD8 && content[2] == 0xFF:
		return "image/jpeg", nil
	// PNG: 89 50 4E 47 0D 0A 1A 0A
	case bytes.HasPrefix(content, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png", nil
	default:
		return "", errUnsupportedFileType
	}
}

// extensionForContentType 回傳儲存時使用的副檔名。
// 依實際偵測到的型別決定，不沿用使用者提供的副檔名。
func extensionForContentType(contentType string) string {
	switch contentType {
	case "application/pdf":
		return ".pdf"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	default:
		return ".bin"
	}
}

// sanitizeOriginalName 清理使用者提供的檔名。
//
// 這個值只用於顯示與下載時的 filename，絕不用於組出儲存路徑，
// 但仍需清掉路徑分隔符與控制字元，避免污染日誌或下載標頭。
func sanitizeOriginalName(name string) string {
	// 先取 base，去掉任何目錄成分（含 Windows 風格的反斜線）
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)

	cleaned := make([]rune, 0, len(name))
	for _, char := range name {
		switch {
		case char < 0x20 || char == 0x7F:
			// 丟掉控制字元
		case char == '"' || char == '\n' || char == '\r':
			// 這些會破壞 Content-Disposition 標頭
		default:
			cleaned = append(cleaned, char)
		}
	}

	result := strings.TrimSpace(string(cleaned))
	if result == "" || result == "." || result == ".." {
		return "document"
	}
	if utf8.RuneCountInString(result) > maxOriginalNameRune {
		runes := []rune(result)
		result = string(runes[:maxOriginalNameRune])
	}
	return result
}

// newDocumentID 產生文件識別碼。
func newDocumentID() (string, error) {
	buffer := make([]byte, 12)
	if _, err := cryptorand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate document id: %w", err)
	}
	return "doc_" + hex.EncodeToString(buffer), nil
}

// documentStorage 把檔案內容寫到磁碟。
//
// 儲存路徑完全由伺服器生成（日期目錄 + 隨機檔名 + 依實際型別決定的副檔名），
// 因此使用者無法透過檔名進行路徑穿越，也無法讓 .pdf 實際上是可執行檔。
type documentStorage struct {
	root string
}

func newDocumentStorage(root string) (*documentStorage, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("document storage root is empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve storage root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("create storage root: %w", err)
	}
	return &documentStorage{root: absolute}, nil
}

// generatePath 產生一個相對儲存路徑，例如 2026/09/a1b2c3….pdf。
// 以日期分層避免單一目錄累積過多檔案。
func generatePath(contentType string) (string, error) {
	buffer := make([]byte, 16)
	if _, err := cryptorand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate storage name: %w", err)
	}
	now := time.Now().UTC()
	return filepath.ToSlash(filepath.Join(
		now.Format("2006"),
		now.Format("01"),
		hex.EncodeToString(buffer)+extensionForContentType(contentType),
	)), nil
}

// resolve 把相對路徑轉為絕對路徑，並確認它仍在 root 之內。
//
// 即使 storage_path 來自資料庫，仍要做這道檢查：
// 資料庫遭篡改或程式有誤時，不可讓讀寫跑出儲存根目錄。
func (s *documentStorage) resolve(relativePath string) (string, error) {
	if relativePath == "" {
		return "", errors.New("empty storage path")
	}
	cleaned := filepath.Clean(filepath.FromSlash(relativePath))
	if filepath.IsAbs(cleaned) {
		return "", errors.New("storage path must be relative")
	}
	absolute := filepath.Join(s.root, cleaned)

	// 確認結果仍在 root 底下（擋 ../ 穿越）
	relative, err := filepath.Rel(s.root, absolute)
	if err != nil {
		return "", fmt.Errorf("resolve storage path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("storage path escapes the storage root")
	}
	return absolute, nil
}

// write 寫入檔案內容。
func (s *documentStorage) write(relativePath string, content []byte) error {
	absolute, err := s.resolve(relativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		return fmt.Errorf("create storage directory: %w", err)
	}
	// O_EXCL：路徑由隨機值生成，若已存在表示有問題，不覆蓋
	file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return fmt.Errorf("create document file: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(content); err != nil {
		// 寫入失敗時清掉半成品，避免留下無主檔案
		_ = os.Remove(absolute)
		return fmt.Errorf("write document: %w", err)
	}
	return nil
}

// open 開啟檔案供下載。呼叫端負責關閉。
func (s *documentStorage) open(relativePath string) (io.ReadSeekCloser, error) {
	absolute, err := s.resolve(relativePath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, fmt.Errorf("open document: %w", err)
	}
	return file, nil
}

// remove 刪除檔案。檔案不存在視為成功（幂等）。
func (s *documentStorage) remove(relativePath string) error {
	absolute, err := s.resolve(relativePath)
	if err != nil {
		return err
	}
	if err := os.Remove(absolute); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove document: %w", err)
	}
	return nil
}

// checksumOf 回傳內容的 SHA-256（十六進位）。
func checksumOf(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
