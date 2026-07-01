package webrtc

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/datarhei/core/v16/session"
)

// sessionTracker reports byte counters to a session.Collector once a
// second, the same way the client type in the rtmp and srt packages does.
// pion doesn't expose a simple running byte counter the way joy4/gosrt
// connections do, so this accumulates the counts itself as packets are
// forwarded through the relay.
type sessionTracker struct {
	id        string
	collector session.Collector

	rx atomic.Uint64
	tx atomic.Uint64

	cancel context.CancelFunc
}

func newSessionTracker(id string, collector session.Collector) *sessionTracker {
	t := &sessionTracker{
		id:        id,
		collector: collector,
	}

	var ctx context.Context
	ctx, t.cancel = context.WithCancel(context.Background())

	go t.run(ctx)

	return t
}

func (t *sessionTracker) run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastRx, lastTx uint64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rx := t.rx.Load()
			tx := t.tx.Load()

			t.collector.Ingress(t.id, int64(rx-lastRx))
			t.collector.Egress(t.id, int64(tx-lastTx))

			lastRx = rx
			lastTx = tx
		}
	}
}

func (t *sessionTracker) AddRx(n int) {
	t.rx.Add(uint64(n))
}

func (t *sessionTracker) AddTx(n int) {
	t.tx.Add(uint64(n))
}

func (t *sessionTracker) Close() {
	t.cancel()
}
