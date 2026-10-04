// Package orders is the only path to order data.
//
// Design rules enforced here, not in the prompt:
//   - An order is returned only when order ID AND email match (verification).
//   - "Not found" and "email mismatch" return the same error, so the chat
//     cannot be used to enumerate valid order IDs.
//   - The model receives a View, never the raw Order: no email, no full
//     address, no internal notes, and no customer-written free text
//     (gift messages are a prompt-injection vector).
//   - Date arithmetic (days since delivery) is computed here, not by the LLM.
package orders

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

type Status string

const (
	StatusProcessing Status = "processing"
	StatusShipped    Status = "shipped"
	StatusDelivered  Status = "delivered"
	StatusCancelled  Status = "cancelled"
)

func (s Status) valid() bool {
	switch s {
	case StatusProcessing, StatusShipped, StatusDelivered, StatusCancelled:
		return true
	}
	return false
}

type Item struct {
	SKU       string  `json:"sku"`
	Name      string  `json:"name"`
	Size      string  `json:"size"`
	Qty       int     `json:"qty"`
	UnitPrice float64 `json:"unit_price"`
	FinalSale bool    `json:"final_sale"`
}

type Address struct {
	Line1   string `json:"line1"`
	City    string `json:"city"`
	Postal  string `json:"postal"`
	Country string `json:"country"`
}

// Order is the raw record. It must never be serialised into a prompt.
type Order struct {
	ID             string     `json:"id"`
	Email          string     `json:"email"`
	CustomerName   string     `json:"customer_name"`
	Status         Status     `json:"status"`
	PlacedAt       time.Time  `json:"placed_at"`
	ShippedAt      *time.Time `json:"shipped_at,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	Carrier        string     `json:"carrier,omitempty"`
	TrackingNumber string     `json:"tracking_number,omitempty"`
	Items          []Item     `json:"items"`
	Total          float64    `json:"total"`
	Currency       string     `json:"currency"`
	ShipTo         Address    `json:"ship_to"`
	GiftMessage    string     `json:"gift_message,omitempty"`
	InternalNote   string     `json:"internal_note,omitempty"`
}

// View is the model-safe projection of an Order.
type View struct {
	ID                 string     `json:"id"`
	Status             Status     `json:"status"`
	FirstName          string     `json:"first_name"`
	PlacedAt           time.Time  `json:"placed_at"`
	ShippedAt          *time.Time `json:"shipped_at,omitempty"`
	DeliveredAt        *time.Time `json:"delivered_at,omitempty"`
	Carrier            string     `json:"carrier,omitempty"`
	TrackingNumber     string     `json:"tracking_number,omitempty"`
	Items              []Item     `json:"items"`
	Total              float64    `json:"total"`
	Currency           string     `json:"currency"`
	ShipToCity         string     `json:"ship_to_city"`
	ShipToCountry      string     `json:"ship_to_country"`
	ShipToPostalMasked string     `json:"ship_to_postal_masked"`
}

var (
	ErrInvalidOrderID = errors.New("order id format is invalid")
	// ErrNotVerified covers both "no such order" and "email does not match".
	ErrNotVerified = errors.New("order could not be verified")
)

var orderIDRe = regexp.MustCompile(`^SS-\d{5}$`)

// NormalizeOrderID accepts common user spellings ("ss10003", " SS-10003 ").
func NormalizeOrderID(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	if strings.HasPrefix(s, "SS") && !strings.HasPrefix(s, "SS-") {
		s = "SS-" + s[2:]
	}
	if !orderIDRe.MatchString(s) {
		return "", ErrInvalidOrderID
	}
	return s, nil
}

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

type Store struct {
	byID map[string]Order
}

func LoadFile(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []Order
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("orders: parse %s: %w", path, err)
	}
	return NewStore(list)
}

func NewStore(list []Order) (*Store, error) {
	s := &Store{byID: make(map[string]Order, len(list))}
	for _, o := range list {
		if !orderIDRe.MatchString(o.ID) {
			return nil, fmt.Errorf("orders: bad id %q", o.ID)
		}
		if !o.Status.valid() {
			return nil, fmt.Errorf("orders: %s: bad status %q", o.ID, o.Status)
		}
		if normalizeEmail(o.Email) == "" {
			return nil, fmt.Errorf("orders: %s: missing email", o.ID)
		}
		if o.Status == StatusDelivered && o.DeliveredAt == nil {
			return nil, fmt.Errorf("orders: %s: delivered without delivered_at", o.ID)
		}
		if _, dup := s.byID[o.ID]; dup {
			return nil, fmt.Errorf("orders: duplicate id %s", o.ID)
		}
		s.byID[o.ID] = o
	}
	return s, nil
}

// Lookup returns the model-safe view only when the order ID and email match.
func (s *Store) Lookup(orderID, email string) (View, error) {
	id, err := NormalizeOrderID(orderID)
	if err != nil {
		return View{}, err
	}
	o, ok := s.byID[id]
	e := normalizeEmail(email)
	if !ok || e == "" || normalizeEmail(o.Email) != e {
		return View{}, ErrNotVerified
	}
	return toView(o), nil
}

func toView(o Order) View {
	first := o.CustomerName
	if i := strings.IndexByte(first, ' '); i > 0 {
		first = first[:i]
	}
	items := make([]Item, len(o.Items))
	copy(items, o.Items)
	return View{
		ID:                 o.ID,
		Status:             o.Status,
		FirstName:          first,
		PlacedAt:           o.PlacedAt,
		ShippedAt:          o.ShippedAt,
		DeliveredAt:        o.DeliveredAt,
		Carrier:            o.Carrier,
		TrackingNumber:     o.TrackingNumber,
		Items:              items,
		Total:              o.Total,
		Currency:           o.Currency,
		ShipToCity:         o.ShipTo.City,
		ShipToCountry:      o.ShipTo.Country,
		ShipToPostalMasked: maskPostal(o.ShipTo.Postal),
	}
}

func maskPostal(p string) string {
	if len(p) <= 2 {
		return strings.Repeat("*", len(p))
	}
	return strings.Repeat("*", len(p)-2) + p[len(p)-2:]
}

// DaysSinceDelivery returns whole calendar days between delivery and now,
// in the delivery's own timezone. ok is false when not delivered.
func (v View) DaysSinceDelivery(now time.Time) (days int, ok bool) {
	if v.DeliveredAt == nil {
		return 0, false
	}
	loc := v.DeliveredAt.Location()
	d := v.DeliveredAt.In(loc)
	n := now.In(loc)
	start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	end := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	return int(end.Sub(start).Hours() / 24), true
}

func (v View) HasFinalSaleItems() bool {
	for _, it := range v.Items {
		if it.FinalSale {
			return true
		}
	}
	return false
}
