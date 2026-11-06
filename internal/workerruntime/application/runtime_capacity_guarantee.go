package application

// CapacityGuaranteeRef identifies a deployment-owned contract, never execution authority.
type CapacityGuaranteeRef struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Digest   string `json:"digest"`
}
