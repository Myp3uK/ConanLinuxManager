package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Cron struct{ fields [5]map[int]bool }

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

func (c Cron) Next(now time.Time, location *time.Location) time.Time {
	local := now.In(location).Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 366*24*60; i++ {
		month, day, weekday := int(local.Month()), local.Day(), int(local.Weekday())
		dayMatches, weekdayMatches := c.fields[2][day], c.fields[4][weekday]
		calendarMatches := dayMatches && weekdayMatches
		if len(c.fields[2]) < 31 && len(c.fields[4]) < 7 {
			calendarMatches = dayMatches || weekdayMatches
		}
		if c.fields[0][local.Minute()] && c.fields[1][local.Hour()] && c.fields[3][month] && calendarMatches {
			return local.UTC()
		}
		local = local.Add(time.Minute)
	}
	return now.UTC().Add(366 * 24 * time.Hour)
}
