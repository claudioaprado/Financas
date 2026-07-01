package importer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/money"
	"github.com/claudioaprado/financas/internal/service/account"
	"github.com/claudioaprado/financas/internal/service/transaction"
	"github.com/claudioaprado/financas/internal/store"
)

// TestImportOFX exercises the OFX Preview/Commit path end-to-end against a real
// DB: FITID dedup, the no-FITID warning + re-import, and the anti-content-dedup
// guard (same date/description/value + different FITID ⇒ both import).
func TestImportOFX(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, url, db.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := store.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	accts := account.New(pool)
	txns := transaction.New(pool)
	svc := New(pool)
	run := time.Now().UnixNano()

	cash, err := accts.Create(ctx, fmt.Sprintf("OFX-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// income A1, expense A2, a no-FITID expense (warns), and a 30-Feb error row.
	content := "<OFX><BANKTRANLIST>\n" +
		"<STMTTRN><DTPOSTED>20240301<TRNAMT>5000.00<FITID>A1<NAME>Salary</STMTTRN>\n" +
		"<STMTTRN><DTPOSTED>20240302<TRNAMT>-1234.56<FITID>A2<NAME>Rent</STMTTRN>\n" +
		"<STMTTRN><DTPOSTED>20240303<TRNAMT>-10.00<NAME>NoFitid</STMTTRN>\n" +
		"<STMTTRN><DTPOSTED>20240230<TRNAMT>-5.00<FITID>A3<NAME>BadDate</STMTTRN>\n" +
		"</BANKTRANLIST></OFX>\n"

	prev, err := svc.PreviewOFX(ctx, cash.ID, content)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if prev.New != 3 || prev.Duplicate != 0 || prev.Errors != 1 {
		t.Fatalf("preview = %d new / %d dup / %d err; want 3/0/1", prev.New, prev.Duplicate, prev.Errors)
	}
	if warned := countWarnings(prev); warned != 1 {
		t.Errorf("preview surfaced %d warnings; want 1 (the no-FITID row)", warned)
	}

	if _, err := svc.CommitOFX(ctx, cash.ID, content, nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Balance = 5000 - 1234.56 - 10 = 3755.44 USD.
	if bal, _ := txns.Balance(ctx, cash.ID); !bal.Amount().Equal(decimal.RequireFromString("3755.44")) {
		t.Fatalf("balance after import = %s; want 3755.44", bal.Amount())
	}

	// Re-import: A1/A2 are duplicates (FITID), the no-FITID row imports AGAIN
	// (documenting the duplicate risk), the bad-date row is still an error.
	prev2, err := svc.PreviewOFX(ctx, cash.ID, content)
	if err != nil {
		t.Fatalf("preview 2: %v", err)
	}
	if prev2.New != 1 || prev2.Duplicate != 2 || prev2.Errors != 1 {
		t.Errorf("re-preview = %d new / %d dup / %d err; want 1/2/1", prev2.New, prev2.Duplicate, prev2.Errors)
	}
	if _, err := svc.CommitOFX(ctx, cash.ID, content, nil); err != nil {
		t.Fatalf("commit 2: %v", err)
	}
	// The no-FITID row re-imported: balance drops another 10 → 3745.44.
	if bal, _ := txns.Balance(ctx, cash.ID); !bal.Amount().Equal(decimal.RequireFromString("3745.44")) {
		t.Errorf("balance after re-import = %s; want 3745.44 (no-FITID row re-imports)", bal.Amount())
	}

	// Anti-content-dedup guard: two transactions with identical date/description/
	// value but different FITIDs must BOTH import (FITID is the only dedup key).
	guard, err := accts.Create(ctx, fmt.Sprintf("OFXguard-%d", run), account.Cash, money.USD)
	if err != nil {
		t.Fatalf("create guard account: %v", err)
	}
	twins := "<OFX><BANKTRANLIST>\n" +
		"<STMTTRN><DTPOSTED>20240210<TRNAMT>-42.00<FITID>C1<NAME>Coffee</STMTTRN>\n" +
		"<STMTTRN><DTPOSTED>20240210<TRNAMT>-42.00<FITID>C2<NAME>Coffee</STMTTRN>\n" +
		"</BANKTRANLIST></OFX>\n"
	gprev, err := svc.PreviewOFX(ctx, guard.ID, twins)
	if err != nil {
		t.Fatalf("guard preview: %v", err)
	}
	if gprev.New != 2 || gprev.Duplicate != 0 {
		t.Fatalf("guard preview = %d new / %d dup; want 2/0 (different FITID ⇒ not duplicates)", gprev.New, gprev.Duplicate)
	}
	if _, err := svc.CommitOFX(ctx, guard.ID, twins, nil); err != nil {
		t.Fatalf("guard commit: %v", err)
	}
	if bal, _ := txns.Balance(ctx, guard.ID); !bal.Amount().Equal(decimal.RequireFromString("-84")) {
		t.Errorf("guard balance = %s; want -84 (both twins imported)", bal.Amount())
	}

	// Non-cash/credit account is rejected (same rule as the tab importer).
	inv, err := accts.Create(ctx, fmt.Sprintf("OFXinv-%d", run), account.Investment, money.USD)
	if err != nil {
		t.Fatalf("create investment: %v", err)
	}
	if _, err := svc.PreviewOFX(ctx, inv.ID, content); !errors.Is(err, ErrUnsupportedAccountType) {
		t.Errorf("OFX import to investment = %v; want ErrUnsupportedAccountType", err)
	}
}

func countWarnings(res Result) int {
	n := 0
	for _, r := range res.Rows {
		if r.Warning != "" {
			n++
		}
	}
	return n
}
