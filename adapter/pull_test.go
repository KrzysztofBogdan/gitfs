package adapter

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type stubPullEmitter struct {
	tracked    []string
	files      []string
	tombstones []string
}

func (s *stubPullEmitter) File(path string, _ []byte) error {
	s.files = append(s.files, path)
	return nil
}

func (s *stubPullEmitter) Tombstone(path string) error {
	s.tombstones = append(s.tombstones, path)
	return nil
}

func (s *stubPullEmitter) TrackedPaths() []string {
	return append([]string(nil), s.tracked...)
}

func TestFullRefreshPullTombstonesMissingTrackedPaths(t *testing.T) {
	emit := &stubPullEmitter{tracked: []string{"gone.md", "keep.md"}}

	head, err := FullRefreshPull(context.Background(),
		func(_ context.Context, _ Credentials, emit Emitter) ([]byte, error) {
			if err := emit.File("keep.md", []byte("same")); err != nil {
				return nil, err
			}
			if err := emit.File("new.md", []byte("new")); err != nil {
				return nil, err
			}
			return []byte("HEAD-1"), nil
		},
		nil, emit)
	if err != nil {
		t.Fatalf("FullRefreshPull: %v", err)
	}
	if got, want := string(head), "HEAD-1"; got != want {
		t.Fatalf("head = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(emit.files, []string{"keep.md", "new.md"}) {
		t.Fatalf("files = %#v", emit.files)
	}
	if !reflect.DeepEqual(emit.tombstones, []string{"gone.md"}) {
		t.Fatalf("tombstones = %#v", emit.tombstones)
	}
}

func TestFullRefreshPullTreatsFileAtAsSeen(t *testing.T) {
	emit := &stubPullEmitter{tracked: []string{"keep.md", "gone.md"}}

	_, err := FullRefreshPull(context.Background(),
		func(_ context.Context, _ Credentials, emit Emitter) ([]byte, error) {
			return []byte("HEAD-1"),
				emit.FileAt("keep.md", []byte("same"), time.Unix(123, 0))
		},
		nil, emit)
	if err != nil {
		t.Fatalf("FullRefreshPull: %v", err)
	}
	if !reflect.DeepEqual(emit.files, []string{"keep.md"}) {
		t.Fatalf("files = %#v", emit.files)
	}
	if !reflect.DeepEqual(emit.tombstones, []string{"gone.md"}) {
		t.Fatalf("tombstones = %#v", emit.tombstones)
	}
}
