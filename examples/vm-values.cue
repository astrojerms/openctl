// A values file for vm-parameterized.cue — the openctl analog of a Terraform
// -var-file. It fills the abstract fields and overrides defaults, unified into
// the manifest at apply time:
//
//   openctl ctl apply -f vm-parameterized.cue --values vm-values.cue
//
// Keep environment-specific values here (a prod.cue, a staging.cue, …) and the
// shape in the manifest.
spec: {
	node: "pve1"
	cpu: cores:   8
	memory: size: 8192
	disks: [{storage: "local-lvm"}]
}
