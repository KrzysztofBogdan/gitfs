package ksef

import (
	"context"
	"fmt"
	"sync"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/ksefpdf"
)

// roleInvoice is one invoice of a listing in one role.
type roleInvoice struct {
	role string
	inv  invoice
}

// renderAll renders one PDF per distinct invoice (ksef pdf spec §5.2). A
// failed render is reported as a warning and leaves that invoice without a
// PDF; only a cancelled context fails.
func (s *session) renderAll(ctx context.Context, found []roleInvoice) (map[string][]byte, error) {
	out := map[string][]byte{}
	if !s.t.pdf {
		return out, nil
	}
	var todo []invoice
	seen := map[string]bool{}
	for _, f := range found {
		if k := f.inv.meta.KsefNumber; !seen[k] {
			seen[k] = true
			todo = append(todo, f.inv)
		}
	}
	if len(todo) == 0 {
		return out, nil
	}
	pool := ksefpdf.NewPool(s.pdfWorkers)
	defer pool.Close()
	var mu sync.Mutex
	var wg sync.WaitGroup
	done := 0
	s.report(adapter.Progress{Phase: "render", Total: len(todo)})
	for _, inv := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := inv.meta.KsefNumber
			pdf, err := pool.Render(ctx, ksefpdf.Input{XML: inv.xml, KsefNumber: k, QRBase: qrBase[s.t.host]})
			mu.Lock()
			defer mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			done++
			if err != nil {
				s.report(adapter.Progress{Phase: "warn", Item: fmt.Sprintf("PDF of %s: %v", k, err)})
			} else {
				out[k] = pdf
			}
			s.report(adapter.Progress{Phase: "render", Done: done, Total: len(todo), Item: k})
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
