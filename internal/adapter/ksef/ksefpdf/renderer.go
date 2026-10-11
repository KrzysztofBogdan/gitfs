package ksefpdf

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/dop251/goja"
)

//go:embed shim.js
var shimJS string

// renderTimeout bounds one render (ksef pdf spec §4.1); tests shorten it.
var renderTimeout = 60 * time.Second

// clockFromXML asks render for the render time of §4.3.
const clockFromXML = -1

var (
	errTimeout = errors.New("timeout")
	errBroken  = errors.New("this renderer was interrupted; use another")
)

var compiled = sync.OnceValues(func() (*goja.Program, error) {
	src, err := assets.ReadFile("assets/generator.js")
	if err != nil {
		return nil, err
	}
	return goja.Compile("ksef-pdf-generator.js", string(src), true)
})

// Renderer is one goja runtime with the generator loaded and its fonts
// added (ksef pdf spec §4.1). It is not safe for concurrent use.
type Renderer struct {
	vm             *goja.Runtime
	gen, runTimers goja.Callable
	broken         bool // interrupted: the runtime's state is unknown
}

// NewRenderer starts a runtime: about 0.5 s and 140 MB.
func NewRenderer() (*Renderer, error) {
	prog, err := compiled()
	if err != nil {
		return nil, fmt.Errorf("ksef-pdf-generator: %w", err)
	}
	vm := goja.New()
	vm.Set("__now", int64(0))
	vm.Set("__waw", func(ms int64) []any {
		t := time.UnixMilli(ms).In(warsaw)
		_, off := t.Zone()
		return []any{t.Year(), int(t.Month()) - 1, t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond() / 1e6, int(t.Weekday()), -off / 60}
	})
	vm.Set("__wawLocal", func(y, m0, d, h, mi, s, ms int64) int64 {
		return time.Date(int(y), time.Month(m0+1), int(d), int(h), int(mi), int(s), int(ms)*1e6, warsaw).UnixMilli()
	})
	vm.Set("__dtf", func(ms int64, withTime, withSeconds bool) string {
		layout := "02.01.2006"
		if withTime {
			layout += ", 15:04"
			if withSeconds {
				layout += ":05"
			}
		}
		return time.UnixMilli(ms).In(warsaw).Format(layout)
	})
	vm.Set("__encode", func(s string) goja.ArrayBuffer { return vm.NewArrayBuffer([]byte(s)) })
	vm.Set("__decode", func(b goja.Value, enc string) (string, error) { return decodeJS(b, enc) })
	if _, err := vm.RunScript("shim.js", shimJS); err != nil {
		return nil, fmt.Errorf("ksef-pdf-generator shims: %w", err)
	}
	if _, err := vm.RunProgram(prog); err != nil {
		return nil, fmt.Errorf("ksef-pdf-generator: %w", err)
	}
	lib := vm.Get("ksef-fe-invoice-converter")
	if lib == nil || goja.IsUndefined(lib) || goja.IsNull(lib) {
		return nil, errors.New("ksef-pdf-generator: the bundle defines no library")
	}
	o := lib.ToObject(vm)
	add, ok1 := goja.AssertFunction(o.Get("gfsAddFont"))
	gen, ok2 := goja.AssertFunction(o.Get("generateInvoice"))
	run, ok3 := goja.AssertFunction(vm.Get("__runTimers"))
	if !ok1 || !ok2 || !ok3 {
		return nil, errors.New("ksef-pdf-generator: the bundle lacks gfsAddFont or generateInvoice")
	}
	for _, name := range fontNames {
		b, err := assets.ReadFile("assets/" + name)
		if err != nil {
			return nil, err
		}
		if _, err := add(goja.Undefined(), vm.ToValue(name), vm.ToValue(vm.NewArrayBuffer(b))); err != nil {
			return nil, fmt.Errorf("ksef-pdf-generator: font %s: %w", name, jsError(err))
		}
	}
	return &Renderer{vm: vm, gen: gen, runTimers: run}, nil
}

// Render renders one invoice at its render time (§4.3).
func (r *Renderer) Render(in Input) ([]byte, error) {
	return r.render(context.Background(), in, clockFromXML)
}

