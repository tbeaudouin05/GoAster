package skills

import _ "embed"

// OVHProvisioning contains the read-only-first OVH onboarding instructions
// that are packaged into the GoAster executable.
//
//go:embed ovh/SKILL.md
var OVHProvisioning string

// OVHRemote contains the existing-host SSH onboarding instructions that are
// packaged into the GoAster executable.
//
//go:embed ovh-remote/SKILL.md
var OVHRemote string

// InternetFetch contains reusable guidance for reliable public internet
// content retrieval that is packaged into the GoAster executable.
//
//go:embed internet-fetch/SKILL.md
var InternetFetch string

// TelegramGroupPrivacy contains guidance for Telegram group setup.
//
//go:embed telegram-group-privacy/SKILL.md
var TelegramGroupPrivacy string

// MemoryConsolidation contains the optional user-managed memory workflow.
//
//go:embed self-improvement-memory-consolidation/SKILL.md
var MemoryConsolidation string
