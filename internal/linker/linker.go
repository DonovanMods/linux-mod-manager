package linker

import "github.com/DonovanMods/linux-mod-manager/internal/domain"

// Linker deploys and undeploys mod files to game directories
type Linker interface {
	Deploy(src, dst string) error
	Undeploy(dst string) error
	IsDeployed(dst string) (bool, error)
	Method() domain.LinkMethod
}

// New creates a linker for the given method
func New(method domain.LinkMethod) Linker {
	switch method {
	case domain.LinkHardlink:
		return NewHardlink()
	case domain.LinkCopy:
		return NewCopy()
	default:
		return NewSymlink()
	}
}
