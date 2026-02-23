package tunnel

import "errors"

type ActionableDetails struct {
	Code      string
	What      string
	Why       string
	Checks    []string
	NextSteps []string
}

type ActionableError interface {
	error
	Actionable() ActionableDetails
}

func ActionableFromError(err error) (ActionableDetails, bool) {
	var ae ActionableError
	if !errors.As(err, &ae) {
		return ActionableDetails{}, false
	}
	return ae.Actionable(), true
}
