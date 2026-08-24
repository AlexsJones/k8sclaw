package v1alpha1

import "strings"

// ReservedNamePrefix marks a name as Sympozium's own.
//
// Sympozium injects things into a run's namespaces that look, to the agent,
// exactly like things an operator configured — an MCP server on loopback that
// serves the run's SkillPack tools, for instance. Those need names, and a name
// an operator can also choose is a name an operator can shadow.
//
// So the prefix is reserved: anything starting with it, in any case, belongs to
// Sympozium and is rejected on a user-supplied field. This is the same reasoning
// as reservedVolumeNames and builtinToolNames in the admission webhook — a
// collision that the runtime would resolve silently is refused up front instead.
//
// Reserving a prefix rather than a fixed list means a future internal name
// needs no new rejection rule, and no existing manifest breaks the day one is
// added.
const ReservedNamePrefix = "sympozium"

// IsReservedName reports whether a user-supplied name intrudes on Sympozium's
// namespace. Case-insensitive: the comparison must not depend on how a client
// happened to capitalise, since the consumers differ (an MCP client namespacing
// tools is case-sensitive, Kubernetes names are lowercase).
func IsReservedName(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), ReservedNamePrefix)
}
