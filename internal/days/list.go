package days

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// When selects the Home list half (SPEC §8).
type When string

const (
	WhenFuture When = "future"
	WhenPast   When = "past"
)

// ParseWhen maps a query value to a When, defaulting to WhenFuture.
func ParseWhen(s string) When {
	if When(s) == WhenPast {
		return WhenPast
	}
	return WhenFuture
}

// Entry is a day with its calculation for one point in time.
type Entry struct {
	Day  Day
	Calc Calculation
}

// HomeList classifies days at now in loc (paused archived days at their
// archive time) and keeps those in the requested half:
// future (including today) ascending by effective target, or past descending.
// Ties are broken by title, then ID. Days whose schedule cannot be calculated
// are skipped.
func HomeList(list []Day, when When, now time.Time, loc *time.Location) []Entry {
	out := make([]Entry, 0, len(list))
	for _, d := range list {
		c, err := Calculate(d.Schedule(), d.CountAt(now), loc)
		if err != nil {
			continue
		}
		if (c.Classification == Past) != (when == WhenPast) {
			continue
		}
		out = append(out, Entry{Day: d, Calc: c})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.Calc.Target.Equal(b.Calc.Target) {
			if when == WhenPast {
				return a.Calc.Target.After(b.Calc.Target)
			}
			return a.Calc.Target.Before(b.Calc.Target)
		}
		if a.Day.Title != b.Day.Title {
			return a.Day.Title < b.Day.Title
		}
		return a.Day.ID < b.Day.ID
	})
	return out
}

// ListActive returns the owner's non-archived days with their tag IDs.
func (s *Days) ListActive(ctx context.Context, ownerID int64) ([]Day, error) {
	return s.list(ctx, ownerID, "archived_at IS NULL")
}

// ListArchived returns only the owner's archived days.
func (s *Days) ListArchived(ctx context.Context, ownerID int64) ([]Day, error) {
	return s.list(ctx, ownerID, "archived_at IS NOT NULL")
}

// ListCollection includes archived days unless hideArchived is set.
func (s *Days) ListCollection(ctx context.Context, ownerID, collectionID int64, hideArchived bool) ([]Day, error) {
	if _, err := s.collections.Get(ctx, ownerID, collectionID); err != nil {
		return nil, err
	}
	filter := "collection_id = ?"
	if hideArchived {
		filter += " AND archived_at IS NULL"
	}
	return s.list(ctx, ownerID, filter, collectionID)
}

// ListTag includes archived days unless hideArchived is set.
func (s *Days) ListTag(ctx context.Context, ownerID, tagID int64, hideArchived bool) ([]Day, error) {
	if _, err := s.tags.Get(ctx, ownerID, tagID); err != nil {
		return nil, err
	}
	filter := "EXISTS (SELECT 1 FROM day_tags WHERE day_id = days.id AND tag_id = ?)"
	if hideArchived {
		filter += " AND archived_at IS NULL"
	}
	return s.list(ctx, ownerID, filter, tagID)
}

// list accepts only predicates supplied by the repository, never user input.
func (s *Days) list(ctx context.Context, ownerID int64, filter string, args ...any) ([]Day, error) {
	args = append([]any{ownerID, IncludesPrivate(ctx), IsPrivate(ctx)}, args...)
	where := " WHERE owner_id = ? AND (? OR is_private = ?) AND " + filter
	rows, err := s.db.QueryContext(ctx, `SELECT `+dayColumns+` FROM days
		`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list days: %w", err)
	}
	defer rows.Close()
	var list []Day
	index := make(map[int64]int)
	for rows.Next() {
		d, err := scanDay(rows)
		if err != nil {
			return nil, fmt.Errorf("list days: %w", err)
		}
		index[d.ID] = len(list)
		list = append(list, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list days: %w", err)
	}
	if len(list) == 0 {
		return list, nil
	}
	tagRows, err := s.db.QueryContext(ctx, `SELECT dt.day_id, dt.tag_id FROM day_tags dt
		JOIN days ON days.id = dt.day_id
		`+where+` ORDER BY dt.day_id, dt.tag_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list day tags: %w", err)
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var dayID, tagID int64
		if err := tagRows.Scan(&dayID, &tagID); err != nil {
			return nil, fmt.Errorf("list day tags: %w", err)
		}
		if i, ok := index[dayID]; ok {
			list[i].TagIDs = append(list[i].TagIDs, tagID)
		}
	}
	if err := tagRows.Err(); err != nil {
		return nil, fmt.Errorf("list day tags: %w", err)
	}
	return list, nil
}
