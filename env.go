package main

import (
	"fmt"
	"os"

	"github.com/hashicorp/go-multierror"
)

const (
	envSlackToken         = "WATERME_SLACK_TOKEN"
	envSlackSigningSecret = "WATERME_SLACK_SIGNING_SECRET"
	envRemindSchedule     = "WATERME_REMIND_SCHEDULE"
	envNoWaterRemindDelay = "WATERME_NO_WATER_REMIND_DELAY"
	envNoWaterSchedule    = "WATERME_NO_WATER_SCHEDULE"
	envMessagesPath       = "WATERME_MESSAGES_PATH"
	envChannel            = "WATERME_CHANNEL"
	envCbAddr             = "WATERME_CB_ADDR"
	envTZ                 = "WATERME_TZ"
)

type envError struct {
	env string
	err error
}

func (e envError) Env() string {
	return e.env
}

func (e envError) Error() string {
	return e.err.Error()
}

func (e envError) Unwrap() error {
	return e.err
}

type envNotSetError struct {
	envError
}

type envInvalidValueError struct {
	envError
}

func newEnvNotSetError(env string) error {
	return envNotSetError{
		envError: envError{
			env: env,
			err: fmt.Errorf("environment variable %s is not set", env),
		},
	}
}

func newEnvInvalidValueError(env string, err error) error {
	return envInvalidValueError{
		envError: envError{
			env: env,
			err: fmt.Errorf("environment variable %s has an invalid value: %w", env, err),
		},
	}
}

type envLoader struct {
	err error
}

func (e *envLoader) getRequiredEnv(name string) string {
	val := os.Getenv(name)
	if val == "" {
		e.err = multierror.Append(e.err, newEnvNotSetError(name))
	}
	return val
}
