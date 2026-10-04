package kb

import (
	"slices"
	"testing"

	"github.com/falahlaz/support-agent/internal/policy"
)

// Regression test for D24. Grounded eval questions must clear the policy
// threshold with the right chunk in the top 4 (what the drafter sees), and
// off-topic questions must stay below it. Both sides are checked: lowering
// the threshold or over-stemming would pass one side and fail the other.
func TestThresholdSeparatesEvalQuestions(t *testing.T) {
	idx, err := LoadDir("../../data/kb")
	if err != nil {
		t.Fatal(err)
	}
	min := policy.DefaultConfig().MinKBScore

	grounded := map[string]string{
		"How many days do I have to return my shoes?":                  "returns#return-window",
		"Can I return shoes I have already worn a little?":             "returns#comfort-guarantee",
		"Is shipping free?":                                            "shipping#shipping-fees",
		"How long does a refund take?":                                 "returns#refund-timing",
		"Can I put my shoes in the washing machine?":                   "care#machine-washing",
		"I have wide feet. Which models come in a wide fit?":           "sizing#wide-feet",
		"Can I exchange shoes for a different size if they don't fit?": "sizing#size-exchanges",
		"What does the warranty cover and for how long?":               "warranty#manufacturing-defects",
	}
	for q, want := range grounded {
		hits := idx.Search(q, 4)
		if len(hits) == 0 || hits[0].Score < min {
			t.Errorf("%q: top score below threshold %.1f: %v", q, min, hits)
			continue
		}
		ids := make([]string, len(hits))
		for i, h := range hits {
			ids[i] = h.ChunkID
		}
		if !slices.Contains(ids, want) {
			t.Errorf("%q: %s not in top 4 %v", q, want, ids)
		}
	}

	for _, q := range []string{
		"What are your store opening hours?",
		"Do you ship to Japan?",
		"Do you sell bags?",
		"Do you sell gift cards?",
		"Do you have any discount codes?",
	} {
		if hits := idx.Search(q, 1); len(hits) > 0 && hits[0].Score >= min {
			t.Errorf("off-topic %q clears threshold via %s (%.2f)", q, hits[0].ChunkID, hits[0].Score)
		}
	}
}

func TestStem(t *testing.T) {
	cases := map[string]string{
		"shoes": "shoe", "shipping": "ship", "shipped": "ship", "ship": "ship",
		"covered": "cover", "refunds": "refund", "refunded": "refund", "fitting": "fit",
		"dress": "dress", "small": "small", "fizz": "fizz", "red": "red", "bring": "bring",
	}
	for in, want := range cases {
		if got := stem(in); got != want {
			t.Errorf("stem(%q) = %q, want %q", in, got, want)
		}
	}
}
