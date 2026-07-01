package importer

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/category"
	"github.com/claudioaprado/financas/internal/store"
)

// isolatedDB creates a throwaway database for this test and returns its URL; the
// cleanup drops it. The categorization test asserts per-row suggestions, which
// depend on the GLOBAL category_rule set — so it cannot share the base DB with
// other suites (or its own prior runs) without their rules polluting the match.
func isolatedDB(t *testing.T, baseURL string) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("fin_importer_test_%d", time.Now().UnixNano())

	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create database: %v", err)
	}
	admin.Close()

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	u.Path = "/" + name
	testURL := u.String()

	t.Cleanup(func() {
		a, err := pgxpool.New(ctx, baseURL)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return testURL
}

// TestImportCategorization proves FR-17 at commit: a rule's suggestion is applied
// by default, an explicit per-row choice overrides it, a kind-mismatched choice
// is ignored (uncategorized), and an unmatched row stays uncategorized.
func TestImportCategorization(t *testing.T) {
	ctx := context.Background()
	dbURL := isolatedDB(t, testDatabaseURL(t))
	if err := store.Migrate(ctx, dbURL, db.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := store.NewPool(ctx, dbURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	accts := account.New(pool)
	cats := category.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()

	cash, err := accts.Create(ctx, fmt.Sprintf("Cat-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	food, err := cats.Create(ctx, fmt.Sprintf("Food-%d", run), category.Expense)
	if err != nil {
		t.Fatalf("food: %v", err)
	}
	transport, err := cats.Create(ctx, fmt.Sprintf("Transport-%d", run), category.Expense)
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	pay, err := cats.Create(ctx, fmt.Sprintf("Pay-%d", run), category.Income)
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	if _, err := store.New(pool).CreateCategoryRule(ctx, store.CreateCategoryRuleParams{MatchText: "uber", CategoryID: food.ID}); err != nil {
		t.Fatalf("rule: %v", err)
	}

	// line1 UBER → suggests Food; line2 Grocery → no suggestion; line3 Coffee;
	// line4 UBER pool → suggests Food.
	content := "01/03/2024\tUBER ride\t-20,00\n" +
		"02/03/2024\tGrocery\t-15,00\n" +
		"03/03/2024\tCoffee\t-5,00\n" +
		"04/03/2024\tUBER pool\t-8,00\n"

	// Preview: only the two UBER rows carry the Food suggestion.
	prev, err := svc.Preview(ctx, cash.ID, content)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	sugg := map[string]int64{}
	for _, r := range prev.Rows {
		sugg[r.Description] = r.SuggestedCategoryID
	}
	if sugg["UBER ride"] != food.ID || sugg["UBER pool"] != food.ID {
		t.Errorf("UBER rows should suggest Food(%d): %+v", food.ID, sugg)
	}
	if sugg["Grocery"] != 0 || sugg["Coffee"] != 0 {
		t.Errorf("unmatched rows should suggest 0: %+v", sugg)
	}

	// Commit choices: line1 keeps the suggestion (Food); line2 explicitly Transport;
	// line3 a kind-mismatched income category (ignored); line4 overrides to Transport.
	cats2 := map[int]int64{
		2: transport.ID, // Grocery → Transport (explicit on an unmatched row)
		3: pay.ID,       // Coffee → income cat on an expense row ⇒ ignored
		4: transport.ID, // UBER pool → override beats the Food suggestion
	}
	if _, err := svc.Commit(ctx, cash.ID, content, cats2); err != nil {
		t.Fatalf("commit: %v", err)
	}

	txns, err := store.New(pool).ListAccountTransactions(ctx, pgtype.Int8{Int64: cash.ID, Valid: true})
	if err != nil {
		t.Fatalf("list txns: %v", err)
	}
	got := map[string]pgtype.Int8{}
	for _, tr := range txns {
		got[tr.Description] = tr.CategoryID
	}
	wantCat := func(desc string, want int64) {
		c := got[desc]
		if want == 0 {
			if c.Valid {
				t.Errorf("%s: category = %d; want uncategorized", desc, c.Int64)
			}
			return
		}
		if !c.Valid || c.Int64 != want {
			t.Errorf("%s: category = %+v; want %d", desc, c, want)
		}
	}
	wantCat("UBER ride", food.ID)      // suggestion applied by default
	wantCat("Grocery", transport.ID)   // explicit choice on an unmatched row
	wantCat("Coffee", 0)               // kind-mismatched choice ignored
	wantCat("UBER pool", transport.ID) // override beats suggestion
}
