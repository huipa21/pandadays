package days

import (
	"context"
	"errors"
	"testing"

	"pandadays/internal/db/dbtest"
)

func TestLunarDayPersistence(t *testing.T) {
	ctx := context.Background()
	database := dbtest.Open(t)
	owner := newOwner(t, database, "lunar-owner")
	other := newOwner(t, database, "lunar-other")
	repo := NewDays(database)
	input := DayInput{Title: "New Year", Date: "2024-02-10", Calendar: CalendarLunar, Repeat: RepeatYearly}
	created, err := repo.Create(ctx, owner, input, groupNow)
	if err != nil || created.Calendar != CalendarLunar || created.Schedule().Calendar != CalendarLunar {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if _, err := repo.Get(ctx, other, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign read = %v", err)
	}
	if _, err := repo.Update(ctx, other, created.ID, input, groupNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign update = %v", err)
	}
	input.Repeat = RepeatMonthly
	updated, err := repo.Update(ctx, owner, created.ID, input, groupNow)
	if err != nil || updated.Calendar != CalendarLunar || updated.Repeat != RepeatMonthly {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	listed, err := repo.ListActive(ctx, owner)
	if err != nil || len(listed) != 1 || listed[0].Calendar != CalendarLunar {
		t.Fatalf("list = %+v, %v", listed, err)
	}
}
