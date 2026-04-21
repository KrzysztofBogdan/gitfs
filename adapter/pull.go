package adapter

import (
	"context"
	"time"
)

// FullRefreshPull lets an adapter satisfy Adapter.Pull by reusing its
// Fetch implementation as a full refresh. Every path Fetch emits is
// offered as a File proposal; no tombstones are produced. The core's
// classification step turns paths whose tree already matches shadow into
// applied no-ops, so this is safe but can be bandwidth-heavy for services
// whose Fetch re-downloads everything.
//
// Adapters that support true incremental delta can implement Pull
// directly; FullRefreshPull is the opt-in path for the simple case.
func FullRefreshPull(
	ctx context.Context, fetch func(context.Context, Credentials, Emitter) ([]byte, error),
	creds Credentials, emit PullEmitter,
) ([]byte, error) {
	bridge := &fetchBridge{
		pe:   emit,
		seen: map[string]struct{}{},
	}
	head, err := fetch(ctx, creds, bridge)
	if err != nil {
		return nil, err
	}
	paths, ok := emit.(trackedPather)
	if !ok {
		return head, nil
	}
	for _, path := range paths.TrackedPaths() {
		if _, ok := bridge.seen[path]; ok {
			continue
		}
		if err := emit.Tombstone(path); err != nil {
			return nil, err
		}
	}
	return head, nil
}

type trackedPather interface {
	TrackedPaths() []string
}

// fetchBridge exposes a PullEmitter through the richer Emitter interface
// expected by Fetch. FileAt's mtime is discarded — pull proposals do not
// carry mtime in v1.
type fetchBridge struct {
	pe   PullEmitter
	seen map[string]struct{}
}

func (b *fetchBridge) File(path string, content []byte) error {
	b.seen[path] = struct{}{}
	return b.pe.File(path, content)
}

func (b *fetchBridge) FileAt(path string, content []byte, _ time.Time) error {
	b.seen[path] = struct{}{}
	return b.pe.File(path, content)
}
