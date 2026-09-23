package confluence

import (
	"context"
	"errors"
	"io"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// Download is replaced by the real implementation in Task 11 of the attachments plan.
func (s *session) Download(context.Context, string, string, io.Writer) (adapter.AttachmentInfo, error) {
	return adapter.AttachmentInfo{}, errors.New("confluence: attachments are not supported yet")
}
