// The authored demo war: every sector, every operation, every map.
//
// Everything here is written from the player's point of view -- 'ally' is the
// side the player chose, 'enemy' is the other one -- so RED and BLU play the
// same script and only the colours change. Coordinates are for a 1600x900
// theater with the ally HQ on the left; BLU sees the same theater mirrored.
//
// Nothing in this file has behaviour. Rules live in campaign.js, drawing in
// ui/*.js. Changing the demo is changing this file.

export const THEATER = { width: 1600, height: 900 };

// Strategic nodes. `op` names the operation template an attack on the node
// uses; ally nodes carry one too, for when the enemy takes them back.
//
// Sized for a festival session: from the first front line to the enemy
// headquarters is three operations -- nine battles if every one is won.
export const NODES = [
	{ id: 'hq_ally',   name: 'hq',        x: 150,  y: 455, kind: 'hq',      owner: 'ally',  weight: 1.25 },
	{ id: 'farms',     name: 'farms',     x: 320,  y: 215, kind: 'farm',    owner: 'ally',  op: 'rural' },
	{ id: 'coaltown',  name: 'coaltown',  x: 305,  y: 700, kind: 'mine',    owner: 'ally',  op: 'industrial' },
	{ id: 'railyard',  name: 'railyard',  x: 470,  y: 460, kind: 'rail',    owner: 'ally',  op: 'rail' },
	{ id: 'foundry',   name: 'foundry',   x: 625,  y: 250, kind: 'factory', owner: 'ally',  op: 'industrial' },
	{ id: 'sawmill',   name: 'sawmill',   x: 610,  y: 690, kind: 'lumber',  owner: 'ally',  op: 'rural' },
	{ id: 'quarry',    name: 'quarry',    x: 830,  y: 170, kind: 'mine',    owner: 'enemy', op: 'quarry' },
	{ id: 'junction',  name: 'junction',  x: 840,  y: 455, kind: 'rail',    owner: 'enemy', op: 'rail' },
	{ id: 'reservoir', name: 'reservoir', x: 830,  y: 740, kind: 'dam',     owner: 'enemy', op: 'hydro' },
	{ id: 'hydro',     name: 'hydro',     x: 1120, y: 280, kind: 'power',   owner: 'enemy', op: 'hydro' },
	{ id: 'badwater',  name: 'badwater',  x: 1125, y: 640, kind: 'factory', owner: 'enemy', op: 'industrial' },
	{ id: 'hq_enemy',  name: 'hq',        x: 1420, y: 455, kind: 'hq',      owner: 'enemy', op: 'hq', weight: 1.25 },
];

// Roads and rails. `rail: true` is drawn as track.
export const EDGES = [
	['hq_ally', 'farms'], ['hq_ally', 'coaltown'], ['hq_ally', 'railyard', true],
	['farms', 'railyard'], ['coaltown', 'railyard'], ['farms', 'foundry'],
	['coaltown', 'sawmill'], ['railyard', 'foundry', true], ['railyard', 'sawmill'],
	['railyard', 'junction', true], ['foundry', 'quarry'], ['foundry', 'junction'],
	['sawmill', 'junction'], ['sawmill', 'reservoir'],
	['quarry', 'hydro'], ['junction', 'hydro', true], ['junction', 'badwater'],
	['reservoir', 'badwater'], ['hydro', 'badwater'],
	['hydro', 'hq_enemy', true], ['badwater', 'hq_enemy'],
];

// Decorative terrain, in the same coordinates as the nodes.
export const TERRAIN = {
	rivers: [
		'M 700 -10 C 690 110, 745 190, 720 300 S 660 420, 700 520 S 780 640, 735 760 S 700 860, 720 910',
		'M 720 300 C 800 330, 880 330, 920 390 S 960 470, 1010 500',
	],
	ridges: [
		{ x: 990, y: 110, s: 1.2 }, { x: 1300, y: 150, s: 0.9 }, { x: 1300, y: 760, s: 1.1 },
		{ x: 1380, y: 690, s: 0.8 }, { x: 420, y: 110, s: 0.9 }, { x: 190, y: 820, s: 1 },
		{ x: 880, y: 820, s: 0.8 }, { x: 540, y: 110, s: 0.7 },
	],
};

