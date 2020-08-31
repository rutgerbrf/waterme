package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/joho/godotenv"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
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
