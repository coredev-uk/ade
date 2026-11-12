package ai

import "github.com/coredev-uk/ade/internal/audit"

// auditSpend is an ai.* entry that spent usd today, for cap tests.
func auditSpend(usd float64) audit.Entry {
	return audit.Entry{Action: "ai.summarize", Item: "x", AI: &audit.AIInfo{Profile: "p", Model: "m", CostUSD: usd, CostSource: CostReported}}
}
