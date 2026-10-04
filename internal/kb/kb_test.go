package kb

import (
	"strings"
	"testing"
)

func loadReal(t *testing.T) *Index {
	t.Helper()
	idx, err := LoadDir("../../data/kb")
	if err != nil {
		t.Fatalf("load kb: %v", err)
	}
	return idx
}

func TestSearchTopHit(t *testing.T) {
	idx := loadReal(t)
	cases := []struct {
		query string
		want  string
	}{
		{"how many days do I have to return my shoes", "returns#return-window"},
		{"is shipping free", "shipping#shipping-fees"},
		{"can I wash them in the washing machine", "care#machine-washing"},
		{"will these help my plantar fasciitis", "health#foot-conditions-and-medical-advice"},
		{"I want to change my delivery address", "orders#changing-the-delivery-address"},
		{"do you have shoes for wide feet", "sizing#wide-feet"},
		{"when will I get my refund", "returns#refund-timing"},
	}
	for _, c := range cases {
		hits := idx.Search(c.query, 3)
		if len(hits) == 0 {
			t.Errorf("%q: no hits", c.query)
			continue
		}
		if hits[0].ChunkID != c.want {
			var got []string
			for _, h := range hits {
				got = append(got, h.ChunkID)
			}
			t.Errorf("%q: top=%s want=%s (all=%v)", c.query, hits[0].ChunkID, c.want, got)
		}
	}
}

func TestSearchNoMatch(t *testing.T) {
	idx := loadReal(t)
	if hits := idx.Search("cryptocurrency staking rewards", 3); len(hits) != 0 {
		t.Fatalf("expected no hits, got %v", hits)
	}
	if hits := idx.Search("the and of", 3); hits != nil {
		t.Fatalf("stopword-only query should return nil, got %v", hits)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	if _, err := parseFile("x", "# Title only\nno sections"); err == nil {
		t.Fatal("expected error for file without sections")
	}
	if _, err := parseFile("x", "## Empty\n\n## Next\nbody"); err == nil || !strings.Contains(err.Error(), "empty section") {
		t.Fatalf("expected empty-section error, got %v", err)
	}
	if _, err := Build([]Chunk{{ID: "a#b", Text: "x"}, {ID: "a#b", Text: "y"}}); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestGet(t *testing.T) {
	idx := loadReal(t)
	if _, ok := idx.Get("returns#return-window"); !ok {
		t.Fatal("expected chunk to exist")
	}
	if _, ok := idx.Get("returns#made-up"); ok {
		t.Fatal("unexpected chunk")
	}
}
