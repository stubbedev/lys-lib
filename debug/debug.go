// Package debug is gelm's compile-time trace facility. Call sites stay
// in the source at the points that matter (input routing, frame
// pacing, configure handshakes) but compile out of prod builds: the
// gelmdebug build tag swaps in an enabled implementation, and with the
// tag off Log is an empty function, so the compiler eliminates every
// call and the evaluation of its arguments.
//
// In a tagged build GOELM_DEBUG selects categories at runtime: unset
// or "*" traces everything, a comma-separated list traces those
// categories, and a "-name" entry excludes one.
package debug
