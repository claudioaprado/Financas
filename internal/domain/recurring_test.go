package domain

import (
	"testing"
	"time"
)

func d(y int, m time.Month, day int) time.Time {
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

func TestAdvanceDateWeekly(t *testing.T) {
	got := AdvanceDate(d(2026, time.January, 1), d(2026, time.January, 1), Weekly, 2)
	if want := d(2026, time.January, 15); !got.Equal(want) {
		t.Fatalf("weekly ×2: got %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	// Across a month boundary.
	got = AdvanceDate(d(2026, time.January, 29), d(2026, time.January, 1), Weekly, 1)
	if want := d(2026, time.February, 5); !got.Equal(want) {
		t.Fatalf("weekly boundary: got %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

// A month-end monthly template must anchor on the start day and clamp, with NO
// progressive drift across short months.
func TestAdvanceDateMonthlyEndOfMonthAnchored(t *testing.T) {
	start := d(2026, time.January, 31)
	steps := []time.Time{
		d(2026, time.February, 28), // clamp: Feb has 28 days in 2026
		d(2026, time.March, 31),    // re-anchored to 31, not 28
		d(2026, time.April, 30),
		d(2026, time.May, 31),
	}
	cur := start
	for i, want := range steps {
		cur = AdvanceDate(cur, start, Monthly, 1)
		if !cur.Equal(want) {
			t.Fatalf("step %d: got %s, want %s", i, cur.Format("2006-01-02"), want.Format("2006-01-02"))
		}
	}
}

func TestAdvanceDateMonthlyAcrossYear(t *testing.T) {
	got := AdvanceDate(d(2026, time.November, 15), d(2026, time.November, 15), Monthly, 3)
	if want := d(2027, time.February, 15); !got.Equal(want) {
		t.Fatalf("monthly ×3 across year: got %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

// A leap-day yearly template clamps to Feb 28 in non-leap years and returns to
// Feb 29 on the next leap year (anchored on the start day).
func TestAdvanceDateYearlyLeapDay(t *testing.T) {
	start := d(2024, time.February, 29)
	got := AdvanceDate(start, start, Yearly, 1)
	if want := d(2025, time.February, 28); !got.Equal(want) {
		t.Fatalf("leap+1: got %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	// From the clamped 2025-02-28, the next leap year re-anchors to the 29th.
	got = AdvanceDate(got, start, Yearly, 3)
	if want := d(2028, time.February, 29); !got.Equal(want) {
		t.Fatalf("back to leap: got %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestIsDue(t *testing.T) {
	today := d(2026, time.July, 1)
	end := d(2026, time.August, 1)

	cases := []struct {
		name    string
		nextDue time.Time
		end     *time.Time
		want    bool
	}{
		{"past is due", d(2026, time.June, 1), nil, true},
		{"today is due", today, nil, true},
		{"future not due", d(2026, time.July, 2), nil, false},
		{"within end date", d(2026, time.June, 15), &end, true},
		{"after end date not due", d(2026, time.September, 1), &end, false},
		{"on end date due", end, &end, false}, // end (Aug 1) is after today (Jul 1)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsDue(c.nextDue, c.end, today); got != c.want {
				t.Fatalf("IsDue(%s)=%v, want %v", c.nextDue.Format("2006-01-02"), got, c.want)
			}
		})
	}
}

func TestCadenceIsValid(t *testing.T) {
	for _, c := range []Cadence{Weekly, Monthly, Yearly} {
		if !c.IsValid() {
			t.Fatalf("%q should be valid", c)
		}
	}
	if Cadence("days").IsValid() {
		t.Fatal("unknown cadence should be invalid")
	}
}
