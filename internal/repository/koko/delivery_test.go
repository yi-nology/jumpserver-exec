package koko

import (
	"errors"
	"fmt"
	"testing"

	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
)

// The delivery state is the one fact a caller cannot work out for itself
// after a failure — and the one that decides whether running the command
// again is allowed. It has to survive the wrapping the errors go through,
// or it is lost exactly where it is needed.
func TestTheDeliveryStateTravelsWithTheError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want jms.Delivery
	}{
		{"the command never left", errNotDelivered, jms.Delivery_not_sent},
		{"the reply was lost", errPossiblyDelivered, jms.Delivery_maybe_sent},
		{"the command timed out", ErrCommandTimeout, jms.Delivery_maybe_sent},
		{
			"wrapped the way the layer wraps it",
			fmt.Errorf("%w: open an exec channel on node-071: %v", errNotDelivered, errors.New("boom")),
			jms.Delivery_not_sent,
		},
		{
			"wrapped twice",
			fmt.Errorf("%w (reopening the connection failed too: %v)", errPossiblyDelivered, errors.New("boom")),
			jms.Delivery_maybe_sent,
		},
		{
			"an error nobody classified",
			errors.New("the bastion said no"),
			jms.Delivery(0),
		},
		{
			"no error at all",
			nil,
			jms.Delivery(0),
		},
		{
			"a failure marked as never sent",
			notSent(errors.New("the connection to node-071 is busy")),
			jms.Delivery_not_sent,
		},
		{
			"a failure marked as maybe sent",
			maybeSent(errors.New("the command did not finish within 60s")),
			jms.Delivery_maybe_sent,
		},
	}
	for _, c := range cases {
		if got := reg.DeliveryOf(c.err); got != c.want {
			t.Errorf("%s: the delivery state is %v, want %v", c.name, got, c.want)
		}
	}
}

// Marking a failure must not swallow it: the text stays and the original
// error stays in the chain.
func TestMarkingAFailureKeepsIt(t *testing.T) {
	inner := errors.New("the connection to node-071 is busy")
	marked := notSent(inner)
	if !errors.Is(marked, inner) {
		t.Error("the marked error lost the original in its chain")
	}
	if marked.Error() != inner.Error() {
		t.Errorf("the marked error says %q, want %q", marked.Error(), inner.Error())
	}
}

// A session that never opened means the command never left this process.
//
// That is the answer to "did it reach the machine?" for the most common
// sweep failure (a host that cannot be reached at all), and it must not be
// a guess: the session is what carries the commands.
func TestASessionThatNeverOpenedNeverSentAnything(t *testing.T) {
	inner := errors.New("no such asset: \"no-such-host-xyz\"")
	marked := notSent(inner)
	if got := reg.DeliveryOf(marked); got != jms.Delivery_not_sent {
		t.Errorf("the delivery state is %v, want not_sent", got)
	}
	if !errors.Is(marked, inner) {
		t.Error("the marked error lost the original in its chain")
	}
	if notSent(nil) != nil {
		t.Error("marking no error produced an error")
	}
}

// The wording is the sentence an operator reads to decide whether running
// the command again is safe — so it has to say it.
func TestTheDeliveryStateSaysItInWords(t *testing.T) {
	if got := errNotDelivered.Error(); got == "" {
		t.Error("the not-delivered error says nothing")
	}
	if got := ErrCommandTimeout.Error(); got == "" {
		t.Error("the timeout error says nothing")
	}
}
