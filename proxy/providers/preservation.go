package providers

// ProtectedResult excludes protected and unresolved results when policy is enabled.
// Duplicate call IDs are conservatively protected if any matching call is protected.
func ProtectedResult(calls map[string]bool, id string, enabled bool) bool {
	protected, known := calls[id]
	return protected || (enabled && (!known || id == ""))
}
