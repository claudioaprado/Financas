// Package assetcategory is the use-case for the owner's asset categories:
// owner-defined classes for securities (e.g. "Ações BR", "FIIs", "Renda Fixa",
// "Cripto"), independent of the fixed security.type enum. Categories are
// created, listed, renamed, and deleted here. The name is normalized (trimmed)
// and unique case-sensitively, backed by a UNIQUE constraint; the service is the
// validation authority with the DB UNIQUE/CHECK as the backstop (AD-3). Assigning
// a category to a security is a separate concern added later.
package assetcategory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioaprado/financas/internal/store"
	"github.com/claudioaprado/financas/internal/validate"
)

// Input errors. The service is the validation authority; DB CHECK/UNIQUE
// constraints are the backstop.
var (
	// ErrEmptyName means the category name was blank.
	ErrEmptyName = errors.New("assetcategory: name must not be empty")
	// ErrDuplicateName means a category with that name already exists.
	ErrDuplicateName = errors.New("assetcategory: a category with that name already exists")
	// ErrNotFound means no category matched the given id.
	ErrNotFound = errors.New("assetcategory: not found")
)

// Category is one owner-defined asset class.
type Category struct {
	ID   int64
	Name string
}

// Service creates, lists, renames, and deletes asset categories.
type Service struct {
	pool *pgxpool.Pool
}

// New returns an asset-category Service backed by the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Create validates and appends a new asset category, returning the stored row. A
// duplicate name is rejected (ErrDuplicateName). It writes inside one
// transaction (AD-3).
func (s *Service) Create(ctx context.Context, name string) (Category, error) {
	name, err := cleanName(name)
	if err != nil {
		return Category{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Category{}, fmt.Errorf("assetcategory: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := store.New(tx).CreateAssetCategory(ctx, name)
	if err != nil {
		if isUniqueViolation(err) {
			return Category{}, ErrDuplicateName
		}
		return Category{}, fmt.Errorf("assetcategory: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Category{}, fmt.Errorf("assetcategory: commit: %w", err)
	}
	return Category{ID: row.ID, Name: row.Name}, nil
}

// Rename changes a category's name. A missing id returns ErrNotFound; a name
// clash returns ErrDuplicateName. It writes inside one transaction (AD-3).
func (s *Service) Rename(ctx context.Context, id int64, name string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("assetcategory: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := store.New(tx).RenameAssetCategory(ctx, store.RenameAssetCategoryParams{ID: id, Name: name})
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateName
		}
		return fmt.Errorf("assetcategory: rename: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("assetcategory: commit: %w", err)
	}
	return nil
}

// Delete removes a category. A missing id returns ErrNotFound. It writes inside
// one transaction (AD-3).
func (s *Service) Delete(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("assetcategory: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := store.New(tx).DeleteAssetCategory(ctx, id)
	if err != nil {
		return fmt.Errorf("assetcategory: delete: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("assetcategory: commit: %w", err)
	}
	return nil
}

// Get returns one category by id, or ErrNotFound if none matches.
func (s *Service) Get(ctx context.Context, id int64) (Category, error) {
	row, err := store.New(s.pool).GetAssetCategory(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Category{}, ErrNotFound
	}
	if err != nil {
		return Category{}, fmt.Errorf("assetcategory: get: %w", err)
	}
	return Category{ID: row.ID, Name: row.Name}, nil
}

// List returns all asset categories ordered by name.
func (s *Service) List(ctx context.Context) ([]Category, error) {
	rows, err := store.New(s.pool).ListAssetCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("assetcategory: list: %w", err)
	}
	out := make([]Category, len(rows))
	for i, r := range rows {
		out[i] = Category{ID: r.ID, Name: r.Name}
	}
	return out, nil
}

// cleanName trims and validates a category name (shared free-text guard).
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrEmptyName
	}
	if err := validate.Name(name); err != nil {
		return "", err
	}
	return name, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
