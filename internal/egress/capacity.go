package egress

import "time"

const (
	capacityWindow        = 30 * time.Second
	capacityTrialTimeout  = 2 * time.Minute
	capacityRetryInterval = 10 * time.Minute
	capacityMinGain       = 1.10
	capacityFastRatio     = 0.80
)

// A trial compares total bytes per second at the normal and raised limit.
// Both comparisons require a full window at the respective concurrency.
type capacityTrial struct {
	fullSince  time.Time
	fullBytes  uint64
	baseline   float64
	trialUntil time.Time
	retryAt    time.Time
	confirmed  bool
}

func (c *capacityTrial) observe(now time.Time, active, limit int, bytes uint64) {
	if !c.trialUntil.IsZero() && !now.Before(c.trialUntil) {
		c.finish(now)
	}
	target := limit
	if !c.trialUntil.IsZero() {
		target++
	}
	if active != target {
		c.fullSince = time.Time{}
		return
	}
	if c.fullSince.IsZero() {
		c.fullSince, c.fullBytes = now, bytes
	}
	if c.trialUntil.IsZero() || now.Sub(c.fullSince) < capacityWindow {
		return
	}
	if c.rate(now, bytes) < c.baseline*capacityMinGain {
		c.finish(now)
		return
	}
	if !c.confirmed {
		c.confirmed = true
		c.trialUntil = now.Add(capacityRetryInterval)
	}
	c.fullSince, c.fullBytes = now, bytes
}

func (c *capacityTrial) rate(now time.Time, bytes uint64) float64 {
	return float64(bytes-c.fullBytes) / now.Sub(c.fullSince).Seconds()
}

func (c *capacityTrial) canStart(now time.Time, bytes uint64) bool {
	return c.trialUntil.IsZero() && !now.Before(c.retryAt) && !c.fullSince.IsZero() &&
		now.Sub(c.fullSince) >= capacityWindow && c.rate(now, bytes) > 0
}

func (c *capacityTrial) start(now time.Time, bytes uint64) {
	c.baseline = c.rate(now, bytes)
	c.trialUntil = now.Add(capacityTrialTimeout)
	c.fullSince, c.fullBytes = now, bytes
}

func (c *capacityTrial) finish(now time.Time) {
	*c = capacityTrial{retryAt: now.Add(capacityRetryInterval)}
}
