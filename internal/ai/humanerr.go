package ai

import (
	"context"
	"errors"
	"net"
	"os/exec"
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
	case errors.As(err, &ne):
		return "the AI service could not be reached · check the network and try again"
	}
	return "the AI call failed · the Log page has the details"
}
