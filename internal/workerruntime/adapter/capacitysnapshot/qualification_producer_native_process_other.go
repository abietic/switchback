//go:build !linux

package capacitysnapshot

import "context"

func qualificationObserveFrozenRole(context.Context, qualificationRole) (qualificationFrozenProcess, error) {
	return qualificationFrozenProcess{}, errQualification
}
func qualificationInspectFrozenContainer(context.Context, qualificationRole, qualificationFrozenProcess, string, string) error {
	return errQualification
}
