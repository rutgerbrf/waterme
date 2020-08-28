package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/hashicorp/go-multierror"
	"github.com/joho/godotenv"
	"github.com/julienschmidt/httprouter"
	"github.com/robfig/cron/v3"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
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

	actionIDPlantsWatered = "plants-watered"
)

func main() {
	zlog, err := zap.NewProduction()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "could not instantiate logger: %v", err)
		os.Exit(1)
	}
	log := zapr.NewLogger(zlog)

	if err := realMain(log); err != nil {
		log.Error(err, "an error occurred")
		os.Exit(1)
	}
}

func realMain(log logr.Logger) error {
	_ = godotenv.Load()

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("could not load configuration: %w", err)
	}

	slackAPI := slack.New(cfg.slackToken)
	carer := newCarer(&cfg, slackAPI, log)

	ctx, cancel := context.WithCancel(context.Background())
	go callOnShutdown(cancel)

	return carer.run(ctx)
}

func callOnShutdown(f func()) {
	sigc := make(chan os.Signal, 1)

	signal.Notify(sigc, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(sigc)

	<-sigc
	f()
}

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

func loadConfig() (config, error) {
	rawCfg, err := loadRawConfig()
	if err != nil {
		return config{}, err
	}
	return rawCfg.process()
}

func (c rawConfig) process() (config, error) {
	return configConverter{}.process(c)
}

type configConverter struct {
	err error
}

func (cc configConverter) process(c rawConfig) (config, error) {
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

type rawMessages struct {
	Thanks        []string `json:"thanks"`
	Reminder      []string `json:"reminder"`
	NoWater       []string `json:"noWater"`
	WateredButton []string `json:"wateredButton"`
}

func (rm *rawMessages) convert() (messages, error) {
	return (&messageConverter{}).convert(rm)
}

type messageConverter struct {
	err error
}

func (mc *messageConverter) reset() {
	mc.err = nil
}

func (mc *messageConverter) convert(rm *rawMessages) (messages, error) {
	mc.reset()
	return messages{
		thanks:        mc.loadTemplates("thanks", rm.Thanks),
		reminder:      mc.loadTemplates("reminder", rm.Reminder),
		noWater:       mc.loadTemplates("noWater", rm.NoWater),
		wateredButton: mc.loadTemplates("wateredButton", rm.WateredButton),
	}, mc.err
}

func (mc *messageConverter) loadTemplates(listName string, s []string) []*template.Template {
	templates := make([]*template.Template, 0, len(s))

	for i, str := range s {
		tpl, err := template.New("message").Parse(str)
		if err != nil {
			mc.err = multierror.Append(mc.err,
				fmt.Errorf("could not load template %d (0-indexed) from list %s: %w; value: %q",
					i, listName, err, str,
				),
			)
		}

		templates = append(templates, tpl)
	}

	return templates
}

func loadMessagesFromFile(path string) (messages, error) {
	// perm is not set because we're not creating the file.
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return messages{}, fmt.Errorf("could not open messages file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	return loadMessages(f)
}

func loadMessages(r io.Reader) (messages, error) {
	var raw rawMessages

	err := json.NewDecoder(r).Decode(&raw)
	if err != nil {
		return messages{}, fmt.Errorf("could not decode messages JSON: %w", err)
	}

	return raw.convert()
}

type thanksTemplateData struct {
	User string
}

type messages struct {
	thanks,
	reminder,
	noWater,
	wateredButton []*template.Template
}

type templatedMessages struct {
	thanks, reminder, noWater, wateredButton string
}

func (m *messages) getRandom(getters ...func(m *templatedMessages) error) (templatedMessages, error) {
	var tm templatedMessages

	for _, g := range getters {
		err := g(&tm)
		if err != nil {
			return tm, err
		}
	}

	return tm, nil
}

func (m *messages) randomWateredButtonMessage() func(dest *templatedMessages) error {
	return func(dest *templatedMessages) (err error) {
		dest.wateredButton, err = templateRandomMessage(m.wateredButton, nil)
		return
	}
}

func (m *messages) randomNoWaterMessage() func(dest *templatedMessages) error {
	return func(dest *templatedMessages) (err error) {
		dest.noWater, err = templateRandomMessage(m.noWater, nil)
		return
	}
}

func (m *messages) randomReminderMessage() func(dest *templatedMessages) error {
	return func(dest *templatedMessages) (err error) {
		dest.reminder, err = templateRandomMessage(m.reminder, nil)
		return
	}
}

func (m *messages) randomThanksMessage(data thanksTemplateData) func(dest *templatedMessages) error {
	return func(dest *templatedMessages) (err error) {
		dest.thanks, err = templateRandomMessage(m.thanks, data)
		return
	}
}

func randomMessage(s []*template.Template) (*template.Template, bool) {
	rand.Seed(time.Now().UnixNano())
	if len(s) == 0 {
		return nil, false
	}
	return s[rand.Intn(len(s))], true
}

func templateRandomMessage(s []*template.Template, data interface{}) (string, error) {
	msg, ok := randomMessage(s)
	if !ok {
		return "", errors.New("no messages available in list")
	}

	var w strings.Builder
	err := msg.ExecuteTemplate(&w, "message", data)
	if err != nil {
		return "", err
	}
	return w.String(), nil
}

type carer struct {
	cfg    *config
	log    logr.Logger
	slack  *slack.Client
	router *httprouter.Router
	plants plantsSM
}

func newCarer(cfg *config, slack *slack.Client, log logr.Logger) *carer {
	carer := &carer{
		cfg:    cfg,
		log:    log,
		slack:  slack,
		router: httprouter.New(),
		plants: plantsSM{},
	}
	carer.registerRoutes()

	return carer
}

func (c *carer) registerRoutes() {
	c.router.POST("/slack/actions", c.handleSlackActionsCallback)
}

func (c *carer) run(ctx context.Context) error {
	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		return c.runCronJobs(ctx)
	})

	eg.Go(func() error {
		return c.handleEvents(ctx)
	})

	eg.Go(func() error {
		return c.plants.run(ctx)
	})

	return eg.Wait()
}

func (c *carer) handleEvents(ctx context.Context) error {
	srv := &http.Server{
		Addr:    c.cfg.cbAddr,
		Handler: c.router,
	}

	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		errs <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		_ = srv.Close()
		return ctx.Err()
	case err := <-errs:
		return err
	}
}

