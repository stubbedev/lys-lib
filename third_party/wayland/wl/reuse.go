package wl

import "reflect"

// Reusable is a proxy its constructor takes back once delete_id retires its
// id, for objects a client makes one of every frame.
type Reusable interface {
	Proxy
	// Reset drops every handler, as a new proxy has none.
	Reset()
}

// retire keeps p for its constructor once delete_id retired its id, without
// the handlers that would keep what they reach alive.
func (ctx *Context) retire(p Proxy) {
	r, ok := p.(Reusable)
	if !ok {
		return
	}
	r.Reset()
	t := reflect.TypeOf(r)
	ctx.mu.Lock()
	switch {
	case ctx.closed.Load():
	case ctx.free == nil:
		ctx.free = map[reflect.Type][]Reusable{t: {r}}
	default:
		ctx.free[t] = append(ctx.free[t], r)
	}
	ctx.mu.Unlock()
}

// Reused takes a T delete_id retired on ctx, if there is one.
func Reused[T Reusable](ctx *Context) (T, bool) {
	t := reflect.TypeFor[T]()
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	free := ctx.free[t]
	if len(free) == 0 {
		var none T
		return none, false
	}
	r := free[len(free)-1]
	free[len(free)-1] = nil
	ctx.free[t] = free[:len(free)-1]
	return r.(T), true
}
