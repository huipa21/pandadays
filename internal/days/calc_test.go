package days_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"pandadays/internal/days"
	"pandadays/internal/i18n"
)

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	result, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func location(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestEffectiveTarget(t *testing.T) {
	tests := []struct {
		name, date, clock string
		repeat            days.Repeat
		now, zone, target string
		state             days.Classification
		ordinal           int
	}{
		{"fixed future", "2026-10-20", "", days.RepeatNone, "2026-10-18T12:00:00Z", "UTC", "2026-10-20T00:00:00Z", days.Future, 0},
		{"all day today late", "2026-10-18", "", days.RepeatNone, "2026-10-18T23:59:59Z", "UTC", "2026-10-18T00:00:00Z", days.Today, 0},
		{"all day past at midnight", "2026-10-17", "", days.RepeatNone, "2026-10-18T00:00:00Z", "UTC", "2026-10-17T00:00:00Z", days.Past, 0},
		{"timed future today", "2026-10-18", "12:01", days.RepeatNone, "2026-10-18T12:00:00Z", "UTC", "2026-10-18T12:01:00Z", days.Future, 0},
		{"timed exact", "2026-10-18", "12:00", days.RepeatNone, "2026-10-18T12:00:00Z", "UTC", "2026-10-18T12:00:00Z", days.Today, 0},
		{"timed past today", "2026-10-18", "12:00", days.RepeatNone, "2026-10-18T12:00:00.000000001Z", "UTC", "2026-10-18T12:00:00Z", days.Past, 0},
		{"empty repeat", "2026-10-19", "", "", "2026-10-18T12:00:00Z", "UTC", "2026-10-19T00:00:00Z", days.Future, 0},
		{"local date ahead UTC", "2026-10-19", "", days.RepeatNone, "2026-10-18T17:00:00Z", "Asia/Shanghai", "2026-10-19T00:00:00+08:00", days.Today, 0},
		{"local date behind UTC", "2026-10-17", "", days.RepeatNone, "2026-10-18T01:00:00Z", "America/New_York", "2026-10-17T00:00:00-04:00", days.Today, 0},
		{"weekly today", "2026-10-11", "", days.RepeatWeekly, "2026-10-18T23:59:59Z", "UTC", "2026-10-18T00:00:00Z", days.Future, 1},
		{"weekly tomorrow", "2026-10-12", "", days.RepeatWeekly, "2026-10-18T12:00:00Z", "UTC", "2026-10-19T00:00:00Z", days.Future, 1},
		{"weekly timed rolls", "2026-10-11", "12:00", days.RepeatWeekly, "2026-10-18T12:00:01Z", "UTC", "2026-10-25T12:00:00Z", days.Future, 2},
		{"weekly timed exact", "2026-10-11", "12:00", days.RepeatWeekly, "2026-10-18T12:00:00Z", "UTC", "2026-10-18T12:00:00Z", days.Future, 1},
		{"weekly future original", "2027-01-04", "", days.RepeatWeekly, "2026-10-18T12:00:00Z", "UTC", "2027-01-04T00:00:00Z", days.Future, 0},
		{"monthly clamp common", "2025-01-31", "", days.RepeatMonthly, "2025-02-01T12:00:00Z", "UTC", "2025-02-28T00:00:00Z", days.Future, 1},
		{"monthly clamp leap", "2024-01-31", "", days.RepeatMonthly, "2024-02-01T12:00:00Z", "UTC", "2024-02-29T00:00:00Z", days.Future, 1},
		{"monthly restore 31", "2024-01-31", "", days.RepeatMonthly, "2024-03-01T12:00:00Z", "UTC", "2024-03-31T00:00:00Z", days.Future, 2},
		{"monthly restore 30", "2025-01-30", "", days.RepeatMonthly, "2025-03-01T12:00:00Z", "UTC", "2025-03-30T00:00:00Z", days.Future, 2},
		{"monthly restore 29", "2025-01-29", "", days.RepeatMonthly, "2025-03-01T12:00:00Z", "UTC", "2025-03-29T00:00:00Z", days.Future, 2},
		{"monthly clamped today", "2025-01-31", "", days.RepeatMonthly, "2025-02-28T23:59:59Z", "UTC", "2025-02-28T00:00:00Z", days.Future, 1},
		{"monthly timed rolls", "2025-01-31", "09:00", days.RepeatMonthly, "2025-02-28T09:01:00Z", "UTC", "2025-03-31T09:00:00Z", days.Future, 2},
		{"monthly year boundary", "2024-12-31", "", days.RepeatMonthly, "2025-01-01T00:00:00Z", "UTC", "2025-01-31T00:00:00Z", days.Future, 1},
		{"yearly Feb29 common", "2024-02-29", "", days.RepeatYearly, "2025-02-01T00:00:00Z", "UTC", "2025-02-28T00:00:00Z", days.Future, 1},
		{"yearly Feb29 restore", "2024-02-29", "", days.RepeatYearly, "2028-02-01T00:00:00Z", "UTC", "2028-02-29T00:00:00Z", days.Future, 4},
		{"century not leap", "2096-02-29", "", days.RepeatYearly, "2100-02-01T00:00:00Z", "UTC", "2100-02-28T00:00:00Z", days.Future, 4},
		{"400 year leap", "1996-02-29", "", days.RepeatYearly, "2000-02-01T00:00:00Z", "UTC", "2000-02-29T00:00:00Z", days.Future, 4},
		{"yearly future original", "2030-02-28", "", days.RepeatYearly, "2026-03-01T00:00:00Z", "UTC", "2030-02-28T00:00:00Z", days.Future, 0},
		{"yearly timed rolls", "2024-02-29", "10:00", days.RepeatYearly, "2025-02-28T10:01:00Z", "UTC", "2026-02-28T10:00:00Z", days.Future, 2},
		{"spring weekly preserves clock", "2024-03-03", "12:00", days.RepeatWeekly, "2024-03-10T15:00:00Z", "America/New_York", "2024-03-10T12:00:00-04:00", days.Future, 1},
		{"fall weekly preserves clock", "2024-10-27", "12:00", days.RepeatWeekly, "2024-11-03T16:00:00Z", "America/New_York", "2024-11-03T12:00:00-05:00", days.Future, 1},
		{"spring gap shifts forward", "2024-03-03", "02:30", days.RepeatWeekly, "2024-03-10T06:00:00Z", "America/New_York", "2024-03-10T03:30:00-04:00", days.Future, 1},
		{"gap next week restores clock", "2024-03-03", "02:30", days.RepeatWeekly, "2024-03-10T07:31:00Z", "America/New_York", "2024-03-17T02:30:00-04:00", days.Future, 2},
		{"gap on original", "2024-03-10", "02:30", days.RepeatNone, "2024-03-10T06:00:00Z", "America/New_York", "2024-03-10T03:30:00-04:00", days.Future, 0},
		{"fold picks first", "2024-10-27", "01:30", days.RepeatWeekly, "2024-11-03T05:00:00Z", "America/New_York", "2024-11-03T01:30:00-04:00", days.Future, 1},
		{"fold does not repeat twice", "2024-10-27", "01:30", days.RepeatWeekly, "2024-11-03T06:00:00Z", "America/New_York", "2024-11-10T01:30:00-05:00", days.Future, 2},
		{"half hour gap", "2024-10-06", "02:15", days.RepeatNone, "2024-10-05T14:00:00Z", "Australia/Lord_Howe", "2024-10-06T02:45:00+11:00", days.Future, 0},
		{"skipped civil date", "2011-12-30", "12:00", days.RepeatNone, "2011-12-29T12:00:00Z", "Pacific/Apia", "2011-12-31T12:00:00+14:00", days.Future, 0},
		{"earliest date", "0001-01-01", "", days.RepeatNone, "0001-01-01T00:00:00Z", "UTC", "0001-01-01T00:00:00Z", days.Today, 0},
		{"far future leap year boundary", "2040-12-31", "12:00", days.RepeatNone, "2040-12-30T12:00:00Z", "America/New_York", "2040-12-31T12:00:00-05:00", days.Future, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := instant(t, tt.now)
			loc := location(t, tt.zone)
			s := days.Schedule{Date: tt.date, Time: tt.clock, Repeat: tt.repeat}
			got, err := days.Calculate(s, now, loc)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Target.Equal(instant(t, tt.target)) || got.Target.Location() != loc {
				t.Errorf("target = %s (%s), want %s (%s)", got.Target, got.Target.Location(), tt.target, loc)
			}
			if got.Classification != tt.state || got.OccurrenceOrdinal != tt.ordinal {
				t.Errorf("state/ordinal = %s/%d, want %s/%d", got.Classification, got.OccurrenceOrdinal, tt.state, tt.ordinal)
			}
			// The same instant represented in a different zone cannot change results.
			again, err := days.Calculate(s, now.In(time.FixedZone("other", 9*3600)), loc)
			if err != nil || !reflect.DeepEqual(again, got) {
				t.Errorf("calculation depends on now's input timezone: %v", err)
			}
		})
	}
}

