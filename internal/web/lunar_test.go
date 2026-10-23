package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/days"
)

func TestLunarCalendarEndpoint(t *testing.T) {
	s, _, _, token := authenticatedServer(t)
	if rr := get(s.Handler(), "/days/calendar?date=2024-02-10", ""); rr.Code != http.StatusSeeOther {
		t.Fatalf("anonymous conversion: %d", rr.Code)
	}
	for _, query := range []string{"date=2024-02-10", "year=2024&month=1&day=1"} {
		rr := get(s.Handler(), "/days/calendar?"+query, token)
		var got struct {
			Solar  string
			Lunar  days.LunarDate
			Months []days.LunarMonth
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || rr.Code != http.StatusOK || got.Solar != "2024-02-10" || got.Lunar != (days.LunarDate{Year: 2024, Month: 1, Day: 1}) || len(got.Months) != 12 {
			t.Fatalf("%s: %d %s (%v)", query, rr.Code, rr.Body.String(), err)
		}
	}
	for _, query := range []string{"date=1800-01-01", "year=2024&month=-2&day=1", "year=2024&month=1&day=31", "year=999999999"} {
		if rr := get(s.Handler(), "/days/calendar?"+query, token); rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("accepted %s: %d", query, rr.Code)
		}
	}
}

func TestLunarPickerData(t *testing.T) {
	data, err := lunarPickerData()
	if err != nil {
		t.Fatal(err)
	}
	var years []lunarPickerYear
	if err := json.Unmarshal([]byte(data), &years); err != nil {
		t.Fatal(err)
	}
	if len(years) != days.MaxLunarYear-days.MinLunarYear+1 || len(data) > 20000 {
		t.Fatalf("unexpected calendar data: %d years, %d bytes", len(years), len(data))
	}
	for index, entry := range years {
		if entry.Year != days.MinLunarYear+index {
			t.Fatalf("year sequence: %+v", entry)
		}
		start, err := time.Parse(time.DateOnly, entry.Start)
		if err != nil {
			t.Fatal(err)
		}
		month := 0
		for _, length := range entry.Months {
			monthID := month
			if length > 0 {
				month++
				monthID = month
			} else {
				monthID = -month
				length = -length
			}
			if length != 29 && length != 30 {
				t.Fatalf("invalid month length in %+v", entry)
			}
			for _, day := range []int{1, length} {
				want, err := days.LunarToSolar(days.LunarDate{Year: entry.Year, Month: monthID, Day: day})
				got := start.AddDate(0, 0, day-1).Format(time.DateOnly)
				if err != nil || got != want {
					t.Fatalf("year %d month %d day %d: got %s, want %s (%v)", entry.Year, monthID, day, got, want, err)
				}
			}
			start = start.AddDate(0, 0, length)
		}
		if index+1 < len(years) && start.Format(time.DateOnly) != years[index+1].Start {
			t.Fatalf("year boundary mismatch after %d: %s", entry.Year, start)
		}
	}
}

func TestLunarDayCreateAndEdit(t *testing.T) {
	s, _, user, token := authenticatedServer(t)
	s.now = func() time.Time { return time.Date(2024, 2, 11, 0, 0, 0, 0, time.UTC) }
	form := dayValues("title", "Lunar New Year", "calendar", "lunar", "lunar_year", "2024", "lunar_month", "1", "lunar_day", "1", "repeat", "yearly")
	rr := sessionPost(t, s, "/days", token, form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	day := onlyDay(t, s, user.ID)
	if day.Date != "2024-02-10" || day.Calendar != days.CalendarLunar {
		t.Fatalf("saved %+v", day)
	}
	rr = get(s.Handler(), dayPath(day.ID), token)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "2025-01-29") {
		t.Fatalf("card: %d %s", rr.Code, rr.Body.String())
	}
	rr = get(s.Handler(), dayPath(day.ID)+"/edit", token)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `value="lunar" data-calendar-toggle checked`) {
		t.Fatalf("edit: %d", rr.Code)
	}
	form.Set("lunar_month", "-2")
	rr = sessionPost(t, s, "/days/"+strconv.FormatInt(day.ID, 10)+"/edit", token, form)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid leap month: %d", rr.Code)
	}
	form.Set("lunar_year", "2023")
	rr = sessionPost(t, s, dayPath(day.ID)+"/edit", token, form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("edit leap month: %d %s", rr.Code, rr.Body.String())
	}
	day = onlyDay(t, s, user.ID)
	if day.Date != "2023-03-22" || day.Calendar != days.CalendarLunar {
		t.Fatalf("updated %+v", day)
	}
}
