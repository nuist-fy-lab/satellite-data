package himawari

import (
	"compress/bzip2"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

const (
	maxObservationObjects = 256
	maxObservationBytes   = 128 << 20
	maxObjectBytes        = 2 << 20
	observationWorkers    = 4
	cacheEntries          = 32768
	cacheTTL              = 24 * time.Hour
)

type observationInterval struct {
	start, end       time.Time
	startMJD, endMJD float64
}
type cachedInterval struct {
	interval observationInterval
	expires  time.Time
	used     uint64
}
type observationCache struct {
	mu      sync.Mutex
	entries map[string]cachedInterval
	clock   uint64
}

func (c *observationCache) get(key string) (observationInterval, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[key]
	if !ok || time.Now().After(v.expires) {
		delete(c.entries, key)
		return observationInterval{}, false
	}
	c.clock++
	v.used = c.clock
	c.entries[key] = v
	return v.interval, true
}
func (c *observationCache) put(key string, value observationInterval) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]cachedInterval)
	}
	if len(c.entries) >= cacheEntries {
		oldest, min := "", ^uint64(0)
		for k, v := range c.entries {
			if v.used < min {
				oldest, min = k, v.used
			}
		}
		delete(c.entries, oldest)
	}
	c.clock++
	c.entries[key] = cachedInterval{interval: value, expires: time.Now().Add(cacheTTL), used: c.clock}
}

type byteBudget struct {
	mu        sync.Mutex
	remaining int64
}
type budgetReader struct {
	reader io.Reader
	budget *byteBudget
}

func (r budgetReader) Read(p []byte) (int, error) {
	r.budget.mu.Lock()
	if r.budget.remaining <= 0 {
		r.budget.mu.Unlock()
		return 0, fmt.Errorf("HSD query byte budget exceeded")
	}
	if int64(len(p)) > r.budget.remaining {
		p = p[:r.budget.remaining]
	}
	r.budget.remaining -= int64(len(p))
	r.budget.mu.Unlock()
	n, err := r.reader.Read(p)
	r.budget.mu.Lock()
	r.budget.remaining += int64(len(p) - n)
	r.budget.mu.Unlock()
	return n, err
}

// readHSDHeader closes each stream immediately after the basic block is decoded.
func readHSDHeader(ctx context.Context, open func(context.Context, model.Asset) (io.ReadCloser, error), asset model.Asset, budget *byteBudget) (observationInterval, error) {
	stream, err := open(ctx, asset)
	if err != nil {
		return observationInterval{}, err
	}
	defer stream.Close()
	compressed := budgetReader{reader: io.LimitReader(stream, maxObjectBytes), budget: budget}
	reader := bzip2.NewReader(compressed)
	block := make([]byte, 282)
	if _, err := io.ReadFull(reader, block); err != nil {
		return observationInterval{}, fmt.Errorf("read HSD header %s: %w", asset.ID, err)
	}
	start, end, err := parseObservationHeader(block)
	if err != nil {
		return observationInterval{}, fmt.Errorf("parse HSD header %s: %w", asset.ID, err)
	}
	if err := validateObservationIdentity(block, asset.ID); err != nil {
		return observationInterval{}, err
	}
	var order binary.ByteOrder = binary.LittleEndian
	if block[5] == 1 {
		order = binary.BigEndian
	}
	return observationInterval{start: start, end: end, startMJD: math.Float64frombits(order.Uint64(block[46:54])), endMJD: math.Float64frombits(order.Uint64(block[54:62]))}, nil
}

func (p *Provider) resolveObservations(ctx context.Context, assets []model.Asset) ([]observationInterval, error) {
	if len(assets) > maxObservationObjects {
		return nil, fmt.Errorf("HSD query object budget exceeded: %d > %d", len(assets), maxObservationObjects)
	}
	budget := &byteBudget{remaining: maxObservationBytes}
	results := make([]observationInterval, len(assets))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	for i := 0; i < observationWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				a := assets[idx]
				key := a.ID + "\x00" + a.ETag
				if a.ETag != "" {
					if value, ok := p.observationCache.get(key); ok {
						results[idx] = value
						continue
					}
				}
				value, err := readHSDHeader(ctx, p.openObservation, a, budget)
				if err != nil {
					once.Do(func() { firstErr = err; cancel() })
					continue
				}
				results[idx] = value
				if a.ETag != "" {
					p.observationCache.put(key, value)
				}
			}
		}()
	}
	for i := range assets {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func sortAssets(assets []model.Asset) {
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
}
