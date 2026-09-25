package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

// OCR 從上傳的文件擷取文字，用於比對申請人填寫的資料是否出現在文件中。
//
// 能力邊界：OCR 無法辨識變造，因此不判斷文件真偽，也不自動核准或婉拒。
// 結果僅供風控參考，決策權仍在人。

const (
	// tesseract 對單頁文件通常在數秒內完成；逾時多半代表檔案異常
	ocrTimeoutSeconds = 45
	// 擷取文字的保存上限，避免異常檔案產生巨量輸出
	maxOCRTextBytes = 64 << 10
	// PDF 只處理第一頁：身分證、薪轉存摺等證明文件的關鍵資訊都在首頁
	pdfFirstPageOnly = 1
)

var errOCRUnavailable = errors.New("OCR tooling is not available")

// ocrEngine 封裝外部 OCR 工具的呼叫。
type ocrEngine struct {
	tesseractPath string
	pdftoppmPath  string
	languages     string
}

// newOCREngine 尋找所需的外部程序。
// 找不到 tesseract 時回傳 nil，呼叫端應把文件標記為 skipped 而非 failed——
// 缺少工具是部署環境的問題，不是文件的問題。
func newOCREngine() (*ocrEngine, error) {
	tesseract, err := exec.LookPath("tesseract")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errOCRUnavailable, err)
	}
	// pdftoppm 可選：沒有它就只能處理圖片
	pdftoppm, _ := exec.LookPath("pdftoppm")

	return &ocrEngine{
		tesseractPath: tesseract,
		pdftoppmPath:  pdftoppm,
		// 繁體中文 + 英文：身分證與薪轉文件兩者都有
		languages: env("OCR_LANGUAGES", "chi_tra+eng"),
	}, nil
}

// extractText 從檔案內容擷取文字。
//
// 內容來自使用者上傳，因此全程不經過 shell：以 exec.Command 傳遞參數，
// 檔名由伺服器生成於暫存目錄，不使用使用者提供的名稱。
func (e *ocrEngine) extractText(ctx context.Context, content []byte, contentType string) (string, error) {
	workDir, err := os.MkdirTemp("", "creditflow-ocr-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	// 無論成功或失敗都要清掉暫存檔，避免累積未加密的文件內容
	defer func() { _ = os.RemoveAll(workDir) }()

	imagePath, err := e.prepareImage(ctx, workDir, content, contentType)
	if err != nil {
		return "", err
	}

	// tesseract <input> stdout -l <langs>
	command := exec.CommandContext(ctx, e.tesseractPath, imagePath, "stdout", "-l", e.languages)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("OCR timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("tesseract failed: %w (%s)", err, truncate(stderr.String(), 200))
	}

	text := stdout.String()
	if len(text) > maxOCRTextBytes {
		text = text[:maxOCRTextBytes]
	}
	return text, nil
}

// prepareImage 把內容寫成 tesseract 可讀的檔案；PDF 先轉成圖片。
func (e *ocrEngine) prepareImage(
	ctx context.Context, workDir string, content []byte, contentType string,
) (string, error) {
	if contentType == "application/pdf" {
		if e.pdftoppmPath == "" {
			return "", fmt.Errorf("%w: pdftoppm is required for PDF input", errOCRUnavailable)
		}
		pdfPath := filepath.Join(workDir, "input.pdf")
		if err := os.WriteFile(pdfPath, content, 0o600); err != nil {
			return "", fmt.Errorf("write pdf: %w", err)
		}

		// pdftoppm -png -r 200 -f 1 -l 1 input.pdf page
		prefix := filepath.Join(workDir, "page")
		command := exec.CommandContext(ctx, e.pdftoppmPath,
			"-png", "-r", "200",
			"-f", fmt.Sprint(pdfFirstPageOnly),
			"-l", fmt.Sprint(pdfFirstPageOnly),
			pdfPath, prefix)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			return "", fmt.Errorf("pdftoppm failed: %w (%s)", err, truncate(stderr.String(), 200))
		}

		// pdftoppm 的輸出檔名帶頁碼後綴，實際名稱依版本而異，故以樣式搜尋
		matches, err := filepath.Glob(prefix + "*")
		if err != nil || len(matches) == 0 {
			return "", errors.New("pdftoppm produced no output")
		}
		return matches[0], nil
	}

	extension := extensionForContentType(contentType)
	imagePath := filepath.Join(workDir, "input"+extension)
	if err := os.WriteFile(imagePath, content, 0o600); err != nil {
		return "", fmt.Errorf("write image: %w", err)
	}
	return imagePath, nil
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// ---------------------------------------------------------------- 文字比對

// normalizeForMatching 正規化文字以便比對。
//
// OCR 的輸出常帶有多餘空白、換行，以及全形／半形混用；
// 直接字串比對會因為這些差異而漏掉實際存在的內容。
func normalizeForMatching(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))

	for _, char := range value {
		switch {
		case unicode.IsSpace(char):
			// 空白全部移除：OCR 可能在字元之間插入空白
		case char >= 0xFF01 && char <= 0xFF5E:
			// 全形 ASCII 轉半形（ＡＢＣ→ABC、１２３→123）
			builder.WriteRune(char - 0xFEE0)
		case char == 0x3000:
			// 全形空白，同樣移除
		default:
			builder.WriteRune(unicode.ToUpper(char))
		}
	}
	return builder.String()
}

// 比對結果。
const (
	verificationMatched    = "matched"
	verificationNotFound   = "not_found"
	verificationUnreadable = "unreadable"
)

// minReadableChars 是判定「擷取成功」的最低字元數。
//
// OCR 對模糊或空白的影像常回傳極少的雜訊字元；低於此門檻時
// 回報 unreadable 而非 not_found——後者會讓風控誤以為文件內容不符。
const minReadableChars = 10

// verifyFieldInText 判斷 expected 是否出現在 OCR 擷取的文字中。
func verifyFieldInText(extracted, expected string) string {
	normalizedText := normalizeForMatching(extracted)
	if len([]rune(normalizedText)) < minReadableChars {
		return verificationUnreadable
	}

	normalizedExpected := normalizeForMatching(expected)
	if normalizedExpected == "" {
		return verificationUnreadable
	}
	if strings.Contains(normalizedText, normalizedExpected) {
		return verificationMatched
	}
	return verificationNotFound
}
