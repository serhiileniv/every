package schedule

import (
	"fmt"

	"github.com/serhiileniv/every/internal/jsonx"
)

// Record is the on-disk form of a schedule, and the wire form inside a task
// record in tasks.json.
//
// Field order is the emitted key order, and tasks.json is compared byte for
// byte against fixtures generated from the Ruby, so this ordering is a
// compatibility surface rather than a style choice. Ruby built the hash as raw,
// kind, then whichever of interval/entries applied -- never both.
//
// The legacy hour/minute/weekday fields belong only to the pre-0.2 "daily" and
// "weekly" kinds. They are decode-only: FromRecord migrates them to entries,
// and a re-save writes the modern shape.
type Record struct {
	Raw  string `json:"raw"`
	Kind string `json:"kind"`
	// A pointer, because encoding/json's omitempty does nothing for a struct
	// value -- it would emit "interval":0 on every calendar schedule, which
	// Ruby never wrote and the byte-comparison would rightly reject.
	Interval *Seconds `json:"interval,omitempty"`
	Entries  []Entry  `json:"entries,omitempty"`

	Hour    *int `json:"hour,omitempty"`
	Minute  *int `json:"minute,omitempty"`
	Weekday *int `json:"weekday,omitempty"`
}

// ToRecord is Ruby's Schedule#to_h.
func (s *Schedule) ToRecord() Record {
	r := Record{Raw: s.Raw, Kind: string(s.Kind)}
	if s.Kind == Interval {
		v := s.Interval
		r.Interval = &v
		return r
	}
	// An empty entries list still serializes as [], matching Ruby, where the
	// key is present whenever entries is non-nil.
	if s.Entries == nil {
		r.Entries = []Entry{}
	} else {
		r.Entries = s.Entries
	}
	return r
}

// FromRecord is Ruby's Schedule.from_h. It accepts the current format plus the
// pre-0.2 "daily" and "weekly" task records, so a store written by 0.1 still
// loads. Dropping that would strand anyone who has not re-added their tasks.
func FromRecord(r Record) (*Schedule, error) {
	switch r.Kind {
	case "interval":
		sched := &Schedule{Raw: r.Raw, Kind: Interval}
		if r.Interval != nil {
			sched.Interval = *r.Interval
		}
		return sched, nil

	case "calendar":
		return &Schedule{Raw: r.Raw, Kind: Calendar, Entries: normalizeEntries(r.Entries)}, nil

	case "daily":
		return &Schedule{Raw: r.Raw, Kind: Calendar, Entries: []Entry{{
			Hour: deref(r.Hour), Minute: deref(r.Minute),
		}}}, nil

	case "weekly":
		return &Schedule{Raw: r.Raw, Kind: Calendar, Entries: []Entry{{
			Hour: deref(r.Hour), Minute: deref(r.Minute),
			Weekday: intp(clampWeekday(deref(r.Weekday))),
		}}}, nil

	default:
		return nil, fmt.Errorf("unknown schedule kind %s", inspect(r.Kind))
	}
}

// normalizeEntries clamps any persisted weekday into 0-6. A legacy 7 means
// Sunday, which is 0.
func normalizeEntries(entries []Entry) []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	for i := range out {
		if out[i].Weekday != nil {
			out[i].Weekday = intp(clampWeekday(*out[i].Weekday))
		}
	}
	return out
}

// clampWeekday uses floored modulo, matching Ruby, so a negative persisted
// value lands in 0..6 rather than staying negative.
func clampWeekday(v int) int { return ((v % 7) + 7) % 7 }

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// MarshalRecord encodes a schedule record. See package jsonx for why the
// standard library's Marshal is not used directly.
func MarshalRecord(r Record) ([]byte, error) {
	return jsonx.Marshal(r)
}