// The three stages of every operation. Force size grows with the stage -- the
// demo's stand-in for battles that scale with the population.
export const STAGES = [
	{ id: 'breakthrough', players: 8,  size: '4v4', minutes: 8  },
	{ id: 'advance',      players: 12, size: '6v6', minutes: 12 },
	{ id: 'assault',      players: 18, size: '9v9', minutes: 15 },
];

// A landing zone is one way into a stage: a place on the sector map and the
// battle that happens if you land there. Both sides fight the same maps.
//
// Payload and Attack/Defend maps hard-code BLU as the attacker, so on those
// the attacking side always plays the game's BLU team; when that side is RED,
// greyline_uniform_swap redraws both teams in the war's colours (see
// src/game/shared/greyline/greyline_uniform.h). Symmetric modes need nothing.
//
// Every map here must be in cfg/mapcycle_quickplay_bots.txt (bots need a nav).
export const DIRECTIONAL_MODES = [ 'pl', 'ad' ];

const Z = ( id, mode, map ) => ( { id, mode, map } );

export const OPERATIONS = {
	industrial: [
		[ Z( 'slag',     'ctf',  'ctf_doublecross_snowy' ), Z( 'stacks',     'koth', 'koth_viaduct' ) ],
		[ Z( 'siding',   'pl',   'pl_badwater' ),           Z( 'conveyor',   'pl',   'pl_barnblitz' ) ],
		[ Z( 'gate',     'ad',   'cp_dustbowl' ),           Z( 'furnace',    'ad',   'cp_gorge' ) ],
	],
	rail: [
		[ Z( 'signal',   'koth', 'koth_sawmill' ),          Z( 'tunnel',     'ctf',  'ctf_frosty' ) ],
		[ Z( 'mainline', 'pl',   'pl_goldrush' ),           Z( 'spur',       'pl',   'pl_upward' ) ],
		[ Z( 'yard',     'ad',   'cp_dustbowl' ),           Z( 'roundhouse', 'ad',   'cp_mossrock' ) ],
	],
	quarry: [
		[ Z( 'rim',      'koth', 'koth_badlands' ),         Z( 'haul',       'ctf',  'ctf_sidewinder' ) ],
		[ Z( 'ramp',     'pl',   'pl_thundermountain' ),    Z( 'crusher',    'pl',   'pl_hoodoo_final' ) ],
		[ Z( 'pit',      'ad',   'cp_gorge' ),              Z( 'office',     'ad',   'cp_mercenarypark' ) ],
	],
	hydro: [
		[ Z( 'spillway', 'koth', 'koth_lakeside_final' ),   Z( 'pipeline',   'ctf',  'ctf_pelican_peak' ) ],
		[ Z( 'penstock', 'pl',   'pl_swiftwater_final1' ),  Z( 'canal',      'pl',   'pl_pier' ) ],
		[ Z( 'turbine',  'ad',   'cp_altitude' ),           Z( 'dam',        'ad',   'cp_gorge' ) ],
	],
	rural: [
		[ Z( 'orchard',  'koth', 'koth_nucleus' ),          Z( 'barns',      'ctf',  'ctf_penguin_peak' ) ],
		[ Z( 'lane',     'pl',   'pl_barnblitz' ),          Z( 'silo',       'pl',   'pl_enclosure_final' ) ],
		[ Z( 'mill',     'ad',   'cp_dustbowl' ),           Z( 'ridge',      'ad',   'cp_mossrock' ) ],
	],
	hq: [
		[ Z( 'outer',    'koth', 'koth_viaduct' ),          Z( 'wire',       'ctf',  'ctf_doublecross_snowy' ) ],
		[ Z( 'supply',   'pl',   'pl_upward' ),             Z( 'rail',       'pl',   'pl_goldrush' ) ],
		[ Z( 'command',  'ad',   'cp_dustbowl' ),           Z( 'bunker',     'ad',   'cp_gorge' ) ],
	],
};

