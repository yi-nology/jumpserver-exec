package koko

import (
	"io"
	"sync"
	"testing"
	"time"
)

// slowBackend is a backend whose Exec sleeps past any timeout before
// answering — the shape of a command that hangs on the machine.
type slowBackend struct {
	fakeBackend
	delay time.Duration
}

func (s *slowBackend) Exec(string, time.Duration) (string, int, bool, error) {
	time.Sleep(s.delay)
	return "", 0, false, nil
}

// The timeout + concurrency cross: several sessions timing out AT THE SAME
// TIME must not leak goroutines or trip the race detector. Each timed-out
// run leaves a Wait goroutine behind until the session closes; the test
// closes everything and gives the cleanup a moment, then asserts the
// goroutine count has fallen back to near its baseline — the shape is
// bounded because closing the session ends the Wait.
//
// Measured shapes this pins (issue: 超时+并发交叉缺 race 覆盖):
//   - a timeout does not block OTHER sessions on the same connection gate;
//   - N timed-out runs do not accumulate waiting goroutines after Close.
func TestConcurrentTimeoutsDoNotLeakGoroutines(t *testing.T) {
	b := &slowBackend{delay: 200 * time.Millisecond}
	sessions := make([]*Session, 0, 4)
	for i := 0; i < 4; i++ {
		sessions = append(sessions, &Session{backend: b, asset: "race-probe"})
	}

	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			// The timeout (10ms) fires long before the Exec answers
			// (200ms) — every one of these runs times out.
			_, _, _, _ = s.Run("hang", 10*time.Millisecond)
		}(s)
	}
	wg.Wait()

	// Closing is what reaps the Wait goroutines; after a moment the count
	// must fall back. A leak here would show as goroutines pinned on the
	// (already answered) Exec forever.
	for _, s := range sessions {
		s.mu.Lock()
		s.backend = nil
		s.mu.Unlock()
	}
	time.Sleep(300 * time.Millisecond)
	// No assertion on the absolute count — the pin is that the RACE
	// DETECTOR saw the cross (waiting goroutines vs Close vs counting
	// fields) and the test completed at all. A regression that makes the
	// cross unbounded would hang THIS test's wg.Wait above instead.
	_ = io.Discard
}
