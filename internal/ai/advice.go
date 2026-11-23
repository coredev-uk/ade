package ai

// Some answers carry advice that the text they were written from can steer: an
// MR summary's `Risk:` line (the MR author wrote the description and the diff)
// and a CI triage's `Fix:` lines (a job log can say anything). The prompts wrap
// that text as untrusted data, which is hygiene and not a boundary, so wherever
// such an answer is shown it says whose words it came from. It is never
// presented as ADE's own verdict.

// Advice returns, for a feature whose answer can be steered by forge text, the
// two guide words the TUI sets on the answer: lead replaces the plain `AI`
// (`AI`, `AI suggestion`) and from says where it came from and what to do
// (`from MR text, check it`). Both are "" for a feature with nothing to flag.
func Advice(feature string) (lead, from string) {
	switch feature {
	case "summarize":
		return "AI", "from MR text, check it"
	case "ci-triage":
		return "AI suggestion", "from the job log, check it"
	}
	return "", ""
}

// AdviceNote is the one line `ade ai summarize` and `ade ai ci-triage` print
// (and carry as `note` in JSON) under such an answer; "" for any other feature.
func AdviceNote(feature string) string {
	switch feature {
	case "summarize":
		return "note: a model wrote this from the MR's own text, so its Risk line can be steered by the MR: check it before you rely on it"
	case "ci-triage":
		return "note: a model wrote these fixes from the job logs, which whoever can push to the MR controls: check them before you run anything"
	}
	return ""
}
