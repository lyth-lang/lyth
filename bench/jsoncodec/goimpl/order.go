// Package goimpl is the hand-written Go version of the jsoncodec workload.
package goimpl

import (
	"encoding/json"
	"fmt"
)

type Item struct {
	SKU        string `json:"sku"`
	Quantity   int    `json:"quantity"`
	PriceCents int64  `json:"priceCents"`
}

type Customer struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Email *string `json:"email"`
	VIP   bool    `json:"vip"`
}

type Order struct {
	ID       int64    `json:"id"`
	Customer Customer `json:"customer"`
	Items    []Item   `json:"items"`
	Note     *string  `json:"note"`
	Tags     []string `json:"tags"`
}

// Validate checks business rules.
func (o Order) Validate() error {
	if len(o.Items) == 0 {
		return fmt.Errorf("order %d: no items", o.ID)
	}
	for _, it := range o.Items {
		if it.Quantity <= 0 {
			return fmt.Errorf("order %d: bad quantity for %s", o.ID, it.SKU)
		}
	}
	return nil
}

// Total returns the order total in cents.
func (o Order) Total() int64 {
	var t int64
	for _, it := range o.Items {
		t += int64(it.Quantity) * it.PriceCents
	}
	return t
}

// Decode parses and validates an order.
func Decode(data []byte) (Order, error) {
	var o Order
	if err := json.Unmarshal(data, &o); err != nil {
		return Order{}, err
	}
	if err := o.Validate(); err != nil {
		return Order{}, err
	}
	return o, nil
}

// DecodeTotal decodes an order and returns its total.
func DecodeTotal(data []byte) (int64, error) {
	o, err := Decode(data)
	if err != nil {
		return 0, err
	}
	return o.Total(), nil
}

// RoundTrip decodes, validates, and re-encodes an order.
func RoundTrip(data []byte) ([]byte, error) {
	o, err := Decode(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(o)
}
