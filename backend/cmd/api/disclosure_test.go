package main

import (
	"net/http/httptest"
	"testing"
)

func TestIncomeBracket(t *testing.T) {
	tests := []struct {
		amount int64
		want   string
	}{
		{0, "未提供"},
		{-1, "未提供"},
		{1, "50 萬以下"},
		{499999, "50 萬以下"},
		{500000, "50～80 萬"}, // 邊界歸上一級
		{799999, "50～80 萬"},
		{800000, "80～120 萬"},
		{1080000, "80～120 萬"},
		{1199999, "80～120 萬"},
		{1200000, "120～200 萬"},
		{2000000, "200～300 萬"},
		{2999999, "200～300 萬"},
		{3000000, "300 萬以上"},
		{99000000, "300 萬以上"},
	}

	for _, test := range tests {
		if got := incomeBracket(test.amount); got != test.want {
			t.Errorf("incomeBracket(%d) = %q, want %q", test.amount, got, test.want)
		}
	}
}

// 級距標籤不可洩漏精確金額：同一級距內的不同金額必須得到相同標籤。
func TestIncomeBracketHidesExactAmount(t *testing.T) {
	sameRange := []int64{800000, 900000, 1000000, 1080000, 1199999}
	first := incomeBracket(sameRange[0])
	for _, amount := range sameRange[1:] {
		if got := incomeBracket(amount); got != first {
			t.Errorf("incomeBracket(%d) = %q but incomeBracket(%d) = %q; "+
				"amounts in the same bracket must be indistinguishable",
				amount, got, sameRange[0], first)
		}
	}
}

func TestExpenseBracket(t *testing.T) {
	tests := map[int64]string{
		0:      "未提供",
		-100:   "未提供",
		5000:   "1 萬以下",
		9999:   "1 萬以下",
		10000:  "1～3 萬", // 下界
		29999:  "1～3 萬",
		30000:  "3～5 萬", // 邊界歸上一級
		32000:  "3～5 萬",
		49999:  "3～5 萬",
		50000:  "5～10 萬",
		99999:  "5～10 萬",
		100000: "10 萬以上",
	}
	for amount, want := range tests {
		if got := expenseBracket(amount); got != want {
			t.Errorf("expenseBracket(%d) = %q, want %q", amount, got, want)
		}
	}
}

func TestParsePageParams(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantLimit  int
		wantOffset int
	}{
		{"no params uses defaults", "", defaultPageLimit, 0},
		{"explicit values", "?limit=10&offset=20", 10, 20},
		{"limit is capped", "?limit=99999", maxPageLimit, 0},
		{"zero limit falls back", "?limit=0", defaultPageLimit, 0},
		{"negative limit falls back", "?limit=-5", defaultPageLimit, 0},
		{"negative offset is ignored", "?offset=-5", defaultPageLimit, 0},
		// 無效值不該讓整個請求失敗——分頁是呈現層細節
		{"garbage limit falls back", "?limit=abc", defaultPageLimit, 0},
		{"garbage offset falls back", "?offset=xyz", defaultPageLimit, 0},
		{"limit at the cap", "?limit=200", maxPageLimit, 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/v1/applications"+test.query, nil)
			got := parsePageParams(request)
			if got.Limit != test.wantLimit {
				t.Errorf("limit = %d, want %d", got.Limit, test.wantLimit)
			}
			if got.Offset != test.wantOffset {
				t.Errorf("offset = %d, want %d", got.Offset, test.wantOffset)
			}
		})
	}
}

func TestPageMeta(t *testing.T) {
	tests := []struct {
		name        string
		params      pageParams
		total       int
		wantHasMore bool
	}{
		{"first page of many", pageParams{Limit: 10, Offset: 0}, 100, true},
		{"last page exactly", pageParams{Limit: 10, Offset: 90}, 100, false},
		{"beyond the end", pageParams{Limit: 10, Offset: 200}, 100, false},
		{"single page fits all", pageParams{Limit: 50, Offset: 0}, 20, false},
		{"empty result", pageParams{Limit: 50, Offset: 0}, 0, false},
		{"one more remains", pageParams{Limit: 10, Offset: 0}, 11, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := test.params.meta(test.total)
			if meta.HasMore != test.wantHasMore {
				t.Errorf("hasMore = %v, want %v (total %d, offset %d, limit %d)",
					meta.HasMore, test.wantHasMore, test.total, test.params.Offset, test.params.Limit)
			}
			if meta.Total != test.total {
				t.Errorf("total = %d, want %d", meta.Total, test.total)
			}
			if meta.Limit != test.params.Limit || meta.Offset != test.params.Offset {
				t.Error("meta does not echo back the requested limit and offset")
			}
		})
	}
}

// 分頁上限存在的理由是單一請求不該能拖垮資料庫或回應體積。
func TestPageLimitBounds(t *testing.T) {
	if defaultPageLimit <= 0 || defaultPageLimit > maxPageLimit {
		t.Errorf("defaultPageLimit %d is not within (0, %d]", defaultPageLimit, maxPageLimit)
	}
	if maxPageLimit > 1000 {
		t.Errorf("maxPageLimit %d is high enough to risk oversized responses", maxPageLimit)
	}
}
