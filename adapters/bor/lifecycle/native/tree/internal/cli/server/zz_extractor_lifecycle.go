// Extractor patch (E01-02.lifecycle_seams, Bor block): the capture wiring of
// `bor server`. The preparation adds this file to the accepted Bor tree; see
// adapters/bor/lifecycle/native/README.md.

package server

import (
	"errors"

	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/node"
)

// ErrExtractorCapture refuses a capture flag without the capture wiring.
var ErrExtractorCapture = errors.New("extractor capture: the capture wiring is not installed")

var (
	// ExtractorCaptureProfile checks the effective configuration of a
	// capture node (--extractor.capture) after buildEth and before eth.New:
	// the capture profile and the store preflight. extractor/borlifecycle/wire
	// installs it from the shipped bor main.
	ExtractorCaptureProfile func(config *Config) error
	// ExtractorCaptureStart starts the capture of a node after eth.New and
	// before node.Start: the admission, the lifecycle gate and the decision
	// recovery driver, then the live observer.
	ExtractorCaptureStart func(config *Config, stack *node.Node, backend *eth.Ethereum) error
	// ExtractorPlainStore checks a node started without --extractor.capture:
	// it refuses a capture store, which a native node without the gate would
	// change without decisions.
	ExtractorPlainStore func(config *Config) error
)

func extractorCaptureProfile(config *Config) error {
	if config.ExtractorCapture == "" {
		if ExtractorPlainStore != nil {
			return ExtractorPlainStore(config)
		}
		return nil
	}
	if ExtractorCaptureProfile == nil || ExtractorCaptureStart == nil {
		return ErrExtractorCapture
	}
	return ExtractorCaptureProfile(config)
}

func extractorCaptureStart(config *Config, stack *node.Node, backend *eth.Ethereum) error {
	if config.ExtractorCapture == "" {
		return nil
	}
	return ExtractorCaptureStart(config, stack, backend)
}
