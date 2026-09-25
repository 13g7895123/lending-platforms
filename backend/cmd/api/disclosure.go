package main

import (
	"net/http"
	"strconv"
)

// 收入與支出不以原值回傳 API：前端從未顯示它們，但每筆清單回應都帶著，
// 形成不必要的曝露面。風控判讀所需的是 DBR 與大致級距，兩者都不需要精確金額。
//
// 資料庫仍存明文——DBR 由後端計算，且風控可能需要以收入排序或篩選。
// 完整值透過 POST /v1/admin/applications/{id}/reveal 取得，該端點要求理由並寫入稽核。

// incomeBrackets 由小到大排列；上界為 0 表示「以上」。
var incomeBrackets = []struct {
	upper int64
	label string
}{
	{500000, "50 萬以下"},
	{800000, "50～80 萬"},
	{1200000, "80～120 萬"},
	{2000000, "120～200 萬"},
	{3000000, "200～300 萬"},
	{0, "300 萬以上"},
}

// incomeBracket 把年收入歸入級距標籤。
func incomeBracket(amount int64) string {
	if amount <= 0 {
		return "未提供"
	}
	for _, bracket := range incomeBrackets {
		if bracket.upper == 0 || amount < bracket.upper {
			return bracket.label
		}
	}
	return incomeBrackets[len(incomeBrackets)-1].label
}

// expenseBracket 把每月固定支出歸入級距。
// 級距較窄，因為支出的數量級比年收入小。
func expenseBracket(amount int64) string {
	switch {
	case amount <= 0:
		return "未提供"
	case amount < 10000:
		return "1 萬以下"
	case amount < 30000:
		return "1～3 萬"
	case amount < 50000:
		return "3～5 萬"
	case amount < 100000:
		return "5～10 萬"
	default:
		return "10 萬以上"
	}
}

// ---------------------------------------------------------------- 分頁

// 分頁參數的界線。上限存在的理由是單一請求不該能拖垮資料庫或回應體積。
const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

type pageParams struct {
	Limit  int
	Offset int
}

// pageMeta 隨清單一起回傳，讓呼叫端知道還有沒有下一頁。
type pageMeta struct {
	Total   int  `json:"total"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"hasMore"`
}

// parsePageParams 解析 ?limit= 與 ?offset=。
//
// 無效或超出範圍的值一律夾到合法區間而非回錯誤：
// 分頁參數是呈現層的細節，不該讓整個請求失敗。
func parsePageParams(r *http.Request) pageParams {
	params := pageParams{Limit: defaultPageLimit, Offset: 0}

	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			params.Limit = value
		}
	}
	if params.Limit < 1 {
		params.Limit = defaultPageLimit
	}
	if params.Limit > maxPageLimit {
		params.Limit = maxPageLimit
	}

	if raw := r.URL.Query().Get("offset"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			params.Offset = value
		}
	}
	return params
}

// meta 依總筆數與本頁結果組出分頁資訊。
func (p pageParams) meta(total int) pageMeta {
	return pageMeta{
		Total:   total,
		Limit:   p.Limit,
		Offset:  p.Offset,
		HasMore: p.Offset+p.Limit < total,
	}
}
