package handler

// What a sweep will cost, before it runs.
//
// The rehearsal question has two halves: "is this safe" and "what will it
// cost me". The second half has a shape -- twenty machines, five rounds,
// about a minute -- and answering it with numbers is worth the trouble,
// because the difference between concurrency 4 and 8 is the difference
// between waiting and not.
//
// The numbers behind the answer are measured: koko.MeasuredCost reports
// what the TUI walk and the commands actually cost on this bastion. Until
// something has been measured, the documented defaults stand in and the
// answer says so, rather than pretending to a precision it does not have.
//
// Nothing here opens a session. A plan that cost a session to produce would
// be a session the caller did not ask for, on a machine they did not pick.

import (
	"context"
	"fmt"
	"github.com/oekit/pkg/mathx"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"time"

	"github.com/yi-nology/jumpserver-exec/biz/model/jmsexec"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
)

const (
	// defaultConcurrency is how many hosts a sweep runs at once when the
	// caller does not say.
	defaultConcurrency = 4

	// maxConcurrency is the ceiling: every host gets its own recorded
	// session, and the bastion should not have twenty of them at once.
	maxConcurrency = 8

	// defaultTimeoutSeconds is the per-command timeout a sweep uses when
	// the caller does not say.
	defaultTimeoutSeconds = 60
)

// effectiveConcurrency is the concurrency a sweep will actually run with.
//
// The clamping is the same in the plan and in the run: a plan that used a
// different number than the sweep would be a lie.
func effectiveConcurrency(want, hosts int) int {
	c := want
	if c <= 0 {
		c = defaultConcurrency
	}
	if c > maxConcurrency {
		c = maxConcurrency
	}
	if hosts > 0 && c > hosts {
		c = hosts
	}
	return c
}

// effectiveTimeout is the per-command timeout a sweep will actually use.
func effectiveTimeout(want int) int {
	if want <= 0 {
		return defaultTimeoutSeconds
	}
	return want
}

// commandTimeout is the per-command timeout a single-machine tool will
// actually use: the default when the caller did not say.
//
// Unlike effectiveTimeout it answers in a duration and carries no sweep
// semantics — it is the same idea at the one-command scale, and the two
// spellings used to be hand-rolled at every call site, drifting in their
// defaults before they were collected here.
func commandTimeout(want, def int) time.Duration {
	if want <= 0 {
		return time.Duration(def) * time.Second
	}
	return time.Duration(want) * time.Second
}

