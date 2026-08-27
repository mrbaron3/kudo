package controller

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeClock は実時間の sleep なしに scheduler と backoff を進める。
//
// 待機の登録を counter で観測できるようにしているのは、「時刻を進める前に相手が
// 待機に入っている」ことを test 側から確定させるためである。sleep で待つと、
// 実装の内部順序が変わるたびに flaky になる。
type fakeClock struct {
	mu        sync.Mutex
	now       time.Time
	waiters   []*fakeWaiter
	waits     int
	requested []time.Duration
	changed   chan struct{}
}

type fakeWaiter struct {
	deadline time.Time
	signal   chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:     time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
		changed: make(chan struct{}),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiter := &fakeWaiter{deadline: c.now.Add(d), signal: make(chan time.Time, 1)}
	if d <= 0 {
		waiter.signal <- c.now
	} else {
		c.waiters = append(c.waiters, waiter)
	}
	c.waits++
	c.requested = append(c.requested, d)
	c.broadcastLocked()
	return waiter.signal
}

// Advance は時刻を進め、期限に達した待機を起こす。
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	remaining := c.waiters[:0]
	for _, waiter := range c.waiters {
		if !waiter.deadline.After(c.now) {
			waiter.signal <- c.now
			continue
		}
		remaining = append(remaining, waiter)
	}
	c.waiters = remaining
	c.broadcastLocked()
}

func (c *fakeClock) broadcastLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// awaitWaits は After の呼び出しが n 回に達するまで戻らない。
func (c *fakeClock) awaitWaits(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		c.mu.Lock()
		reached := c.waits >= n
		changed := c.changed
		c.mu.Unlock()
		if reached {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("待機の登録が %d 回に達しなかった", n)
		}
	}
}

// pendingWaits は After の呼び出し回数を返す。
func (c *fakeClock) pendingWaits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waits
}

// requestedDelays は After へ渡された待機時間を呼び出し順で返す。
func (c *fakeClock) requestedDelays() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.requested)
}

// autoAdvance は登録された待機を順に起こし続ける。scheduler の間隔そのものを検証しない
// test（capacity 再投入や backlog の検証）で、実時間を待たずに進めるために使う。
func (c *fakeClock) autoAdvance(ctx context.Context, step time.Duration) {
	go func() {
		seen := 0
		for {
			c.mu.Lock()
			current, changed := c.waits, c.changed
			c.mu.Unlock()
			if current > seen {
				seen = current
				c.Advance(step)
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
		}
	}()
}
