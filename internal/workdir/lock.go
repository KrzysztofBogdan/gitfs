package workdir

import (
	"fmt"
	"os"
)

func (t *Tree) Lock() (func(), error) {
	p := t.gfs("lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another gfs command is running (remove %s if it is not)", p)
		}
		return nil, err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	f.Close()
	return func() { os.Remove(p) }, nil
}
