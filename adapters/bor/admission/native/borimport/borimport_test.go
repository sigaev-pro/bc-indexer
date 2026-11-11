package borimport

import "testing"

func TestProfileDigest(t *testing.T) {
	if ProfileDigest() != "placeholder" {
		t.Skip("placeholder")
	}
}
