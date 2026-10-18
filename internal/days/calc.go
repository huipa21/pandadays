// Package days calculates countdowns and elapsed calendar time.
package days

import (
	"errors"
	"fmt"
	"time"
)

type Repeat string

const (
	RepeatNone    Repeat = "none"
	RepeatWeekly  Repeat = "weekly"
	RepeatMonthly Repeat = "monthly"
	RepeatYearly  Repeat = "yearly"
)

type Classification string

const (
	Future Classification = "future"
	Today  Classification = "today"
	Past   Classification = "past"
)

// Schedule uses the stored civil date and optional clock time, not UTC instants.
type Schedule struct {
	Date   string // YYYY-MM-DD
	Time   string // HH:MM, or empty for all-day
	Repeat Repeat
}

// Message can be rendered with i18n.Bundle.T(lang, Key, Args...).
type Message struct {
	Key  string
	Args []any
}

type Breakdown struct {
	Years   int
	Months  int
	Days    int
	Hours   int
	Minutes int
}

type Calculation struct {
	Original          time.Time
	Target            time.Time
	Classification    Classification
	ListLabel         Message
	Breakdown         Breakdown
	OccurrenceOrdinal int        // Original = 0; first repeat/anniversary = 1.
	SinceOriginal     *Breakdown // Only repeating days whose original has arrived.
}

var ErrInvalidSchedule = errors.New("invalid day schedule")

// Calculate is pure: all comparisons use now and loc supplied by the caller.
// All-day values compare civil dates; timed values compare instants. Empty repeat
// means none. Calendar years/months are clamped, days are local calendar days,
// and remaining hours/minutes are elapsed time truncated to whole minutes.
func Calculate(s Schedule, now time.Time, loc *time.Location) (Calculation, error) {
	if loc == nil {
		return Calculation{}, fmt.Errorf("%w: timezone is required", ErrInvalidSchedule)
	}
	date, err := time.Parse(time.DateOnly, s.Date)
	if err != nil || date.Year() < 1 || date.Format(time.DateOnly) != s.Date {
		return Calculation{}, fmt.Errorf("%w: date must be YYYY-MM-DD in years 0001-9999", ErrInvalidSchedule)
	}
	hour, minute := 0, 0
	if s.Time != "" {
		clock, err := time.Parse("15:04", s.Time)
		if err != nil || clock.Format("15:04") != s.Time {
			return Calculation{}, fmt.Errorf("%w: time must be HH:MM", ErrInvalidSchedule)
		}
		hour, minute = clock.Hour(), clock.Minute()
	}
	if s.Repeat == "" {
		s.Repeat = RepeatNone
	}
	switch s.Repeat {
	case RepeatNone, RepeatWeekly, RepeatMonthly, RepeatYearly:
	default:
		return Calculation{}, fmt.Errorf("%w: unsupported repeat %q", ErrInvalidSchedule, s.Repeat)
	}
	now = now.In(loc)
	if now.Year() < 1 || now.Year() > 9999 {
		return Calculation{}, fmt.Errorf("%w: now must be in years 0001-9999", ErrInvalidSchedule)
	}
	allDay := s.Time == ""
	reference := now
	if allDay {
		reference = localTime(civil(now), loc)
	}
	anchor := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, time.UTC)
	original := localTime(anchor, loc)
	target := original
	ordinal := 0
	if s.Repeat != RepeatNone {
		switch s.Repeat {
		case RepeatWeekly:
			ordinal = civilDays(anchor, now) / 7
		case RepeatMonthly:
			ordinal = (now.Year()-anchor.Year())*12 + int(now.Month()-anchor.Month())
		case RepeatYearly:
			ordinal = now.Year() - anchor.Year()
		}
		ordinal = max(0, ordinal)
		for {
			switch s.Repeat {
			case RepeatWeekly:
				target = localTime(anchor.AddDate(0, 0, ordinal*7), loc)
			case RepeatMonthly:
				target = localTime(clampMonth(anchor, 0, ordinal), loc)
			case RepeatYearly:
				target = localTime(clampMonth(anchor, ordinal, 0), loc)
			}
			if !target.Before(reference) {
				break
			}
			ordinal++
		}
	}
	if target.Year() > 9999 {
		return Calculation{}, fmt.Errorf("%w: next occurrence exceeds year 9999", ErrInvalidSchedule)
	}

	state := Today
	if s.Repeat != RepeatNone || target.After(reference) {
		state = Future
	} else if target.Before(reference) {
		state = Past
	}
	result := Calculation{
		Original: original, Target: target, Classification: state,
		OccurrenceOrdinal: ordinal,
		Breakdown:         between(reference, target, allDay, loc),
		ListLabel:         listLabel(reference, target, allDay),
	}
	if s.Repeat != RepeatNone && !original.After(reference) {
		elapsed := between(original, reference, allDay, loc)
		result.SinceOriginal = &elapsed
	}
	return result, nil
}

