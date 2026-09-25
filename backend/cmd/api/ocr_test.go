package main

import (
	"strings"
	"testing"
)

func TestNormalizeForMatching(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain ascii is uppercased", "a123456789", "A123456789"},
		// OCR 常在字元之間插入空白
		{"spaces are removed", "A 123 456 789", "A123456789"},
		{"newlines are removed", "陳建宏\n身分證", "陳建宏身分證"},
		{"tabs are removed", "A\t123", "A123"},
		// 全形數字與字母要能與半形比對
		{"fullwidth digits become halfwidth", "Ａ１２３", "A123"},
		{"fullwidth space is removed", "陳　建宏", "陳建宏"},
		{"mixed width", "Ａ１2３456789", "A123456789"},
		{"empty stays empty", "", ""},
		{"whitespace only becomes empty", "  \n\t ", ""},
		{"chinese is preserved", "陳建宏", "陳建宏"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeForMatching(test.input); got != test.want {
				t.Errorf("normalizeForMatching(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

// 正規化的目的是讓「實際相同但格式有差」的文字能比對成功。
func TestNormalizeMakesEquivalentFormsMatch(t *testing.T) {
	equivalent := []string{
		"A123456789",
		"A 123 456 789",
		"Ａ１２３４５６７８９",
		"a123456789",
		"A123456789\n",
	}
	first := normalizeForMatching(equivalent[0])
	for _, variant := range equivalent[1:] {
		if got := normalizeForMatching(variant); got != first {
			t.Errorf("normalizeForMatching(%q) = %q, want %q (equivalent forms must match)",
				variant, got, first)
		}
	}
}

func TestVerifyFieldInText(t *testing.T) {
	// 模擬 OCR 從身分證擷取的文字
	extracted := `中華民國 國民身分證
姓名 陳建宏
統一編號 A123456789
出生年月日 民國 70 年 5 月 20 日`

	tests := []struct {
		name     string
		expected string
		want     string
	}{
		{"name is found", "陳建宏", verificationMatched},
		{"id number is found", "A123456789", verificationMatched},
		// 空白與大小寫差異不該造成漏判
		{"id with spaces is found", "A 123 456 789", verificationMatched},
		{"lowercase id is found", "a123456789", verificationMatched},
		{"different name is not found", "王小明", verificationNotFound},
		{"different id is not found", "B987654321", verificationNotFound},
		// 沒有期望值時無從比對
		{"empty expected is unreadable", "", verificationUnreadable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := verifyFieldInText(extracted, test.expected); got != test.want {
				t.Errorf("verifyFieldInText(_, %q) = %q, want %q", test.expected, got, test.want)
			}
		})
	}
}

// OCR 對模糊影像常回傳極少的雜訊字元。
// 此時要回報 unreadable 而非 not_found——後者會讓風控誤以為文件內容不符。
func TestVerifyFieldInTextDistinguishesUnreadableFromMismatch(t *testing.T) {
	cases := []struct {
		name      string
		extracted string
		want      string
	}{
		{"empty output is unreadable", "", verificationUnreadable},
		{"a few noise characters are unreadable", "x7 .", verificationUnreadable},
		{"whitespace only is unreadable", "   \n\n  ", verificationUnreadable},
		// 擷取到足夠文字但找不到目標 → 確實是不符，而非無法辨識
		{"enough text without the value is not_found",
			"這是一份與申請人無關的文件內容，字數足夠但沒有目標值", verificationNotFound},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := verifyFieldInText(test.extracted, "陳建宏")
			if got != test.want {
				t.Errorf("verifyFieldInText(%q, _) = %q, want %q",
					test.extracted, got, test.want)
			}
		})
	}
}

func TestMinReadableCharsThreshold(t *testing.T) {
	// 剛好達到門檻的文字應被視為可讀
	atThreshold := strings.Repeat("字", minReadableChars)
	if got := verifyFieldInText(atThreshold, "不存在"); got != verificationNotFound {
		t.Errorf("text at the threshold gave %q, want not_found (it is readable)", got)
	}

	belowThreshold := strings.Repeat("字", minReadableChars-1)
	if got := verifyFieldInText(belowThreshold, "不存在"); got != verificationUnreadable {
		t.Errorf("text below the threshold gave %q, want unreadable", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 100); got != "short" {
		t.Errorf("truncate did not leave a short string alone: %q", got)
	}
	long := strings.Repeat("a", 300)
	got := truncate(long, 100)
	if len(got) > 104 { // 100 + 省略號
		t.Errorf("truncate returned %d bytes, want about 100", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("truncated output should be marked with an ellipsis")
	}
	if got := truncate("  padded  ", 100); got != "padded" {
		t.Errorf("truncate did not trim whitespace: %q", got)
	}
}

// OCR 逾時與輸出上限的存在理由是異常檔案不該拖垮 worker。
func TestOCRLimits(t *testing.T) {
	if ocrTimeoutSeconds < 5 {
		t.Error("the OCR timeout is too short for a legitimate multi-page scan")
	}
	if ocrTimeoutSeconds > 120 {
		t.Errorf("an OCR timeout of %ds lets one bad file stall the worker", ocrTimeoutSeconds)
	}
	if maxOCRTextBytes < 1024 {
		t.Error("the OCR text limit is too small to hold a page of text")
	}
	if ocrBatchSize < 1 {
		t.Error("the OCR batch size must process at least one document")
	}
}
