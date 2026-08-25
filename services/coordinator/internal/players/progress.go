package players

// Progression: turning a match record into a level and a number the game can
// show.
//
// Deliberately arithmetic, not a rating system. There is no skill model here
// and no attempt at one -- a Glicko implementation that nobody has tuned is
// worse than an honest "you have played twenty matches and won nine", because
// it looks authoritative and is not. What this is for is the two numbers the
// stock UI already has a place for: an XP total that goes up when you play,
// and a level derived from it.
//
// Everything below is a pure function of a Record, so nothing extra is stored
// and an old log replays into the same numbers.

const (
	// XPPerMatch is what finishing a match is worth, win or lose. Showing up
	// is the behaviour worth rewarding in a small population.
	XPPerMatch = 100
	// XPWinBonus is what winning adds on top.
	XPWinBonus = 50
	// XPAbandonPenalty comes off for a match the player was assigned to and
	// walked out of. It cannot take a total below zero.
	XPAbandonPenalty = 150

	// XPPerLevel is flat on purpose. A curve makes the first levels fly past
	// and the later ones unreachable, which for a community of a few dozen
	// people is all downside.
	XPPerLevel = 1000
	// MaxLevel matches the number of levels the stock casual badge has art
	// for. Past it the XP keeps counting and the badge stops changing.
	MaxLevel = 150
)

// XP is the player's total experience.
func (r Record) XP() int {
	xp := r.Matches*XPPerMatch + r.Wins*XPWinBonus - r.Abandons*XPAbandonPenalty
	if xp < 0 {
		return 0
	}
	return xp
}

// Level is the badge level the XP total earns. Levels start at 1: a player who
// has never finished a match is level 1 with no progress, not level 0.
func (r Record) Level() int {
	level := r.XP()/XPPerLevel + 1
	if level > MaxLevel {
		return MaxLevel
	}
	return level
}

// LevelProgress is how far into the current level the player is, as XP into
// this level and XP the level takes. At MaxLevel it reports the level full,
// because there is nowhere further to go.
func (r Record) LevelProgress() (into, needed int) {
	if r.Level() >= MaxLevel {
		return XPPerLevel, XPPerLevel
	}
	return r.XP() % XPPerLevel, XPPerLevel
}
