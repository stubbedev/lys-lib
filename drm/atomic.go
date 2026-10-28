package drm

import (
	"cmp"
	"runtime"
	"slices"
	"unsafe"
)

type atomicEntry struct {
	object, property uint32
	value            uint64
}

// Request is an atomic KMS commit being built: property values to set
// on KMS objects. The zero value is empty and ready. Reusing a Request
// across commits with Reset keeps its buffers.
type Request struct {
	entries []atomicEntry
	objects []uint32
	counts  []uint32
	props   []uint32
	values  []uint64
}

// Reset empties r and keeps its buffers.
func (r *Request) Reset() {
	r.entries = r.entries[:0]
}

// Add sets property on KMS object to value.
func (r *Request) Add(object, property uint32, value uint64) {
	r.entries = append(r.entries, atomicEntry{object, property, value})
}

func (r *Request) build() {
	slices.SortStableFunc(r.entries, func(a, b atomicEntry) int { return cmp.Compare(a.object, b.object) })
	r.objects, r.counts, r.props, r.values = r.objects[:0], r.counts[:0], r.props[:0], r.values[:0]
	for i, e := range r.entries {
		if i == 0 || e.object != r.entries[i-1].object {
			r.objects = append(r.objects, e.object)
			r.counts = append(r.counts, 0)
		}
		r.counts[len(r.counts)-1]++
		r.props = append(r.props, e.property)
		r.values = append(r.values, e.value)
	}
}

// Commit applies r with DRM_IOCTL_MODE_ATOMIC, grouping its values by
// object. flags are the Atomic* constants; userData comes back in the
// FlipEvent when flags hold AtomicPageFlipEvent.
func (d *Device) Commit(r *Request, flags uint32, userData uint64) error {
	r.build()
	arg := struct {
		flags, countObjs                 uint32
		objs, counts, props, values, res uint64
		userData                         uint64
	}{flags: flags, countObjs: uint32(len(r.objects)), userData: userData} //nolint:gosec // a handful of KMS objects
	var pin runtime.Pinner
	arg.objs, arg.counts, arg.props, arg.values = addr(&pin, r.objects), addr(&pin, r.counts), addr(&pin, r.props), addr(&pin, r.values)
	err := d.ioctl("DRM_IOCTL_MODE_ATOMIC", ioctlAtomic, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads the pinned arrays during the call
	pin.Unpin()
	return err
}

// Each calls f for every value in r, in the order they were added
// until the next Commit, which sorts them by object.
func (r *Request) Each(f func(object, property uint32, value uint64)) {
	for _, e := range r.entries {
		f(e.object, e.property, e.value)
	}
}
