package guard

import (
	"errors"
	"testing"
)

func TestValidateCitations(t *testing.T) {
	retrieved := []string{"returns#return-window", "returns#refund-timing"}

	if err := ValidateCitations([]string{"returns#return-window"}, retrieved); err != nil {
		t.Errorf("valid citation rejected: %v", err)
	}
	if err := ValidateCitations(nil, retrieved); !errors.Is(err, ErrNoCitation) {
		t.Errorf("want ErrNoCitation, got %v", err)
	}
	// Real chunk, but not retrieved this turn.
	if err := ValidateCitations([]string{"shipping#shipping-fees"}, retrieved); !errors.Is(err, ErrUnknownCitation) {
		t.Errorf("want ErrUnknownCitation, got %v", err)
	}
	// One good, one invented.
	if err := ValidateCitations([]string{"returns#return-window", "returns#90-day-policy"}, retrieved); !errors.Is(err, ErrUnknownCitation) {
		t.Errorf("want ErrUnknownCitation, got %v", err)
	}
}

func TestRedactPII(t *testing.T) {
	cases := map[string]string{
		"my email is Lina.Wong@example.com":        "my email is [EMAIL]",
		"card 4111 1111 1111 1111 exp 12/28":       "card [CARD] exp 12/28",
		"card 4111-1111-1111-1111":                 "card [CARD]",
		"call me at +65 9123 4567":                 "call me at [PHONE]",
		"call 91234567":                            "call [PHONE]",
		"order SS-10003 tracking SP77881234SG":     "order SS-10003 tracking SP77881234SG",
		"NinjaVan NV2026093012345 for SS-10002 ok": "NinjaVan NV2026093012345 for SS-10002 ok",
	}
	for in, want := range cases {
		if got := RedactPII(in); got != want {
			t.Errorf("RedactPII(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikePaymentData(t *testing.T) {
	if !LooksLikePaymentData("here is my card 4111111111111111") {
		t.Error("should detect card")
	}
	if !LooksLikePaymentData("my CVV is 123") {
		t.Error("should detect cvv mention")
	}
	if LooksLikePaymentData("order SS-10003 please") {
		t.Error("order id is not payment data")
	}
}
