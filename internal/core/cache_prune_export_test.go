package core

// SnapshotsDirForTest exposes gameID's snapshot directory, so a test can
// write a snapshot document naming versions no current state does (#678).
func (s *Service) SnapshotsDirForTest(gameID string) string {
	return snapshotsDirFor(s.dataDir, gameID)
}
