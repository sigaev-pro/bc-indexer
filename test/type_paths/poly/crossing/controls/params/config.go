// Portable type double; no native fork implementation.
package params

import "math/big"

type ChainConfig struct{ BerlinBlock, LondonBlock, PragueBlock *big.Int }
