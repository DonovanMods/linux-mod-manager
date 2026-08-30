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