type readCloser struct {
	io.Reader
	close func() error
}

func (c readCloser) Close() error {
	return c.close()
}

func teeReadCloser(r io.ReadCloser, w io.Writer) io.ReadCloser {
	tr := io.TeeReader(r, w)

	return readCloser{Reader: tr, close: r.Close}
}

func (c *carer) handleSlackActionsCallback(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var payload slack.InteractionCallback

	v, err := slack.NewSecretsVerifier(r.Header, c.cfg.slackSigningSecret)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	r.Body = teeReadCloser(r.Body, &v)
	err = json.Unmarshal([]byte(r.FormValue("payload")), &payload)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = v.Ensure()
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	for _, act := range payload.ActionCallback.BlockActions {
		if act.ActionID == actionIDPlantsWatered {
			// :)
			c.plants.Transition(plantsStateHealthy)
			c.thank(payload.User.Name)
		}
	}
}

func (c *carer) runCronJobs(ctx context.Context) error {
	cr := cron.New(
		cron.WithLocation(c.cfg.location),
		cron.WithLogger(c.log),
	)
	cr.Schedule(c.cfg.remindSchedule, cron.FuncJob(c.remind))
	cr.Schedule(c.cfg.noWaterSchedule, cron.FuncJob(c.noWater))
	cr.Start()

	for {
		select {
		case <-ctx.Done():
			<-cr.Stop().Done()
			return ctx.Err()
		}
	}
}

