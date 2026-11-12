package gpu

type reach uint8

const (
	writesSurface reach = iota
	readsSurface
	elsewhere
)

func occlude[T any](ops []T, hides func(*T) bool, reachOf func(*T) reach) []T {
	kept, hidden := len(ops), false
	for i := len(ops) - 1; i >= 0; i-- {
		op := &ops[i]
		switch reachOf(op) {
		case readsSurface:
			hidden = false
		case writesSurface:
			if hidden {
				continue
			}
			hidden = hides(op)
		}
		kept--
		ops[kept] = *op
	}
	n := copy(ops, ops[kept:])
	clear(ops[n:])
	return ops[:n]
}
