package match

import (
	"testing"
	"time"
)

func ids(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.HostID
	}
	return out
}

func TestRank(t *testing.T) {
	now := time.Now()
	cands := []Candidate{
		{HostID: "far", Region: "us-east", LastSeenAt: now},
		{HostID: "unprobed", Region: "ap-south", LastSeenAt: now},
		{HostID: "near-stale", Region: "eu-central", LastSeenAt: now.Add(-20 * time.Second)},
		{HostID: "near-fresh", Region: "eu-central", LastSeenAt: now},
	}
	got := ids(Rank(cands, map[string]int{"eu-central": 18, "us-east": 110}))
	want := []string{"near-fresh", "near-stale", "far", "unprobed"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if cands[0].HostID != "far" {
		t.Error("Rank modified its input")
	}
}

func TestRankNoLatencies(t *testing.T) {
	now := time.Now()
	got := ids(Rank([]Candidate{
		{HostID: "old", LastSeenAt: now.Add(-time.Minute)},
		{HostID: "new", LastSeenAt: now},
	}, nil))
	if got[0] != "new" {
		t.Errorf("got %v", got)
	}
}
