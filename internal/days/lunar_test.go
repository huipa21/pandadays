package days

import (
	"testing"
	"time"
)

func TestLunarConversions(t *testing.T) {
	for _, test := range []struct {
		solar string
		lunar LunarDate
	}{
		{"2024-02-10", LunarDate{2024, 1, 1}},
		{"2025-01-29", LunarDate{2025, 1, 1}},
		{"2023-03-22", LunarDate{2023, -2, 1}},
		{"1900-01-31", LunarDate{1900, 1, 1}},
	} {
		got, err := SolarToLunar(test.solar)
		if err != nil || got != test.lunar {
			t.Fatalf("SolarToLunar(%s) = %+v, %v", test.solar, got, err)
		}
		solar, err := LunarToSolar(test.lunar)
		if err != nil || solar != test.solar {
			t.Fatalf("LunarToSolar(%+v) = %s, %v", test.lunar, solar, err)
		}
	}
	for _, invalid := range []LunarDate{{2024, -2, 1}, {2024, 1, 31}, {1899, 1, 1}, {2101, 1, 1}, {2024, 0, 1}} {
		if _, err := LunarToSolar(invalid); err == nil {
			t.Fatalf("accepted invalid lunar date %+v", invalid)
		}
	}
}

func TestLunarRecurrence(t *testing.T) {
	for _, test := range []struct {
		name, date, now, want string
		repeat                Repeat
		ordinal               int
	}{
		{"new year", "2024-02-10", "2024-02-11", "2025-01-29", RepeatYearly, 1},
		{"today", "2024-02-10", "2025-01-29", "2025-01-29", RepeatYearly, 1},
		{"future anchor", "2025-01-29", "2024-02-11", "2025-01-29", RepeatYearly, 0},
		{"leap month fallback", "2023-03-22", "2023-03-23", "2024-03-10", RepeatYearly, 1},
		{"monthly leap month", "2023-02-20", "2023-02-21", "2023-03-22", RepeatMonthly, 1},
		{"monthly after leap month", "2023-02-20", "2023-03-23", "2023-04-20", RepeatMonthly, 2},
		{"short month clamps", "2023-03-21", "2023-03-22", "2023-04-19", RepeatMonthly, 1},
		{"anchor day restored", "2023-03-21", "2023-05-19", "2023-06-17", RepeatMonthly, 3},
		{"yearly short month", "2023-01-21", "2024-02-10", "2025-01-28", RepeatYearly, 2},
		{"weekly", "2024-02-10", "2024-02-11", "2024-02-17", RepeatWeekly, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			now, _ := time.Parse(time.DateOnly, test.now)
			got, err := Calculate(Schedule{Date: test.date, Repeat: test.repeat, Calendar: CalendarLunar}, now, time.UTC)
			if err != nil || got.Target.Format(time.DateOnly) != test.want || got.OccurrenceOrdinal != test.ordinal {
				t.Fatalf("target = %s, ordinal = %d, err = %v; want %s, %d", got.Target.Format(time.DateOnly), got.OccurrenceOrdinal, err, test.want, test.ordinal)
			}
		})
	}
}

func TestLunarTimedRecurrenceAndBoundaries(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 1, 29, 14, 0, 0, 0, time.UTC)
	got, err := Calculate(Schedule{Date: "2024-02-10", Time: "08:30", Calendar: CalendarLunar, Repeat: RepeatYearly}, now, loc)
	if err != nil || got.Target.Format("2006-01-02 15:04") != "2026-02-17 08:30" || got.OccurrenceOrdinal != 2 {
		t.Fatalf("timed lunar recurrence: %+v, %v", got, err)
	}
	last, err := LunarToSolar(LunarDate{2100, 12, 29})
	if err != nil {
		t.Fatal(err)
	}
	date, err := SolarToLunar(last)
	if err != nil || date != (LunarDate{2100, 12, 29}) {
		t.Fatalf("upper boundary: %+v, %v", date, err)
	}
	end, _ := time.Parse(time.DateOnly, last)
	if _, err := Calculate(Schedule{Date: last, Calendar: CalendarLunar, Repeat: RepeatYearly}, end.AddDate(0, 0, 2), time.UTC); err == nil {
		t.Fatal("accepted recurrence beyond supported lunar years")
	}
}
