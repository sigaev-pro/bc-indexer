// The random seam of the spool: crypto-random identities.
package walspool

import "crypto/rand"

func readRandom(b []byte) (int, error) {
	return rand.Read(b)
}
