package koko

// What a session costs.
//
// Two numbers decide how long a fleet sweep takes: the cost of opening a
// session and the cost of the commands inside it. Both are measured here
// rather than assumed, because the estimate a caller is given before a
// sweep is only as good as the number behind it.
//
// THREE numbers, not two, because there are three costs:
//
//   - opening a session FROM SCRATCH: a new connection, a new token, a new
//     handshake. Every host in a sweep is a different asset and therefore a
//     different connection, so THIS is what a sweep is priced with.
//   - opening a session on a connection that is already open: a map
//     lookup. This is what a repeat call on the same machine costs.
//   - running a command.
//
// The first two used to be one number, and that was wrong in both
// directions: a plan priced with the pooled cost would say a twenty-host
// sweep takes a second, and a plan priced with the cold cost would say it
// takes a minute when it takes four.
//
// The samples are also TAGGED BY BACKEND, because the two ways of opening a
// session cost an order of magnitude apart — a connection token takes about
// a second, the koko terminal about eight — and a median that mixes them
// would report a number that belongs to neither.

import (
	"github.com/oekit/pkg/mathx"
	"sort"
	"sync"
	"time"
)

const (
	// DefaultWalkSeconds is what a TUI walk costs when this process has
	// not measured one yet. It is the upper end of what was measured on
	// this fleet (7.25s and 9.12s on 2026-10-26), so it is a remembered
	// measurement rather than a guess -- and the pessimistic end of one.
	DefaultWalkSeconds = 9.0

	// DefaultTokenWalkSeconds is what opening a session with a connection
	// token costs when this process has not measured one yet: 239ms to mint
	// the token and 205ms to complete the SSH handshake, measured on this
	// bastion on 2026-10-28. The TUI default is six to seven times that,
	// so quoting the wrong one would misprice a fleet sweep by the same
	// factor.
	DefaultTokenWalkSeconds = 0.5

	// DefaultPooledSeconds is what opening a session on an already-open
	// connection costs when this process has not measured one yet.
	//
	// It is small because it is a map lookup, not a network round trip:
	// measured at 0.26s on 2026-10-28, of which almost all is the token
	// mint and the handshake that the REUSED connection does not pay
	// again.
	DefaultPooledSeconds = 0.05

	// DefaultCommandSeconds is what one ordinary command costs once the
	// shell is open. Measured here: 1.5s. Rounded up, because the slow
	// case is the one a plan has to survive.
	DefaultCommandSeconds = 2.0

	// costSamples is how many recent measurements are kept. Enough for a
	// median to mean something, few enough to stay free.
	costSamples = 16
)

// The backend tags a sample carries.
//
// They are the two ways of opening a session, spelled the way the tools
// spell them.
const (
	tagToken = "token"
	tagTUI   = "tui"
)

// costSample is one measurement.
type costSample struct {
	seconds float64
	tag     string
}

// costLog holds the recent measurements of one kind of event.
type costLog struct {
	mu      sync.Mutex
	samples []costSample
}

// add notes a measurement, dropping the oldest when the log is full.
func (c *costLog) add(v float64, tag string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, costSample{seconds: v, tag: tag})
	if len(c.samples) > costSamples {
		c.samples = c.samples[len(c.samples)-costSamples:]
	}
}

// median returns the middle sample for one tag.
//
// It does NOT fall back to the other tag's samples. The two backends cost an
// order of magnitude apart, so a median that mixed them would report a
// number belonging to neither — and it would report it as MEASURED, with a
// sample count of zero, which is the worst of both: wrong and confident.
func (c *costLog) median(tag string) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	values := make([]float64, 0, len(c.samples))
	for _, s := range c.samples {
		if s.tag == tag {
			values = append(values, s.seconds)
		}
	}
	if len(values) == 0 {
		return 0, false
	}
	sort.Float64s(values)
	return values[len(values)/2], true
}

