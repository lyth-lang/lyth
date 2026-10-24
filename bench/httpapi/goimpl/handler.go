// Package goimpl is the hand-written Go version of the httpapi workload.
package goimpl

import (
	"encoding/json"
	"net/http"

	"lythbench/jsoncodec/goimpl"
)

type orderResponse struct {
	ID         int64 `json:"id"`
	TotalCents int64 `json:"totalCents"`
	Items      int   `json:"items"`
}

// HandleOrder accepts a JSON order and responds with its total.
func HandleOrder(w http.ResponseWriter, r *http.Request) {
	var o goimpl.Order
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := o.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(orderResponse{ID: o.ID, TotalCents: o.Total(), Items: len(o.Items)})
}
