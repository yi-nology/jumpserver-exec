package koko

// The delivery state of an action: did it reach the machine it was aimed
// at?
//
// This is the one fact a caller cannot work out for itself after a failure,
// and the one that decides whether running the command again is allowed. A
// command that was delivered may still be running on the machine, and
// running it again is how `rm -rf` happens twice.
//
// The state travels AS PART OF THE ERROR, because that is where it is
// known: the layer that sent the command is the layer that knows whether
// the send went through. The base declares the shape it reads (see
// reg.DeliveryAware), so the two sides do not have to know each other —
// and the trail can answer "did it get there?" without parsing prose.

import (
	"github.com/yi-nology/jumpserver-kit/model/jms"
)

// deliveryError is an error that knows how far the action got.
type deliveryError struct {
	text     string
	delivery jms.Delivery
	cause    error
}

func (e *deliveryError) Error() string { return e.text }

// Delivery is the state this error carries.
func (e *deliveryError) Delivery() jms.Delivery { return e.delivery }

// Unwrap keeps the original error in the chain: the state is added to a
// failure, it does not replace it.
func (e *deliveryError) Unwrap() error { return e.cause }

// notSent marks a failure as "this never left this process".
//
// The wording is kept as it was: the sentence an operator reads to decide
// whether running it again is safe.
func notSent(err error) error {
	return &deliveryError{
		text:     err.Error(),
		delivery: jms.Delivery_not_sent,
		cause:    err,
	}
}

// maybeSent marks a failure as "it may already be running".
func maybeSent(err error) error {
	return &deliveryError{
		text:     err.Error(),
		delivery: jms.Delivery_maybe_sent,
		cause:    err,
	}
}
