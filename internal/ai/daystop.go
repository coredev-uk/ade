package ai

import (
	"errors"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/ledger"
)

// Budget stops, once a day. Every refused call is audited as budget.stop,
// but only a profile's first stop of the local day (user.timezone) raises the
// budget.stop event (pop-up, inbox, the external command); later ones are
// audited only. The audit log is what makes it "once" across processes, the
// way budget.warn is once per period (warn.go). On a day a profile was
// stopped, calls nobody asked for (Job.Auto, the dashboard's automatic
// summaries) are not made at all; the next day tries again, and a refusal
// then raises one new alert.

// ErrAutoPaused is returned instead of making an automatic call (Job.Auto) on
// a day its profile hit a budget stop. Nothing was sent or audited.
var ErrAutoPaused = errors.New("AI budget reached today: automatic AI calls are paused until tomorrow")

// DayStop is what the audit log holds about one profile's budget stops in
// the current local day.
type DayStop struct {
	// First is the day's first budget.stop of the profile, from any source.
	First audit.Entry
	// Provider is the day's first refusal from the provider side (gateway,
	// provider, omp); nil when only ADE's own caps refused.
	Provider *audit.Entry
}

// StopsToday reads the budget stops of the local day containing now, keyed
// by profile.
func StopsToday(log *audit.Log, now time.Time, loc *time.Location) (map[string]DayStop, error) {
	es, err := log.Query(audit.Filter{Action: ledger.StopAction, Since: ledger.DayStart(now, loc)})
	if err != nil {
		return nil, err
	}
	out := map[string]DayStop{}
	for i := range es {
		e := es[i]
		p, _ := e.Params["profile"].(string)
		d, seen := out[p]
		if !seen {
			d.First = e
		}
		if src, _ := e.Params["source"].(string); src != "local" && d.Provider == nil {
			d.Provider = &e
		}
		out[p] = d
	}
	return out, nil
}

// Stopped is one refused call to record.
type Stopped struct {
	Item, Feature, Profile, Model string
	Stop                          *StopError
}

// RecordStop writes the budget.stop audit entry and, when it is the
// profile's first stop of the day, publishes the budget.stop event. bus may
// be nil. When the log cannot be read it stays quiet rather than alert on
// every call; the refusal itself still reaches the caller. The entry is
// stamped with now, the caller's clock.
func RecordStop(log *audit.Log, bus *events.Bus, now time.Time, loc *time.Location, s Stopped) {
	first := false
	if stops, err := StopsToday(log, now, loc); err == nil {
		_, seen := stops[s.Profile]
		first = !seen
	}
	_ = log.Append(audit.Entry{
		TS: now.UTC(), Action: ledger.StopAction, Target: s.Item, Item: s.Item, Outcome: "error", Error: s.Stop.Message,
		Params: map[string]any{"feature": s.Feature, "profile": s.Profile, "model": s.Model, "source": s.Stop.Source},
	})
	if !first || bus == nil {
		return
	}
	e := events.New(ledger.StopAction, events.Error, "AI budget reached: "+s.Stop.Message)
	e.Subject = s.Item
	e.Detail = "Profile " + s.Profile + ". Further refusals today are audited without another notification, and automatic AI calls wait until tomorrow."
	bus.Publish(e)
}

// stoppedToday reports whether profile has a budget stop recorded today. An
// unreadable log counts as no stop: the call's own pre-flight still applies.
func (s *Service) stoppedToday(profile string) bool {
	stops, err := StopsToday(s.Audit, s.Now(), s.Loc)
	if err != nil {
		return false
	}
	_, ok := stops[profile]
	return ok
}
