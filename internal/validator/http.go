package validator

import (
	"context"
	"net/http"
	"sync"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
)

// MaxBatch is the synchronous batch limit; larger lists go through
// POST /v1/contacts/validate (background job).
const MaxBatch = 100

func (v *Validator) Routes(mux *http.ServeMux, authed httpx.Middleware) {
	mux.Handle("POST /v1/validations", authed(http.HandlerFunc(v.handleSingle)))
	mux.Handle("POST /v1/validations/batch", authed(http.HandlerFunc(v.handleBatch)))
}

func (v *Validator) handleSingle(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v.Validate(r.Context(), in.Email))
}

func (v *Validator) handleBatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Emails []string `json:"emails"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(in.Emails) == 0 || len(in.Emails) > MaxBatch {
		httpx.Error(w, r, apperr.Invalid("batch_size", "emails must contain 1..100 addresses"))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": v.ValidateMany(r.Context(), in.Emails, 10)})
}

// ValidateMany validates concurrently with bounded parallelism, preserving order.
func (v *Validator) ValidateMany(ctx context.Context, emails []string, parallel int) []Result {
	out := make([]Result, len(emails))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, e := range emails {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			out[i] = v.Validate(ctx, e)
		}()
	}
	wg.Wait()
	return out
}
