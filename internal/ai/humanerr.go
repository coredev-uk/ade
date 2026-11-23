package ai

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"regexp"

	"github.com/coredev-uk/ade/internal/config"
)

// NotInstalledError is a driver whose command is not on PATH. Its text is
// the raw exec error, which is what the audit log keeps; Human words it for
// a person.
type NotInstalledError struct {
	Driver string
	Err    error
}

func (e *NotInstalledError) Error() string { return e.Driver + ": " + e.Err.Error() }
func (e *NotInstalledError) Unwrap() error { return e.Err }

// ErrBadAnswer marks a model answer ADE could not read as a suggestion.
var ErrBadAnswer = errors.New("the model's answer was not usable")

// badAnswer carries the parse failure's detail for the log while matching
// ErrBadAnswer.
type badAnswer struct{ err error }

func (e *badAnswer) Error() string {
	return "classify: the model's answer is not the expected JSON: " + e.err.Error()
}
func (e *badAnswer) Unwrap() error        { return e.err }
func (e *badAnswer) Is(target error) bool { return target == ErrBadAnswer }

// Human words a failed AI call as a cause and a way forward, one line, with
// no config keys beyond the one setting a person would change and none of
// the raw error: that stays in the audit log, which every failed call is
// written to.
func Human(err error) string {
	var ni *NotInstalledError
	var ne net.Error
	switch stop, isStop := AsStop(err); {
	case errors.Is(err, ErrDisabled):
		return "AI is off · set ai.mode to ask or auto to use it"
	case errors.Is(err, config.ErrNoProfile):
		return "no AI profile is selected · set ai.profile in the config"
	case errors.Is(err, config.ErrProfileUndefined):
		return "the selected AI profile is not defined · check ai.profile in the config"
	case errors.Is(err, config.ErrNoSecret):
		return "the AI profile has no API key · set key_env or key_cmd on it"
	case errors.Is(err, ErrAutoPaused):
		return "automatic AI calls are paused until tomorrow (the budget was reached) · ask by hand to go on"
	case errors.As(err, &ni), errors.Is(err, exec.ErrNotFound):
		name := "the AI command"
		if ni != nil {
			name = ni.Driver
		}
		return name + " is not installed · install it or set ai.mode = off"
	case isStop && stop.Source == "local":
		return "a spending cap in ADE stopped the call · the Budget page shows the caps"
	case isStop:
		return "the provider's own budget is used up · the Budget page shows its figures"
	case errors.Is(err, ErrBadAnswer):
		return "the model's answer was not usable · try again"
	case errors.Is(err, context.DeadlineExceeded):
		return "the model took too long to answer · try again"
	case errors.Is(err, context.Canceled):
		return "cancelled · nothing was sent"
	case errors.Is(err, ErrCancelled):
		return "cancelled at the pre-flight prompt · nothing was sent"
	case httpStatus(err) == 401 || httpStatus(err) == 403:
		return "the AI service refused the key · check key_env or key_cmd on the profile"
	case httpStatus(err) == 429:
		return "the AI service is rate limiting · wait a moment and try again"
	case errors.As(err, &ne):
		return "the AI service could not be reached · check the network and try again"
	}
	return "the AI call failed · the Log page has the details"
}

var httpStatusRe = regexp.MustCompile(`HTTP (\d{3})\b`)

// httpStatus is the status code of a driver's "HTTP 401: ..." error text, 0
// when err carries none. The drivers word every non-2xx answer that way.
func httpStatus(err error) int {
	m := httpStatusRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n := 0
	for _, c := range m[1] {
		n = n*10 + int(c-'0')
	}
	return n
}
