package main

import (
	"fmt"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/robfig/cron/v3"
)

type rawConfig struct {
	slackToken         string
	slackSigningSecret string
	remindSchedule     string
	noWaterRemindDelay string
	noWaterSchedule    string
	messagesPath       string
	channel            string
	cbAddr             string
	tz                 string
}

type config struct {
	slackToken         string
	slackSigningSecret string
	remindSchedule     cron.Schedule
	noWaterRemindDelay time.Duration
	noWaterSchedule    cron.Schedule
	channel            string
	location           *time.Location
	cbAddr             string
	messages           messages
}

func loadRawConfig() (cfg rawConfig, err error) {
	var el envLoader

	cfg = rawConfig{
		slackToken:         el.getRequiredEnv(envSlackToken),
		slackSigningSecret: el.getRequiredEnv(envSlackSigningSecret),
		remindSchedule:     el.getRequiredEnv(envRemindSchedule),
		noWaterRemindDelay: el.getRequiredEnv(envNoWaterRemindDelay),
		noWaterSchedule:    el.getRequiredEnv(envNoWaterSchedule),
		messagesPath:       el.getRequiredEnv(envMessagesPath),
		channel:            el.getRequiredEnv(envChannel),
		tz:                 el.getRequiredEnv(envTZ),
		cbAddr:             el.getRequiredEnv(envCbAddr),
	}

	return cfg, el.err
}

func loadConfig() (config, error) {
	rawCfg, err := loadRawConfig()
	if err != nil {
		return config{}, err
	}
	return rawCfg.convert()
}

func (c rawConfig) convert() (config, error) {
	return configConverter{}.convert(c)
}

type configConverter struct {
	err error
}

func (cc configConverter) convert(c rawConfig) (config, error) {
	cc.reset()

	return config{
		slackToken:         c.slackToken,
		slackSigningSecret: c.slackSigningSecret,
		remindSchedule:     cc.cronParseSchedule(envRemindSchedule, c.remindSchedule),
		noWaterRemindDelay: cc.timeParseDuration(envNoWaterRemindDelay, c.noWaterRemindDelay),
		noWaterSchedule:    cc.cronParseSchedule(envNoWaterSchedule, c.noWaterSchedule),
		channel:            c.channel,
		location:           cc.timeLoadLocation(envTZ, c.tz),
		cbAddr:             c.cbAddr,
		messages:           cc.loadMessages(envMessagesPath, c.messagesPath),
	}, cc.err
}

func (cc *configConverter) reset() {
	cc.err = nil
}

func (cc *configConverter) timeParseDuration(isEnv, dur string) time.Duration {
	td, err := time.ParseDuration(dur)
	if err != nil {
		cc.err = multierror.Append(cc.err, newEnvInvalidValueError(isEnv, err))
		return 0
	}
	return td
}

func (cc *configConverter) timeLoadLocation(isEnv, name string) *time.Location {
	tl, err := time.LoadLocation(name)
	if err != nil {
		cc.err = multierror.Append(cc.err, newEnvInvalidValueError(isEnv, err))
		return nil
	}
	return tl
}

func (cc *configConverter) cronParseSchedule(isEnv, spec string) cron.Schedule {
	cs, err := cron.ParseStandard(spec)
	if err != nil {
		cc.err = multierror.Append(cc.err, newEnvInvalidValueError(isEnv, err))
		return nil
	}
	return cs
}

func (cc *configConverter) loadMessages(isEnv, path string) messages {
	msgs, err := loadMessagesFromFile(path)
	if err != nil {
		cc.err = multierror.Append(cc.err, envError{
			env: isEnv,
			err: fmt.Errorf("could not load messages from path specified in %s: %w", isEnv, err),
		})
		return messages{}
	}
	return msgs
}
