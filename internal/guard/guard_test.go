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

func TestClaimsAction(t *testing.T) {
	claims := []string{
		"Your refund has been issued.",
		"Good news: your refund is processed.",
		"The refund was approved this morning.",
		"Your return is approved!",
		"I've cancelled your order.",
		"I have changed the delivery address.",
		"We have processed your refund.",
		"I will refund you today.",
		"We'll cancel it right away.",
		"I'm going to process the exchange now.",
		"Your order has been cancelled.",
		"Your address was updated.",
		"I’ve cancelled the order.", // curly apostrophe
		"I've gone ahead and arranged a replacement.",
		"Your replacement is on its way.",
	}
	for _, s := range claims {
		if !ClaimsAction(s) {
			t.Errorf("should flag: %q", s)
		}
	}
	policy := []string{
		"You can return unworn shoes within 30 days of delivery.",
		"Refunds are processed within 5-7 business days after we receive the return.",
		"Our customer care team handles refunds and cancellations.",
		"Once your return is received, a refund is issued to the original payment method.",
		"Your order was delivered on 17 September.",
		"Your order has shipped with NinjaVan.",
		"Orders can be cancelled before they ship.",
		"To change your address, contact our team before the order ships.",
		"I can't cancel orders, but our team can help.",
	}
	for _, s := range policy {
		if ClaimsAction(s) {
			t.Errorf("false positive: %q", s)
		}
	}
}
