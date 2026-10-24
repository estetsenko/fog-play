// Package match ranks candidate hosts for a new session.
package match

import (
	"sort"
	"time"
)

// unknownLatency ranks hosts in regions the player did not probe after all probed ones.
const unknownLatency = 1 << 30

type Candidate struct {
	HostID     string
	Region     string
	LastSeenAt time.Time
}

// Rank orders candidates best first: lowest measured latency to the host's
// region, then the most recent heartbeat. latencyMS maps region to the
// round-trip time the player's client measured; it may be empty.
func Rank(cands []Candidate, latencyMS map[string]int) []Candidate {
	out := append([]Candidate(nil), cands...)
	lat := func(c Candidate) int {
		if ms, ok := latencyMS[c.Region]; ok && ms >= 0 {
			return ms
		}
		return unknownLatency
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := lat(out[i]), lat(out[j])
		if li != lj {
			return li < lj
		}
		return out[i].LastSeenAt.After(out[j].LastSeenAt)
	})
	return out
}
