package orders

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func loadReal(t *testing.T) *Store {
	t.Helper()
	s, err := LoadFile("../../data/orders.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

func TestNormalizeOrderID(t *testing.T) {
	ok := map[string]string{
		"SS-10003":   "SS-10003",
		" ss-10003 ": "SS-10003",
		"ss10003":    "SS-10003",
		"SS 10003":   "SS-10003",
	}
	for in, want := range ok {
		got, err := NormalizeOrderID(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "10003", "SS-1003", "SS-100031", "XX-10003", "SS-10003; DROP"} {
		if _, err := NormalizeOrderID(bad); !errors.Is(err, ErrInvalidOrderID) {
			t.Errorf("%q: want ErrInvalidOrderID, got %v", bad, err)
		}
	}
}

func TestLookupVerification(t *testing.T) {
	s := loadReal(t)

	if _, err := s.Lookup("SS-10003", "LINA.WONG@example.com "); err != nil {
		t.Fatalf("valid lookup failed: %v", err)
	}

	// Wrong email and unknown order must be indistinguishable.
	_, errWrongEmail := s.Lookup("SS-10003", "ravi.k@example.com")
	_, errUnknown := s.Lookup("SS-99999", "lina.wong@example.com")
	_, errEmpty := s.Lookup("SS-10003", "")
	for name, err := range map[string]error{"wrong email": errWrongEmail, "unknown": errUnknown, "empty email": errEmpty} {
		if !errors.Is(err, ErrNotVerified) {
			t.Errorf("%s: want ErrNotVerified, got %v", name, err)
		}
	}
	if errWrongEmail == nil || errUnknown == nil {
		t.Fatal("verification bypassed; cannot compare error messages")
	}
	if errWrongEmail.Error() != errUnknown.Error() {
		t.Error("wrong-email and unknown-order errors must have identical messages")
	}
}

// The View is what reaches the model. Serialise it and make sure nothing
// sensitive or customer-authored leaks through, including the injection
// payload stored in SS-10007's gift message.
func TestViewDoesNotLeak(t *testing.T) {
	s := loadReal(t)
	for _, c := range []struct{ id, email string }{
		{"SS-10004", "daniel.lim@example.com"},
		{"SS-10007", "chloe.ng@example.com"},
	} {
		v, err := s.Lookup(c.id, c.email)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		js := strings.ToLower(string(b))
		for _, forbidden := range []string{
			"@example.com", "ignore all previous", "refund", "comfort guarantee",
			"clementi", "marine parade", "120200", "449290", "internal",
		} {
			if strings.Contains(js, forbidden) {
				t.Errorf("%s: view leaks %q: %s", c.id, forbidden, js)
			}
		}
	}
}

func TestDaysSinceDelivery(t *testing.T) {
	sgt := time.FixedZone("SGT", 8*3600)
	delivered := time.Date(2026, 9, 17, 23, 30, 0, 0, sgt)
	v := View{DeliveredAt: &delivered}

	cases := []struct {
		now  time.Time
		want int
	}{
		{time.Date(2026, 9, 17, 23, 59, 0, 0, sgt), 0},
		{time.Date(2026, 9, 18, 0, 1, 0, 0, sgt), 1},
		{time.Date(2026, 10, 17, 12, 0, 0, 0, sgt), 30},
		// now given in UTC: 2026-10-17 17:00 UTC is already 18 Oct in SGT.
		{time.Date(2026, 10, 17, 17, 0, 0, 0, time.UTC), 31},
	}
	for _, c := range cases {
		got, ok := v.DaysSinceDelivery(c.now)
		if !ok || got != c.want {
			t.Errorf("now=%v: got %d,%v want %d", c.now, got, ok, c.want)
		}
	}

	if _, ok := (View{}).DaysSinceDelivery(time.Now()); ok {
		t.Error("undelivered order should return ok=false")
	}
}

func TestStoreValidation(t *testing.T) {
	good := Order{ID: "SS-00001", Email: "a@b.c", Status: StatusProcessing}
	bad := []Order{
		{ID: "BAD", Email: "a@b.c", Status: StatusProcessing},
		{ID: "SS-00002", Email: "a@b.c", Status: "lost"},
		{ID: "SS-00003", Email: " ", Status: StatusProcessing},
		{ID: "SS-00004", Email: "a@b.c", Status: StatusDelivered},
	}
	for _, o := range bad {
		if _, err := NewStore([]Order{good, o}); err == nil {
			t.Errorf("expected validation error for %+v", o)
		}
	}
	if _, err := NewStore([]Order{good, good}); err == nil {
		t.Error("expected duplicate id error")
	}
}

func TestFinalSale(t *testing.T) {
	s := loadReal(t)
	v, _ := s.Lookup("SS-10005", "aisha.r@example.com")
	if !v.HasFinalSaleItems() {
		t.Error("SS-10005 contains a final sale item")
	}
	v, _ = s.Lookup("SS-10003", "lina.wong@example.com")
	if v.HasFinalSaleItems() {
		t.Error("SS-10003 has no final sale items")
	}
}
