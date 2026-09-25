package main

import (
	"math"
	"testing"
	"time"
)

func TestMonthlyPayment(t *testing.T) {
	tests := []struct {
		name      string
		principal float64
		rate      float64
		months    int
		want      float64
	}{
		// 50 萬 / 4.88% / 36 期：年金公式精確值 14958.52。
		// 舊版前端與 seed 曾寫死 14982，該數字不對應任何實際利率，已一併修正。
		{"grade A 36 months", 500000, 4.88, 36, 14958.52},
		{"zero rate splits evenly", 120000, 0, 12, 10000},
		{"zero months returns zero", 500000, 4.88, 0, 0},
		{"negative months returns zero", 500000, 4.88, -6, 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := monthlyPayment(test.principal, test.rate, test.months)
			if math.Abs(got-test.want) > 1 {
				t.Errorf("monthlyPayment(%v, %v, %v) = %.2f, want ~%.2f",
					test.principal, test.rate, test.months, got, test.want)
			}
		})
	}
}

// 攤還表的核心不變量：本金加總必須等於核貸金額，且末期餘額歸零。
func TestBuildAmortizationScheduleInvariants(t *testing.T) {
	cases := []struct {
		name      string
		principal int64
		rate      float64
		months    int
	}{
		{"A grade 36m", 500000, 4.88, 36},
		{"B grade 48m", 1200000, 6.80, 48},
		{"C grade 18m", 180000, 9.60, 18},
		{"long term 84m", 3000000, 5.20, 84},
		{"short term 12m", 50000, 4.88, 12},
		{"zero rate", 240000, 0, 24},
	}

	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			schedule := buildAmortizationSchedule("LN-TEST", test.principal, test.rate, test.months, start)

			if len(schedule) != test.months {
				t.Fatalf("got %d installments, want %d", len(schedule), test.months)
			}

			var principalSum, interestSum int64
			for _, row := range schedule {
				if row.Principal < 0 || row.Interest < 0 {
					t.Fatalf("installment %d has negative component: principal=%d interest=%d",
						row.Number, row.Principal, row.Interest)
				}
				if row.AmountDue != row.Principal+row.Interest {
					t.Errorf("installment %d: amountDue %d != principal %d + interest %d",
						row.Number, row.AmountDue, row.Principal, row.Interest)
				}
				principalSum += row.Principal
				interestSum += row.Interest
			}

			if principalSum != test.principal {
				t.Errorf("principal sum = %d, want exactly %d (diff %d)",
					principalSum, test.principal, principalSum-test.principal)
			}
			if final := schedule[len(schedule)-1].RemainingBalance; final != 0 {
				t.Errorf("final remaining balance = %d, want 0", final)
			}
			if test.rate == 0 && interestSum != 0 {
				t.Errorf("zero-rate loan accrued %d interest, want 0", interestSum)
			}
		})
	}
}

func TestBuildAmortizationScheduleDueDates(t *testing.T) {
	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	schedule := buildAmortizationSchedule("LN-TEST", 500000, 4.88, 3, start)

	want := []time.Time{
		time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC),
	}
	for i, row := range schedule {
		if !row.DueDate.Equal(want[i]) {
			t.Errorf("installment %d due %v, want %v", row.Number, row.DueDate, want[i])
		}
	}
}

func TestBuildAmortizationScheduleRejectsInvalidInput(t *testing.T) {
	start := time.Now()
	if got := buildAmortizationSchedule("LN-TEST", 0, 4.88, 36, start); got != nil {
		t.Errorf("zero principal returned %d rows, want nil", len(got))
	}
	if got := buildAmortizationSchedule("LN-TEST", 500000, 4.88, 0, start); got != nil {
		t.Errorf("zero months returned %d rows, want nil", len(got))
	}
}

func TestNextFirstDueDate(t *testing.T) {
	tests := []struct {
		name string
		from time.Time
		want time.Time
	}{
		{
			"before the 15th uses this month",
			time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			"on the 15th rolls to next month",
			time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			"after the 15th rolls to next month",
			time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			"december rolls into next year",
			time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC),
			time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextFirstDueDate(test.from); !got.Equal(test.want) {
				t.Errorf("nextFirstDueDate(%v) = %v, want %v", test.from, got, test.want)
			}
		})
	}
}
