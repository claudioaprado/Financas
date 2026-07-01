package assetcategory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/claudioaprado/financas/db"
	"github.com/claudioaprado/financas/internal/store"
	"github.com/claudioaprado/financas/internal/validate"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TEST_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run assetcategory integration tests")
	return ""
}

func TestAssetCategory(t *testing.T) {
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

	svc := New(pool)
	run := time.Now().UnixNano()
	name := fmt.Sprintf("Ações BR %d", run)

	// Create trims + stores.
	cat, err := svc.Create(ctx, "  "+name+"  ")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cat.Name != name {
		t.Errorf("name = %q; want trimmed %q", cat.Name, name)
	}

	// Duplicate name is rejected.
	if _, err := svc.Create(ctx, name); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("duplicate = %v; want ErrDuplicateName", err)
	}

	// Validation: empty + over-long.
	if _, err := svc.Create(ctx, "   "); !errors.Is(err, ErrEmptyName) {
		t.Errorf("empty = %v; want ErrEmptyName", err)
	}
	if _, err := svc.Create(ctx, strings.Repeat("x", validate.MaxNameLen+1)); !errors.Is(err, validate.ErrNameTooLong) {
		t.Errorf("over-long = %v; want ErrNameTooLong", err)
	}

	// Rename works; a missing id is ErrNotFound.
	renamed := fmt.Sprintf("FIIs %d", run)
	if err := svc.Rename(ctx, cat.ID, renamed); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got, _ := svc.Get(ctx, cat.ID); got.Name != renamed {
		t.Errorf("after rename Name = %q; want %q", got.Name, renamed)
	}
	if err := svc.Rename(ctx, -1, "X"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename missing = %v; want ErrNotFound", err)
	}

	// Renaming onto an existing name is rejected.
	other, err := svc.Create(ctx, fmt.Sprintf("Cripto %d", run))
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if err := svc.Rename(ctx, other.ID, renamed); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("rename onto existing = %v; want ErrDuplicateName", err)
	}

	// List includes both, ordered by name (non-decreasing).
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Name > list[i].Name {
			t.Errorf("list not ordered by name: %q before %q", list[i-1].Name, list[i].Name)
		}
	}

	// Delete works; deleting again is ErrNotFound.
	if err := svc.Delete(ctx, cat.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := svc.Delete(ctx, cat.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing = %v; want ErrNotFound", err)
	}
	_ = svc.Delete(ctx, other.ID) // cleanup
}
