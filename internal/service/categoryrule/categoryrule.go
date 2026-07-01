// Package categoryrule is the use-case for auto-categorization rules (FR-17): a
// global "description contains match_text → category" rule that only SUGGESTS a
// category during an import preview. A rule inherits its category's kind, so it
// is offered only on matching-type rows. Writes go through one DB transaction
// (AD-3). Kind is carried as a plain string to keep this package free of a
// service→service dependency (AD-1).
package categoryrule

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioaprado/financas/internal/store"
)

// Errors. The service is the validation authority; DB constraints back it up.
var (
	ErrEmptyMatch       = errors.New("categoryrule: match text must not be empty")
	ErrCategoryNotFound = errors.New("categoryrule: category not found")
	ErrNotFound         = errors.New("categoryrule: not found")
)

// Rule is one auto-categorization rule with its category's name and kind joined
// in for display and matching.
type Rule struct {
	ID           int64
	MatchText    string
	CategoryID   int64
	CategoryName string
	Kind         string // "income" | "expense" (the category's kind)
}

// Service creates, lists, and deletes rules.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a categoryrule Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// List returns all rules in id order (insertion order ⇒ first-match-wins).
func (s *Service) List(ctx context.Context) ([]Rule, error) {
	rows, err := store.New(s.pool).ListCategoryRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("categoryrule: list: %w", err)
	}
	out := make([]Rule, len(rows))
	for i, r := range rows {
		out[i] = Rule{ID: r.ID, MatchText: r.MatchText, CategoryID: r.CategoryID, CategoryName: r.CategoryName, Kind: r.CategoryKind}
	}
	return out, nil
}

// Add validates and appends a rule in one transaction. The category must exist
// (the FK enforces it); an empty match text is rejected.
func (s *Service) Add(ctx context.Context, matchText string, categoryID int64) (Rule, error) {
	matchText = strings.TrimSpace(matchText)
	if matchText == "" {
		return Rule{}, ErrEmptyMatch
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Rule{}, fmt.Errorf("categoryrule: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := store.New(tx).CreateCategoryRule(ctx, store.CreateCategoryRuleParams{MatchText: matchText, CategoryID: categoryID})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // FK violation ⇒ no such category
			return Rule{}, ErrCategoryNotFound
		}
		return Rule{}, fmt.Errorf("categoryrule: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Rule{}, fmt.Errorf("categoryrule: commit: %w", err)
	}
	return Rule{ID: row.ID, MatchText: row.MatchText, CategoryID: row.CategoryID}, nil
}

// Delete removes a rule by id (one transaction). Returns ErrNotFound if no row
// matched.
func (s *Service) Delete(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("categoryrule: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := store.New(tx).DeleteCategoryRule(ctx, id)
	if err != nil {
		return fmt.Errorf("categoryrule: delete: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("categoryrule: commit: %w", err)
	}
	return nil
}
