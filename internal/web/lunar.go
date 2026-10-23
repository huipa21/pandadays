package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"pandadays/internal/days"
	"pandadays/internal/i18n"
)

type lunarMonthOption struct {
	Value string
	Key   string
	Leap  bool
}

type lunarPickerYear struct {
	Year   int    `json:"year"`
	Start  string `json:"start"`
	Months []int  `json:"months"`
}

var lunarPickerData = sync.OnceValues(func() (string, error) {
	years := make([]lunarPickerYear, 0, days.MaxLunarYear-days.MinLunarYear+1)
	for year := days.MinLunarYear; year <= days.MaxLunarYear; year++ {
		start, err := days.LunarToSolar(days.LunarDate{Year: year, Month: 1, Day: 1})
		if err != nil {
			return "", err
		}
		months, err := days.LunarMonths(year)
		if err != nil {
			return "", err
		}
		entry := lunarPickerYear{Year: year, Start: start}
		for _, month := range months {
			length := month.Days
			if month.Month < 0 {
				length = -length
			}
			entry.Months = append(entry.Months, length)
		}
		years = append(years, entry)
	}
	data, err := json.Marshal(years)
	return string(data), err
})

func (s *Server) handleDayCalendar(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var date days.LunarDate
	var solar string
	var err error
	if query.Has("date") {
		solar = query.Get("date")
		date, err = days.SolarToLunar(solar)
	} else {
		date.Year, _ = strconv.Atoi(query.Get("year"))
		date.Month, _ = strconv.Atoi(query.Get("month"))
		date.Day, _ = strconv.Atoi(query.Get("day"))
		if query.Has("day") {
			solar, err = days.LunarToSolar(date)
		}
	}
	months, monthsErr := days.LunarMonths(date.Year)
	if err != nil || monthsErr != nil {
		http.Error(w, s.i18n.T(i18n.LangFrom(r.Context()), "days.error.calendar"), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Solar  string            `json:"solar"`
		Lunar  days.LunarDate    `json:"lunar"`
		Months []days.LunarMonth `json:"months"`
	}{solar, date, months})
}

func (s *Server) lunarDateLabel(lang, solar string) string {
	date, err := days.SolarToLunar(solar)
	if err != nil {
		return ""
	}
	month := date.Month
	if month < 0 {
		month = -month
	}
	label := s.i18n.T(lang, "days.lunar.month."+strconv.Itoa(month))
	if date.Month < 0 {
		label = s.i18n.T(lang, "days.lunar.leap") + " " + label
	}
	return s.i18n.T(lang, "days.lunar.date", date.Year, label, date.Day)
}
