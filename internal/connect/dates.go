package connect

import "github.com/evolutionlabs-core/beerus/internal/clock"

func historyStart(start, today clock.Date, days int) (clock.Date, bool) {
	if floor := today.AddDays(-days); start.Before(floor) {
		return floor, true
	}
	return start, false
}

func reconnectFrom(current, eff clock.Date) clock.Date {
	if eff.Before(current) {
		return eff
	}
	return current
}

func asOfBounds(from, today clock.Date) (min, max clock.Date) {
	return from.AddDays(-1), today.AddDays(-1)
}
