package gateway

import "github.com/JuliusBrussee/caveman/proxy/providers"

func (s *Server) logToolPreservation(body []byte, meta providers.RequestMetadata) {
	if s.logger == nil || !meta.PreserveTools.Enabled() {
		return
	}
	report := providers.AuditPreservation(body, meta)
	s.logger.Info("tool preservation request", "protocol", report.Protocol, "supported", report.Supported,
		"protected_tools", report.Tools, "protected_results", report.ProtectedResults,
		"unresolved_results", report.UnresolvedResults, "ambiguous_ids", report.AmbiguousIDs)
}
