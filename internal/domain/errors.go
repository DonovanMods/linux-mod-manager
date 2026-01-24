package domain

import "errors"

var (
	ErrModNotFound     = errors.New("mod not found")
	ErrGameNotFound    = errors.New("game not found")
	ErrProfileNotFound = errors.New("profile not found")
	ErrDependencyLoop  = errors.New("circular dependency detected")
	ErrAuthRequired    = errors.New("authentication required")
	ErrInvalidConfig   = errors.New("invalid configuration")
	ErrFileConflict    = errors.New("file conflict detected")
	ErrDownloadFailed  = errors.New("download failed")
	ErrLinkFailed      = errors.New("link operation failed")
)
