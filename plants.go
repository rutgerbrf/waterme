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

func newPlantsSM(log logr.Logger) plantsSM {
	return plantsSM{
		stateMachine: newStateMachine(log),
		state:        plantsStateHealthy,
	}
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
	log     logr.Logger
}

func newStateMachine(log logr.Logger) stateMachine {
	return stateMachine{
		actionc: make(chan func()),
		log:     log,
	}
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
						s.log.Error(errors.New("panicked"), "state machine panicked",
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
