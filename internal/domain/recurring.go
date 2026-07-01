package domain

import "time"

// Recurring cadence math (Epic 9 / FR-20). The "due" state of a recurring
// template and the date of its next occurrence are DERIVED here (AD-10) from the
// authored template (start date, cadence, interval, end date) and its schedule
// cursor — never a background job. The service stores and advances `next_due`
// using AdvanceDate; whether an occurrence is due is IsDue.

// Cadence is a recurrence's calendar unit.
type Cadence string

const (
	// Weekly cadence: each step is interval×7 days (exact — no anchoring).
	Weekly Cadence = "weeks"
	// Monthly cadence: each step is interval calendar months, anchored on the
	// start date's day-of-month and clamped to the target month's last day.
	Monthly Cadence = "months"
	// Yearly cadence: each step is interval calendar years, anchored on the start
	// date's month/day and clamped (Feb 29 → Feb 28 in non-leap years).
	Yearly Cadence = "years"
)

// IsValid reports whether c is one of the supported cadences.
func (c Cadence) IsValid() bool { return c == Weekly || c == Monthly || c == Yearly }

// AdvanceDate returns the occurrence after `current` for a template that started
// on `start`, stepping by n units of the given cadence.
//
// Weekly steps add n×7 days exactly. Monthly and yearly steps ANCHOR on the
// start date's day-of-month so the schedule never drifts: e.g. a monthly template
// starting Jan 31 yields Jan 31 → Feb 28 → Mar 31 → Apr 30 (each occurrence
// re-derives the day from `start`, clamped to the target month's length), rather
// than the progressive Feb 28 → Mar 28 → … drift that repeated month addition
// would produce. A yearly Feb 29 template clamps to Feb 28 in non-leap years and
// returns to Feb 29 on the next leap year. `current` is normalized to midnight UTC
// (dates are calendar days, AD — no time component).
func AdvanceDate(current, start time.Time, cadence Cadence, n int) time.Time {
	if n < 1 {
		n = 1
	}
	switch cadence {
	case Weekly:
		return dateUTC(current.Year(), current.Month(), current.Day()+7*n)
	case Monthly:
		total := int(current.Month()) - 1 + n // 0-based month index from current
		y := current.Year() + total/12
		m := time.Month(total%12 + 1)
		return clampDay(y, m, start.Day())
	case Yearly:
		return clampDay(current.Year()+n, current.Month(), start.Day())
	default:
		return current
	}
}

// IsDue reports whether an occurrence dated nextDue is due to be posted as of
// `today`: it has arrived (nextDue ≤ today) and still falls within the template's
// window (no end date, or nextDue ≤ end date). All three are compared as calendar
// days.
func IsDue(nextDue time.Time, endDate *time.Time, today time.Time) bool {
	if dayAfter(nextDue, today) {
		return false // not yet arrived
	}
	if endDate != nil && dayAfter(nextDue, *endDate) {
		return false // past the end of the schedule
	}
	return true
}

// clampDay builds a date in (year, month) on `day`, clamped to that month's last
// valid day (so Feb 31 → Feb 28/29). Anchors monthly/yearly recurrences.
func clampDay(year int, month time.Month, day int) time.Time {
	if last := daysInMonth(year, month); day > last {
		day = last
	}
	return dateUTC(year, month, day)
}

// daysInMonth returns the number of days in the given month, leap-year aware.
func daysInMonth(year int, month time.Month) int {
	// Day 0 of the next month is the last day of this month.
	return dateUTC(year, month+1, 0).Day()
}

// dateUTC constructs a midnight-UTC calendar date; time.Date normalizes any
// out-of-range day/month (used deliberately by daysInMonth and the weekly step).
func dateUTC(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// dayAfter reports whether a's calendar day is strictly after b's.
func dayAfter(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	if ay != by {
		return ay > by
	}
	if am != bm {
		return am > bm
	}
	return ad > bd
}
