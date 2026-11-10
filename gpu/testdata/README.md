# Test data

## vp9-resize.ivf

A VP9 stream whose frames change size without a key frame, so the frames
after each change predict from references of the other size: 15 frames of
432x240, 20 of 324x180 and 7 of 432x240 again, 42 frames with one key frame,
10459 bytes. `TestDecodeVP9ScalesItsReferences` decodes it.

libvpx resizes a stream on its own only in one-pass constant bitrate mode
with `resize_allowed`, which ffmpeg's libvpx encoder does not set, so the
tests cannot make the stream as they make the others: it comes from vpxenc
(libvpx 1.16.0), which the development shell does not have. Noise at 8 kbit/s
underflows the encoder's buffer until it steps down to three quarters of the
size, and the flat grey after it lets the quantizer fall until it steps back
up:

```sh
ffmpeg -f lavfi -i "testsrc2=size=432x240:rate=5,noise=alls=40:allf=t,trim=end_frame=18[a];color=gray:size=432x240:rate=5,trim=end_frame=24[b];[a][b]concat" -pix_fmt yuv420p in.y4m
vpxenc --codec=vp9 --ivf --rt --cpu-used=8 --passes=1 --end-usage=cbr --target-bitrate=8 --resize-allowed=1 --lag-in-frames=0 --kf-max-dist=9999 --drop-frame=0 --buf-sz=1000 --buf-initial-sz=500 --buf-optimal-sz=600 -o vp9-resize.ivf in.y4m
```

Another libvpx or ffmpeg version may place the steps elsewhere; the test
checks that the stream it reads has both.
