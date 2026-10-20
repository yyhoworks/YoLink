package main

import (
	"context"
	"sync"
)

type playbackPriorityKey struct{}

type accountWaiter struct {
	ready   chan struct{}
	high    bool
	granted bool
}

// accountGate protects account credentials as well as requests. A scan holds
// it for one page, not an entire directory. Four foreground grants at most may
// overtake an already-waiting scan, keeping background work from starving.
type accountGate struct {
	mu         sync.Mutex
	busy       bool
	foreground int
	waiters    []*accountWaiter
}

func (g *accountGate) acquire(ctx context.Context, high bool) error {
	g.mu.Lock()
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return err
	}
	if !g.busy {
		g.busy = true
		g.mu.Unlock()
		return nil
	}
	w := &accountWaiter{ready: make(chan struct{}), high: high}
	g.waiters = append(g.waiters, w)
	g.mu.Unlock()
	select {
	case <-w.ready:
		if err := ctx.Err(); err != nil {
			g.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		if w.granted {
			// The lease was handed to this waiter concurrently with cancellation.
			g.releaseLocked()
		} else {
			for i, pending := range g.waiters {
				if pending == w {
					g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
					break
				}
			}
		}
		g.mu.Unlock()
		return ctx.Err()
	}
}

// Lock/Unlock preserve the existing mutation guards. Provider calls use
// acquire directly so cancellation while queued is observed promptly.
func (g *accountGate) Lock() { _ = g.acquire(context.Background(), true) }
func (g *accountGate) Unlock() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releaseLocked()
}

func (g *accountGate) releaseLocked() {
	if !g.busy {
		panic("unlock of unheld account gate")
	}
	if len(g.waiters) == 0 {
		g.busy = false
		g.foreground = 0
		return
	}
	index, high, low := 0, -1, -1
	for i, w := range g.waiters {
		if w.high && high < 0 {
			high = i
		}
		if !w.high && low < 0 {
			low = i
		}
	}
	if high >= 0 && (low < 0 || g.foreground < 4) {
		index = high
		g.foreground++
	} else if low >= 0 {
		index = low
		g.foreground = 0
	}
	w := g.waiters[index]
	g.waiters = append(g.waiters[:index], g.waiters[index+1:]...)
	w.granted = true
	close(w.ready)
}
