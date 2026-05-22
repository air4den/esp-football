package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// ── Configuration ─────────────────────────────────────────────────────────────

const teamID = "218" // 364 = Liverpool FC. Change to any ESPN soccer team ID.
// Examples: 660=USA national, 655=Saudi Arabia, 382=Man City, 478=France

// ── URLs ──────────────────────────────────────────────────────────────────────

const (
	coreEventsURL = "https://sports.core.api.espn.com/v2/sports/soccer/teams/" + teamID + "/events?limit=20"
	summaryFmt    = "https://site.api.espn.com/apis/site/v2/sports/soccer/%s/summary?event=%s"
)

var (
	httpClient    = &http.Client{Timeout: 10 * time.Second}
	leagueEventRe = regexp.MustCompile(`leagues/([^/]+)/events/(\d+)`)
)

// ── ESPN JSON structs ─────────────────────────────────────────────────────────

type coreEventsResp struct {
	Items []struct {
		Ref string `json:"$ref"`
	} `json:"items"`
}

type summaryStatusType struct {
	State string `json:"state"`
}

type summaryStatus struct {
	DisplayClock *string           `json:"displayClock"`
	Type         summaryStatusType `json:"type"`
}

type summaryTeam struct {
	Abbreviation string `json:"abbreviation"`
}

type summaryCompetitor struct {
	ID       string      `json:"id"`
	HomeAway string      `json:"homeAway"`
	Score    interface{} `json:"score"` // int or nil
	Team     summaryTeam `json:"team"`
}

type summaryCompetition struct {
	Date        string              `json:"date"`
	Status      summaryStatus       `json:"status"`
	Competitors []summaryCompetitor `json:"competitors"`
}

type summaryHeader struct {
	Competitions []summaryCompetition `json:"competitions"`
}

type summaryResp struct {
	Header summaryHeader `json:"header"`
}

// ── Output ────────────────────────────────────────────────────────────────────

type response struct {
	HomeTeam      string `json:"home_team"`
	AwayTeam      string `json:"away_team"`
	HomeScore     string `json:"home_score"`
	AwayScore     string `json:"away_score"`
	MatchClock    string `json:"match_clock"`
	GameState     string `json:"game_state"`
	SleepSeconds  int    `json:"sleep_seconds"`
	NextMatchDate string `json:"next_match_date,omitempty"` // YYYY-MM-DD UTC
	NextMatchTime string `json:"next_match_time,omitempty"` // HH:MMZ
	NextOpponent  string `json:"next_opponent,omitempty"`   // abbreviation
}

func noneResponse() response { return response{GameState: "none", SleepSeconds: 43200} }

// ── Helpers ───────────────────────────────────────────────────────────────────

func fetchJSON(url string, dest interface{}) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible)")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("ESPN %d: %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dest)
}

func parseDate(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04Z", s)
	}
	return t, err
}

func sameUTCDay(a, b time.Time) bool {
	y1, m1, d1 := a.UTC().Date()
	y2, m2, d2 := b.UTC().Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

func scoreStr(v interface{}) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case float64:
		return fmt.Sprintf("%.0f", s)
	case string:
		return s
	}
	return fmt.Sprintf("%v", v)
}

func clockStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func opponent(comps []summaryCompetitor) string {
	for _, c := range comps {
		if c.ID != teamID {
			return c.Team.Abbreviation
		}
	}
	return ""
}

// ── Core logic ────────────────────────────────────────────────────────────────

func run() response {
	// Step 1: get upcoming events across all competitions
	var core coreEventsResp
	if err := fetchJSON(coreEventsURL, &core); err != nil || len(core.Items) == 0 {
		return noneResponse()
	}

	now := time.Now().UTC()

	// Step 2: for each event ref, fetch summary and find first non-post
	for _, item := range core.Items {
		m := leagueEventRe.FindStringSubmatch(item.Ref)
		if m == nil {
			continue
		}
		league, eventID := m[1], m[2]

		var s summaryResp
		if err := fetchJSON(fmt.Sprintf(summaryFmt, league, eventID), &s); err != nil {
			continue
		}
		if len(s.Header.Competitions) == 0 {
			continue
		}

		comp := s.Header.Competitions[0]
		state := comp.Status.Type.State

		kickoff, err := parseDate(comp.Date)
		if err != nil {
			continue
		}

		opp := opponent(comp.Competitors)
		nextDate := kickoff.UTC().Format("2006-01-02")
		nextTime := kickoff.UTC().Format("15:04") + "Z"

		var home, away summaryCompetitor
		for _, cp := range comp.Competitors {
			if cp.HomeAway == "home" {
				home = cp
			} else {
				away = cp
			}
		}

		switch state {
		case "pre":
			if !sameUTCDay(kickoff, now) {
				return response{
					GameState:     "none",
					SleepSeconds:  43200,
					NextMatchDate: nextDate,
					NextMatchTime: nextTime,
					NextOpponent:  opp,
				}
			}
			secs := int(time.Until(kickoff).Seconds())
			if secs < 1 {
				secs = 1
			}
			return response{
				GameState:     "pre",
				SleepSeconds:  secs,
				NextMatchDate: nextDate,
				NextMatchTime: nextTime,
				NextOpponent:  opp,
			}

		case "in":
			return response{
				HomeTeam:     home.Team.Abbreviation,
				AwayTeam:     away.Team.Abbreviation,
				HomeScore:    scoreStr(home.Score),
				AwayScore:    scoreStr(away.Score),
				MatchClock:   clockStr(comp.Status.DisplayClock),
				GameState:    "in",
				SleepSeconds: 60,
			}

		case "post":
			if sameUTCDay(kickoff, now) {
				return response{
					HomeTeam:     home.Team.Abbreviation,
					AwayTeam:     away.Team.Abbreviation,
					HomeScore:    scoreStr(home.Score),
					AwayScore:    scoreStr(away.Score),
					MatchClock:   clockStr(comp.Status.DisplayClock),
					GameState:    "post",
					SleepSeconds: 43200,
				}
			}
			// post but not today — keep looking for a future event
			continue
		}
	}

	return noneResponse()
}