// planSweep prices a sweep of `hosts` machines, each running
// `commandsPerHost` commands.
//
// rounds is how many waves of hosts the sweep takes: twenty hosts at
// concurrency 4 is five waves, because every wave opens one session per
// host and pays the walk.
//
// Three time figures come out of it, and they mean different things:
//
//	min_seconds    the floor -- the walks and nothing else
//	estimate_seconds the walks plus the commands at their measured cost
//	max_seconds    every command of every host running into its timeout
//
// The floor and the ceiling are bounds, not predictions. The estimate is the
// number to plan against, and it is only as good as the walk and command
// costs behind it -- which is why the answer says where they came from.
func planSweep(ctx context.Context, cfg config.Config, assets []string, commandsPerHost, concurrency, timeout int) *jmsexec.SweepPlan {
	hosts := len(assets)
	eff := effectiveConcurrency(concurrency, hosts)
	to := effectiveTimeout(timeout)
	cost := koko.MeasuredCost(cfg.Koko.Backend)

	rounds := 0
	if hosts > 0 && eff > 0 {
		rounds = (hosts + eff - 1) / eff
	}

	// Which of these hosts already have a connection? A machine this
	// process has already talked to is opened from the pool, and one it
	// has not is opened from scratch — a new token and a new handshake. A
	// plan that quoted the same price for both would be wrong for one of
	// them, and a repeat sweep is exactly the case where it matters.
	pooled := make([]bool, hosts)
	pooledCount := 0
	for i, a := range assets {
		if cfg.Koko.Pool != nil && cfg.Koko.Pool.Holds(ctx, cfg.Koko, a, "") {
			pooled[i] = true
			pooledCount++
		}
	}

	// The sweep runs in waves of `eff` hosts, and the sessions of one wave
	// open in PARALLEL. So a wave's opening costs the most expensive open
	// in it, not the sum: one cold host in the wave makes the whole wave
	// cost a cold open.
	estimate, floor, ceiling := sweepTiming(assets, pooled, commandsPerHost, eff, to, cost)

	// seconds_per_host is what ONE WAVE costs — the walk plus that wave's
	// commands. The hosts of a wave run in parallel, so this is the time
	// the wave takes, and the sweep is this many waves. Dividing it by the
	// number of hosts instead would answer a different question ("what is
	// each host's share of the total"), which is not what the number is
	// for.
	perHost := 0.0
	if rounds > 0 {
		perHost = estimate / float64(rounds)
	}

	caveat := "the walk and the commands are measured on this bastion; how long " +
		"your commands actually take is not known until they run, so min_seconds " +
		"is the floor and max_seconds is the ceiling"
	if cfg.Koko.Backend != koko.BackendTUI {
		// The terminal is the fallback, and it is an order of magnitude
		// slower to open. Saying so keeps the estimate honest about the
		// case where the fast path is not there.
		tui := koko.MeasuredCost(koko.BackendTUI)
		caveat += fmt.Sprintf("; if the connection-token path is unavailable, the session falls "+
			"back to the koko terminal, which costs about %s to open instead", humanSeconds(tui.WalkSeconds))
	}

	plan := &jmsexec.SweepPlan{
		Hosts:             jms.OptInt32(int32(hosts)),
		Sessions:          jms.OptInt32(int32(hosts)), // one recorded bastion session per host
		CommandsPerHost:   jms.OptInt32(int32(commandsPerHost)),
		Concurrency:       jms.OptInt32(int32(eff)),
		Rounds:            jms.OptInt32(int32(rounds)),
		SecondsPerHost:    jms.OptFloat64(round1(perHost)),
		EstimateSeconds:   jms.OptFloat64(round1(estimate)),
		MinSeconds:        jms.OptFloat64(round1(floor)),
		MaxSeconds:        jms.OptFloat64(round1(ceiling)),
		Estimate:          jms.Opt(humanSeconds(estimate)),
		WalkSeconds:       jms.OptFloat64(round1(cost.WalkSeconds)),
		CommandSeconds:    jms.OptFloat64(round1(cost.CommandSeconds)),
		PooledHosts:       jms.OptInt32(int32(pooledCount)),
		PooledWalkSeconds: jms.OptFloat64(round1(cost.PooledSeconds)),
		MeasureWith:       jms.Opt("jmsx_session_cost"),
		Basis: jms.Opt(fmt.Sprintf(
			"one session opened per host (~%s for a new connection, ~%s when the connection "+
				"is already open) plus %d command(s) per host (~%s each); "+
				"%d host(s) at concurrency %d is %d round(s); %d of them already have a connection open",
			humanSeconds(cost.WalkSeconds), humanSeconds(cost.PooledSeconds), commandsPerHost,
			humanSeconds(cost.CommandSeconds), hosts, eff, rounds, pooledCount)),
		Caveat: jms.Opt(caveat),
	}

	if cost.WalkMeasured {
		plan.WalkSource = jms.Opt(fmt.Sprintf("measured on this bastion (median of %d session opening(s))", cost.WalkSamples))
	} else {
		plan.WalkSource = jms.Opt("default: this process has not measured a session opening yet")
	}
	if cost.CommandMeasured {
		plan.CommandSource = jms.Opt(fmt.Sprintf("measured on this bastion (median of %d command(s))", cost.CommandSamples))
	} else {
		plan.CommandSource = jms.Opt("default: this process has not measured a command yet")
	}
	if cost.PooledMeasured {
		plan.PooledWalkSource = jms.Opt(fmt.Sprintf("measured on this bastion (median of %d pooled opening(s))", cost.PooledSamples))
	} else {
		plan.PooledWalkSource = jms.Opt("default: this process has not reused a connection yet")
	}

	// The knob is the point of the estimate: if more concurrency makes the
	// sweep cheaper, say what it buys, in the same units.
	//
	// It is priced with the same wave arithmetic, because the waves are
	// composed differently at a higher concurrency — and a wave that
	// happens to hold a cold host costs a cold open whichever wave it is.
	if eff < maxConcurrency && hosts > eff {
		faster := effectiveConcurrency(maxConcurrency, hosts)
		fasterEstimate, _, _ := sweepTiming(assets, pooled, commandsPerHost, faster, to, cost)
		fasterRounds := (hosts + faster - 1) / faster
		plan.AtMaxConcurrency = &jmsexec.ConcurrencyGain{
			Concurrency:     jms.OptInt32(int32(faster)),
			Rounds:          jms.OptInt32(int32(fasterRounds)),
			EstimateSeconds: jms.OptFloat64(round1(fasterEstimate)),
			Estimate:        jms.Opt(humanSeconds(fasterEstimate)),
		}
	}

	return plan
}

// sweepTiming prices a sweep at one concurrency.
//
// It returns three figures and they mean different things:
//
//	estimate the waves, each with its opening and its commands
//	floor    the openings and nothing else
//	ceiling  every command of every wave running into its timeout
//
// The floor and the ceiling are bounds, not predictions. The estimate is the
// number to plan against, and it is only as good as the costs behind it —
// which is why the answer says where they came from.
func sweepTiming(assets []string, pooled []bool, commandsPerHost, concurrency, timeout int, cost koko.Cost) (estimate, floor, ceiling float64) {
	eff := effectiveConcurrency(concurrency, len(assets))
	for start := 0; start < len(assets); start += eff {
		end := start + eff
		if end > len(assets) {
			end = len(assets)
		}
		// A wave's opening costs the most expensive open in it: the opens
		// run in parallel, so one cold host makes the whole wave cost a
		// cold open.
		open := cost.PooledSeconds
		for i := start; i < end; i++ {
			if !pooled[i] {
				open = cost.WalkSeconds
				break
			}
		}
		estimate += open + float64(commandsPerHost)*cost.CommandSeconds
		floor += open
		ceiling += open + float64(commandsPerHost)*float64(timeout)
	}
	return estimate, floor, ceiling
}

// humanSeconds renders a duration the way a person says it out loud.
//
// The sub-second case matters: a pooled session opens in a fraction of a
// second, and rounding that to "0s" makes the sentence say nothing —
// "a new connection costs ~0s and an open one costs ~0s" is not a number.
func humanSeconds(v float64) string {
	d := time.Duration(v * float64(time.Second))
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// round1 trims a number to one decimal, so a report does not carry a
// precision nobody can act on.
func round1(v float64) float64 { return mathx.Round(v, 1) }
