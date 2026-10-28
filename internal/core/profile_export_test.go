package core

import "github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

// CountProfileLoadsForTest runs fn with the package's profile-load seam
// (loadProfile, profile.go) wrapped in a counter, returning how many times
// it was called - used by core_test (updater_test.go,
// TestService_CheckGameUpdates_LoadsProfileOnce, #289 review) to verify
// CheckGameUpdates' lock-state stamping loop loads the profile once per
// call rather than once per listed mod. Test-only: package core so it can
// swap the unexported loadProfile var, exported so core_test (an external
// test package in the same directory) can call it, mirroring
// NewUpdatePlanForApplyTest's own test-export convention
// (update_export_test.go).
func CountProfileLoadsForTest(fn func()) int {
	orig := loadProfile
	count := 0
	loadProfile = func(configDir, gameID, name string) (*domain.Profile, error) {
		count++
		return orig(configDir, gameID, name)
	}
	defer func() { loadProfile = orig }()

	fn()
	return count
}

// AfterProfileLoadForTest runs fn with after called straight after every
// profile load (loadProfile), given the number of loads so far - so a test
// can change a profile file between the read a Plan decides from and
// anything that Plan reads later (#431 fix round 3).
func AfterProfileLoadForTest(after func(loads int), fn func()) {
	orig := loadProfile
	loads := 0
	loadProfile = func(configDir, gameID, name string) (*domain.Profile, error) {
		p, err := orig(configDir, gameID, name)
		loads++
		after(loads)
		return p, err
	}
	defer func() { loadProfile = orig }()

	fn()
}
