// Package category is the use-case for income/expense categories (FR-7). A
// category has a kind (income or expense) and may be assigned to a matching
// transaction (the kind rule is enforced in service/transaction). Deletes are
// guarded: a category in use cannot be removed unless the caller forces it,
// which first unassigns it from its transactions. Writes go through one DB
// transaction (AD-3).
package category

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioaprado/financas/internal/store"
)

// Kind is a category's type: it may only be assigned to a matching transaction.
type Kind string

const (
	// Income categories attach only to Income transactions.
	Income Kind = "income"
	// Expense categories attach only to Expense transactions.
	Expense Kind = "expense"
)

// IsValid reports whether k is a supported kind.
func (k Kind) IsValid() bool { return k == Income || k == Expense }

// Errors. The service is the validation authority; DB constraints back it up.
var (
	ErrEmptyName     = errors.New("category: name must not be empty")
	ErrInvalidKind   = errors.New("category: kind must be income or expense")
	ErrCategoryInUse = errors.New("category: in use by transactions")
	ErrNotFound      = errors.New("category: not found")
)

// Category is one income/expense label.
type Category struct {
	ID   int64
	Name string
	Kind Kind
}

// CategoryUsage pairs a category with how many transactions reference it.
type CategoryUsage struct {
	Category
	Count int64
}

// Service creates, lists, and deletes categories.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a category Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Create validates and appends a category. It writes inside one transaction.
func (s *Service) Create(ctx context.Context, name string, kind Kind) (Category, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Category{}, ErrEmptyName
	}
	if !kind.IsValid() {
		return Category{}, ErrInvalidKind
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Category{}, fmt.Errorf("category: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := store.New(tx).CreateCategory(ctx, store.CreateCategoryParams{Name: name, Kind: string(kind)})
	if err != nil {
		return Category{}, fmt.Errorf("category: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Category{}, fmt.Errorf("category: commit: %w", err)
	}
	return toCategory(row), nil
}

// List returns all categories ordered by kind then name.
func (s *Service) List(ctx context.Context) ([]Category, error) {
	rows, err := store.New(s.pool).ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("category: list: %w", err)
	}
	out := make([]Category, len(rows))
	for i, r := range rows {
		out[i] = toCategory(r)
	}
	return out, nil
}

// ListWithUsage returns categories annotated with how many transactions use each.
func (s *Service) ListWithUsage(ctx context.Context) ([]CategoryUsage, error) {
	q := store.New(s.pool)
	rows, err := q.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("category: list: %w", err)
	}
	counts, err := q.CategoryUsageCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("category: usage counts: %w", err)
	}
	byID := make(map[int64]int64, len(counts))
	for _, c := range counts {
		if c.CategoryID.Valid {
			byID[c.CategoryID.Int64] = c.N
		}
	}
	out := make([]CategoryUsage, len(rows))
	for i, r := range rows {
		out[i] = CategoryUsage{Category: toCategory(r), Count: byID[r.ID]}
	}
	return out, nil
}

// Delete removes a category. If it is referenced by transactions and force is
// false, it returns ErrCategoryInUse. With force, it first unassigns the
// category from those transactions, then deletes it — all in one transaction.
func (s *Service) Delete(ctx context.Context, id int64, force bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("category: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := store.New(tx)
	if force {
		if _, err := q.ClearCategoryFromTransactions(ctx, pgtype.Int8{Int64: id, Valid: true}); err != nil {
			return fmt.Errorf("category: unassign: %w", err)
		}
	}
	n, err := q.DeleteCategory(ctx, id)
	if err != nil {
		// A foreign-key violation means the category is still referenced.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrCategoryInUse
		}
		return fmt.Errorf("category: delete: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("category: commit: %w", err)
	}
	return nil
}

func toCategory(r store.Category) Category {
	return Category{ID: r.ID, Name: r.Name, Kind: Kind(r.Kind)}
}
