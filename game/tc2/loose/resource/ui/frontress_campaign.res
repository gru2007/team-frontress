// The campaign line drawn on the main menu.
//
// This is a demo: it is the theater from services/coordinator/theater.example.json
// laid out by hand, so the panel has something to draw before the coordinator
// is answering. The shape of the file is the shape of the war layer's own data
// -- nodes, edges, and the front that is live -- so filling it from
// GET /v1/status later changes nothing here.
//
// x and y are 0..1 across the map area, so the layout survives any resolution.
"Campaign"
{
	"name"		"#Frontress_Menu_ExampleLine"

	"nodes"
	{
		"red_hq"
		{
			"name"		"#Frontress_Menu_RedHQ"
			"owner"		"RED"
			"x"			"0.00"
			"y"			"0.30"
		}

		"yard"
		{
			"name"		"#Frontress_Menu_RailYard"
			"owner"		"RED"
			"x"			"0.33"
			"y"			"0.62"
		}

		"works"
		{
			"name"		"#Frontress_Menu_Foundry"
			"owner"		"BLU"
			"x"			"0.67"
			"y"			"0.30"
		}

		"blu_hq"
		{
			"name"		"#Frontress_Menu_BluHQ"
			"owner"		"BLU"
			"x"			"1.00"
			"y"			"0.62"
		}
	}

	"edges"
	{
		"1"	{ "a" "red_hq"	"b" "yard" }
		"2"	{ "a" "yard"	"b" "works" }
		"3"	{ "a" "works"	"b" "blu_hq" }
	}

	// Where the fighting is. Remove this block and the map draws quiet.
	"front"
	{
		"node"		"works"
		"attacker"	"RED"
		"stage"		"2"
		"stages"	"3"
		"kind"		"breakthrough"
		"map"		"cp_process_final"
	}
}
