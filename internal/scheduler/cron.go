package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronField struct {
	values   []bool
	minimum  int
	maximum  int
	wildcard bool
}

type cronSpec struct {
	seconds  cronField
	minutes  cronField
	hours    cronField
	days     cronField
	months   cronField
	weekdays cronField
}

func parseCronExpression(expression string) (cronSpec, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return cronSpec{}, fmt.Errorf("cron expression is empty")
	}
	if alias, ok := cronAliases[strings.ToLower(expression)]; ok {
		expression = alias
	}
	parts := strings.Fields(expression)
	if len(parts) != 5 && len(parts) != 6 {
		return cronSpec{}, fmt.Errorf("cron expression must have five fields (or six with seconds), got %d", len(parts))
	}

	index := 0
	spec := cronSpec{}
	if len(parts) == 5 {
		spec.seconds = fixedCronField(0, 0)
	} else {
		var err error
		spec.seconds, err = parseCronField(parts[index], 0, 59, nil)
		if err != nil {
			return cronSpec{}, fmt.Errorf("parse cron seconds: %w", err)
		}
		index++
	}
	fields := []struct {
		name   string
		value  string
		min    int
		max    int
		names  map[string]int
		target *cronField
	}{
		{name: "minutes", value: parts[index], min: 0, max: 59, target: &spec.minutes},
		{name: "hours", value: parts[index+1], min: 0, max: 23, target: &spec.hours},
		{name: "days", value: parts[index+2], min: 1, max: 31, target: &spec.days},
		{name: "months", value: parts[index+3], min: 1, max: 12, names: monthNames, target: &spec.months},
		{name: "weekdays", value: parts[index+4], min: 0, max: 7, names: weekdayNames, target: &spec.weekdays},
	}
	for _, field := range fields {
		parsed, err := parseCronField(field.value, field.min, field.max, field.names)
		if err != nil {
			return cronSpec{}, fmt.Errorf("parse cron %s: %w", field.name, err)
		}
		*field.target = parsed
	}
	return spec, nil
}

var cronAliases = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var weekdayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func fixedCronField(minimum, maximum int) cronField {
	field := cronField{minimum: minimum, maximum: maximum, values: make([]bool, maximum-minimum+1)}
	field.values[0] = true
	field.wildcard = true
	return field
}

func parseCronField(expression string, minimum, maximum int, names map[string]int) (cronField, error) {
	field := cronField{minimum: minimum, maximum: maximum, values: make([]bool, maximum-minimum+1)}
	parts := strings.Split(strings.ToLower(strings.TrimSpace(expression)), ",")
	if len(parts) == 0 {
		return cronField{}, fmt.Errorf("field is empty")
	}
	field.wildcard = strings.TrimSpace(expression) == "*"
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return cronField{}, fmt.Errorf("empty list item")
		}
		base, step, err := parseCronPart(part, minimum, maximum, names)
		if err != nil {
			return cronField{}, err
		}
		for value := base.start; value <= base.end; value += step {
			field.values[value-minimum] = true
			if maximum == 7 && value == 7 {
				// Unix cron accepts both 0 and 7 for Sunday.
				field.values[0] = true
			}
		}
	}
	return field, nil
}

type cronRange struct {
	start int
	end   int
}

func parseCronPart(part string, minimum, maximum int, names map[string]int) (cronRange, int, error) {
	pieces := strings.Split(part, "/")
	if len(pieces) > 2 {
		return cronRange{}, 0, fmt.Errorf("invalid step expression %q", part)
	}
	step := 1
	if len(pieces) == 2 {
		parsed, err := strconv.Atoi(strings.TrimSpace(pieces[1]))
		if err != nil || parsed < 1 {
			return cronRange{}, 0, fmt.Errorf("step must be a positive integer in %q", part)
		}
		step = parsed
	}
	base := strings.TrimSpace(pieces[0])
	if base == "*" {
		return cronRange{start: minimum, end: maximum}, step, nil
	}
	rangeParts := strings.Split(base, "-")
	if len(rangeParts) > 2 {
		return cronRange{}, 0, fmt.Errorf("invalid range %q", base)
	}
	start, err := parseCronValue(rangeParts[0], names)
	if err != nil {
		return cronRange{}, 0, err
	}
	end := start
	if len(rangeParts) == 2 {
		end, err = parseCronValue(rangeParts[1], names)
		if err != nil {
			return cronRange{}, 0, err
		}
	}
	if start < minimum || start > maximum || end < minimum || end > maximum || start > end {
		return cronRange{}, 0, fmt.Errorf("value range %d-%d is outside %d-%d", start, end, minimum, maximum)
	}
	return cronRange{start: start, end: end}, step, nil
}

func parseCronValue(value string, names map[string]int) (int, error) {
	if names != nil {
		if named, ok := names[strings.ToLower(strings.TrimSpace(value))]; ok {
			return named, nil
		}
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", value)
	}
	return parsed, nil
}

func (field cronField) matches(value int) bool {
	if value == 7 && field.maximum == 7 {
		value = 0
	}
	if value < field.minimum || value > field.maximum {
		return false
	}
	return field.values[value-field.minimum]
}

func (spec cronSpec) matches(at time.Time) bool {
	if !spec.seconds.matches(at.Second()) ||
		!spec.minutes.matches(at.Minute()) ||
		!spec.hours.matches(at.Hour()) ||
		!spec.months.matches(int(at.Month())) {
		return false
	}
	domMatches := spec.days.matches(at.Day())
	dowMatches := spec.weekdays.matches(int(at.Weekday()))
	if spec.days.wildcard || spec.weekdays.wildcard {
		return domMatches && dowMatches
	}
	return domMatches || dowMatches
}

func nextCron(expression string, after time.Time) (time.Time, error) {
	spec, err := parseCronExpression(expression)
	if err != nil {
		return time.Time{}, err
	}
	step := time.Minute
	if spec.seconds.maximum != 0 {
		step = time.Second
	}
	candidate := after.Truncate(step).Add(step)
	limit := candidate.Add(5 * 366 * 24 * time.Hour)
	for !candidate.After(limit) {
		if spec.matches(candidate) {
			return candidate, nil
		}
		candidate = candidate.Add(step)
	}
	return time.Time{}, fmt.Errorf("cron expression has no run time within five years")
}
