package war

import (
	"fmt"
	"time"
)

// AdvanceCampaign retains the completed campaign in the log and resets only
// territory/fronts after the intermission. The GC waits for old battles first.
func (e *Engine) AdvanceCampaign(now time.Time, pause time.Duration) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.winner == "" || now.Unix() < e.endedAt+int64(pause.Seconds()) {
		return false, nil
	}
	ev, err := e.log.Append(Event{Kind: EventCampaignStarted, Campaign: fmt.Sprintf("%s-%d", e.theater.ID, now.Unix()), At: now.Unix()})
	if err != nil {
		return false, err
	}
	e.apply(ev)
	return true, nil
}
