package capture

import (
	"fmt"

	"github.com/stubbedev/lys-lib/wlr"
)

// Dynamic is the compositor's cast target as a Source: a session on it
// captures whatever window or output the user points the target at, and
// goes on across changes, reporting a size change as ErrReconfigured.
// Its sessions claim nothing: they may overlap.
type Dynamic struct{}

func (Dynamic) imageSource(c *Client) (*wlr.ImageCaptureSourceV1, func(), error) {
	if c.copyCapture == nil || c.dynamicSource == nil {
		return nil, nil, fmt.Errorf("%w: %s + %s", ErrUnsupported, ifaceCopyCapture, ifaceDynamicSource)
	}
	source, err := c.dynamicSource.CreateSource()
	if err != nil {
		return nil, nil, fmt.Errorf("capture: dynamic source: %w", err)
	}
	return source, func() {}, nil
}

// CaptureDynamic copies one frame of the cast target. Before a target is
// first chosen, it blocks until one is.
func (c *Client) CaptureDynamic(opts Options) (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.captureSourceOnce(Dynamic{}, opts)
}

// OpenDynamicCursor follows the pointer over the cast target.
// ErrUnsupported means the compositor offers no dynamic source or cursor
// sessions, or the seat has no pointer.
func OpenDynamicCursor() (*Cursor, error) {
	return openCursor(func(*Client) (Source, error) { return Dynamic{}, nil })
}