// render renders at clockMs, or at §4.3's time for clockFromXML. ctx and
// renderTimeout interrupt it; an interrupted renderer is broken for good.
func (r *Renderer) render(ctx context.Context, in Input, clockMs int64) ([]byte, error) {
	if r.broken {
		return nil, errBroken
	}
	f := factsOf(in)
	if clockMs == clockFromXML {
		clockMs = f.clockMs
	}
	r.vm.Set("__now", clockMs)
	timer := time.AfterFunc(renderTimeout, func() { r.vm.Interrupt(errTimeout) })
	stop := context.AfterFunc(ctx, func() { r.vm.Interrupt(ctx.Err()) })
	pdf, err := r.run(f, in.KsefNumber)
	timer.Stop()
	stop()
	r.vm.ClearInterrupt()
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		r.broken = true
		if ie.Value() == errTimeout {
			return nil, fmt.Errorf("the generator ran longer than %v", renderTimeout)
		}
		if e, ok := ie.Value().(error); ok {
			return nil, e
		}
		return nil, err
	}
	return pdf, err
}

func (r *Renderer) run(f facts, ksef string) ([]byte, error) {
	vm := r.vm
	file, err := vm.New(vm.Get("File"), vm.ToValue(f.text))
	if err != nil {
		return nil, err
	}
	add := vm.NewObject()
	add.Set("nrKSeF", ksef)
	if f.acDate != "" {
		add.Set("acDate", f.acDate)
	}
	if f.qrCode != "" {
		add.Set("qrCode", f.qrCode)
	}
	res, err := r.gen(goja.Undefined(), file, add, vm.ToValue("base64"))
	if err != nil {
		return nil, jsError(err)
	}
	p, ok := res.Export().(*goja.Promise)
	if !ok {
		return nil, errors.New("generateInvoice returned no promise")
	}
	for p.State() == goja.PromiseStatePending {
		n, err := r.runTimers(goja.Undefined())
		if err != nil {
			return nil, jsError(err)
		}
		if n.ToInteger() == 0 && p.State() == goja.PromiseStatePending {
			return nil, errors.New("the generator stopped without finishing")
		}
	}
	if p.State() == goja.PromiseStateRejected {
		return nil, errors.New(jsMessage(p.Result()))
	}
	return base64.StdEncoding.DecodeString(p.Result().String())
}

// jsError turns a JavaScript exception into its message; interrupts pass.
func jsError(err error) error {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return errors.New(jsMessage(ex.Value()))
	}
	return err
}

func jsMessage(v goja.Value) string {
	if o, ok := v.(*goja.Object); ok {
		if m := o.Get("message"); m != nil && !goja.IsUndefined(m) {
			return m.String()
		}
	}
	return v.String()
}

// decodeJS is TextDecoder.decode for the encodings fontkit and the
// generator use.
func decodeJS(b goja.Value, enc string) (string, error) {
	var bs []byte
	switch v := b.Export().(type) {
	case goja.ArrayBuffer:
		bs = v.Bytes()
	case []byte:
		bs = v
	default:
		return "", fmt.Errorf("TextDecoder: cannot decode %T", v)
	}
	switch strings.ToLower(enc) {
	case "utf-8", "utf8", "":
		return strings.ToValidUTF8(string(bs), "\ufffd"), nil
	case "utf-16be", "utf-16le", "utf-16":
		u := make([]uint16, len(bs)/2)
		for i := range u {
			if strings.ToLower(enc) == "utf-16be" {
				u[i] = uint16(bs[2*i])<<8 | uint16(bs[2*i+1])
			} else {
				u[i] = uint16(bs[2*i]) | uint16(bs[2*i+1])<<8
			}
		}
		return string(utf16.Decode(u)), nil
	case "ascii", "latin1", "iso-8859-1", "us-ascii":
		r := make([]rune, len(bs))
		for i, c := range bs {
			r[i] = rune(c)
		}
		return string(r), nil
	}
	return "", fmt.Errorf("TextDecoder: unsupported encoding %q", enc)
}
