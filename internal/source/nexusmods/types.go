package nexusmods

import "time"

// REST API v1 response types

// ModData represents a mod from the NexusMods REST API
type ModData struct {
	ModID                int       `json:"mod_id"`
	GameID               int       `json:"game_id"`
	DomainName           string    `json:"domain_name"`
	Name                 string    `json:"name"`
	Summary              string    `json:"summary"`
	Description          string    `json:"description"`
	Version              string    `json:"version"`
	Author               string    `json:"author"`
	UploadedBy           string    `json:"uploaded_by"`
	UploadedByProfileURL string    `json:"uploaded_users_profile_url"`
	CategoryID           int       `json:"category_id"`
	PictureURL           string    `json:"picture_url"`
	ContainsAdultContent bool      `json:"contains_adult_content"`
	Status               string    `json:"status"`
	Available            bool      `json:"available"`
	EndorsementCount     int       `json:"endorsement_count"`
	CreatedTime          time.Time `json:"created_time"`
	UpdatedTime          time.Time `json:"updated_time"`
	AllowRating          bool      `json:"allow_rating"`
}

// FileData represents a mod file from the NexusMods REST API
type FileData struct {
	FileID          int       `json:"file_id"`
	Name            string    `json:"name"`
	FileName        string    `json:"file_name"`
	Version         string    `json:"version"`
	CategoryID      int       `json:"category_id"`
	CategoryName    string    `json:"category_name"`
	IsPrimary       bool      `json:"is_primary"`
	Size            int64     `json:"size"`
	SizeKB          int64     `json:"size_kb"`
	SizeInBytes     *int64    `json:"size_in_bytes"`
	UploadedTime    time.Time `json:"uploaded_time"`
	ModVersion      string    `json:"mod_version"`
	ExternalVirusID string    `json:"external_virus_scan_url"`
	Description     string    `json:"description"`
	Changelog       string    `json:"changelog_html"`
}

// ModFileList represents the response from the mod files endpoint
type ModFileList struct {
	Files       []FileData   `json:"files"`
	FileUpdates []FileUpdate `json:"file_updates"`
}

// FileUpdate represents file version relationships
type FileUpdate struct {
	OldFileID    int    `json:"old_file_id"`
	NewFileID    int    `json:"new_file_id"`
	OldFileName  string `json:"old_file_name"`
	NewFileName  string `json:"new_file_name"`
	UploadedTime string `json:"uploaded_time"`
}

// UpdateEntry represents a mod update from the updated mods endpoint
type UpdateEntry struct {
	ModID             int   `json:"mod_id"`
	LatestFileUpdate  int64 `json:"latest_file_update"`
	LatestModActivity int64 `json:"latest_mod_activity"`
}

// DownloadLink represents a download URL response
type DownloadLink struct {
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	URI       string `json:"URI"`
}
