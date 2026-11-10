package native

import (
	"context"
	"fmt"
)

// ServerInfo queries the server's defaults.
func (c *Conn) ServerInfo(ctx context.Context) (ServerInfo, error) {
	return one(ctx, c, CmdGetServerInfo, nil, decodeServerInfo)
}

// Sinks lists every sink.
func (c *Conn) Sinks(ctx context.Context) ([]DeviceInfo, error) {
	return list(ctx, c, CmdGetSinkInfoList, decodeDevice)
}

// Sources lists every source, monitors included.
func (c *Conn) Sources(ctx context.Context) ([]DeviceInfo, error) {
	return list(ctx, c, CmdGetSourceInfoList, decodeDevice)
}

// SinkInputs lists every playback stream.
func (c *Conn) SinkInputs(ctx context.Context) ([]StreamInfo, error) {
	return list(ctx, c, CmdGetSinkInputInfoList, decodeSinkInput)
}

// SourceOutputs lists every recording stream.
func (c *Conn) SourceOutputs(ctx context.Context) ([]StreamInfo, error) {
	return list(ctx, c, CmdGetSourceOutputList, decodeSourceOutput)
}

// Sink queries one sink by index.
func (c *Conn) Sink(ctx context.Context, index uint32) (DeviceInfo, error) {
	return one(ctx, c, CmdGetSinkInfo, byIndexAndName(index), decodeDevice)
}

// Source queries one source by index.
func (c *Conn) Source(ctx context.Context, index uint32) (DeviceInfo, error) {
	return one(ctx, c, CmdGetSourceInfo, byIndexAndName(index), decodeDevice)
}

// SinkInput queries one playback stream by index.
func (c *Conn) SinkInput(ctx context.Context, index uint32) (StreamInfo, error) {
	return one(ctx, c, CmdGetSinkInputInfo, byIndex(index), decodeSinkInput)
}

// SourceOutput queries one recording stream by index.
func (c *Conn) SourceOutput(ctx context.Context, index uint32) (StreamInfo, error) {
	return one(ctx, c, CmdGetSourceOutputInfo, byIndex(index), decodeSourceOutput)
}

// Subscribe selects the facilities whose changes arrive at
// Options.OnSubscribe.
func (c *Conn) Subscribe(ctx context.Context, mask SubscriptionMask) error {
	var w Writer
	w.U32(uint32(mask))
	return c.exec(ctx, CmdSubscribe, &w)
}

// SetSinkVolume sets a sink's per-channel volume by index.
func (c *Conn) SetSinkVolume(ctx context.Context, index uint32, v CVolume) error {
	w := byIndexAndName(index)
	w.CVolume(v)
	return c.exec(ctx, CmdSetSinkVolume, w)
}

// SetSourceVolume sets a source's per-channel volume by index.
func (c *Conn) SetSourceVolume(ctx context.Context, index uint32, v CVolume) error {
	w := byIndexAndName(index)
	w.CVolume(v)
	return c.exec(ctx, CmdSetSourceVolume, w)
}

// SetSinkInputVolume sets a playback stream's volume.
func (c *Conn) SetSinkInputVolume(ctx context.Context, index uint32, v CVolume) error {
	w := byIndex(index)
	w.CVolume(v)
	return c.exec(ctx, CmdSetSinkInputVolume, w)
}

// SetSourceOutputVolume sets a recording stream's volume.
func (c *Conn) SetSourceOutputVolume(ctx context.Context, index uint32, v CVolume) error {
	w := byIndex(index)
	w.CVolume(v)
	return c.exec(ctx, CmdSetSourceOutputVolume, w)
}

// SetSinkMute mutes or unmutes a sink by index.
func (c *Conn) SetSinkMute(ctx context.Context, index uint32, mute bool) error {
	w := byIndexAndName(index)
	w.Bool(mute)
	return c.exec(ctx, CmdSetSinkMute, w)
}

