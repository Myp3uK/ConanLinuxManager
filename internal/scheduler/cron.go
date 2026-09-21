package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron holds the allowed values of a five field expression:
// minute, hour, day of month, month, day of week.
type Cron struct{ fields [5]map[int]bool }

// searchLimit bounds the wall clock walk. A little over four years covers every
// leap-year pattern, so an expression that finds nothing here can never match.
const searchLimit = 4*366*24*60 + 24*60

func ParseCron(expression string) (Cron, error) {
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return Cron{}, fmt.Errorf("cron expression must have five fields")
	}
	ranges := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	var result Cron
	for i, part := range parts {
		values, err := parseCronField(part, ranges[i][0], ranges[i][1])
		if err != nil {
			return Cron{}, fmt.Errorf("cron field %d: %w", i+1, err)
		}
		result.fields[i] = values
	}
	return result, nil
}

func parseCronField(value string, minimum, maximum int) (map[int]bool, error) {
	result := make(map[int]bool)
	for _, item := range strings.Split(value, ",") {
		base, step := item, 1
		if strings.Contains(item, "/") {
			parts := strings.Split(item, "/")
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid step")
			}
			base = parts[0]
			parsed, err := strconv.Atoi(parts[1])
			if err != nil || parsed < 1 {
				return nil, fmt.Errorf("invalid step")
			}
			step = parsed
		}
		start, end := minimum, maximum
		if base != "*" {
			if strings.Contains(base, "-") {
				parts := strings.Split(base, "-")
				if len(parts) != 2 {
					return nil, fmt.Errorf("invalid range")
				}
				var err error
				start, err = strconv.Atoi(parts[0])
				if err != nil {
					return nil, err
				}
				end, err = strconv.Atoi(parts[1])
				if err != nil {
					return nil, err
				}
			} else {
				parsed, err := strconv.Atoi(base)
				if err != nil {
					return nil, err
				}
				start, end = parsed, parsed
				// A bare value combined with a step runs to the end of the range,
				// matching the usual "every N starting at X" reading of 5/15.
				if step > 1 {
					end = maximum
				}
			}
		}
		if start < minimum || end > maximum || start > end {
			return nil, fmt.Errorf("value outside %d-%d", minimum, maximum)
		}
		for current := start; current <= end; current += step {
			result[current] = true
		}
	}
	return result, nil
}

// matches reports whether a calendar minute satisfies the expression. When both
// day of month and day of week are restricted, cron matches either of them.
func (c Cron) matches(month, day, weekday, hour, minute int) bool {
	if !c.fields[0][minute] || !c.fields[1][hour] || !c.fields[3][month] {
		return false
	}
	dayRestricted := len(c.fields[2]) < 31
	weekdayRestricted := len(c.fields[4]) < 7
	if dayRestricted && weekdayRestricted {
		return c.fields[2][day] || c.fields[4][weekday]
	}
	return c.fields[2][day] && c.fields[4][weekday]
}

// Next walks wall clock minutes in the schedule's own location, so a daily job
// keeps its local time across a DST change instead of drifting by an hour.
//
// On a spring forward the named local time does not exist; time.Date normalizes
// it into the new offset so the job still runs that day. On a fall back the hour
// repeats and the earlier instant wins, so the job runs once rather than twice.
func (c Cron) Next(now time.Time, location *time.Location) (time.Time, error) {
	local := now.In(location)
	year, month, day := local.Date()
	hour, minute := local.Hour(), local.Minute()
	for i := 0; i < searchLimit; i++ {
		minute++
		if minute > 59 {
			minute = 0
			hour++
		}
		if hour > 23 {
			hour = 0
			day++
			// Normalize an overflowing day in UTC: plain calendar arithmetic
			// must not be perturbed by the offset change it is looking for.
			normalized := time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
			year, month, day = normalized.Date()
		}
		weekday := int(time.Date(year, month, day, 12, 0, 0, 0, time.UTC).Weekday())
		if !c.matches(int(month), day, weekday, hour, minute) {
			continue
		}
		candidate := time.Date(year, month, day, hour, minute, 0, 0, location)
		// A normalized nonexistent time can land at or before now; keep walking.
		if !candidate.After(now) {
			continue
		}
		return candidate.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("cron expression has no occurrence within four years")
}
