// Package pyrand reproduces Python's random.Random bit for bit.
//
// It exists for one reason: the estate CostCrew ships is built by a SEEDED
// generator, and several of its numbers (a team's budget factor, a run's
// cost) come out of that generator rather than out of the data. Porting the
// product to Go without porting the generator would change every one of those
// numbers, and the parity gate would light up on surfaces where nothing is
// actually wrong.
//
// The alternatives were worse. Freezing the generated tables as shipped data
// would mean the product can no longer rebuild its own estate, which is a
// documented promise. Changing the algorithm would mean re-capturing the
// golden master, which is to say giving up the reference the port is held to.
//
// This is Mersenne Twister MT19937 with CPython's seeding
// (`init_by_array` over the seed's 32-bit words) and CPython's 53-bit
// `random()`, which is what `uniform` is built on. Every claim here is pinned
// against real CPython output in pyrand_test.go rather than believed.
package pyrand

const (
	n         = 624
	m         = 397
	matrixA   = 0x9908b0df
	upperMask = 0x80000000
	lowerMask = 0x7fffffff
)

// Rand is the Mersenne Twister state. Not safe for concurrent use, exactly
// like the Python object it mirrors.
type Rand struct {
	mt  [n]uint32
	idx int
}

// New seeds the generator the way CPython's random.Random(seed) does for a
// non-negative integer seed: the absolute value is split into 32-bit words,
// little end first, and fed to init_by_array. A plain init_genrand(seed)
// would be the obvious guess and produces a completely different stream.
func New(seed uint64) *Rand {
	var key []uint32
	if seed == 0 {
		key = []uint32{0}
	}
	for s := seed; s > 0; s >>= 32 {
		key = append(key, uint32(s&0xffffffff))
	}
	r := &Rand{}
	r.initByArray(key)
	return r
}

func (r *Rand) initGenrand(s uint32) {
	r.mt[0] = s
	for i := 1; i < n; i++ {
		r.mt[i] = 1812433253*(r.mt[i-1]^(r.mt[i-1]>>30)) + uint32(i)
	}
	r.idx = n
}

func (r *Rand) initByArray(key []uint32) {
	r.initGenrand(19650218)
	i, j := 1, 0
	k := n
	if len(key) > k {
		k = len(key)
	}
	for ; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1664525)) + key[j] + uint32(j)
		i++
		j++
		if i >= n {
			r.mt[0] = r.mt[n-1]
			i = 1
		}
		if j >= len(key) {
			j = 0
		}
	}
	for k = n - 1; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1566083941)) - uint32(i)
		i++
		if i >= n {
			r.mt[0] = r.mt[n-1]
			i = 1
		}
	}
	r.mt[0] = 0x80000000
}

func (r *Rand) genrandUint32() uint32 {
	if r.idx >= n {
		var y uint32
		for k := 0; k < n-m; k++ {
			y = (r.mt[k] & upperMask) | (r.mt[k+1] & lowerMask)
			r.mt[k] = r.mt[k+m] ^ (y >> 1) ^ ((y & 1) * matrixA)
		}
		for k := n - m; k < n-1; k++ {
			y = (r.mt[k] & upperMask) | (r.mt[k+1] & lowerMask)
			r.mt[k] = r.mt[k+(m-n)] ^ (y >> 1) ^ ((y & 1) * matrixA)
		}
		y = (r.mt[n-1] & upperMask) | (r.mt[0] & lowerMask)
		r.mt[n-1] = r.mt[m-1] ^ (y >> 1) ^ ((y & 1) * matrixA)
		r.idx = 0
	}
	y := r.mt[r.idx]
	r.idx++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	y ^= y >> 18
	return y
}

// Float64 is CPython's random(): 53 bits of precision assembled from two
// 32-bit draws, the first contributing 27 bits and the second 26. The order
// matters and so does the split; getting either wrong still yields plausible
// numbers in [0,1), which is why this is pinned rather than eyeballed.
func (r *Rand) Float64() float64 {
	a := r.genrandUint32() >> 5
	b := r.genrandUint32() >> 6
	return (float64(a)*67108864.0 + float64(b)) * (1.0 / 9007199254740992.0)
}

// Uniform is CPython's uniform(a, b): a + (b-a) * random().
//
// The explicit float64() conversion is load-bearing and must not be tidied
// away. Go permits an implementation to fuse `x*y + z` into a single FMA with
// ONE rounding, and on arm64 it does. CPython rounds twice: once after the
// multiply, once after the add. Written as the obvious `a + (b-a)*random()`
// this function returned -0.0057613155033800195 where CPython returns
// -0.0057613155033800212, a difference in the last bit that the pinned vector
// below caught immediately and that nothing else would have.
//
// A conversion to float64 forces the intermediate to be rounded, which is the
// spec's own way of saying "do not fuse this".
func (r *Rand) Uniform(a, b float64) float64 {
	return a + float64((b-a)*r.Float64())
}
