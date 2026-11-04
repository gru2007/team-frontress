package players

import "math"

// Rating is Elo, the same formula tf2pickup uses for its ladder: a provisional
// K of 32 for a player's first ten matches, 16 after, each player scored
// against the average rating of the other team.
//
// It is one rating per player rather than per class: the queue does not know
// what anybody will play, and a number it cannot use to balance teams is not
// worth keeping.
const (
	DefaultRating        = 1500
	ProvisionalThreshold = 10
)

func kFactor(gamesPlayed int) float64 {
	if gamesPlayed < ProvisionalThreshold {
		return 32
	}
	return 16
}

func expectedScore(rating, enemyAvg float64) float64 {
	return 1 / (1 + math.Pow(10, (enemyAvg-rating)/400))
}

func actualScore(r Result) float64 {
	switch r {
	case Win:
		return 1
	case Loss:
		return 0
	}
	return 0.5
}

// RatingUpdates computes everybody's new rating after one match. ratings and
// games are the players' current ratings and finished-match counts. A match
// with only one team present changes nobody.
func RatingUpdates(ps []Participant, ratings, games map[uint64]int) map[uint64]int {
	var sum [2]float64
	var n [2]int
	for _, p := range ps {
		if p.Team != 0 && p.Team != 1 {
			continue
		}
		sum[p.Team] += float64(ratings[p.SteamID])
		n[p.Team]++
	}
	out := make(map[uint64]int, len(ps))
	for _, p := range ps {
		out[p.SteamID] = ratings[p.SteamID]
	}
	if n[0] == 0 || n[1] == 0 {
		return out
	}
	for _, p := range ps {
		if p.Team != 0 && p.Team != 1 {
			continue
		}
		enemy := 1 - p.Team
		enemyAvg := sum[enemy] / float64(n[enemy])
		r := float64(ratings[p.SteamID])
		next := r + kFactor(games[p.SteamID])*(actualScore(p.Result)-expectedScore(r, enemyAvg))
		out[p.SteamID] = int(math.Round(next))
	}
	return out
}
