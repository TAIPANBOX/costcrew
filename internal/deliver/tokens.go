package deliver

// Tokens is an UPPER BOUND on a prompt, not an estimate of it. Moved from
// tools/run/main.go's tokens(): see packet.go's package comment.
//
// It was len/4, the usual rule of thumb, and the first live call on the
// Anthropic route cost 0.0185 against a "worst case" of 0.0182. The prompt
// came in at 174 tokens where the rule predicted about 66, under by two and a
// half times, and a bound that the very first call steps over is not a bound.
//
// So: one token per byte. No tokeniser splits below a byte, which makes this
// provably an upper bound rather than a better guess, and it needs no
// vocabulary file and no vendor agreement about what a token is.
func Tokens(parts ...string) int {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	return n + 1
}
