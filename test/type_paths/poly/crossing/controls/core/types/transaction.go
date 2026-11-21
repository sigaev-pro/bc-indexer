// Portable type double; no native signing or execution.
package types

type Transaction struct {
	Kind  byte
	Nonce uint64
}
type Transactions []*Transaction
