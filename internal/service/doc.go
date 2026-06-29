// Package service holds one use-case per operation (record transaction, value
// portfolio, import file, update price, authenticate). It is the only layer
// that mutates state and it owns the database-transaction boundary: one DB
// transaction per use-case, rolled back whole on failure (AD-3).
//
// A use-case loads authored inputs and calls domain for every derived figure
// (AD-10). It depends on store, domain, and money, and never on http (AD-1).
// Use-cases land from Story 1.3 onward.
package service
