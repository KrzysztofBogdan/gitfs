package bitbucket

import (
	"context"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

func (s *session) Apply(context.Context, adapter.ApplyRequest) []adapter.Result { return nil }
func (s *session) Check(context.Context, adapter.ApplyRequest) []adapter.Result { return nil }
