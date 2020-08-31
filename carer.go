package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"github.com/julienschmidt/httprouter"
	"github.com/robfig/cron/v3"
	"github.com/slack-go/slack"
	"golang.org/x/sync/errgroup"
)

const actionIDPlantsWatered = "plants-watered"

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
		plants: newPlantsSM(log),
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
	io.Closer
}

func teeReadCloser(r io.ReadCloser, w io.Writer) io.ReadCloser {
	tr := io.TeeReader(r, w)

	return readCloser{
		Reader: tr,
		Closer: r,
	}
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
