package utils

// Phase 6a tests for timeutil.go.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsZeroDate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   time.Time
		want bool
	}{
		{"zero value", time.Time{}, true},
		{"epoch UTC (1970)", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"early 1970 still flagged", time.Date(1970, 6, 15, 12, 30, 0, 0, time.UTC), true},
		{"late 1969 NOT flagged", time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC), false},
		{"normal date", time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, IsZeroDate(tc.in))
		})
	}
}

// TestAsLocal_PreservesWallClockDigits confirms AsLocal keeps the
// wall-clock components verbatim while swapping the location to
// time.Local. SQL drivers tag DB strings with UTC even though they
// represent local-time digits; this function is what bridges that gap.
func TestAsLocal_PreservesWallClockDigits(t *testing.T) {
	t.Parallel()

	// A time tagged as UTC, intended to be interpreted as local.
	source := time.Date(2026, 4, 25, 14, 30, 45, 12345, time.UTC)
	got := AsLocal(source)

	assert.Equal(t, time.Local, got.Location(), "tag must be flipped to time.Local")
	assert.Equal(t, source.Year(), got.Year())
	assert.Equal(t, source.Month(), got.Month())
	assert.Equal(t, source.Day(), got.Day())
	assert.Equal(t, source.Hour(), got.Hour())
	assert.Equal(t, source.Minute(), got.Minute())
	assert.Equal(t, source.Second(), got.Second())
	assert.Equal(t, source.Nanosecond(), got.Nanosecond())
}

func TestAsLocal_AlreadyLocalIsNoOp(t *testing.T) {
	t.Parallel()

	source := time.Date(2026, 4, 25, 14, 30, 45, 0, time.Local)
	got := AsLocal(source)
	assert.True(t, source.Equal(got),
		"input already tagged time.Local should produce an equal time")
	assert.Equal(t, time.Local, got.Location())
}

// TestLayoutConstants_AreParseable acts as a smoke check on the
// formatting constants — all four must be parseable by time.Parse.
func TestLayoutConstants_AreParseable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		layout string
		input  string
	}{
		{LayoutDate, "2026-04-25"},
		{LayoutDateTime, "04/25/2026 02:30 PM"},
		{LayoutDateTimeLocal, "2026-04-25T14:30:45"},
		{LayoutDB, "2026-04-25 14:30:45"},
	}
	for _, tc := range cases {
		t.Run(tc.layout, func(t *testing.T) {
			t.Parallel()
			parsed, err := time.Parse(tc.layout, tc.input)
			if assert.NoError(t, err) {
				assert.False(t, parsed.IsZero())
			}
		})
	}
}

func TestCalendarDay(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("test", -5*3600)
	at := func(y int, m time.Month, d, h, mi int) time.Time {
		return time.Date(y, m, d, h, mi, 0, 0, loc)
	}
	cases := []struct {
		name  string
		start time.Time
		now   time.Time
		want  int
	}{
		{"start date is day 1", at(2026, 10, 1, 20, 0), at(2026, 10, 1, 21, 0), 1},
		{"same day, now earlier than start clock time", at(2026, 10, 1, 20, 0), at(2026, 10, 1, 0, 5), 1},
		{"rolls over at midnight, not 24h after start", at(2026, 10, 1, 20, 0), at(2026, 10, 2, 0, 5), 2},
		{"just under 24h later is already the next day", at(2026, 10, 1, 20, 0), at(2026, 10, 2, 9, 0), 2},
		{"the case that disagreed: 8pm start checked at 9am six days on", at(2026, 10, 1, 20, 0), at(2026, 10, 7, 9, 0), 7},
		{"month boundary", at(2026, 1, 30, 12, 0), at(2026, 2, 2, 12, 0), 4},
		{"year boundary", at(2025, 12, 31, 23, 59), at(2026, 1, 1, 0, 1), 2},
		{"start in the future", at(2026, 10, 5, 12, 0), at(2026, 10, 1, 12, 0), -3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, CalendarDay(tc.start, tc.now))
		})
	}
}

// TestCalendarDay_AcrossDST guards the whole-day count over a spring-forward
// (23h) and fall-back (25h) transition.
func TestCalendarDay_AcrossDST(t *testing.T) {
	t.Parallel()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata not available")
	}
	// US DST began 2026-03-08 and ends 2026-11-01.
	assert.Equal(t, 3, CalendarDay(
		time.Date(2026, 3, 7, 22, 0, 0, 0, ny),
		time.Date(2026, 3, 9, 1, 0, 0, 0, ny)))
	assert.Equal(t, 3, CalendarDay(
		time.Date(2026, 10, 31, 22, 0, 0, 0, ny),
		time.Date(2026, 11, 2, 1, 0, 0, 0, ny)))
}

func TestWeekOf(t *testing.T) {
	t.Parallel()
	cases := []struct{ day, want int }{
		{-3, 0}, {0, 0}, {1, 1}, {7, 1}, {8, 2}, {14, 2}, {15, 3}, {64, 10},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, WeekOf(tc.day), "day %d", tc.day)
	}
}
