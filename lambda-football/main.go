package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// Configuration
const teamID = "464" 	// 364 = Liverpool FC. Change to any ESPN soccer team ID.
						// Other ESPN team IDs: 660=USMNT, 655=Saudi Arabia, 382=Man City, 478=France, 464=Norway

// URLs
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

type summaryTeamLogo struct {
	Href string `json:"href"`
}

type summaryTeam struct {
	Abbreviation string            `json:"abbreviation"`
	Logos        []summaryTeamLogo `json:"logos"`
}

func (t summaryTeam) LogoURL() string {
	if len(t.Logos) > 0 {
		return t.Logos[0].Href
	}
	return ""
}

type summaryCompetitor struct {
	ID       string      `json:"id"`
	HomeAway string      `json:"homeAway"`
	Score    interface{} `json:"score"` // number or nil
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

// ── Output structs ────────────────────────────────────────────────────────────

// currentMatch describes a match that is live or just finished.
type currentMatch struct {
	HomeTeam     string `json:"home_team"`
	AwayTeam     string `json:"away_team"`
	HomeScore    string `json:"home_score"`
	AwayScore    string `json:"away_score"`
	HomeImageURL string `json:"home_image_url,omitempty"`
	AwayImageURL string `json:"away_image_url,omitempty"`
	MatchClock   string `json:"match_clock"`
}

// nextMatch describes the next scheduled match.
type nextMatch struct {
	Date             string `json:"date"`            // YYYY-MM-DD UTC
	Time             string `json:"time"`            // HH:MMZ
	Opponent         string `json:"opponent"`        // abbreviation
	OpponentImageURL string `json:"opponent_image_url,omitempty"`
}

// response is the top-level Lambda output.
// game_state values: "in", "pre", "post", "none"
//   - "in":   match is live       → current_match populated, next_match null
//   - "post": match finished today → current_match populated, next_match populated if available
//   - "pre":  match today, not yet kicked off → current_match null, next_match populated
//   - "none": no match today       → current_match null, next_match populated if available
type response struct {
	GameState    string        `json:"game_state"`
	SleepSeconds int           `json:"sleep_seconds"`
	CurrentMatch *currentMatch `json:"current_match"`
	NextMatch    *nextMatch    `json:"next_match"`
}

func noneResponse() response {
	return response{GameState: "none", SleepSeconds: 43200}
}

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

// opponentOf returns the competitor that is not our configured team.
func opponentOf(comps []summaryCompetitor) *summaryCompetitor {
	for i := range comps {
		if comps[i].ID != teamID {
			return &comps[i]
		}
	}
	return nil
}

// homeAwayOf returns the home and away competitors.
func homeAwayOf(comps []summaryCompetitor) (home, away summaryCompetitor) {
	for _, c := range comps {
		if c.HomeAway == "home" {
			home = c
		} else {
			away = c
		}
	}
	return
}

// findNextMatch scans event refs starting at startIdx for the first future
// "pre" event and returns a populated nextMatch, or nil if none found.
func findNextMatch(items []struct{ Ref string `json:"$ref"` }, startIdx int, now time.Time) *nextMatch {
	for i := startIdx; i < len(items); i++ {
		m := leagueEventRe.FindStringSubmatch(items[i].Ref)
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
		if comp.Status.Type.State != "pre" {
			continue
		}

		kickoff, err := parseDate(comp.Date)
		if err != nil || !kickoff.After(now) {
			continue
		}

		opp := opponentOf(comp.Competitors)
		nm := &nextMatch{
			Date:     kickoff.UTC().Format("2006-01-02"),
			Time:     kickoff.UTC().Format("15:04") + "Z",
			Opponent: "",
		}
		if opp != nil {
			nm.Opponent = opp.Team.Abbreviation
			nm.OpponentImageURL = opp.Team.LogoURL()
		}
		return nm
	}
	return nil
}

// ── Core logic ────────────────────────────────────────────────────────────────

func run() response {
	// Step 1: fetch the team's event list
	var core coreEventsResp
	if err := fetchJSON(coreEventsURL, &core); err != nil || len(core.Items) == 0 {
		return noneResponse()
	}

	now := time.Now().UTC()

	// Step 2: iterate events chronologically, find the first relevant one
	for idx, item := range core.Items {
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

		home, away := homeAwayOf(comp.Competitors)

		switch state {
		case "pre":
			// Build next_match from this event itself
			opp := opponentOf(comp.Competitors)
			nm := &nextMatch{
				Date:     kickoff.UTC().Format("2006-01-02"),
				Time:     kickoff.UTC().Format("15:04") + "Z",
				Opponent: "",
			}
			if opp != nil {
				nm.Opponent = opp.Team.Abbreviation
				nm.OpponentImageURL = opp.Team.LogoURL()
			}

			if !sameUTCDay(kickoff, now) {
				// Match is in the future but not today — show as "none" with next info
				return response{
					GameState:    "none",
					SleepSeconds: 43200,
					NextMatch:    nm,
				}
			}
			// Match is today, count down to kickoff
			secs := int(time.Until(kickoff).Seconds())
			if secs < 1 {
				secs = 1
			}
			return response{
				GameState:    "pre",
				SleepSeconds: secs,
				NextMatch:    nm,
			}

		case "in":
			return response{
				GameState:    "in",
				SleepSeconds: 60,
				CurrentMatch: &currentMatch{
					HomeTeam:     home.Team.Abbreviation,
					AwayTeam:     away.Team.Abbreviation,
					HomeScore:    scoreStr(home.Score),
					AwayScore:    scoreStr(away.Score),
					HomeImageURL: home.Team.LogoURL(),
					AwayImageURL: away.Team.LogoURL(),
					MatchClock:   clockStr(comp.Status.DisplayClock),
				},
			}

		case "post":
			if sameUTCDay(kickoff, now) {
				// Match finished today — return score and look ahead for next match
				return response{
					GameState:    "post",
					SleepSeconds: 43200,
					CurrentMatch: &currentMatch{
						HomeTeam:     home.Team.Abbreviation,
						AwayTeam:     away.Team.Abbreviation,
						HomeScore:    scoreStr(home.Score),
						AwayScore:    scoreStr(away.Score),
						HomeImageURL: home.Team.LogoURL(),
						AwayImageURL: away.Team.LogoURL(),
						MatchClock:   clockStr(comp.Status.DisplayClock),
					},
					// Search remaining events for next upcoming match
					NextMatch: findNextMatch(core.Items, idx+1, now),
				}
			}
			// post but not today — keep scanning forward
			continue
		}
	}

	return noneResponse()
}
