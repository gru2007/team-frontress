// The campaign drawn on the main menu.
//
// This is a demo: it is the theater from services/coordinator/theater.example.json
// laid out by hand and given a plausible state, so the map has something to
// draw before the coordinator is answering. The shape of the file is the shape
// of the war layer's own data -- nodes, edges, the fronts that are live -- so
// filling it from GET /v1/status later changes nothing in the map.
//
// "demo" is why the map wears a DEMO badge: the population on a made-up front
// is made up too, and a player being shown it should know that. Delete the key
// once the numbers come from a coordinator.
//
// x and y are 0..1 across the map area, so the layout survives any resolution.
"Campaign"
{
	"name"		"#Frontress_Menu_ExampleLine"
	"demo"		"1"

	"nodes"
	{
		"red_hq"
		{
			"name"		"#Frontress_Menu_RedHQ"
			"owner"		"RED"
			"hq"		"1"
			"kind"		"command"
			"region"	"EU"
			"x"			"0.07"
			"y"			"0.50"
		}

		"yard"
		{
			"name"		"#Frontress_Menu_RailYard"
			"owner"		"RED"
			"kind"		"rail"
			"region"	"EU"
			"x"			"0.24"
			"y"			"0.22"
			"players"	"6"
		}

		"depot"
		{
			"name"		"#Frontress_Menu_SawmillDepot"
			"owner"		"RED"
			"kind"		"supply"
			"region"	"EU"
			"x"			"0.22"
			"y"			"0.79"
		}

		"works"
		{
			"name"		"#Frontress_Menu_Foundry"
			"owner"		"RED"
			"kind"		"industrial"
			"region"	"EU"
			"x"			"0.45"
			"y"			"0.55"
			"players"	"21"
			"battles"	"1"
		}

		"reservoir"
		{
			"name"		"#Frontress_Menu_Reservoir"
			"owner"		"BLU"
			"kind"		"water"
			"region"	"EU"
			"x"			"0.53"
			"y"			"0.18"
			"players"	"11"
			"battles"	"1"
		}

		"quarry"
		{
			"name"		"#Frontress_Menu_Quarry"
			"owner"		"BLU"
			"kind"		"mining"
			"region"	"RU"
			"x"			"0.62"
			"y"			"0.83"
		}

		"junction"
		{
			"name"		"#Frontress_Menu_IronJunction"
			"owner"		"BLU"
			"kind"		"rail"
			"region"	"RU"
			"x"			"0.78"
			"y"			"0.47"
		}

		"blu_hq"
		{
			"name"		"#Frontress_Menu_BluHQ"
			"owner"		"BLU"
			"hq"		"1"
			"kind"		"command"
			"region"	"RU"
			"x"			"0.94"
			"y"			"0.52"
		}
	}

	"edges"
	{
		"1"		{ "a" "red_hq"		"b" "yard" }
		"2"		{ "a" "red_hq"		"b" "depot" }
		"3"		{ "a" "yard"		"b" "works" }
		"4"		{ "a" "depot"		"b" "works" }
		"5"		{ "a" "yard"		"b" "reservoir" }
		"6"		{ "a" "works"		"b" "reservoir" }
		"7"		{ "a" "works"		"b" "quarry" }
		"8"		{ "a" "depot"		"b" "quarry" }
		"9"		{ "a" "reservoir"	"b" "junction" }
		"10"	{ "a" "quarry"		"b" "junction" }
		"11"	{ "a" "junction"	"b" "blu_hq" }
	}

	// Where the fighting is. As many as the population supports -- the war is
	// as wide as the number of people online, which is the coordinator's whole
	// job. Remove the block and the map draws quiet.
	"fronts"
	{
		"1"
		{
			"node"		"works"
			"attacker"	"BLU"
			"stage"		"2"
			"stages"	"3"
			"kind"		"assault"
			"map"		"cp_gravelpit"
			"progress"	"0.62"
			"players"	"21"
			"server"	"eu-1"
		}

		"2"
		{
			"node"		"reservoir"
			"attacker"	"RED"
			"stage"		"1"
			"stages"	"3"
			"kind"		"skirmish"
			"map"		"koth_viaduct"
			"progress"	"0.28"
			"players"	"11"
			"server"	"eu-2"
		}
	}

	// Which machine is running which battle. The coordinator knows this from
	// its pool; until it publishes it, this is what the map shows.
	"servers"
	{
		"eu-1"
		{
			"name"		"Frankfurt #1"
			"region"	"EU"
			"map"		"cp_gravelpit"
			"node"		"works"
			"players"	"21"
			"max"		"24"
		}

		"eu-2"
		{
			"name"		"Frankfurt #2"
			"region"	"EU"
			"map"		"koth_viaduct"
			"node"		"reservoir"
			"players"	"11"
			"max"		"24"
		}

		"ru-1"
		{
			"name"		"Moscow #1"
			"region"	"RU"
			"map"		""
			"node"		"junction"
			"players"	"0"
			"max"		"24"
		}
	}
}