func TestBreakdownAndLabels(t *testing.T) {
	tests := []struct {
		name, date, clock, now, zone string
		breakdown                    days.Breakdown
		en, zh                       string
	}{
		{"today", "2024-05-01", "", "2024-05-01T23:59:59Z", "UTC", days.Breakdown{}, "Today", "今天"},
		{"one day left", "2024-05-02", "", "2024-05-01T23:59:59Z", "UTC", days.Breakdown{Days: 1}, "1 day left", "还剩 1 天"},
		{"one day ago", "2024-04-30", "", "2024-05-01T00:00:00Z", "UTC", days.Breakdown{Days: 1}, "1 day ago", "1 天前"},
		{"days left", "2024-05-04", "", "2024-05-01T12:00:00Z", "UTC", days.Breakdown{Days: 3}, "3 days left", "还剩 3 天"},
		{"days ago", "2024-04-28", "", "2024-05-01T12:00:00Z", "UTC", days.Breakdown{Days: 3}, "3 days ago", "3 天前"},
		{"calendar month clamp", "2025-02-28", "", "2025-01-31T12:00:00Z", "UTC", days.Breakdown{Months: 1}, "28 days left", "还剩 28 天"},
		{"calendar year clamp", "2025-02-28", "", "2024-02-29T12:00:00Z", "UTC", days.Breakdown{Years: 1}, "365 days left", "还剩 365 天"},
		{"full breakdown", "2025-05-20", "15:45", "2024-03-15T12:10:00Z", "UTC", days.Breakdown{Years: 1, Months: 2, Days: 5, Hours: 3, Minutes: 35}, "431 days left", "还剩 431 天"},
		{"past breakdown", "2024-03-15", "12:10", "2025-05-20T15:45:00Z", "UTC", days.Breakdown{Years: 1, Months: 2, Days: 5, Hours: 3, Minutes: 35}, "431 days ago", "431 天前"},
		{"month not yet complete", "2025-02-28", "11:00", "2025-01-31T12:00:00Z", "UTC", days.Breakdown{Days: 27, Hours: 23}, "28 days left", "还剩 28 天"},
		{"subday future", "2024-05-01", "14:35", "2024-05-01T12:10:00Z", "UTC", days.Breakdown{Hours: 2, Minutes: 25}, "02:25 left", "还剩 02:25"},
		{"subday past", "2024-05-01", "12:10", "2024-05-01T14:35:59Z", "UTC", days.Breakdown{Hours: 2, Minutes: 25}, "02:25 ago", "02:25 前"},
		{"under minute not today", "2024-05-01", "12:11", "2024-05-01T12:10:59Z", "UTC", days.Breakdown{}, "00:00 left", "还剩 00:00"},
		{"timed exact today", "2024-05-01", "12:10", "2024-05-01T12:10:00Z", "UTC", days.Breakdown{}, "Today", "今天"},
		{"timed crosses midnight", "2024-05-02", "01:00", "2024-05-01T23:30:00Z", "UTC", days.Breakdown{Hours: 1, Minutes: 30}, "01:30 left", "还剩 01:30"},
		{"spring all day", "2024-03-11", "", "2024-03-10T05:00:00Z", "America/New_York", days.Breakdown{Days: 1}, "1 day left", "还剩 1 天"},
		{"fall all day", "2024-11-04", "", "2024-11-03T04:00:00Z", "America/New_York", days.Breakdown{Days: 1}, "1 day left", "还剩 1 天"},
		{"midnight gap all day", "2018-11-05", "", "2018-11-04T12:00:00Z", "America/Sao_Paulo", days.Breakdown{Days: 1}, "1 day left", "还剩 1 天"},
		{"spring timed calendar day", "2024-03-11", "00:00", "2024-03-10T05:00:00Z", "America/New_York", days.Breakdown{Days: 1}, "23:00 left", "还剩 23:00"},
		{"fall timed calendar day", "2024-11-04", "00:00", "2024-11-03T04:00:00Z", "America/New_York", days.Breakdown{Days: 1}, "1 day left", "还剩 1 天"},
		{"spring elapsed hours", "2024-03-10", "04:30", "2024-03-10T06:30:00Z", "America/New_York", days.Breakdown{Hours: 2}, "02:00 left", "还剩 02:00"},
		{"fold elapsed hours", "2024-11-03", "02:30", "2024-11-03T05:30:00Z", "America/New_York", days.Breakdown{Hours: 2}, "02:00 left", "还剩 02:00"},
		{"start in second fold", "2024-11-03", "02:30", "2024-11-03T06:30:00Z", "America/New_York", days.Breakdown{Hours: 1}, "01:00 left", "还剩 01:00"},
		{"long range", "2001-01-01", "", "0001-01-01T00:00:00Z", "UTC", days.Breakdown{Years: 2000}, "730485 days left", "还剩 730485 天"},
	}
	bundle, err := i18n.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := days.Calculate(days.Schedule{Date: tt.date, Time: tt.clock}, instant(t, tt.now), location(t, tt.zone))
			if err != nil {
				t.Fatal(err)
			}
			if got.Breakdown != tt.breakdown {
				t.Errorf("breakdown = %+v, want %+v", got.Breakdown, tt.breakdown)
			}
			for lang, want := range map[string]string{"en": tt.en, "zh": tt.zh} {
				if label := bundle.T(lang, got.ListLabel.Key, got.ListLabel.Args...); label != want {
					t.Errorf("%s label = %q, want %q", lang, label, want)
				}
			}
			if got.SinceOriginal != nil {
				t.Error("non-repeating schedule has SinceOriginal")
			}
		})
	}
}

