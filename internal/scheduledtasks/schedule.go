package scheduledtasks

import (
	"errors"
	"fmt"
	"strings"
	"time"

	rrule "github.com/teambition/rrule-go"
)

const MinimumEffectiveInterval = 5 * time.Minute

type Schedule struct {
	Text      string
	Timezone  string
	Kind      string
	Interval  time.Duration
	set       *rrule.Set
	oneShot   bool
	oneShotAt time.Time
}

func ParseSchedule(raw, timezone string) (Schedule, error) {
	lines := recurrenceLines(raw)
	if len(lines) == 0 {
		return Schedule{}, errors.New("schedule 不能为空")
	}
	dtstartCount := 0
	dtstartIndex := -1
	rruleCount := 0
	for index, line := range lines {
		name := strings.ToUpper(strings.SplitN(line, ":", 2)[0])
		switch {
		case strings.HasPrefix(name, "DTSTART"):
			dtstartCount++
			dtstartIndex = index
		case name == "RRULE":
			rruleCount++
		case strings.HasPrefix(name, "RDATE"), strings.HasPrefix(name, "EXDATE"):
		default:
			return Schedule{}, fmt.Errorf("schedule 包含不支持的属性 %q", line)
		}
	}
	if dtstartCount != 1 {
		return Schedule{}, errors.New("schedule 必须且只能包含一个 DTSTART")
	}
	if rruleCount > 1 {
		return Schedule{}, errors.New("schedule 最多包含一个 RRULE")
	}
	if dtstartIndex != 0 {
		ordered := make([]string, 0, len(lines))
		ordered = append(ordered, lines[dtstartIndex])
		ordered = append(ordered, lines[:dtstartIndex]...)
		ordered = append(ordered, lines[dtstartIndex+1:]...)
		lines = ordered
	}

	location, zoneName, err := scheduleLocation(lines, strings.TrimSpace(timezone))
	if err != nil {
		return Schedule{}, err
	}
	set, err := rrule.StrSliceToRRuleSetInLoc(lines, location)
	if err != nil {
		return Schedule{}, fmt.Errorf("解析 RFC 5545 schedule: %w", err)
	}
	normalized := set.String()
	oneShot := set.GetRRule() == nil && len(set.GetRDate()) == 0
	if set.GetRRule() == nil && len(set.GetRDate()) > 0 {
		// rrule-go 的 Set 在没有 RRULE 时不会自动把 DTSTART 加入 RDATE 集合。
		// RFC recurrence set 仍应包含 DTSTART，因此只修改内存集合，保留规范化文本不重复 DTSTART。
		set.RDate(set.GetDTStart())
	}
	oneShotAt := set.GetDTStart()
	if oneShot && containsTime(set.GetExDate(), oneShotAt) {
		oneShotAt = time.Time{}
	}
	result := Schedule{Text: normalized, Timezone: zoneName, Kind: "wall_clock",
		set: set, oneShot: oneShot, oneShotAt: oneShotAt}
	if interval, ok := pureInterval(set); ok {
		result.Kind = "interval"
		result.Interval = interval
	}
	return result, nil
}

func (s Schedule) FirstAfter(now time.Time) time.Time {
	if s.oneShot {
		if !s.oneShotAt.IsZero() && s.oneShotAt.After(now) {
			return s.oneShotAt
		}
		return time.Time{}
	}
	return s.set.After(now, false)
}

func containsTime(values []time.Time, target time.Time) bool {
	for _, value := range values {
		if value.Equal(target) {
			return true
		}
	}
	return false
}

// NextAfterOccurrence 返回 current 之后、且严格晚于 now 的下一个有效 occurrence。
// 高频原始 occurrence 会被折叠，保证有效 occurrence 至少相隔五分钟。
func (s Schedule) NextAfterOccurrence(current, now time.Time) time.Time {
	if s.oneShot {
		return time.Time{}
	}
	minimum := current.Add(MinimumEffectiveInterval)
	if now.Before(minimum) {
		return s.set.After(minimum, true)
	}
	return s.set.After(now, false)
}

func recurrenceLines(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	parts := strings.Split(raw, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			lines = append(lines, part)
		}
	}
	return lines
}

func scheduleLocation(lines []string, requested string) (*time.Location, string, error) {
	dtstart := ""
	for _, line := range lines {
		if strings.HasPrefix(strings.ToUpper(line), "DTSTART") {
			dtstart = line
			break
		}
	}
	if zone := lineTZID(dtstart); zone != "" {
		if requested != "" && requested != zone {
			return nil, "", errors.New("timezone 与 DTSTART 的 TZID 不一致")
		}
		location, err := time.LoadLocation(zone)
		if err != nil {
			return nil, "", fmt.Errorf("加载 TZID %q: %w", zone, err)
		}
		return location, zone, nil
	}
	parts := strings.SplitN(dtstart, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return nil, "", errors.New("dtstart 格式无效")
	}
	value := strings.TrimSpace(parts[1])
	if strings.HasSuffix(strings.ToUpper(value), "Z") {
		if requested != "" && requested != "UTC" {
			return nil, "", errors.New("utc DTSTART 只能使用 UTC timezone")
		}
		return time.UTC, "UTC", nil
	}
	if requested == "" {
		return nil, "", errors.New("浮动 DTSTART 必须提供 IANA timezone")
	}
	location, err := time.LoadLocation(requested)
	if err != nil {
		return nil, "", fmt.Errorf("加载 timezone %q: %w", requested, err)
	}
	return location, requested, nil
}

func lineTZID(line string) string {
	left := strings.SplitN(line, ":", 2)[0]
	for _, parameter := range strings.Split(left, ";")[1:] {
		if strings.HasPrefix(strings.ToUpper(parameter), "TZID=") {
			return strings.TrimSpace(parameter[len("TZID="):])
		}
	}
	return ""
}

func pureInterval(set *rrule.Set) (time.Duration, bool) {
	rule := set.GetRRule()
	if rule == nil || len(set.GetRDate()) > 0 || len(set.GetExDate()) > 0 {
		return 0, false
	}
	option := rule.OrigOptions
	if len(option.Bysetpos) > 0 || len(option.Bymonth) > 0 ||
		len(option.Bymonthday) > 0 || len(option.Byyearday) > 0 ||
		len(option.Byweekno) > 0 || len(option.Byweekday) > 0 || len(option.Byhour) > 0 ||
		len(option.Byminute) > 0 || len(option.Bysecond) > 0 || len(option.Byeaster) > 0 {
		return 0, false
	}
	interval := option.Interval
	if interval <= 0 {
		interval = 1
	}
	switch option.Freq {
	case rrule.SECONDLY:
		return time.Duration(interval) * time.Second, true
	case rrule.MINUTELY:
		return time.Duration(interval) * time.Minute, true
	case rrule.HOURLY:
		return time.Duration(interval) * time.Hour, true
	default:
		return 0, false
	}
}
