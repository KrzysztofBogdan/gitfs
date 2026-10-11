package ksefpdf

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

func TestPool(t *testing.T) {
	c := loadGolden(t)[0]
	in := Input{XML: readTestdata(t, c.XML), KsefNumber: c.Ksef, QRBase: c.QRBase}
	want := readTestdata(t, c.PDF)
	p := NewPool(2)
	defer p.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := p.Render(context.Background(), in)
			if err == nil && !bytes.Equal(got, want) {
				t.Error("pool output differs from the golden PDF")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := p.created.Load(); n < 1 || n > 2 {
		t.Fatalf("at most n renderers, reused: %d created", n)
	}
}

func TestPoolCancelled(t *testing.T) {
	p := NewPool(1)
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := p.Render(ctx, Input{XML: []byte("<Faktura/>")}); err != context.Canceled || time.Since(start) > time.Second {
		t.Fatalf("a cancelled context stops at once: %v", err)
	}
	if p.created.Load() != 0 {
		t.Fatal("no renderer started for a cancelled render")
	}
}
