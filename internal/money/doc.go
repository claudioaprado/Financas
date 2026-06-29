// Package money holds the exact-decimal Money type (a decimal amount plus an
// ISO-4217 currency) and the single Convert(amount, rate) function used to
// project native amounts into the display currency.
//
// Floating point is forbidden for monetary and quantity values end to end
// (AD-4); conversion uses banker's rounding, rounded once at the display
// boundary (AD-12). Like domain, money imports nothing project-internal.
package money
