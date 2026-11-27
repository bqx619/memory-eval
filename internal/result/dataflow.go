package result

// DataFlow describes runtime egress policy; install-time registry contacts are
// disclosed separately and do not turn a network-none run into an online run.
type DataFlow struct {
	Summary         string          `json:"summary"`
	Parties         []DataFlowParty `json:"parties"`
	ZeroEgressProof *DataFlowProof  `json:"zero_egress_proof"`
	Notes           []string        `json:"notes"`
}
type DataFlowParty struct {
	Metered     bool     `json:"metered,omitempty"`
	Role        string   `json:"role"`
	Endpoint    string   `json:"endpoint"`
	Host        string   `json:"host,omitempty"` // Legacy gateway field, retained for readers.
	Direction   string   `json:"direction"`
	Enforcement string   `json:"enforcement"`
	Evidence    []string `json:"evidence"`
}
type DataFlowProof struct {
	Scope               string                   `json:"scope"`
	NetworkMode         string                   `json:"network_mode"`
	Verified            bool                     `json:"verified"`
	NetworkEnforced     bool                     `json:"network_enforced"`
	SelftestSHA256      string                   `json:"selftest_sha256"`
	ConfigurationSHA256 string                   `json:"configuration_sha256"`
	Checks              map[string]DataFlowCheck `json:"checks"`
	RunClean            *bool                    `json:"run_clean"`
	RunCleanEvidence    string                   `json:"run_clean_evidence"`
}
type DataFlowCheck struct {
	Expected      bool  `json:"expected"`
	Observed      *bool `json:"observed"`
	ControlPassed bool  `json:"control_passed"`
	Passed        bool  `json:"passed"`
}
