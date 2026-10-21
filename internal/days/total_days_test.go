package days_test

import (
	"testing"
	"time"

	"pandadays/internal/days"
)

func TestTotalDays(t *testing.T) {
	for _, tc := range []struct {
		name, date, clock, now, zone string
		want                         days.Breakdown
	}{
		{"two months one day can be 63 days", "2026-09-02", "", "2026-07-01T12:00:00Z", "UTC", days.Breakdown{Days: 63}},
		{"62 days across actual months", "2026-08-02", "", "2026-06-01T12:00:00Z", "UTC", days.Breakdown{Days: 62}},
		{"short months", "2025-03-02", "", "2025-01-01T12:00:00Z", "UTC", days.Breakdown{Days: 60}},
		{"leap February", "2024-03-02", "", "2024-01-01T12:00:00Z", "UTC", days.Breakdown{Days: 61}},
		{"leap year", "2025-01-01", "", "2024-01-01T12:00:00Z", "UTC", days.Breakdown{Days: 366}},
		{"past", "2026-06-01", "", "2026-08-02T12:00:00Z", "UTC", days.Breakdown{Days: 62}},
		{"today", "2026-08-02", "", "2026-08-02T23:59:00Z", "UTC", days.Breakdown{}},
		{"timed remainder", "2026-08-02", "11:30", "2026-06-01T12:00:00Z", "UTC", days.Breakdown{Days: 61, Hours: 23, Minutes: 30}},
		{"past timed remainder", "2026-06-01", "12:00", "2026-08-02T11:30:00Z", "UTC", days.Breakdown{Days: 61, Hours: 23, Minutes: 30}},
		{"spring all day", "2024-03-11", "", "2024-03-10T05:00:00Z", "America/New_York", days.Breakdown{Days: 1}},
		{"fall all day", "2024-11-04", "", "2024-11-03T04:00:00Z", "America/New_York", days.Breakdown{Days: 1}},
		{"spring timed", "2024-03-11", "00:00", "2024-03-10T05:00:00Z", "America/New_York", days.Breakdown{Days: 1}},
		{"fall timed", "2024-11-04", "00:00", "2024-11-03T04:00:00Z", "America/New_York", days.Breakdown{Days: 1}},
		{"local timezone", "2024-03-02", "", "2024-02-29T17:00:00Z", "Asia/Shanghai", days.Breakdown{Days: 1}},
		{"long range", "2001-01-01", "", "0001-01-01T00:00:00Z", "UTC", days.Breakdown{Days: 730485}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := days.Calculate(days.Schedule{Date: tc.date, Time: tc.clock}, instant(t, tc.now), location(t, tc.zone))
			if err != nil {
				t.Fatal(err)
			}
			if got.DaysOnly != tc.want {
				t.Errorf("days-only = %+v, want %+v", got.DaysOnly, tc.want)
			}
			if got.SinceOriginalDays != nil {
				t.Error("non-repeating day has original-date counter")
			}
		})
	}
}

func TestRepeatingTotalDays(t *testing.T) {
	schedule := days.Schedule{Date: "2024-02-29", Repeat: days.RepeatYearly}
	for _, tc := range []struct {
		now   string
		next  int
		since int // -1 means the original date has not arrived.
	}{
		{"2024-01-01T12:00:00Z", 59, -1},
		{"2024-02-29T12:00:00Z", 0, 0},
		{"2025-02-28T12:00:00Z", 0, 365},
		{"2025-03-01T12:00:00Z", 364, 366},
		{"2028-02-29T12:00:00Z", 0, 1461},
	} {
		got, err := days.Calculate(schedule, instant(t, tc.now), time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if got.DaysOnly != (days.Breakdown{Days: tc.next}) {
			t.Errorf("%s: next = %+v, want %d days", tc.now, got.DaysOnly, tc.next)
		}
		if tc.since == -1 {
			if got.SinceOriginalDays != nil {
				t.Errorf("%s: unexpected original-date counter", tc.now)
			}
		} else if got.SinceOriginalDays == nil || *got.SinceOriginalDays != (days.Breakdown{Days: tc.since}) {
			t.Errorf("%s: since = %+v, want %d days", tc.now, got.SinceOriginalDays, tc.since)
		}
	}
}
