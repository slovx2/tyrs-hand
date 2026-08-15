package scheduledtasks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseScheduleRequiresTimezoneForFloatingDTStart(t *testing.T) {
	_, err := ParseSchedule("DTSTART:20300101T090000\nRRULE:FREQ=DAILY", "")
	require.ErrorContains(t, err, "timezone")

	schedule, err := ParseSchedule("DTSTART:20300101T090000\nRRULE:FREQ=DAILY", "Asia/Shanghai")
	require.NoError(t, err)
	require.Equal(t, "Asia/Shanghai", schedule.Timezone)
	require.Contains(t, schedule.Text, "DTSTART;TZID=Asia/Shanghai:20300101T090000")
}

func TestScheduleSupportsRDateExDateAndCount(t *testing.T) {
	schedule, err := ParseSchedule(`DTSTART;TZID=America/New_York:20261101T013000
RRULE:FREQ=DAILY;COUNT=3
RDATE;TZID=America/New_York:20261105T013000
EXDATE;TZID=America/New_York:20261102T013000`, "")
	require.NoError(t, err)

	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	first := schedule.FirstAfter(now)
	require.Equal(t, 1, first.Hour())
	require.Equal(t, "America/New_York", first.Location().String())
	require.NotEqual(t, time.Time{}, schedule.NextAfterOccurrence(first, first))
}

func TestScheduleUTCAndUntilAreNormalized(t *testing.T) {
	schedule, err := ParseSchedule(`DTSTART:20300101T000000Z
RRULE:FREQ=DAILY;UNTIL=20300103T000000Z`, "")
	require.NoError(t, err)
	require.Equal(t, "UTC", schedule.Timezone)
	require.Contains(t, schedule.Text, "DTSTART:20300101T000000Z")

	first := schedule.FirstAfter(time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC))
	second := schedule.NextAfterOccurrence(first, first)
	third := schedule.NextAfterOccurrence(second, second)
	require.Equal(t, time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC), third)
	require.True(t, schedule.NextAfterOccurrence(third, third).IsZero())
}

func TestScheduleKeepsWallClockTimeAcrossDST(t *testing.T) {
	schedule, err := ParseSchedule(`DTSTART;TZID=America/New_York:20260307T090000
RRULE:FREQ=DAILY;COUNT=3`, "")
	require.NoError(t, err)

	first := schedule.FirstAfter(time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC))
	second := schedule.NextAfterOccurrence(first, first)
	require.Equal(t, 9, first.Hour())
	require.Equal(t, 9, second.Hour())
	require.Equal(t, 14, first.UTC().Hour())
	require.Equal(t, 13, second.UTC().Hour())
}

func TestScheduleCanUseOnlyRDatesAfterDTStart(t *testing.T) {
	schedule, err := ParseSchedule(`DTSTART:20300101T000000Z
RDATE:20300102T000000Z,20300103T000000Z
EXDATE:20300102T000000Z`, "")
	require.NoError(t, err)

	first := schedule.FirstAfter(time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC))
	require.Equal(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), first)
	require.Equal(t, time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC),
		schedule.NextAfterOccurrence(first, first))
}

func TestOneShotCompletesAfterDTStart(t *testing.T) {
	schedule, err := ParseSchedule("DTSTART:20300101T000000Z", "")
	require.NoError(t, err)
	before := time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)
	require.Equal(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), schedule.FirstAfter(before))
	require.True(t, schedule.NextAfterOccurrence(schedule.FirstAfter(before), before).IsZero())
	require.True(t, schedule.FirstAfter(time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)).IsZero())
}

func TestEffectiveOccurrencesAreAtLeastFiveMinutesApart(t *testing.T) {
	schedule, err := ParseSchedule("DTSTART:20300101T000000Z\nRRULE:FREQ=MINUTELY", "")
	require.NoError(t, err)
	require.Equal(t, "interval", schedule.Kind)
	require.Equal(t, time.Minute, schedule.Interval)

	first := schedule.FirstAfter(time.Date(2029, 12, 31, 23, 59, 0, 0, time.UTC))
	next := schedule.NextAfterOccurrence(first, first)
	require.Equal(t, 5*time.Minute, next.Sub(first))

	misfire := schedule.NextAfterOccurrence(first, first.Add(17*time.Minute+30*time.Second))
	require.Equal(t, first.Add(18*time.Minute), misfire)
}

func TestEffectiveWindowCoalescesIrregularRDates(t *testing.T) {
	schedule, err := ParseSchedule(`DTSTART:20300101T000000Z
RDATE:20300101T000400Z,20300101T000800Z`, "")
	require.NoError(t, err)
	first := schedule.FirstAfter(time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC))
	require.Equal(t, first.Add(8*time.Minute), schedule.NextAfterOccurrence(first, first))
}

func TestScheduleRejectsAmbiguousOrUnsupportedDefinitions(t *testing.T) {
	_, err := ParseSchedule("DTSTART:20300101T000000Z\nDTSTART:20300102T000000Z", "")
	require.ErrorContains(t, err, "一个 DTSTART")

	_, err = ParseSchedule("DTSTART:20300101T000000Z\nDURATION:PT1H", "")
	require.ErrorContains(t, err, "不支持")

	_, err = ParseSchedule("DTSTART;TZID=Asia/Shanghai:20300101T090000", "UTC")
	require.ErrorContains(t, err, "不一致")
}

func TestScheduleNormalizesDTStartToFirstProperty(t *testing.T) {
	schedule, err := ParseSchedule(`RRULE:FREQ=DAILY;COUNT=2
DTSTART:20300101T000000Z`, "")
	require.NoError(t, err)
	require.True(t, len(schedule.Text) > len("DTSTART:"))
	require.Equal(t, "DTSTART:", schedule.Text[:len("DTSTART:")])
}

func TestPureIntervalRejectsCalendarSelectors(t *testing.T) {
	finite, err := ParseSchedule("DTSTART:20300101T000000Z\nRRULE:FREQ=MINUTELY;COUNT=3", "")
	require.NoError(t, err)
	require.Equal(t, "interval", finite.Kind)
	require.Equal(t, time.Minute, finite.Interval)

	schedule, err := ParseSchedule("DTSTART:20300101T000000Z\nRRULE:FREQ=HOURLY;BYMINUTE=15", "")
	require.NoError(t, err)
	require.Equal(t, "wall_clock", schedule.Kind)
	require.Zero(t, schedule.Interval)
}
