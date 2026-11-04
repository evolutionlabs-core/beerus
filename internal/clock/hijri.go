package clock

import "time"

const (
	hijriEpoch  = 1948439
	unixEpochJD = 2440588
)

type Hijri struct {
	Y int
	M int
	D int
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func hijriJD(h Hijri) int {
	return h.D + (59*(h.M-1)+1)/2 + (h.Y-1)*354 + floorDiv(3+11*h.Y, 30) + hijriEpoch
}

func fromJD(jd int) Date {
	t := time.Unix(int64(jd-unixEpochJD)*86400, 0).UTC()
	return Date{t.Year(), t.Month(), t.Day()}
}

func toJD(d Date) int {
	t := time.Date(d.Y, d.M, d.D, 0, 0, 0, 0, time.UTC)
	return int(t.Unix()/86400) + unixEpochJD
}

func FromHijri(h Hijri) Date {
	return fromJD(hijriJD(h))
}

func ToHijri(d Date) Hijri {
	jd := toJD(d)
	y := floorDiv(30*(jd-hijriEpoch), 10631)
	for hijriJD(Hijri{y + 1, 1, 1}) <= jd {
		y++
	}
	for hijriJD(Hijri{y, 1, 1}) > jd {
		y--
	}
	m := 1
	for m < 12 && hijriJD(Hijri{y, m + 1, 1}) <= jd {
		m++
	}
	return Hijri{y, m, jd - hijriJD(Hijri{y, m, 1}) + 1}
}
