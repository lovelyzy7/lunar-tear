package gametime

import "time"

// Zone is the game's day boundary: the global client's BaseTimeZone is
// PacificStandardTime with zone id "Etc/GMT+8", a fixed UTC-8 with no
// daylight saving. Daily sets start at 08:00 UTC all year and weekly
// schedules on Monday 08:00 UTC.
var Zone = time.FixedZone("GMT-8", -8*60*60)

const (
	dayMillis  = int64(24 * time.Hour / time.Millisecond)
	weekMillis = 7 * dayMillis
	// zoneOffsetMillis is how far the game's midnight is after UTC midnight.
	zoneOffsetMillis = int64(8 * time.Hour / time.Millisecond)
	// utcMondayMillis is 1970-01-05 00:00 UTC, the first Monday after the epoch.
	utcMondayMillis = 4 * dayMillis
)

func Now() time.Time {
	real := time.Now().UTC()
	return real.Add(time.Duration(calendarShift(real.UnixMilli())) * time.Millisecond)
}

func NowMillis() int64 {
	return Now().UnixMilli()
}

// StartOfDay returns the start (00:00 GMT-8) of the game day containing millis.
func StartOfDay(millis int64) int64 {
	t := time.UnixMilli(millis).In(Zone)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, Zone).UnixMilli()
}

// StartOfDayMillis returns the start of the current game day.
func StartOfDayMillis() int64 {
	return StartOfDay(NowMillis())
}

// SameDay reports whether two instants fall on the same game day (00:00
// GMT-8). Day-anchored comparison keeps working for saves whose timestamps sit
// in the game clock's future — written under the real-time clock before the
// calendar's shift, or left behind by RewindLoop — where a raw
// `ts >= StartOfDay(now)` test would read "already done today" forever.
func SameDay(a, b int64) bool {
	return StartOfDay(a) == StartOfDay(b)
}

// WeeklyVersion returns a stable weekly identifier (start-of-week timestamp in
// millis, Monday 00:00 GMT-8).
func WeeklyVersion(millis int64) int64 {
	t := time.UnixMilli(millis).In(Zone)
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := time.Date(t.Year(), t.Month(), t.Day()-(weekday-1), 0, 0, 0, 0, Zone)
	return monday.UnixMilli()
}

// Older saves keyed days and weeks by UTC midnight (00:00 UTC, Monday for
// weeks); the game's keys fall at 08:00 UTC, so the two never collide.

// IsLegacyWeekVersion reports whether v is an old Monday 00:00 UTC week key.
func IsLegacyWeekVersion(v int64) bool {
	return v > 0 && (v-utcMondayMillis)%weekMillis == 0
}

// UpgradeWeekVersion maps an old UTC week key to the GMT-8 week starting on
// the same Monday, which covers all but its first 8 hours, so the week in
// progress carries on rather than closing early. Other values are returned
// unchanged.
func UpgradeWeekVersion(v int64) int64 {
	if IsLegacyWeekVersion(v) {
		return v + zoneOffsetMillis
	}
	return v
}

// IsLegacyDayVersion reports whether v is an old 00:00 UTC day key.
func IsLegacyDayVersion(v int64) bool {
	return v > 0 && v%dayMillis == 0
}

// UpgradeDayVersion maps an old UTC day key, as seen at now, to the game day
// of the latest moment it can stand for (the end of that UTC day, or now if
// earlier). Something done once per day is then never done twice on the
// switch day. Other values are returned unchanged.
func UpgradeDayVersion(v, now int64) int64 {
	if !IsLegacyDayVersion(v) {
		return v
	}
	return StartOfDay(min(v+dayMillis-1, max(v, now)))
}
