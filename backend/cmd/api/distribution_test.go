package main

import (
	"testing"
	"time"
)

// 分配的核心不變量：加總必須精確等於待分配金額。
// 分錯錢比不分錢更糟，因此這條在所有情境下都不能破。
func TestAllocateProportionallyConservesTotal(t *testing.T) {
	cases := []struct {
		name   string
		shares []investorShare
		total  int64
	}{
		{
			"single investor takes everything",
			[]investorShare{{InvestmentID: 1, Amount: 100000}},
			17950,
		},
		{
			"two equal investors",
			[]investorShare{{InvestmentID: 1, Amount: 50000}, {InvestmentID: 2, Amount: 50000}},
			17950,
		},
		{
			"three investors with an indivisible amount",
			[]investorShare{
				{InvestmentID: 1, Amount: 100000},
				{InvestmentID: 2, Amount: 100000},
				{InvestmentID: 3, Amount: 100000},
			},
			10000, // 10000 / 3 無法整除
		},
		{
			"lopsided shares",
			[]investorShare{
				{InvestmentID: 1, Amount: 999000},
				{InvestmentID: 2, Amount: 1000},
			},
			17950,
		},
		{
			"many small investors",
			func() []investorShare {
				shares := make([]investorShare, 37)
				for i := range shares {
					shares[i] = investorShare{InvestmentID: int64(i + 1), Amount: 1000}
				}
				return shares
			}(),
			12345,
		},
		{
			"amount smaller than investor count",
			[]investorShare{
				{InvestmentID: 1, Amount: 1000},
				{InvestmentID: 2, Amount: 1000},
				{InvestmentID: 3, Amount: 1000},
			},
			2, // 比人數還少
		},
		{
			"one dollar",
			[]investorShare{{InvestmentID: 1, Amount: 500}, {InvestmentID: 2, Amount: 500}},
			1,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var basis int64
			for _, share := range test.shares {
				basis += share.Amount
			}

			parts := allocateProportionally(test.shares, test.total, basis)
			if len(parts) != len(test.shares) {
				t.Fatalf("got %d parts, want %d", len(parts), len(test.shares))
			}

			var sum int64
			for i, part := range parts {
				if part < 0 {
					t.Errorf("investor %d got a negative allocation: %d", i, part)
				}
				sum += part
			}
			if sum != test.total {
				t.Errorf("allocations sum to %d, want exactly %d (money was created or lost)",
					sum, test.total)
			}
		})
	}
}

// 占比必須反映投資金額：投得多的人分得多（或至少不少）。
func TestAllocateProportionallyRespectsShares(t *testing.T) {
	shares := []investorShare{
		{InvestmentID: 1, Amount: 750000}, // 75%
		{InvestmentID: 2, Amount: 250000}, // 25%
	}
	parts := allocateProportionally(shares, 100000, 1000000)

	if parts[0] != 75000 {
		t.Errorf("75%% investor got %d, want 75000", parts[0])
	}
	if parts[1] != 25000 {
		t.Errorf("25%% investor got %d, want 25000", parts[1])
	}
}

// 捨入殘差應歸給投資金額最大者，使相對誤差最小。
func TestAllocateProportionallyGivesRemainderToLargestInvestor(t *testing.T) {
	shares := []investorShare{
		{InvestmentID: 1, Amount: 1000},   // 小額
		{InvestmentID: 2, Amount: 999000}, // 大額
	}
	// 1000 元分給 1000:999000 的兩人，小額者應得 1 元，剩下由大額者拿
	parts := allocateProportionally(shares, 1000, 1000000)

	if parts[0] != 1 {
		t.Errorf("small investor got %d, want 1", parts[0])
	}
	if parts[1] != 999 {
		t.Errorf("large investor got %d, want 999 (including the remainder)", parts[1])
	}
	if parts[0]+parts[1] != 1000 {
		t.Errorf("sum = %d, want 1000", parts[0]+parts[1])
	}
}

