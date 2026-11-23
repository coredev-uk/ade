package ai

import "slices"

// PickableLabels lists the full label names a person can add by hand for the
// facets the issue lacks (only those named, when facets is given): the team's known types (the three conventional ones
// when none are known) and the manifest's apps, through the manifest's label
// patterns. It is what classify offers when no suggestion fits, so the
// dialog is never a dead end. Nothing here calls a model.
func PickableLabels(in ClassifyInput, facets ...string) []string {
	var out []string
	miss := in.Missing()
	if len(facets) > 0 {
		miss = slices.DeleteFunc(miss, func(f string) bool { return !slices.Contains(facets, f) })
	}
	if slices.Contains(miss, "type") {
		types := sortedCopy(in.KnownTypes)
		if len(types) == 0 {
			for _, tk := range typeKeywords {
				types = append(types, tk.value)
			}
			slices.Sort(types)
		}
		for _, t := range types {
			out = append(out, applyPattern(in.Labels.Type.Pattern, t))
		}
	}
	if slices.Contains(miss, "app") {
		for _, a := range sortedCopy(in.KnownApps) {
			out = append(out, applyPattern(in.Labels.App.Pattern, a))
		}
	}
	return slices.Compact(out)
}
