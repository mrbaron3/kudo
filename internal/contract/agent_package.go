package contract

// AgentPackageSchemaV1Alpha1 は repository 所有の portable Agent Package manifest schema である。
const AgentPackageSchemaV1Alpha1 = "kudo.agent-package/v1alpha1"

// AgentPackageRef は package manifest と全 component digest の closure を指す。
// path は deployment ごとに異なり得るため identity に含めず、schema と digest だけを
// Review Request へ固定する。
type AgentPackageRef struct {
	Schema string `json:"schema"`
	Digest Digest `json:"digest"`
}

// Valid は未知 version を許容しつつ、Agent Package schema family と digest を検証する。
func (r AgentPackageRef) Valid() bool {
	return validSchemaIdentity(r.Schema, agentPackageSchemaPrefix) && r.Digest.Valid()
}
