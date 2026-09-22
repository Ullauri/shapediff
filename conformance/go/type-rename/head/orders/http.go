package orders

import (
	"net/http"
	"time"
)

// Handler serves orders over HTTP.
type Handler struct {
	svc *OrderService
}

// NewHandler returns a Handler.
func NewHandler(svc *OrderService) *Handler {
	return &Handler{svc: svc}
}

// Create handles POST /orders.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if err := h.svc.Create(r.Context(), name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// Timeout is how long a create may take.
func Timeout() time.Duration {
	return 5 * time.Second
}