// Operation codenames, by target. Mostly flavour; the dispatches use them.
export const CODENAMES = {
	quarry: 'hammer', junction: 'irontrack', reservoir: 'floodgate', hydro: 'livewire',
	badwater: 'saltlick',
	hq_enemy: 'lastcall', foundry: 'anvil', sawmill: 'splinter', railyard: 'switchback',
	farms: 'harvest', coaltown: 'blacklung',
};

export const CLASSES = [
	{ id: 'scout',        icon: 'scout',    poster: 'scout',        role: 'offense' },
	{ id: 'soldier',      icon: 'soldier',  poster: 'soldier',      role: 'offense' },
	{ id: 'pyro',         icon: 'pyro',     poster: 'pyro',         role: 'offense' },
	{ id: 'demoman',      icon: 'demoman',  poster: 'demoman',      role: 'defense' },
	{ id: 'heavyweapons', icon: 'heavy',    poster: 'heavyweapons', role: 'defense' },
	{ id: 'engineer',     icon: 'engineer', poster: 'engineer',     role: 'defense' },
	{ id: 'medic',        icon: 'medic',    poster: 'medic',        role: 'support' },
	{ id: 'sniper',       icon: 'sniper',   poster: 'sniper',       role: 'support' },
	{ id: 'spy',          icon: 'spy',      poster: 'spy',          role: 'support' },
];

// The war does not stop between the player's battles. After every resolved
// battle (and on returning after a long break) the first event whose `when`
// holds is applied -- the demo's stand-in for everyone else playing.
// `apply` mutates a draft of the state; see campaign.js.
export const WORLD_EVENTS = [
	{
		id: 'sawmill_probe', node: 'sawmill', tone: 'enemy',
		when: s => s.owners.sawmill === 'ally',
		apply: s => { s.pressure.sawmill = 0.45; },
	},
	{
		id: 'allies_reservoir', node: 'reservoir', tone: 'ally',
		when: s => s.owners.reservoir === 'enemy' && s.op?.target !== 'reservoir',
		apply: s => { s.pressure.reservoir = -0.5; },
	},
	// A counter-attack is a threat, not a loss: the player has THREAT_BATTLES
	// battles to go and defend the sector before it falls (campaign.js).
	{
		id: 'sawmill_attack', node: 'sawmill', tone: 'enemy', threat: true,
		when: s => s.owners.sawmill === 'ally' && !s.threat && s.stats.battles >= 3,
		apply: () => {},
	},
	{
		id: 'enemy_digs_in', node: 'badwater', tone: 'enemy',
		when: s => s.owners.badwater === 'enemy' && s.op?.target !== 'badwater',
		apply: s => { s.pressure.badwater = 0.3; },
	},
	{
		id: 'allies_take_reservoir', node: 'reservoir', tone: 'ally',
		when: s => s.owners.reservoir === 'enemy' && s.op?.target !== 'reservoir' && s.stats.battles >= 5,
		apply: s => { s.owners.reservoir = 'ally'; delete s.pressure.reservoir; },
	},
	{
		id: 'junction_attack', node: 'junction', tone: 'enemy', threat: true,
		when: s => s.owners.junction === 'ally' && !s.threat && s.stats.battles >= 6,
		apply: () => {},
	},
	{
		id: 'quiet_front', node: null, tone: 'neutral',
		when: () => true,
		apply: () => {},
	},
];

// Battles the player has to answer a counter-attack before the sector falls.
export const THREAT_BATTLES = 2;

