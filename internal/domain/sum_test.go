package domain

import (
	"testing"

	"github.com/claudioaprado/financas/internal/money"
)

func TestSumByCurrency(t *testing.T) {
	got := SumByCurrency([]money.Money{
		money.New(dec("10.50"), money.USD),
		money.New(dec("5"), money.BRL),
		money.New(dec("4.50"), money.USD),
		money.New(dec("100"), money.BRL),
	})
	// Ordered by currency code: BRL then USD.
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2", len(got))
	}
	if got[0].Currency() != money.BRL || !got[0].Amount().Equal(dec("105")) {
		t.Errorf("group[0] = %s, want 105 BRL", got[0])
	}
	if got[1].Currency() != money.USD || !got[1].Amount().Equal(dec("15")) {
		t.Errorf("group[1] = %s, want 15 USD", got[1])
	}

	if n := SumByCurrency(nil); len(n) != 0 {
		t.Errorf("empty sum = %d groups, want 0", len(n))
	}
}