func listLabel(now, target time.Time, allDay bool) Message {
	if target.Equal(now) {
		return Message{Key: "days.today"}
	}
	direction := "left"
	start, end := now, target
	if target.Before(now) {
		direction = "ago"
		start, end = target, now
	}
	if !allDay && end.Sub(start) < 24*time.Hour {
		minutes := int(end.Sub(start) / time.Minute)
		return Message{Key: "days.time." + direction, Args: []any{minutes / 60, minutes % 60}}
	}
	n := civilDays(start, end)
	plural := "other"
	if n == 1 {
		plural = "one"
	}
	return Message{Key: "days.count." + direction + "." + plural, Args: []any{n}}
}

func between(start, end time.Time, allDay bool, loc *time.Location) Breakdown {
	if allDay {
		start, end, loc = civil(start), civil(end), time.UTC
	}
	if end.Before(start) {
		start, end = end, start
	}
	years := end.Year() - start.Year()
	cursor := shiftCalendar(start, years, 0, 0, loc)
	if cursor.After(end) {
		years--
		cursor = shiftCalendar(start, years, 0, 0, loc)
	}
	months := (end.Year()-cursor.Year())*12 + int(end.Month()-cursor.Month())
	base := cursor
	cursor = shiftCalendar(base, 0, months, 0, loc)
	if cursor.After(end) {
		months--
		cursor = shiftCalendar(base, 0, months, 0, loc)
	}
	days := civilDays(cursor, end)
	base = cursor
	cursor = shiftCalendar(base, 0, 0, days, loc)
	if cursor.After(end) {
		days--
		cursor = shiftCalendar(base, 0, 0, days, loc)
	}
	result := Breakdown{Years: years, Months: months, Days: days}
	if !allDay {
		minutes := int(end.Sub(cursor) / time.Minute)
		result.Hours, result.Minutes = minutes/60, minutes%60
	}
	return result
}

func shiftCalendar(t time.Time, years, months, days int, loc *time.Location) time.Time {
	if years == 0 && months == 0 && days == 0 {
		return t
	}
	return localTime(clampMonth(t, years, months).AddDate(0, 0, days), loc)
}

func clampMonth(t time.Time, years, months int) time.Time {
	first := time.Date(t.Year()+years, t.Month()+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(t.Day(), last)-1)
}

func civil(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

func civilDays(start, end time.Time) int {
	// Unix seconds avoid time.Duration's roughly 292-year limit.
	return int((civil(end).Unix() - civil(start).Unix()) / 86400)
}

// localTime resolves a civil clock deterministically: first instant in a fold,
// or forward by the gap for a nonexistent time. time.Date alone leaves both
// choices unspecified. Nearby zone bounds also cover non-hour/date-line jumps.
func localTime(t time.Time, loc *time.Location) time.Time {
	requested := wall(t)
	offsets := make(map[int]bool)
	limit := requested.Add(48 * time.Hour)
	for probe := requested.Add(-48 * time.Hour); !probe.After(limit); {
		zoned := probe.In(loc)
		_, offset := zoned.Zone()
		offsets[offset] = true
		_, end := zoned.ZoneBounds()
		if end.IsZero() || end.After(limit) {
			break
		}
		if end.After(probe) {
			probe = end
		} else {
			// Go's POSIX timezone extension can report a stale year-end bound
			// on the last day of a future leap year. Keep traversal advancing.
			probe = probe.Add(time.Hour)
		}
	}
	var exact, forward time.Time
	hasExact, hasForward := false, false
	for offset := range offsets {
		candidate := requested.Add(-time.Duration(offset) * time.Second).In(loc)
		local := wall(candidate)
		if local.Equal(requested) && (!hasExact || candidate.Before(exact)) {
			exact = candidate
			hasExact = true
		}
		if local.After(requested) && (!hasForward || local.Before(wall(forward))) {
			forward = candidate
			hasForward = true
		}
	}
	if hasExact {
		return exact
	}
	return forward
}
