package koko

import (
	"testing"
	"time"
)

// The cost of a session is measured, not assumed, so the estimate a caller
// gets before a sweep is grounded in what this bastion actually did.
//
// The samples are process-wide, so every test here starts from a clean log:
// a measurement left behind by another test would be a measurement of
// nothing.

func resetCosts() {
	walkCost = &costLog{}
	pooledCost = &costLog{}
	commandCost = &costLog{}
}

func TestMeasuredCostFallsBackToTheDefaults(t *testing.T) {
	resetCosts()

	c := MeasuredCost(BackendToken)
	if c.WalkMeasured {
		t.Error("the walk cost claims to be measured when nothing was measured")
	}
	if c.CommandMeasured {
		t.Error("the command cost claims to be measured when nothing was measured")
	}
	// The default follows the backend: the two differ eightfold, and a
	// plan that quoted the terminal's price for a connection-token
	// session would be wrong by that factor.
	if c.WalkSeconds != DefaultTokenWalkSeconds {
		t.Errorf("walk = %v, want the token default %v", c.WalkSeconds, DefaultTokenWalkSeconds)
	}
	if tui := MeasuredCost(BackendTUI); tui.WalkSeconds != DefaultWalkSeconds {
		t.Errorf("the TUI walk = %v, want its default %v", tui.WalkSeconds, DefaultWalkSeconds)
	}
	if c.CommandSeconds != DefaultCommandSeconds {
		t.Errorf("command = %v, want the default %v", c.CommandSeconds, DefaultCommandSeconds)
	}
}

func TestMeasuredCostReportsTheMedian(t *testing.T) {
	resetCosts()

	recordWalk(7*time.Second, tagToken)
	recordWalk(9*time.Second, tagToken)
	recordWalk(8*time.Second, tagToken)
	recordCommand(1500 * time.Millisecond)
	recordCommand(1000 * time.Millisecond)
	recordCommand(2000 * time.Millisecond)

	c := MeasuredCost(BackendToken)
	if !c.WalkMeasured || !c.CommandMeasured {
		t.Fatalf("the measured cost is not marked as measured: %+v", c)
	}
	// The median, not the mean: one slow host must not move the price of
	// the next sweep.
	if c.WalkSeconds != 8 {
		t.Errorf("walk = %v, want the median 8", c.WalkSeconds)
	}
	if c.CommandSeconds != 1.5 {
		t.Errorf("command = %v, want the median 1.5", c.CommandSeconds)
	}
	if c.WalkSamples != 3 || c.CommandSamples != 3 {
		t.Errorf("sample counts = %d/%d, want 3/3", c.WalkSamples, c.CommandSamples)
	}
}

// An even number of samples still has to produce a stable middle, and it
// must not be the mean of the two middle ones: the plan wants the sample
// that was actually observed.
func TestMedianOfAnEvenNumberOfSamples(t *testing.T) {
	resetCosts()
	for _, d := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 10 * time.Second} {
		recordWalk(d, tagToken)
	}
	c := MeasuredCost(BackendToken)
	if c.WalkSeconds != 3 {
		t.Errorf("walk = %v, want 3 (the upper middle sample)", c.WalkSeconds)
	}
}

// The log is bounded: a long-running server must not grow its cost history
// without end.
func TestCostLogIsBounded(t *testing.T) {
	resetCosts()
	for i := 0; i < costSamples*3; i++ {
		recordWalk(time.Second, tagToken)
	}
	if n := walkCost.count(tagToken); n != costSamples {
		t.Errorf("the walk log holds %d samples, want %d", n, costSamples)
	}
}

// A failed walk is not a sample: it says nothing about what a session costs.
func TestRecordIgnoresNonsense(t *testing.T) {
	resetCosts()
	recordWalk(0*time.Second, tagToken)
	recordCommand(-time.Second)
	c := MeasuredCost(BackendToken)
	if c.WalkMeasured || c.CommandMeasured {
		t.Errorf("a zero-length measurement was taken: %+v", c)
	}
}

// The two opening costs are separate numbers, and they must stay separate:
// pricing a sweep with the pooled number understates it, pricing it with
// the cold number overstates it.
func TestThePooledCostIsSeparateFromTheColdOne(t *testing.T) {
	resetCosts()
	recordWalk(1*time.Second, tagToken)
	recordPooled(10*time.Millisecond, tagToken)

	c := MeasuredCost(BackendToken)
	if c.WalkSeconds != 1 {
		t.Errorf("walk = %v, want the cold cost 1", c.WalkSeconds)
	}
	if c.PooledSeconds != 0.01 {
		t.Errorf("pooled = %v, want the reuse cost 0.01", c.PooledSeconds)
	}
	if !c.WalkMeasured || !c.PooledMeasured {
		t.Errorf("the two costs are not marked as measured: %+v", c)
	}
}

// The two backends cost an order of magnitude apart, so a median that mixed
// them would report a number belonging to neither.
func TestMeasuredCostPicksTheBackendYouAskFor(t *testing.T) {
	resetCosts()
	recordWalk(1*time.Second, tagToken)
	recordWalk(8*time.Second, tagTUI)

	if c := MeasuredCost(BackendToken); c.WalkSeconds != 1 {
		t.Errorf("the token backend's walk = %v, want 1", c.WalkSeconds)
	}
	if c := MeasuredCost(BackendTUI); c.WalkSeconds != 8 {
		t.Errorf("the TUI backend's walk = %v, want 8", c.WalkSeconds)
	}
}

// "auto" means the connection token is preferred, so that is where the
// numbers come from.
func TestMeasuredCostPrefersTheTokenPath(t *testing.T) {
	resetCosts()
	recordWalk(1*time.Second, tagToken)
	recordWalk(8*time.Second, tagTUI)

	if c := MeasuredCost(BackendAuto); c.WalkSeconds != 1 {
		t.Errorf("the auto backend's walk = %v, want the token's 1", c.WalkSeconds)
	}
}
