package pipeline

import (
	"errors"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/model"
	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/transfer"
)

// A temporary 404 can be an eventual-consistency delay at the provider,
// an expiring CDN cache, or a transient gateway result. Recheck it several
// times, but do not retry truly absent objects forever.
const missingObjectConfirmations = 6
const missingObjectWindow = 6 * time.Hour

const (
	retryBaseDelay = 5 * time.Second
	retryMaxDelay  = 10 * time.Minute
)

type retryDecision struct {
	delay    time.Duration
	terminal bool
	reason   string
}

// transferRetryDecision applies to every satellite provider. Attempt is the
// number of actual HTTP download attempts (ClaimTransfer increments it before
// calling Download). Transient transport errors keep retrying with a capped
// delay so a multi-hour upstream outage does not permanently lose data.
func transferRetryDecision(task model.Task, cause error, now time.Time) retryDecision {
	attempt := task.Attempts
	var httpErr *transfer.HTTPError
	if errors.As(cause, &httpErr) {
		switch httpErr.Status {
		case http.StatusNotFound:
			if task.Missing404Count+1 >= missingObjectConfirmations && !task.First404At.IsZero() && now.Sub(task.First404At) >= missingObjectWindow {
				return retryDecision{terminal: true, reason: "object unavailable after repeated 404 confirmations over six hours"}
			}
		case http.StatusGone:
			return retryDecision{terminal: true, reason: "provider reports object permanently gone (HTTP 410)"}
		case http.StatusBadRequest, http.StatusMethodNotAllowed,
			http.StatusNotAcceptable, http.StatusRequestEntityTooLarge,
			http.StatusRequestURITooLong, http.StatusUnsupportedMediaType,
			http.StatusUnprocessableEntity, http.StatusUnavailableForLegalReasons:
			// These generally indicate a malformed or disallowed request, not
			// temporary congestion. Authentication has its own rotation flow.
			return retryDecision{terminal: true, reason: "non-retryable HTTP request error"}
		}
		return retryDecision{delay: retryDelayAtLeast(attempt, httpErr.RetryAfter)}
	}
	return retryDecision{delay: retryDelay(attempt)}
}

// Equal jitter avoids both synchronized retries and pathological near-zero
// delays. Exponential stages are 5s,10s,20s,... with a 10-minute cap;
// actual waiting is randomized within [50%,100%] of each stage.
func retryDelay(attempt int) time.Duration {
	return retryDelayWithJitter(attempt, rand.Int64N)
}

func retryDelayWithJitter(attempt int, draw func(int64) int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	exponent := min(attempt-1, 7)
	base := retryBaseDelay * time.Duration(1<<exponent)
	if base > retryMaxDelay {
		base = retryMaxDelay
	}
	half := base / 2
	return half + time.Duration(draw(int64(base-half)+1))
}

func retryDelayAtLeast(attempt int, retryAfter time.Duration) time.Duration {
	delay := retryDelay(attempt)
	if retryAfter <= delay {
		return delay
	}
	// Retry-After is a minimum (particularly for 429). Adding a small
	// independent jitter prevents all workers from restarting on the same
	// second when the rate-limit window expires.
	spread := min(retryAfter/4, 5*time.Minute)
	if spread < time.Second {
		spread = time.Second
	}
	return retryAfter + time.Duration(rand.Int64N(int64(spread)+1))
}
