//go:build !linux && !darwin

package stopper

import "github.com/AnindyaMajumder/Portly/internal/domain"

// pinProcess refuses until this platform's executor lands.
func pinProcess(domain.ProcessID) (pin, error) {
	return nil, domain.NewError(domain.CodePlatform, "Stopping is not enabled on this platform", "Use the operating system's own tools")
}
