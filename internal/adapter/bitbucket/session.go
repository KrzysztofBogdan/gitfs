package bitbucket

import (
	"context"
	"errors"
	"io"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

var errTodo = errors.New("bitbucket: not implemented yet")

type session struct{ t target }

func newSession(t target) *session { return &session{t: t} }

func (s *session) List(context.Context, string) (adapter.Listing, error) {
	return adapter.Listing{}, errTodo
}
func (s *session) Fetch(context.Context, string) (*adapter.Resource, error)     { return nil, errTodo }
func (s *session) Apply(context.Context, adapter.ApplyRequest) []adapter.Result { return nil }
func (s *session) Check(context.Context, adapter.ApplyRequest) []adapter.Result { return nil }
func (s *session) Download(context.Context, string, string, io.Writer) (adapter.AttachmentInfo, error) {
	return adapter.AttachmentInfo{}, errTodo
}
func (s *session) Close() error { return nil }
