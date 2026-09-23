package textdiff

import "slices"

type Chunk struct {
	Conflict            bool
	Lines               []string // clean chunk content
	Local, Base, Remote []string // conflict sides
}

func Merge3(base, local, remote []string) []Chunk {
	ma, mb := Matches(base, local), Matches(base, remote)
	var out []Chunk
	io, ia, ib := 0, 0, 0
	for {
		k := io
		for k < len(base) && (ma[k] < 0 || mb[k] < 0) {
			k++
		}
		if k < len(base) && k == io && ma[k] == ia && mb[k] == ib {
			out = appendClean(out, base[k:k+1])
			io, ia, ib = io+1, ia+1, ib+1
			continue
		}
		ea, eb := len(local), len(remote)
		if k < len(base) {
			ea, eb = ma[k], mb[k]
		}
		o, a, b := base[io:k], local[ia:ea], remote[ib:eb]
		switch {
		case len(o)+len(a)+len(b) == 0:
		case slices.Equal(a, o):
			out = appendClean(out, b)
		case slices.Equal(b, o), slices.Equal(a, b):
			out = appendClean(out, a)
		default:
			out = append(out, Chunk{Conflict: true, Local: a, Base: o, Remote: b})
		}
		if k >= len(base) {
			return out
		}
		io, ia, ib = k, ea, eb
	}
}

func appendClean(out []Chunk, lines []string) []Chunk {
	if len(lines) == 0 {
		return out
	}
	if n := len(out); n > 0 && !out[n-1].Conflict {
		out[n-1].Lines = append(out[n-1].Lines, lines...)
		return out
	}
	return append(out, Chunk{Lines: append([]string(nil), lines...)})
}

func Render(chunks []Chunk, remoteLabel string) ([]string, int) {
	var out []string
	n := 0
	for _, c := range chunks {
		if !c.Conflict {
			out = append(out, c.Lines...)
			continue
		}
		n++
		out = append(out, "<<<<<<< local")
		out = append(out, c.Local...)
		out = append(out, "||||||| base")
		out = append(out, c.Base...)
		out = append(out, "=======")
		out = append(out, c.Remote...)
		out = append(out, ">>>>>>> "+remoteLabel)
	}
	return out, n
}
