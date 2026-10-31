package skills

import _ "embed"

// OVHProvisioning contains the read-only-first OVH onboarding instructions
// that are packaged into the GoAster executable.
//
//go:embed ovh/SKILL.md
var OVHProvisioning string

// InternetFetch contains reusable guidance for reliable public internet
// content retrieval that is packaged into the GoAster executable.
//
//go:embed internet-fetch/SKILL.md
var InternetFetch string
