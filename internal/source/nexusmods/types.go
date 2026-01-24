package nexusmods

import "time"

// GraphQL response types

type ModResponse struct {
	Mod ModData `graphql:"mod(gameId: $gameId, modId: $modId)"`
}

type ModData struct {
	UID         string `graphql:"uid"`
	ModID       int    `graphql:"modId"`
	Name        string `graphql:"name"`
	Summary     string `graphql:"summary"`
	Description string `graphql:"description"`
	Author      string `graphql:"author"`
	Version     string `graphql:"version"`
	Category    struct {
		Name string `graphql:"name"`
	} `graphql:"category"`
	ModDownloadCount int       `graphql:"modDownloadCount"`
	EndorsementCount int       `graphql:"endorsementCount"`
	UpdatedAt        time.Time `graphql:"updatedAt"`
}

type SearchResponse struct {
	Mods struct {
		Nodes []ModData `graphql:"nodes"`
	} `graphql:"mods(gameId: $gameId, filter: $filter, first: $first, offset: $offset)"`
}

type ModFilter struct {
	Name string `json:"name,omitempty"`
}

type FileResponse struct {
	ModFiles struct {
		Nodes []FileData `graphql:"nodes"`
	} `graphql:"modFiles(modId: $modId, gameId: $gameId)"`
}

type FileData struct {
	FileID     int       `graphql:"fileId"`
	Name       string    `graphql:"name"`
	Version    string    `graphql:"version"`
	Size       int64     `graphql:"size"`
	IsPrimary  bool      `graphql:"isPrimary"`
	UploadedAt time.Time `graphql:"uploadedAt"`
}
