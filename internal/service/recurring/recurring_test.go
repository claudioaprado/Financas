package recurring

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/service/transaction"
	"github.com/claudioaprado/financas/internal/store"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TEST_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run recurring integration tests")
	return ""
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// setup builds the services against the shared test DB with fresh, uniquely
// named accounts/categories so parallel-shared-DB runs don't collide.
func setup(t *testing.T) (context.Context, *Service, *account.Service, *category.Service, *transaction.Service, int64) {
	t.Helper()
	url := testDatabaseURL(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, url, db.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := store.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	run := time.Now().UnixNano()
	accts := account.New(pool)
	cats := category.New(pool)
	acct, err := accts.Create(ctx, fmt.Sprintf("Checking-%d", run), account.Cash, money.Currency("USD"))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return ctx, New(pool), accts, cats, transaction.New(pool), acct.ID
}

func TestCreateAndListExpense(t *testing.T) {
	ctx, svc, _, cats, _, acctID := setup(t)
	run := time.Now().UnixNano()
	food, err := cats.Create(ctx, fmt.Sprintf("Food-%d", run), category.Expense)
	if err != nil {
		t.Fatalf("category: %v", err)
	}

	id, err := svc.Create(ctx, Input{
		Type:        Expense,
		AccountID:   acctID,
		Amount:      decimal.RequireFromString("42.50"),
		CategoryID:  food.ID,
		Cadence:     "months",
		IntervalN:   1,
		StartDate:   day(2026, time.January, 15),
		Description: "Netflix",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := find(list, id)
	if got == nil {
		t.Fatal("created template not in list")
	}
	if got.Type != Expense || got.CategoryName != food.Name {
		t.Fatalf("unexpected row: %+v", got)
	}
	if !got.Amount.Amount().Equal(decimal.RequireFromString("42.50")) {
		t.Fatalf("amount = %s", got.Amount.Amount())
	}
	if !got.NextDue.Equal(day(2026, time.January, 15)) {
		t.Fatalf("next_due = %s, want start date", got.NextDue.Format("2006-01-02"))
	}
	if !got.Due { // 2026-01-15 is in the past relative to any real "now"
		t.Fatal("a past-dated template should be due")
	}
}

func TestValidation(t *testing.T) {
	ctx, svc, _, cats, _, acctID := setup(t)
	run := time.Now().UnixNano()
	inc, _ := cats.Create(ctx, fmt.Sprintf("Salary-%d", run), category.Income)
	base := Input{Type: Expense, AccountID: acctID, Amount: decimal.RequireFromString("10"), Cadence: "months", IntervalN: 1, StartDate: day(2026, time.January, 1)}

	cases := []struct {
		name string
		mut  func(Input) Input
		want error
	}{
		{"bad type", func(i Input) Input { i.Type = "buy"; return i }, ErrInvalidType},
		{"zero amount", func(i Input) Input { i.Amount = decimal.Zero; return i }, ErrNonPositiveAmount},
		{"bad cadence", func(i Input) Input { i.Cadence = "days"; return i }, ErrInvalidCadence},
		{"zero interval", func(i Input) Input { i.IntervalN = 0; return i }, ErrNonPositiveInterval},
		{"end before start", func(i Input) Input { e := day(2025, time.December, 1); i.EndDate = &e; return i }, ErrInvalidDateRange},
		{"missing account", func(i Input) Input { i.AccountID = 999999; return i }, ErrAccountNotFound},
		{"category kind mismatch", func(i Input) Input { i.CategoryID = inc.ID; return i }, ErrCategoryKindMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Create(ctx, c.mut(base))
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestPostMaterializesAndAdvances(t *testing.T) {
	ctx, svc, _, _, txns, acctID := setup(t)
	start := day(2026, time.January, 10)
	id, err := svc.Create(ctx, Input{
		Type: Income, AccountID: acctID, Amount: decimal.RequireFromString("100"),
		Cadence: "months", IntervalN: 1, StartDate: start, Description: "Salary",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	before, err := txns.Balance(ctx, acctID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}

	if err := svc.Post(ctx, id, start); err != nil {
		t.Fatalf("post: %v", err)
	}

	after, err := txns.Balance(ctx, acctID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if diff := after.Amount().Sub(before.Amount()); !diff.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("balance delta = %s, want 100", diff)
	}

	// Cursor advanced one month, anchored on the start day.
	got := find(mustList(t, svc), id)
	if !got.NextDue.Equal(day(2026, time.February, 10)) {
		t.Fatalf("next_due = %s, want 2026-02-10", got.NextDue.Format("2006-01-02"))
	}

	// Idempotent: re-posting the SAME occurrence is a no-op (cursor already moved).
	if err := svc.Post(ctx, id, start); !errors.Is(err, ErrNotDue) {
		t.Fatalf("double post: got %v, want ErrNotDue", err)
	}
	after2, _ := txns.Balance(ctx, acctID)
	if !after2.Amount().Equal(after.Amount()) {
		t.Fatalf("double post changed balance: %s -> %s", after.Amount(), after2.Amount())
	}
}

func TestSkipAdvancesWithoutPosting(t *testing.T) {
	ctx, svc, _, _, txns, acctID := setup(t)
	start := day(2026, time.March, 5)
	id, err := svc.Create(ctx, Input{
		Type: Expense, AccountID: acctID, Amount: decimal.RequireFromString("30"),
		Cadence: "weeks", IntervalN: 2, StartDate: start,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before, _ := txns.Balance(ctx, acctID)
	if err := svc.Skip(ctx, id, start); err != nil {
		t.Fatalf("skip: %v", err)
	}
	after, _ := txns.Balance(ctx, acctID)
	if !after.Amount().Equal(before.Amount()) {
		t.Fatalf("skip changed balance: %s -> %s", before.Amount(), after.Amount())
	}
	got := find(mustList(t, svc), id)
	if !got.NextDue.Equal(day(2026, time.March, 19)) { // +2 weeks
		t.Fatalf("next_due = %s, want 2026-03-19", got.NextDue.Format("2006-01-02"))
	}
}

func TestTransferMaterializesOneRow(t *testing.T) {
	ctx, svc, accts, _, txns, fromID := setup(t)
	run := time.Now().UnixNano()
	to, err := accts.Create(ctx, fmt.Sprintf("Savings-%d", run), account.Cash, money.Currency("USD"))
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	start := day(2026, time.February, 1)
	id, err := svc.Create(ctx, Input{
		Type: Transfer, FromAccountID: fromID, ToAccountID: to.ID,
		Amount: decimal.RequireFromString("250"), Cadence: "months", IntervalN: 1, StartDate: start,
	})
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if err := svc.Post(ctx, id, start); err != nil {
		t.Fatalf("post transfer: %v", err)
	}
	// One row from the source's perspective; the destination shows the same row.
	fromList, err := txns.List(ctx, fromID)
	if err != nil {
		t.Fatalf("list from: %v", err)
	}
	count := 0
	for _, r := range fromList {
		if r.Type == transaction.Transfer && r.Amount.Equal(decimal.RequireFromString("250")) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one transfer row on source, got %d", count)
	}
}

func TestTransferRejectsCategoryAndSameAccount(t *testing.T) {
	ctx, svc, _, _, _, acctID := setup(t)
	if _, err := svc.Create(ctx, Input{
		Type: Transfer, FromAccountID: acctID, ToAccountID: acctID,
		Amount: decimal.RequireFromString("5"), Cadence: "months", IntervalN: 1, StartDate: day(2026, time.January, 1),
	}); !errors.Is(err, ErrSameAccount) {
		t.Fatalf("same account: got %v", err)
	}
}

func TestEditAndDelete(t *testing.T) {
	ctx, svc, _, _, _, acctID := setup(t)
	id, err := svc.Create(ctx, Input{
		Type: Expense, AccountID: acctID, Amount: decimal.RequireFromString("10"),
		Cadence: "months", IntervalN: 1, StartDate: day(2026, time.January, 1), Description: "old",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Edit(ctx, id, Input{
		Type: Expense, AccountID: acctID, Amount: decimal.RequireFromString("20"),
		Cadence: "weeks", IntervalN: 3, StartDate: day(2026, time.January, 1), Description: "new",
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	got := find(mustList(t, svc), id)
	if got.Description != "new" || got.Cadence != "weeks" || got.IntervalN != 3 {
		t.Fatalf("edit not applied: %+v", got)
	}
	if !got.Amount.Amount().Equal(decimal.RequireFromString("20")) {
		t.Fatalf("amount = %s", got.Amount.Amount())
	}
	if err := svc.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if find(mustList(t, svc), id) != nil {
		t.Fatal("template still present after delete")
	}
	if err := svc.Delete(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: got %v, want ErrNotFound", err)
	}
}

func mustList(t *testing.T, svc *Service) []Recurring {
	t.Helper()
	l, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return l
}

func find(list []Recurring, id int64) *Recurring {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}
