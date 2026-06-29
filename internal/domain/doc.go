// Package domain holds the core entities and the single canonical
// implementation of every derived figure — holdings, account balances,
// valuation, net worth, realized gain/loss, and allocation.
//
// It is the innermost layer (AD-1): it imports only the inner `money` value
// package (which itself imports nothing project-internal, so the graph stays
// acyclic) and is otherwise pure, with no I/O. Per AD-2 the transaction ledger
// is the single source of truth and every other figure is derived here on read,
// and per AD-10 each derived number has exactly one home in this package
// (AccountBalance is the first; valuation/net worth/allocation follow).
package domain
