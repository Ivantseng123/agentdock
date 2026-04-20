package pool

import "math/rand"

// pickNicknames returns a slice of length count where each element is either
// a randomly selected pool entry (no replacement when pool >= count) or the
// empty string (when the pool runs out). Callers treat "" as "no nickname —
// fall back to the numeric worker-N label".
//
// Algorithm: Fisher–Yates via rng.Perm. The first min(len(pool), count)
// permuted indices are the picks; the remainder of out stays empty.
func pickNicknames(pool []string, count int, rng *rand.Rand) []string {
	out := make([]string, count)
	if count <= 0 || len(pool) == 0 {
		return out
	}
	perm := rng.Perm(len(pool))
	n := count
	if n > len(pool) {
		n = len(pool)
	}
	for i := 0; i < n; i++ {
		out[i] = pool[perm[i]]
	}
	return out
}

// PickNicknames is the exported wrapper around pickNicknames for use by
// worker.Run. Keeps the core algorithm package-private for tests while
// exposing a single call site.
func PickNicknames(pool []string, count int, rng *rand.Rand) []string {
	return pickNicknames(pool, count, rng)
}