// 相同投資金額時，分配結果必須是決定性的（依 InvestmentID 排序），
// 否則同一筆還款重跑會得到不同結果，難以對帳。
func TestAllocateProportionallyIsDeterministic(t *testing.T) {
	makeShares := func() []investorShare {
		return []investorShare{
			{InvestmentID: 3, Amount: 1000},
			{InvestmentID: 1, Amount: 1000},
			{InvestmentID: 2, Amount: 1000},
		}
	}

	first := allocateProportionally(makeShares(), 10, 3000)
	for attempt := 0; attempt < 5; attempt++ {
		again := allocateProportionally(makeShares(), 10, 3000)
		for i := range first {
			if first[i] != again[i] {
				t.Fatalf("allocation is not deterministic: index %d got %d then %d",
					i, first[i], again[i])
			}
		}
	}

	// 殘差 1 元應給 InvestmentID 最小者（金額相同時的決勝規則）
	shares := makeShares()
	parts := allocateProportionally(shares, 10, 3000)
	var extraIndex = -1
	for i, part := range parts {
		if part == 4 {
			extraIndex = i
		}
	}
	if extraIndex < 0 {
		t.Fatalf("no investor received the remainder: %v", parts)
	}
	if shares[extraIndex].InvestmentID != 1 {
		t.Errorf("remainder went to investment %d, want the lowest id (1)",
			shares[extraIndex].InvestmentID)
	}
}

func TestAllocateProportionallyHandlesEdgeCases(t *testing.T) {
	shares := []investorShare{{InvestmentID: 1, Amount: 1000}}

	if parts := allocateProportionally(nil, 1000, 1000); len(parts) != 0 {
		t.Errorf("nil shares returned %d parts, want 0", len(parts))
	}
	if parts := allocateProportionally(shares, 0, 1000); parts[0] != 0 {
		t.Errorf("zero total allocated %d, want 0", parts[0])
	}
	if parts := allocateProportionally(shares, -100, 1000); parts[0] != 0 {
		t.Errorf("negative total allocated %d, want 0", parts[0])
	}
	// basis 為 0 表示沒有投資紀錄，不可除以零
	if parts := allocateProportionally(shares, 1000, 0); parts[0] != 0 {
		t.Errorf("zero basis allocated %d, want 0", parts[0])
	}
}

// 逐期分潤加總後，出借人應收回全部本金。
// 這模擬 36 期還款，驗證長期累積不會產生誤差。
func TestAllocateProportionallyAccumulatesToFullPrincipal(t *testing.T) {
	const principal = 600000
	const months = 36

	shares := []investorShare{
		{InvestmentID: 1, Amount: 400000},
		{InvestmentID: 2, Amount: 150000},
		{InvestmentID: 3, Amount: 50000},
	}

	schedule := buildAmortizationSchedule("LN-TEST", principal, 4.88, months, nextFirstDueDate(testTime()))

	totals := make([]int64, len(shares))
	for _, row := range schedule {
		parts := allocateProportionally(shares, row.Principal, principal)
		var sum int64
		for i, part := range parts {
			totals[i] += part
			sum += part
		}
		if sum != row.Principal {
			t.Fatalf("installment %d: allocated %d, want %d", row.Number, sum, row.Principal)
		}
	}

	var recovered int64
	for i, total := range totals {
		recovered += total
		// 每位出借人收回的本金應接近其投資額（誤差來自逐期捨入）
		invested := shares[i].Amount
		diff := total - invested
		if diff < -int64(months) || diff > int64(months) {
			t.Errorf("investor %d invested %d but recovered %d (diff %d exceeds rounding tolerance)",
				i, invested, total, diff)
		}
	}
	if recovered != principal {
		t.Errorf("total recovered principal = %d, want exactly %d", recovered, principal)
	}
}

// testTime 回傳固定時間，讓攤還表測試不受執行日期影響。
func testTime() time.Time {
	return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
}