func TestSinceOriginal(t *testing.T) {
	tests := []struct {
		name, now string
		clock     string
		want      *days.Breakdown
		ordinal   int
	}{
		{"before original", "2023-01-01T00:00:00Z", "", nil, 0},
		{"original today", "2024-02-29T23:59:59Z", "", &days.Breakdown{}, 0},
		{"first anniversary", "2025-02-28T12:00:00Z", "", &days.Breakdown{Years: 1}, 1},
		{"third anniversary", "2027-02-28T23:59:59Z", "", &days.Breakdown{Years: 3}, 3},
		{"timed before original", "2024-02-29T09:59:00Z", "10:00", nil, 0},
		{"timed elapsed", "2025-03-01T12:30:00Z", "10:00", &days.Breakdown{Years: 1, Days: 1, Hours: 2, Minutes: 30}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := days.Calculate(days.Schedule{Date: "2024-02-29", Time: tt.clock, Repeat: days.RepeatYearly}, instant(t, tt.now), time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			original := instant(t, "2024-02-29T00:00:00Z")
			if tt.clock != "" {
				original = original.Add(10 * time.Hour)
			}
			if !got.Original.Equal(original) {
				t.Errorf("original changed: %s", got.Original)
			}
			if !reflect.DeepEqual(got.SinceOriginal, tt.want) || got.OccurrenceOrdinal != tt.ordinal {
				t.Errorf("elapsed/ordinal = %+v/%d, want %+v/%d", got.SinceOriginal, got.OccurrenceOrdinal, tt.want, tt.ordinal)
			}
			if tt.name == "third anniversary" && got.ListLabel.Key != "days.today" {
				t.Errorf("repeating today label = %+v", got.ListLabel)
			}
		})
	}
}