//-----------------------------------------------------------------------------
// What makes one battle unlike another.
//
// A modifier is a condition of the fight, shown on the map and in the
// dossier, and felt in the battle:
//   roster  changes who fights -- the game adds the bots accordingly
//           (tf_bot_add ... noquota, see tf_frontress_demo.cpp);
//   cfg     the game execs cfg/frontress_mod_<id>.cfg after its own setup,
//           and frontress_demo.cfg undoes it for the next battle.
//   reward  supply earned on top of a win, for the harder conditions.
// Some modifiers touch every bot or the whole server (sv_gravity,
// tf_bot_melee_only): those are 'odd', not an edge for either side.
//-----------------------------------------------------------------------------
export const MODIFIERS = {
	veterans:  { tone: 'hard', roster: { eskill: 1 }, reward: 1 },
	snipers:   { tone: 'hard', roster: { eclass: [ 'sniper', 3 ] }, cfg: true, reward: 1 },
	sentries:  { tone: 'hard', roster: { eclass: [ 'engineer', 2 ] }, cfg: true, reward: 1 },
	longwaves: { tone: 'odd',  cfg: true },
	nocrits:   { tone: 'odd',  cfg: true },
	lowgrav:   { tone: 'odd',  cfg: true },
	melee:     { tone: 'odd',  cfg: true },
	grapples:  { tone: 'good', cfg: true },
	partisans: { tone: 'good', roster: { allies: 1 } },
};

// The conditions of each stage of each operation...
export const OPERATION_MODIFIERS = {
	industrial: [ [ 'nocrits' ], [ 'sentries' ], [ 'veterans' ] ],
	rail:       [ [ 'grapples' ], [ 'longwaves' ], [ 'snipers' ] ],
	quarry:     [ [ 'snipers' ], [ 'lowgrav' ], [ 'sentries' ] ],
	hydro:      [ [ 'longwaves' ], [ 'nocrits' ], [ 'veterans' ] ],
	rural:      [ [ 'melee' ], [ 'snipers' ], [ 'longwaves' ] ],
	hq:         [ [ 'veterans' ], [ 'sentries', 'longwaves' ], [ 'veterans', 'snipers' ] ],
};

// ...and what the state of the war adds: an enemy that dug in fights harder,
// a sector allied squads are already pushing has partisans on your side.
export const PRESSURE_MODIFIERS = { enemy: 'veterans', ally: 'partisans' };

// Holding a sector: the attacker brings more.
export const DEFENSE_MODIFIERS = [ 'veterans' ];

//-----------------------------------------------------------------------------
// Supply: earned by winning, spent on one asset per battle.
//-----------------------------------------------------------------------------
export const SUPPLY_START = 2;
export const SUPPLY_WIN = 1;
export const SUPPLY_DEFENSE_WIN = 2;

export const ASSETS = {
	reinforce: { cost: 2, roster: { allies: 2 } },
	elite:     { cost: 2, roster: { askill: 1 } },
	intel:     { cost: 1, cancels: 'hard' },      // takes the hardest condition off
	gamble:    { cost: 0, roster: { enemies: 2, eskill: 1 }, reward: 2, momentum: 1 },
};

// Bot skill, as tf_bot_add spells it. The demo's base is 'normal'.
export const SKILLS = [ 'easy', 'normal', 'hard', 'expert' ];
export const BASE_SKILL = 1;

//-----------------------------------------------------------------------------
// The player's own record: medals from the battle's scoreboard, and rank from
// the points earned across the war. The thresholds are per battle, for a
// human among bots on a public-sized map.
//-----------------------------------------------------------------------------
export const MEDALS = [
	{ id: 'mvp',          test: st => st.teamRank === 1 },
	{ id: 'slayer',       test: st => st.kills >= 10 },
	{ id: 'wrecker',      test: st => st.damage >= 3000 },
	{ id: 'lifeline',     test: st => st.healing >= 2000 },
	{ id: 'untouchable',  test: st => st.deaths === 0 && st.score >= 5 },
];

export const MEDAL_XP = 10;

export const RANKS = [
	{ id: 'private',    xp: 0 },
	{ id: 'corporal',   xp: 40 },
	{ id: 'sergeant',   xp: 100 },
	{ id: 'lieutenant', xp: 180 },
	{ id: 'captain',    xp: 280 },
	{ id: 'major',      xp: 400 },
];

// How many minutes away count as "while you were away".
export const AWAY_MINUTES = 5;
