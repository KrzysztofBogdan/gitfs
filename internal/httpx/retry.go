// Package httpx holds the retry policy every gfs HTTP client shares: 429 is
// retried for any method (the request was not processed), 502/503/504 and
// network errors only for reads (a write may already have been applied).
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	MaxAttempts  = 6
	MaxTotalWait = 5 * time.Minute
	MaxBackoff   = time.Minute
)

// Retrier runs requests with the shared retry policy.
type Retrier struct {
	Service string // "Jira", "OVH": in wait messages
	Sleep   func(context.Context, time.Duration) error
	Now     func() time.Time
	OnWait  func(msg string) // told before each wait; never nil
}

// SleepCtx sleeps for d or until ctx is done.
func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Send runs one request with retries; read says the request only reads,
// whatever its method, so transient errors are retried as for GET. build
// makes a fresh request per attempt.
func (r *Retrier) Send(ctx context.Context, hc *http.Client, read bool, build func() (*http.Request, error)) (*http.Response, error) {
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		method := req.Method
		if read {
			method = http.MethodGet
		}
		msg, retry := r.reason(method, resp, err)
		if !retry || attempt == MaxAttempts {
			return resp, err
		}
		d := r.Wait(attempt, resp)
		if waited+d > MaxTotalWait {
			return resp, err
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
		}
		r.OnWait(fmt.Sprintf("%s, retrying in %s…", msg, d.Round(time.Second)))
		if err := r.Sleep(ctx, d); err != nil {
			return nil, err
		}
		waited += d
	}
}

var errGaveUp = errors.New("gave up waiting")

// Pause waits before retry attempt+1 of a request the service refused in
// its body (ClouDNS answers limits with HTTP 200); waited accumulates.
// It returns an error wrapping "gave up waiting" when attempts or total
// waiting are used up.
func (r *Retrier) Pause(ctx context.Context, attempt int, waited *time.Duration, resp *http.Response, msg string) error {
	if attempt >= MaxAttempts {
		return fmt.Errorf("%s: %w after %d attempts", msg, errGaveUp, attempt)
	}
	d := r.Wait(attempt, resp)
	if *waited+d > MaxTotalWait {
		return fmt.Errorf("%s: %w after %s", msg, errGaveUp, waited.Round(time.Second))
	}
	r.OnWait(fmt.Sprintf("%s, retrying in %s…", msg, d.Round(time.Second)))
	if err := r.Sleep(ctx, d); err != nil {
		return err
	}
	*waited += d
	return nil
}

func (r *Retrier) reason(method string, resp *http.Response, err error) (string, bool) {
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	switch {
	case status == http.StatusTooManyRequests:
		if why := resp.Header.Get("RateLimit-Reason"); why != "" {
			return "Rate limited by " + r.Service + " (" + why + ")", true
		}
		return "Rate limited by " + r.Service, true
	case method != http.MethodGet:
		return "", false
	case err != nil:
		return "Connection error (" + err.Error() + ")", true
	case status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout:
		return fmt.Sprintf("%s unavailable (HTTP %d)", r.Service, status), true
	}
	return "", false
}

// Wait is Retry-After (seconds or an HTTP date) when the server sent it,
// else the end of a rate-limit window (X-RateLimit-Reset, ISO 8601, or
// Beta-Retry-After, seconds), else exponential backoff from 1s with up to
// 25% jitter, capped at MaxBackoff.
func (r *Retrier) Wait(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
				return time.Duration(n) * time.Second
			}
			if t, err := http.ParseTime(ra); err == nil {
				return max(t.Sub(r.Now()), 0)
			}
		}
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			if t, err := time.Parse(time.RFC3339, reset); err == nil {
				return min(max(t.Sub(r.Now()), 0), MaxTotalWait)
			}
		}
		if n, err := strconv.Atoi(resp.Header.Get("Beta-Retry-After")); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	d := min(time.Second<<(attempt-1), MaxBackoff)
	return d + time.Duration(rand.Int64N(int64(d)/4+1))
}