func TestInvalidSchedule(t *testing.T) {
	tests := []struct {
		name     string
		schedule days.Schedule
		now      string
		nilZone  bool
	}{
		{"empty date", days.Schedule{}, "", false},
		{"invalid leap day", days.Schedule{Date: "2025-02-29"}, "", false},
		{"invalid month day", days.Schedule{Date: "2024-04-31"}, "", false},
		{"invalid format", days.Schedule{Date: "2024-1-01"}, "", false},
		{"year zero", days.Schedule{Date: "0000-01-01"}, "", false},
		{"invalid time", days.Schedule{Date: "2024-01-01", Time: "24:00"}, "", false},
		{"invalid minute", days.Schedule{Date: "2024-01-01", Time: "12:60"}, "", false},
		{"unpadded time", days.Schedule{Date: "2024-01-01", Time: "1:30"}, "", false},
		{"seconds not supported", days.Schedule{Date: "2024-01-01", Time: "12:00:00"}, "", false},
		{"invalid repeat", days.Schedule{Date: "2024-01-01", Repeat: "daily"}, "", false},
		{"nil timezone", days.Schedule{Date: "2024-01-01"}, "", true},
		{"now year zero", days.Schedule{Date: "2024-01-01"}, "0000-01-01T00:00:00Z", false},
		{"target overflow", days.Schedule{Date: "9999-12-31", Time: "00:00", Repeat: days.RepeatYearly}, "9999-12-31T12:00:00Z", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc := time.UTC
			if tt.nilZone {
				loc = nil
			}
			now := "2024-01-01T00:00:00Z"
			if tt.now != "" {
				now = tt.now
			}
			got, err := days.Calculate(tt.schedule, instant(t, now), loc)
			if !errors.Is(err, days.ErrInvalidSchedule) || !reflect.DeepEqual(got, days.Calculation{}) {
				t.Errorf("got %+v, %v; want zero calculation and ErrInvalidSchedule", got, err)
			}
		})
	}
}
