package server

// This file is not part of upstream Bor. The extractor state provider adds it
// to the native server package so the capture preflight reads the same
// effective configuration that `bor server` uses.

import (
	"io"
	"strings"

	"github.com/mitchellh/cli"
)

// ExtractorEffectiveConfig applies the native `bor server` precedence
// (defaults, then the --config file, then explicit flags) to args and returns
// the configuration that Run would pass to NewServer, without starting it.
func ExtractorEffectiveConfig(args []string) (*Config, error) {
	command := &Command{UI: &cli.BasicUi{Reader: strings.NewReader(""), Writer: io.Discard, ErrorWriter: io.Discard}}
	if err := command.extractFlags(args); err != nil {
		return nil, err
	}
	return command.config, nil
}
