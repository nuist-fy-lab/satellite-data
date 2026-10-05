package central

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
)

const transferDiagnosticsInterval = 30 * time.Second

type tcpInfoSnapshot struct {
	RTT          time.Duration
	RTTVar       time.Duration
	RTO          time.Duration
	SndCWND      uint32
	SndSsthresh  uint32
	SndWindow    uint32
	RcvWindow    uint32
	Unacked      uint32
	Lost         uint32
	Retrans      uint32
	TotalRetrans uint32
	DeliveryRate uint64
	PacingRate   uint64
	BytesRetrans uint64
}

type transferDiagnostics struct {
	source string
	host   string
	asset  string
	range_ string
	start  time.Time
	bytes  atomic.Int64

	mu     sync.RWMutex
	conn   net.Conn
	reused bool
}

func newTransferDiagnostics(source, host, asset, byteRange string) *transferDiagnostics {
	return &transferDiagnostics{
		source: source,
		host:   host,
		asset:  asset,
		range_: byteRange,
		start:  time.Now(),
	}
}

func (d *transferDiagnostics) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		d.mu.Lock()
		d.conn = info.Conn
		d.reused = info.Reused
		d.mu.Unlock()
	}}
}

func (d *transferDiagnostics) writer(destination io.Writer) io.Writer {
	return diagnosticsWriter{Writer: destination, diagnostics: d}
}

func (d *transferDiagnostics) start(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(transferDiagnosticsInterval)
		defer ticker.Stop()
		lastAt, lastBytes := d.start, int64(0)
		for {
			select {
			case now := <-ticker.C:
				current := d.bytes.Load()
				d.sample(now, current-lastBytes, now.Sub(lastAt))
				lastAt, lastBytes = now, current
			case <-ctx.Done():
				return
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
	}
}

func (d *transferDiagnostics) sample(now time.Time, intervalBytes int64, interval time.Duration) {
	d.mu.RLock()
	conn, reused := d.conn, d.reused
	d.mu.RUnlock()
	if conn == nil || interval <= 0 {
		return
	}
	info, err := readTCPInfo(conn)
	if err != nil {
		slog.Debug("upstream transfer tcp info unavailable",
			"source", d.source, "host", d.host, "asset", d.asset, "error", err)
		return
	}
	remote := ""
	if addr := conn.RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	slog.Info("upstream transfer tcp info",
		"source", d.source,
		"host", d.host,
		"asset", d.asset,
		"range", d.range_,
		"remote", remote,
		"reused", reused,
		"elapsed", now.Sub(d.start).Round(time.Second),
		"bytes", d.bytes.Load(),
		"interval_bps", float64(intervalBytes)/interval.Seconds(),
		"rtt", info.RTT,
		"rttvar", info.RTTVar,
		"rto", info.RTO,
		"snd_cwnd", info.SndCWND,
		"snd_ssthresh", info.SndSsthresh,
		"snd_wnd", info.SndWindow,
		"rcv_wnd", info.RcvWindow,
		"unacked", info.Unacked,
		"lost", info.Lost,
		"retrans", info.Retrans,
		"total_retrans", info.TotalRetrans,
		"delivery_rate", info.DeliveryRate,
		"pacing_rate", info.PacingRate,
		"bytes_retrans", info.BytesRetrans,
	)
}

type diagnosticsWriter struct {
	io.Writer
	diagnostics *transferDiagnostics
}

func (w diagnosticsWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if n > 0 {
		w.diagnostics.bytes.Add(int64(n))
	}
	return n, err
}
