//========= Team Frontress ====================================================//
//
// Read-only access to an installed Team Fortress 2: loose files, VPK archives
// and VTF textures. The previewer uses it so the menus show the same art and
// fonts they do in the game.
//
//=============================================================================
#pragma once

#include <cstdint>
#include <map>
#include <string>
#include <vector>

class TF2Content {
public:
	// tf2Dir: ".../steamapps/common/Team Fortress 2"
	bool Mount( const std::string &tf2Dir );

	bool ReadFile( const std::string &path, std::string &out ) const;
	// A directory searched before TF2's own, as the game searches tc2/loose.
	void AddLooseRoot( const std::string &dir ) { m_LooseRoots.insert( m_LooseRoots.begin(), dir ); }

	// "materials/<name>.vtf" decoded to RGBA8 (largest mip, first frame).
	bool LoadVTF( const std::string &name, std::vector< uint8_t > &rgba, int &w, int &h ) const;

private:
	struct Entry {
		std::string archive;
		uint32_t offset;
		uint32_t length;
		std::string preload;
	};
	bool MountVPK( const std::string &dirFile );

	std::vector< std::string > m_LooseRoots;
	std::map< std::string, Entry > m_Entries;
};
