package ksefpdf

import (
	"context"
	"sync"
	"sync/atomic"
)

// Pool shares at most n renderers between goroutines (ksef pdf spec §4.5).
type Pool struct {
	sem     chan struct{} // a token per render in progress
	mu      sync.Mutex
	idle    []*Renderer
	created atomic.Int32
}

func NewPool(n int) *Pool { return &Pool{sem: make(chan struct{}, max(n, 1))} }

// Render renders on an idle renderer, or a new one while fewer than n
// exist. ctx cancels the wait and interrupts the render.
func (p *Pool) Render(ctx context.Context, in Input) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.sem }()
	r, err := p.get()
	if err != nil {
		return nil, err
	}
	pdf, err := r.render(ctx, in, clockFromXML)
	p.put(r)
	return pdf, err
}

func (p *Pool) get() (*Renderer, error) {
	p.mu.Lock()
	if k := len(p.idle); k > 0 {
		r := p.idle[k-1]
		p.idle = p.idle[:k-1]
		p.mu.Unlock()
		return r, nil
	}
	p.mu.Unlock()
	p.created.Add(1)
	return NewRenderer()
}

func (p *Pool) put(r *Renderer) {
	if r.broken {
		return
	}
	p.mu.Lock()
	p.idle = append(p.idle, r)
	p.mu.Unlock()
}

// Close drops the idle renderers; their memory goes with the next GC.
func (p *Pool) Close() {
	p.mu.Lock()
	p.idle = nil
	p.mu.Unlock()
}
