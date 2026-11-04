package koko

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
)

// Reading an asset's accounts can fail for two very different reasons, and
// the class decides what happens next:
//
//   - the API cannot answer (5xx) — a problem with the MECHANISM. The
//     terminal path does not need the API at all, so it is worth the walk.
//   - the asset is not there (404) — a problem with the REQUEST. The
//     terminal would answer exactly the same way, ten seconds later.
//
// Getting this wrong in the first direction costs an outage's worth of
// failed sessions; getting it wrong in the second costs ten seconds on
// every miss.
func TestAReadFailureOfTheAccountsIsClassifiedBeforeItLeaves(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		wantWalkIt bool
	}{
		{"the api is broken", http.StatusInternalServerError, true},
		{"the asset is gone", http.StatusNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeBastionStatus(t, tc.status, `{"detail":"boom"}`)
			c := jmsapi.New(jmsapi.Options{BaseURL: f.URL, Token: "t", HTTPClient: f.Client()})

			_, err := assetAccounts(context.Background(), Config{API: c}, "asset-1")
			if err == nil {
				t.Fatal("a failing read must be an error")
			}
			if got := errors.Is(err, ErrTokenUnavailable); got != tc.wantWalkIt {
				t.Fatalf("errors.Is(err, ErrTokenUnavailable) = %v, want %v (err: %v)",
					got, tc.wantWalkIt, err)
			}
		})
	}
}