func (c *carer) remind() {
	if c.plants.State() == plantsStateDying {
		return // :(
	}

	msgs, err := c.cfg.messages.getRandom(
		c.cfg.messages.randomReminderMessage(),
		c.cfg.messages.randomWateredButtonMessage(),
	)
	if err != nil {
		c.log.Error(err, "(remind) could not load messages to send message with")
		return
	}

	_, _, err = c.slack.PostMessage(c.cfg.channel,
		slack.MsgOptionBlocks(
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn",
					msgs.reminder,
					/* emoji */ false /* verbatim */, false,
				),
				nil, nil,
			),
			slack.NewActionBlock("watering",
				slack.NewButtonBlockElement(actionIDPlantsWatered, "",
					slack.NewTextBlockObject(
						"plain_text", msgs.wateredButton,
						/* emoji */ true /* verbatim */, false,
					),
				).WithStyle(slack.StylePrimary),
			),
		),
	)
	if err != nil {
		c.log.Error(err, "could not send Slack (remind) message")
	}
	c.plants.Transition(plantsStateThirsty)

	go func() {
		time.Sleep(c.cfg.noWaterRemindDelay)

		if c.plants.State() == plantsStateThirsty {
			c.noWater()
			c.plants.Transition(plantsStateDying)
		}
	}()
}

func (c *carer) noWater() {
	if c.plants.State() != plantsStateDying {
		return // :)
	}

	msgs, err := c.cfg.messages.getRandom(
		c.cfg.messages.randomNoWaterMessage(),
		c.cfg.messages.randomWateredButtonMessage(),
	)
	if err != nil {
		c.log.Error(err, "(noWater) could not load messages to send message with")
		return
	}

	_, _, err = c.slack.PostMessage(c.cfg.channel,
		slack.MsgOptionBlocks(
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn",
					msgs.noWater,
					/* emoji */ false /* verbatim */, false,
				),
				nil, nil,
			),
			slack.NewActionBlock("watering",
				slack.NewButtonBlockElement(actionIDPlantsWatered, "",
					slack.NewTextBlockObject(
						"plain_text", msgs.wateredButton,
						/* emoji */ true /* verbatim */, false,
					),
				).WithStyle(slack.StylePrimary),
			),
		),
	)
	if err != nil {
		c.log.Error(err, "could not send Slack (noWater) message")
	}
}

func (c *carer) thank(user string) {
	msgs, err := c.cfg.messages.getRandom(
		c.cfg.messages.randomThanksMessage(
			thanksTemplateData{
				User: user,
			},
		),
	)
	if err != nil {
		c.log.Error(err, "(noWater) could not load messages to send message with")
		return
	}

	_, _, _, err = c.slack.SendMessageContext(context.Background(), c.cfg.channel,
		slack.MsgOptionBlocks(
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn",
					msgs.thanks /* emoji */, false /* verbatim */, false,
				),
				nil, nil,
			),
		),
	)
	if err != nil {
		c.log.Error(err, "could not send Slack (thank) message")
	}
}

type stateMachine struct {
	actionc chan func()
	logger  logr.Logger
}

func (s *stateMachine) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f := <-s.actionc:
			func() {
				defer func() {
					if r := recover(); r != nil {
						s.logger.Error(errors.New("panicked"), "state machine panicked",
							"stack", string(debug.Stack()),
							"panicData", r,
						)
					}
				}()

				f()
			}()
		}
	}
}

type plantsState int

const (
	plantsStateHealthy plantsState = iota
	plantsStateDying
	plantsStateThirsty
)

type plantsSM struct {
	stateMachine
	state plantsState
}

func (p *plantsSM) State() plantsState {
	statec := make(chan plantsState)
	p.actionc <- func() {
		statec <- p.state
	}
	return <-statec
}

func (p *plantsSM) Transition(new plantsState) {
	p.actionc <- func() {
		p.state = new
	}
}
