package gpu

// stagingAlign is the offset of every upload: a multiple of every
// format's texel size, as vkCmdCopyBufferToImage requires.
const stagingAlign = 16

// stagingRing allocates uploads from a buffer of size bytes in the order
// the device consumes them. head is where the next one goes and tail
// where the oldest still pending starts; head == tail only when no bytes
// are held.
type stagingRing struct {
	size, head, tail int
	pending          int
}

// reserve is the offset of n free bytes, n at most size, or false while
// pending uploads hold too many.
func (r *stagingRing) reserve(n int) (int, bool) {
	if r.pending == 0 {
		r.head, r.tail = 0, 0
	}
	at := (r.head + stagingAlign - 1) &^ (stagingAlign - 1)
	switch {
	case r.head >= r.tail && at+n <= r.size:
	case r.head >= r.tail && n < r.tail:
		at = 0
	case r.head < r.tail && at+n < r.tail:
	default:
		return 0, false
	}
	r.head = at + n
	return at, true
}

// release frees everything up to end, where a finished upload stopped.
func (r *stagingRing) release(end int) {
	r.tail = end
	r.pending--
}
