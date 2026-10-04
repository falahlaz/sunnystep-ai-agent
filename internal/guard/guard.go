// Package guard holds deterministic checks applied around the model.
package guard

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrNoCitation      = errors.New("answer has no citation")
	ErrUnknownCitation = errors.New("answer cites a chunk that was not retrieved")
)

// ValidateCitations checks that a grounded answer cites at least one chunk
// and only chunks that were actually retrieved for this turn. Citing a real
// chunk that was not retrieved also fails: it means the model is answering
// from memory, not from what it was shown.
func ValidateCitations(cited, retrieved []string) error {
	if len(cited) == 0 {
		return ErrNoCitation
	}
	seen := make(map[string]bool, len(retrieved))
	for _, r := range retrieved {
		seen[r] = true
	}
	for _, c := range cited {
		if !seen[c] {
			return fmt.Errorf("%w: %q", ErrUnknownCitation, c)
		}
	}
	return nil
}

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// 13-19 digits, optionally separated by spaces or dashes: payment cards.
	cardRe = regexp.MustCompile(`\b(?:\d[ \-]?){12,18}\d\b`)
	// Phone numbers: optional +country code, then 8+ digits with separators.
	phoneRe = regexp.MustCompile(`(?:\+\d{1,3}[ \-]?)?\b\d{4}[ \-]?\d{4}\b`)
)

// RedactPII masks emails, card-like numbers and phone numbers before text is
// written to logs. Order IDs (SS-12345) and tracking numbers are kept because
// operators need them. Order matters: cards before phones, since a card
// number also matches the phone pattern.
func RedactPII(s string) string {
	s = emailRe.ReplaceAllString(s, "[EMAIL]")
	s = cardRe.ReplaceAllString(s, "[CARD]")
	s = phoneRe.ReplaceAllString(s, "[PHONE]")
	return s
}

// LooksLikePaymentData reports whether a customer message appears to contain
// card details, so the agent can warn the customer instead of processing it.
func LooksLikePaymentData(s string) bool {
	return cardRe.MatchString(s) || strings.Contains(strings.ToLower(s), "cvv")
}

// actionClaimRes match text that claims or promises a write action the
// agent cannot perform. Broad on verbs, narrow on form: a generic policy
// sentence ("a refund is issued once we receive the return") passes; a
// statement about this customer's case ("your refund has been processed")
// does not. Matching is on lower-cased text with straight apostrophes.
var actionClaimRes = []*regexp.Regexp{
	// Perfect tense is always a claim: "refund has been issued".
	regexp.MustCompile(`\b(refund|return|exchange|cancell?ation|replacement|order|address)\s+(has|have)\s+(now\s+|already\s+)*been\s+(issued|processed|approved|initiated|completed|cancell?ed|refunded|changed|updated|arranged|sent|accepted|granted)\b`),
	// A definite subject makes it about this case: "your return is approved".
	regexp.MustCompile(`\b(your|the|this)\s+(refund|return|exchange|cancell?ation|replacement|order|address)\s+(is|was|were)\s+(now\s+|already\s+)*(issued|processed|approved|initiated|completed|cancell?ed|refunded|changed|updated|arranged|accepted|granted|confirmed|on its way)\b`),
	// First person, done: "I've cancelled", "we have processed".
	regexp.MustCompile(`\b(i|we)('ve|\s+have|\s+just|\s+already)+\s+(cancell?ed|changed|updated|approved|processed|issued|refunded|initiated|arranged|sent|booked|submitted|gone ahead)\b`),
	// First person, promised: "I will refund", "we'll cancel".
	regexp.MustCompile(`\b(i|we)('ll|\s+will|\s+am going to|'m going to|\s+are going to|'re going to)\s+(refund|cancel|change|update|approve|process|issue|send|arrange|replace|exchange)\b`),
}

var apostrophes = strings.NewReplacer("\u2019", "'", "\u2018", "'")

// ClaimsAction reports whether a drafted answer claims or promises a write
// action (refund, cancellation, change...). Such answers are never sent:
// the agent cannot perform actions, so the claim would be false (PRD §4.5).
func ClaimsAction(text string) bool {
	low := apostrophes.Replace(strings.ToLower(text))
	for _, re := range actionClaimRes {
		if re.MatchString(low) {
			return true
		}
	}
	return false
}
