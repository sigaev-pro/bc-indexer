package poly

import "fmt"

// AuthorizationProfile is the closed authorization profile of the current
// Polygon family. EIP-7702 is not active in this profile, so the profile is
// empty and closed like the BSC one: there is no authorization list variant,
// and validation of the outer transaction never filters source tuples.
//
// Source tuples that a captured envelope nevertheless carries (including
// invalid or duplicate ones) are preserved source data that this profile
// rejects with an explicit error; the normalizer never drops them silently,
// because a dropped tuple would publish a payload the source never sent.
type AuthorizationProfile struct{}

// AuthorizationProfileNotApplicable is the single closed profile instance.
var AuthorizationProfileNotApplicable = AuthorizationProfile{}

// Applicable reports whether authorization tuples apply in this profile.
func (AuthorizationProfile) Applicable() bool { return false }

// Name returns the profile name for diagnostics.
func (AuthorizationProfile) Name() string { return "POLY_AUTHORIZATION_EMPTY_V1" }

// normalizeAuthorization applies the closed profile to the captured source
// tuples: any tuple, valid or invalid, is a normalization error in a
// profile where EIP-7702 is not active.
func normalizeAuthorization(tx *TxCapture) (AuthorizationProfile, error) {
	if len(tx.AuthorizationTuples) != 0 {
		return AuthorizationProfile{}, fmt.Errorf("%w: %d source tuples in a profile without EIP-7702",
			ErrAuthorization, len(tx.AuthorizationTuples))
	}
	return AuthorizationProfileNotApplicable, nil
}