// count reports how many measurements the log holds for one tag.
func (c *costLog) count(tag string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, s := range c.samples {
		if s.tag == tag {
			n++
		}
	}
	return n
}

var (
	// walkCost is the cost of opening a session FROM SCRATCH. It is the
	// number a fleet sweep is priced with, because every host in a sweep is
	// a different asset and therefore a different connection.
	walkCost = &costLog{}

	// pooledCost is the cost of opening a session on a connection that was
	// already open. It is what a REPEAT call on the same asset costs, and
	// it is what makes a second command on a machine nearly free.
	pooledCost = &costLog{}

	commandCost = &costLog{}
)

// recordWalk notes how long opening a session from scratch took.
func recordWalk(d time.Duration, tag string) {
	if d <= 0 {
		return
	}
	walkCost.add(d.Seconds(), tag)
}

// recordPooled notes how long opening a session on an already-open
// connection took.
func recordPooled(d time.Duration, tag string) {
	if d <= 0 {
		return
	}
	pooledCost.add(d.Seconds(), tag)
}

// recordCommand notes how long one command took.
func recordCommand(d time.Duration) {
	if d <= 0 {
		return
	}
	commandCost.add(d.Seconds(), tagToken)
}

// Cost is what a session costs, as far as it has been measured.
type Cost struct {
	// WalkSeconds is what opening a session FROM SCRATCH costs. This is
	// what a fleet sweep is priced with.
	WalkSeconds float64
	// CommandSeconds is what one command costs once the shell is open.
	CommandSeconds float64
	// PooledSeconds is what opening a session on an already-open
	// connection costs. It is a different number from WalkSeconds, and
	// quoting the wrong one prices a sweep wrongly in both directions.
	PooledSeconds float64

	// WalkMeasured, CommandMeasured and PooledMeasured say where the three
	// numbers came from: measured on this bastion, or the default because
	// nothing has been measured here yet.
	WalkMeasured    bool
	CommandMeasured bool
	PooledMeasured  bool

	// WalkSamples, CommandSamples and PooledSamples are how many
	// measurements the medians were taken from.
	WalkSamples    int
	CommandSamples int
	PooledSamples  int
}

// MeasuredCost returns what a session costs on this bastion.
//
// The backend says which way of opening a session the numbers should come
// from: the two cost an order of magnitude apart, so a caller that has not
// said must be told which one it is getting.
//
// "auto" means the connection token is preferred, so that is where the
// numbers come from; the terminal is the fallback and its numbers are used
// only when nothing has been measured for the preferred path.
func MeasuredCost(backend string) Cost {
	tag := tagToken
	switch backend {
	case BackendTUI:
		tag = tagTUI
	}
	// The default follows the backend, because the two differ eightfold.
	// "auto" prefers the connection token, so that is what its default is:
	// a plan that quoted the terminal's nine seconds for a session that
	// will really take one would be wrong by a factor of nine on the very
	// first question asked.
	defaultWalk := DefaultTokenWalkSeconds
	if tag == tagTUI {
		defaultWalk = DefaultWalkSeconds
	}
	cost := Cost{
		WalkSeconds:    defaultWalk,
		CommandSeconds: DefaultCommandSeconds,
		PooledSeconds:  DefaultPooledSeconds,
	}
	if v, ok := walkCost.median(tag); ok {
		cost.WalkSeconds = v
		cost.WalkMeasured = true
		cost.WalkSamples = walkCost.count(tag)
	}
	if v, ok := commandCost.median(tag); ok {
		cost.CommandSeconds = v
		cost.CommandMeasured = true
		cost.CommandSamples = commandCost.count(tag)
	}
	if v, ok := pooledCost.median(tag); ok {
		cost.PooledSeconds = v
		cost.PooledMeasured = true
		cost.PooledSamples = pooledCost.count(tag)
	}
	return cost
}

// round2 trims a duration to two decimals, so a report does not carry a
// precision nobody can act on.
func round2(v float64) float64 { return mathx.Round(v, 2) }
