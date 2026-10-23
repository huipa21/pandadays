package days

import (
	"errors"
	"time"

	"github.com/6tail/lunar-go/calendar"
)

type Calendar string

const (
	CalendarGregorian Calendar = "gregorian"
	CalendarLunar     Calendar = "lunar"
	MinLunarYear               = 1900
	MaxLunarYear               = 2100
)

var ErrCalendarInvalid = errors.New("invalid calendar or lunar date outside years 1900-2100")

type LunarDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

type LunarMonth struct {
	Month int `json:"month"`
	Days  int `json:"days"`
}

func LunarMonths(year int) ([]LunarMonth, error) {
	if year < MinLunarYear || year > MaxLunarYear {
		return nil, ErrCalendarInvalid
	}
	months := make([]LunarMonth, 0, 13)
	for entry := calendar.NewLunarYear(year).GetMonthsInYear().Front(); entry != nil; entry = entry.Next() {
		month := entry.Value.(*calendar.LunarMonth)
		months = append(months, LunarMonth{Month: month.GetMonth(), Days: month.GetDayCount()})
	}
	return months, nil
}

func SolarToLunar(date string) (LunarDate, error) {
	parsed, err := time.Parse(time.DateOnly, date)
	if err != nil || parsed.Format(time.DateOnly) != date || parsed.Year() < MinLunarYear || parsed.Year() > MaxLunarYear+1 {
		return LunarDate{}, ErrCalendarInvalid
	}
	lunar := calendar.NewSolarFromYmd(parsed.Year(), int(parsed.Month()), parsed.Day()).GetLunar()
	if lunar.GetYear() < MinLunarYear || lunar.GetYear() > MaxLunarYear {
		return LunarDate{}, ErrCalendarInvalid
	}
	return LunarDate{Year: lunar.GetYear(), Month: lunar.GetMonth(), Day: lunar.GetDay()}, nil
}

func LunarToSolar(date LunarDate) (string, error) {
	if date.Year < MinLunarYear || date.Year > MaxLunarYear || date.Month == 0 || date.Month < -12 || date.Month > 12 {
		return "", ErrCalendarInvalid
	}
	month := calendar.NewLunarYear(date.Year).GetMonth(date.Month)
	if month == nil || date.Day < 1 || date.Day > month.GetDayCount() {
		return "", ErrCalendarInvalid
	}
	return calendar.NewLunarFromYmd(date.Year, date.Month, date.Day).GetSolar().ToYmd(), nil
}

func lunarTarget(anchor time.Time, repeat Repeat, reference time.Time, loc *time.Location) (time.Time, int, error) {
	original := localTime(anchor, loc)
	if !original.Before(reference) {
		return original, 0, nil
	}
	base, err := SolarToLunar(anchor.Format(time.DateOnly))
	if err != nil {
		return time.Time{}, 0, ErrInvalidSchedule
	}
	current, err := SolarToLunar(reference.Format(time.DateOnly))
	if err != nil {
		return time.Time{}, 0, ErrInvalidSchedule
	}
	month := calendar.NewLunarYear(current.Year).GetMonth(current.Month)
	ordinal := current.Year - base.Year
	if repeat == RepeatMonthly {
		ordinal = month.GetIndex() - calendar.NewLunarYear(base.Year).GetMonth(base.Month).GetIndex()
		for year := base.Year; year < current.Year; year++ {
			ordinal += calendar.NewLunarYear(year).GetMonthsInYear().Len()
		}
	}
	for {
		if repeat == RepeatYearly {
			year := base.Year + ordinal
			if year > MaxLunarYear {
				return time.Time{}, 0, ErrInvalidSchedule
			}
			month = calendar.NewLunarYear(year).GetMonth(base.Month)
			if month == nil {
				month = calendar.NewLunarYear(year).GetMonth(-base.Month)
			}
		}
		if month.GetYear() > MaxLunarYear {
			return time.Time{}, 0, ErrInvalidSchedule
		}
		solar := calendar.NewSolarFromJulianDay(month.GetFirstJulianDay() + float64(min(base.Day, month.GetDayCount())-1))
		target := localTime(time.Date(solar.GetYear(), time.Month(solar.GetMonth()), solar.GetDay(), anchor.Hour(), anchor.Minute(), 0, 0, time.UTC), loc)
		if !target.Before(reference) {
			return target, ordinal, nil
		}
		ordinal++
		if repeat == RepeatMonthly {
			month = month.Next(1)
		}
	}
}
