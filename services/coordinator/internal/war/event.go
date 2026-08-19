package war

import "time"

// EventKind names one thing that happened to the war.
//
// The event log is the war. Every mutation is an appended event, and replaying
// the log from the first campaign rebuilds the identical world — which is what
// makes the timeline, the world recap, an audit of a disputed capture and a
// clean restart after a crash all the same feature. Story events land in the
// same stream later, which is how a scripted signal loss will be able to change
// a live campaign without a second system.
type EventKind string

const (
	EventCampaignStarted EventKind = "campaign_started"
	EventCampaignEnded   EventKind = "campaign_ended"
	EventFrontOpened     EventKind = "front_opened"
	EventFrontClosed     EventKind = "front_closed"
	EventBattleRecorded  EventKind = "battle_recorded"
	EventNodeCaptured    EventKind = "node_captured"
	EventMobilization    EventKind = "mobilization"
	// EventStory carries narrative beats injected from outside the battle loop.
	// Nothing emits it yet; the reducer accepts it so the log format does not
	// have to change when UNIT 0 starts showing up in it.
	EventStory EventKind = "story"
)

// Event is one entry in the war log. Exactly one payload pointer is set,
// selected by Kind. A flat union keeps the JSONL log readable by eye, which
// matters more here than compactness.
type Event struct {
	Seq  uint64    `json:"seq"`
	At   time.Time `json:"at"`
	Kind EventKind `json:"kind"`
	// Headline is the line a world recap shows for this event. It is written
	// once, when the event is appended, so the timeline never drifts as the
	// wording elsewhere changes.
	Headline string `json:"headline"`

	CampaignStarted *CampaignStartedData `json:"campaign_started,omitempty"`
	CampaignEnded   *CampaignEndedData   `json:"campaign_ended,omitempty"`
	FrontOpened     *FrontOpenedData     `json:"front_opened,omitempty"`
	FrontClosed     *FrontClosedData     `json:"front_closed,omitempty"`
	Battle          *BattleRecordedData  `json:"battle_recorded,omitempty"`
	NodeCaptured    *NodeCapturedData    `json:"node_captured,omitempty"`
	Mobilization    *MobilizationData    `json:"mobilization,omitempty"`
	Story           *StoryData           `json:"story,omitempty"`
}

// CampaignStartedData opens a war. Owners is the complete starting layout, so
// a replay never has to consult the theater file for state.
type CampaignStartedData struct {
	CampaignID string          `json:"campaign_id"`
	Number     int             `json:"number"`
	Name       string          `json:"name"`
	Theater    string          `json:"theater"`
	Opening    string          `json:"opening"`
	Owners     map[string]Side `json:"owners"`
}

// CampaignEndedData closes a war when an HQ falls.
type CampaignEndedData struct {
	CampaignID string `json:"campaign_id"`
	Winner     Side   `json:"winner"`
	Battles    int    `json:"battles"`
	Captures   int    `json:"captures"`
	DurationS  int64  `json:"duration_s"`
	Reason     string `json:"reason"`
}

// FrontOpenedData carries every decision the coordinator made when it chose to
// open this offensive, so replay re-creates it without re-deciding.
type FrontOpenedData struct {
	FrontID         string      `json:"front_id"`
	Name            string      `json:"name"`
	CampaignID      string      `json:"campaign_id"`
	Attacker        Side        `json:"attacker"`
	Defender        Side        `json:"defender"`
	TargetNode      string      `json:"target_node"`
	SourceNode      string      `json:"source_node"`
	Plan            []StageKind `json:"plan"`
	CollapseAtStage int         `json:"collapse_at_stage"`
	Reason          string      `json:"reason"`
}

// FrontClosedData retires a front, whether it was won, broken or stood down.
type FrontClosedData struct {
	FrontID string      `json:"front_id"`
	Status  FrontStatus `json:"status"`
	Reason  string      `json:"reason"`
}

// BattleRecordedData is a finished match's effect on the war.
type BattleRecordedData struct {
	BattleID  string    `json:"battle_id"`
	FrontID   string    `json:"front_id"`
	ServerID  string    `json:"server_id,omitempty"`
	Outcome   Outcome   `json:"outcome"`
	Winner    Side      `json:"winner"`
	RedScore  uint32    `json:"red_score"`
	BluScore  uint32    `json:"blu_score"`
	Map       string    `json:"map"`
	Mode      string    `json:"mode"`
	Stage     int       `json:"stage"`
	StageKind StageKind `json:"stage_kind"`
	Players   int       `json:"players"`
	DurationS int64     `json:"duration_s"`
	// Weight is how much of a stage this battle was worth, decided from the
	// headcount when it was recorded. It is written into the event rather than
	// recomputed on replay for the same reason Headline is: retuning the rules
	// must not rewrite what already happened. Zero means an event from before
	// battles were weighted, which counted as a whole stage.
	Weight float64 `json:"weight,omitempty"`
}

// NodeCapturedData flips a territory.
type NodeCapturedData struct {
	NodeID  string `json:"node_id"`
	Name    string `json:"name"`
	From    Side   `json:"from"`
	To      Side   `json:"to"`
	FrontID string `json:"front_id"`
}

// MobilizationData turns the soft comeback state on or off.
type MobilizationData struct {
	Side   Side   `json:"side"`
	Active bool   `json:"active"`
	Reason string `json:"reason"`
}

// StoryData is a narrative beat. It carries no mechanical effect today.
type StoryData struct {
	ID     string            `json:"id"`
	NodeID string            `json:"node_id,omitempty"`
	Text   string            `json:"text"`
	Tags   map[string]string `json:"tags,omitempty"`
}
