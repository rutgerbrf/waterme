package main

import (
	"context"
	"errors"
	"runtime/debug"

	"github.com/go-logr/logr"
)

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
