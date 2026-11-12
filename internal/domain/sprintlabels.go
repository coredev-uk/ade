package domain

// SprintLabels returns the labels that name a sprint under pattern
// ("Sprint::{n}"): the ones a sprint move must remove explicitly, because
// "::" labels are not exclusive on every tier. Labels whose value is not a
// positive integer are not sprint labels and are left out.
func SprintLabels(labels []string, pattern string) []string {
	var out []string
	for _, l := range labels {
		if v, ok := matchPattern(pattern, l); ok && isPositiveInt(v) {
			out = append(out, l)
		}
	}
	return out
}

func isPositiveInt(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
