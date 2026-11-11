// Extractor patch (E01-02.admission, Bor block): read-only accessors that
// let the admission check which Heimdall endpoint and transport the client
// uses. The preparation adds this file to the accepted Bor tree; see
// adapters/bor/admission/native/README.md.

package heimdall

import "net/url"

// ExtractorHost returns the host of the client's Heimdall URL, or "" if the
// URL does not parse.
func (h *HeimdallClient) ExtractorHost() string {
	parsed, err := url.Parse(h.urlString)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// ExtractorProcessTransport reports whether the client sends its requests
// through the process transport (http.DefaultTransport).
func (h *HeimdallClient) ExtractorProcessTransport() bool { return h.client.Transport == nil }
