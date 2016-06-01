package google_calendar

/*
 * Copyright 2016 Albert P. Tobey <atobey@netflix.com>
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import (
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/netflix/hal-9001/hal"
)

/* Even when attached, this plugin will not do anything until it is fully configured
 * for the room. At a mininum the calendar-id needs to be set. One or all of autoreply,
 * announce-start, and announce-end should be set to true to make anything happen.
 * Setting up:
 * !prefs set --room <roomid> --plugin google_calendar --key calendar-id --value <calendar link>
 *
 * autoreply: when set to true, the bot will reply with a message for any activity in the
 * room during hours when an event exists on the calendar. If the event has a description
 * set, that will be the text sent to the room. Otherwise a default message is generated.
 * !prefs set --room <roomid> --plugin google_calendar --key autoreply --value true
 *
 * announce-(start|end): the bot will automatically announce when an event is starting or
 * ending. The event's description will be included if it is not empty.
 * !prefs set --room <roomid> --plugin google_calendar --key announce-start --value true
 * !prefs set --room <roomid> --plugin google_calendar --key announce-end --value true
 *
 * timezone: optional, tells the bot which timezone to report dates in
 * !prefs set --room <roomid> --plugin google_calendar --key timezone --value America/Los_Angeles
 */

const DefaultTz = "America/Los_Angeles"
const DefaultMsg = "Calendar event: %q"

type Config struct {
	RoomId        string
	CalendarId    string
	Timezone      time.Location
	Autoreply     bool
	AnnounceStart bool
	AnnounceEnd   bool
	CalEvents     []CalEvent
	LastReply     time.Time
	mut           sync.Mutex
	configTs      time.Time
	calTs         time.Time
}

var configCache map[string]*Config
var topMut sync.Mutex

func init() {
	configCache = make(map[string]*Config)
}

func Register() {
	p := hal.Plugin{
		Name: "google_calendar",
		Func: handleEvt,
		Init: initData,
	}

	p.Register()
}

// initData primes the cache and starts the background goroutine
func initData(inst *hal.Instance) {
	topMut.Lock()
	config := Config{RoomId: inst.RoomId}
	configCache[inst.RoomId] = &config
	topMut.Unlock()

	// initiate the loading of events
	config.getCachedCalEvents(time.Now())

	// TODO: kick off background refresh
}

// handleEvt handles events coming in from the chat system. It does not interact
// directly with the calendar API and relies on the background goroutine to populate
// the cache.
func handleEvt(evt hal.Evt) {
	now := time.Now()
	config := getCachedConfig(evt.RoomId, now)
	calEvents, err := config.getCachedCalEvents(now)
	if err != nil {
		evt.Replyf("Error while getting calendar data: %s", err)
		return
	}

	for _, e := range calEvents {
		if config.Autoreply && e.Start.Before(now) && e.End.After(now) {
			lastReplyAge := now.Sub(config.LastReply)
			// TODO: track more detailed state to make squelching replies easier
			// for now: only reply once an hour
			if lastReplyAge.Hours() < 1 {
				log.Printf("not autoresponding because a message has been sent in the last hour")
				continue
			}

			if e.Description != "" {
				evt.Reply(e.Description)
			} else {
				evt.Replyf(DefaultMsg, e.Name)
			}

			config.LastReply = now
			// return // TODO: should overlapping events mean multiple messages?
		}
	}
}

// TODO: announce start / end

func getCachedConfig(roomId string, now time.Time) Config {
	topMut.Lock()
	c := configCache[roomId]
	topMut.Unlock()

	age := now.Sub(c.configTs)

	if age.Minutes() > 10 {
		c.LoadFromPrefs()
	}

	return *c
}

// getCachedEvents fetches the calendar data from the Google Calendar API,
// holding a mutex while doing so. This prevents handleEvt from firing until
// the first load of data is complete and will block the goroutines for a short
// time.
func (c *Config) getCachedCalEvents(now time.Time) ([]CalEvent, error) {
	c.mut.Lock()
	defer c.mut.Unlock()

	calAge := now.Sub(c.calTs)

	if calAge.Hours() > 1.5 {
		evts, err := getEvents(c.CalendarId, now)
		if err != nil {
			return nil, err
		} else {
			c.CalEvents = evts
		}
	}

	return c.CalEvents, nil
}

func (c *Config) LoadFromPrefs() error {
	c.mut.Lock()
	defer c.mut.Unlock()

	cidpref := hal.GetPref("", "", c.RoomId, "google_calendar", "calendar-id", "")
	if cidpref.Success {
		c.CalendarId = cidpref.Value
	} else {
		return fmt.Errorf("Failed to load calendar-id preference for room %q: %s", c.RoomId, cidpref.Error)
	}

	c.Autoreply = c.loadBoolPref("autoreply")
	c.AnnounceStart = c.loadBoolPref("announce-start")
	c.AnnounceEnd = c.loadBoolPref("announce-end")

	tzpref := hal.GetPref("", "", c.RoomId, "google_calendar", "timezone", DefaultTz)
	tz, err := time.LoadLocation(tzpref.Value)
	if err != nil {
		return fmt.Errorf("Could not load timezone info for '%s': %s\n", tzpref.Value, err)
	}
	c.Timezone = *tz

	c.configTs = time.Now()

	return nil
}

func (c *Config) loadBoolPref(key string) bool {
	pref := hal.GetPref("", "", c.RoomId, "google_calendar", key, "false")

	val, err := strconv.ParseBool(pref.Value)
	if err != nil {
		log.Printf("unable to parse boolean pref value: %s", err)
		return false
	}

	return val
}
