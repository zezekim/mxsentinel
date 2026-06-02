package telemetry

import "strings"

// classifyProvider maps a destination MX hostname to a coarse provider label used for
// deliverability analytics (gmail/microsoft/yahoo/...). Returns "other" when unknown and
// "" when host is empty.
func classifyProvider(mxHost string) string {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(mxHost), "."))
	if h == "" {
		return ""
	}
	switch {
	case strings.Contains(h, "google.com") || strings.Contains(h, "googlemail.com"):
		return "google"
	case strings.Contains(h, "outlook.com") || strings.Contains(h, "hotmail.com") ||
		strings.Contains(h, "office365.com") || strings.Contains(h, "protection.outlook"):
		return "microsoft"
	case strings.Contains(h, "yahoodns.net") || strings.Contains(h, "yahoo.com") ||
		strings.Contains(h, "yahoo.co"):
		return "yahoo"
	case strings.Contains(h, "icloud.com") || strings.Contains(h, "apple.com") ||
		strings.Contains(h, "me.com"):
		return "apple"
	case strings.Contains(h, "protonmail") || strings.Contains(h, "proton.me"):
		return "proton"
	case strings.Contains(h, "pphosted.com") || strings.Contains(h, "proofpoint"):
		return "proofpoint"
	case strings.Contains(h, "mimecast"):
		return "mimecast"
	default:
		return "other"
	}
}