// SetSourceMute mutes or unmutes a source by index.
func (c *Conn) SetSourceMute(ctx context.Context, index uint32, mute bool) error {
	w := byIndexAndName(index)
	w.Bool(mute)
	return c.exec(ctx, CmdSetSourceMute, w)
}

// SetSinkInputMute mutes or unmutes a playback stream.
func (c *Conn) SetSinkInputMute(ctx context.Context, index uint32, mute bool) error {
	w := byIndex(index)
	w.Bool(mute)
	return c.exec(ctx, CmdSetSinkInputMute, w)
}

// SetSourceOutputMute mutes or unmutes a recording stream.
func (c *Conn) SetSourceOutputMute(ctx context.Context, index uint32, mute bool) error {
	w := byIndex(index)
	w.Bool(mute)
	return c.exec(ctx, CmdSetSourceOutputMute, w)
}

// SetDefaultSink makes the named sink the default.
func (c *Conn) SetDefaultSink(ctx context.Context, name string) error {
	return c.exec(ctx, CmdSetDefaultSink, byName(name))
}

// SetDefaultSource makes the named source the default.
func (c *Conn) SetDefaultSource(ctx context.Context, name string) error {
	return c.exec(ctx, CmdSetDefaultSource, byName(name))
}

// MoveSinkInput routes a playback stream to another sink.
func (c *Conn) MoveSinkInput(ctx context.Context, index, sink uint32) error {
	return c.exec(ctx, CmdMoveSinkInput, moveArgs(index, sink))
}

// MoveSourceOutput routes a recording stream to another source.
func (c *Conn) MoveSourceOutput(ctx context.Context, index, source uint32) error {
	return c.exec(ctx, CmdMoveSourceOutput, moveArgs(index, source))
}

// SetSinkPort switches a sink's active port.
func (c *Conn) SetSinkPort(ctx context.Context, index uint32, port string) error {
	return c.exec(ctx, CmdSetSinkPort, portArgs(index, port))
}

// SetSourcePort switches a source's active port.
func (c *Conn) SetSourcePort(ctx context.Context, index uint32, port string) error {
	return c.exec(ctx, CmdSetSourcePort, portArgs(index, port))
}

// exec runs a command whose reply carries nothing.
func (c *Conn) exec(ctx context.Context, cmd uint32, body *Writer) error {
	_, err := c.request(ctx, cmd, body, nil)
	return err
}

func list[T any](ctx context.Context, c *Conn, cmd uint32, decode func(*Reader) T) ([]T, error) {
	r, err := c.request(ctx, cmd, nil, nil)
	if err != nil {
		return nil, err
	}
	items, err := decodeList(r, decode)
	if err != nil {
		return nil, replyError(cmd, err)
	}
	return items, nil
}

func one[T any](ctx context.Context, c *Conn, cmd uint32, args *Writer, decode func(*Reader) T) (T, error) {
	var zero T
	r, err := c.request(ctx, cmd, args, nil)
	if err != nil {
		return zero, err
	}
	item, err := decodeOne(r, decode)
	if err != nil {
		return zero, replyError(cmd, err)
	}
	return item, nil
}

// replyError names the command in a reply decoding error.
func replyError(cmd uint32, err error) error {
	return fmt.Errorf("pulse: %s reply: %w", CommandName(cmd), err)
}

func byIndex(index uint32) *Writer {
	var w Writer
	w.U32(index)
	return &w
}

// byIndexAndName addresses a device by index with a null name (the
// "index xor name" arguments).
func byIndexAndName(index uint32) *Writer {
	w := byIndex(index)
	w.NullString()
	return w
}

func byName(name string) *Writer {
	var w Writer
	w.String(name)
	return &w
}

func moveArgs(index, device uint32) *Writer {
	w := byIndex(index)
	w.U32(device)
	w.NullString()
	return w
}

func portArgs(index uint32, port string) *Writer {
	w := byIndexAndName(index)
	w.String(port)
	return w
}
