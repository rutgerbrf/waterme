package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/go-multierror"
)

type rawMessages struct {
	Thanks        []string `json:"thanks"`
	Reminder      []string `json:"reminder"`
	NoWater       []string `json:"noWater"`
	WateredButton []string `json:"wateredButton"`
}

func (rm rawMessages) convert() (messages, error) {
	return messageConverter{}.convert(rm)
}

type messageConverter struct {
	err error
}

func (mc messageConverter) convert(rm rawMessages) (messages, error) {
	mc.reset()

	return messages{
		thanks:        mc.loadTemplates("thanks", rm.Thanks),
		reminder:      mc.loadTemplates("reminder", rm.Reminder),
		noWater:       mc.loadTemplates("noWater", rm.NoWater),
		wateredButton: mc.loadTemplates("wateredButton", rm.WateredButton),
	}, mc.err
}

func (mc *messageConverter) reset() {
	mc.err = nil
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
