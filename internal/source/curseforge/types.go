package curseforge

import "time"

// CurseForge API v1 response types
// API docs: https://docs.curseforge.com/rest-api/

// APIResponse wraps all CurseForge API responses
type APIResponse[T any] struct {
	Data T `json:"data"`
}

// PaginatedResponse wraps paginated CurseForge API responses
type PaginatedResponse[T any] struct {
	Data       T          `json:"data"`
	Pagination Pagination `json:"pagination"`
}

// Pagination contains pagination info from CurseForge API
type Pagination struct {
	Index       int `json:"index"`
	PageSize    int `json:"pageSize"`
	ResultCount int `json:"resultCount"`
	TotalCount  int `json:"totalCount"`
}

// Mod represents a mod from the CurseForge API
type Mod struct {
	ID                   int         `json:"id"`
	GameID               int         `json:"gameId"`
	Name                 string      `json:"name"`
	Slug                 string      `json:"slug"`
	Links                ModLinks    `json:"links"`
	Summary              string      `json:"summary"`
	Status               int         `json:"status"`
	DownloadCount        int64       `json:"downloadCount"`
	IsFeatured           bool        `json:"isFeatured"`
	PrimaryCategoryID    int         `json:"primaryCategoryId"`
	Categories           []Category  `json:"categories"`
	ClassID              int         `json:"classId"`
	Authors              []Author    `json:"authors"`
	Logo                 *ModAsset   `json:"logo"`
	Screenshots          []ModAsset  `json:"screenshots"`
	MainFileID           int         `json:"mainFileId"`
	LatestFiles          []File      `json:"latestFiles"`
	LatestFilesIndexes   []FileIndex `json:"latestFilesIndexes"`
	DateCreated          time.Time   `json:"dateCreated"`
	DateModified         time.Time   `json:"dateModified"`
	DateReleased         time.Time   `json:"dateReleased"`
	AllowModDistribution *bool       `json:"allowModDistribution"`
	GamePopularityRank   int         `json:"gamePopularityRank"`
	IsAvailable          bool        `json:"isAvailable"`
	ThumbsUpCount        int         `json:"thumbsUpCount"`
}

// ModLinks contains URLs associated with a mod
type ModLinks struct {
	WebsiteURL string `json:"websiteUrl"`
	WikiURL    string `json:"wikiUrl"`
	IssuesURL  string `json:"issuesUrl"`
	SourceURL  string `json:"sourceUrl"`
}

// Category represents a mod category
type Category struct {
	ID               int       `json:"id"`
	GameID           int       `json:"gameId"`
	Name             string    `json:"name"`
	Slug             string    `json:"slug"`
	URL              string    `json:"url"`
	IconURL          string    `json:"iconUrl"`
	DateModified     time.Time `json:"dateModified"`
	IsClass          bool      `json:"isClass"`
	ClassID          int       `json:"classId"`
	ParentCategoryID int       `json:"parentCategoryId"`
	DisplayIndex     int       `json:"displayIndex"`
}

// Author represents a mod author
type Author struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ModAsset represents an image asset (logo, screenshot)
type ModAsset struct {
	ID           int    `json:"id"`
	ModID        int    `json:"modId"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnailUrl"`
	URL          string `json:"url"`
}

// File represents a downloadable mod file
type File struct {
	ID                   int                   `json:"id"`
	GameID               int                   `json:"gameId"`
	ModID                int                   `json:"modId"`
	IsAvailable          bool                  `json:"isAvailable"`
	DisplayName          string                `json:"displayName"`
	FileName             string                `json:"fileName"`
	ReleaseType          int                   `json:"releaseType"` // 1=Release, 2=Beta, 3=Alpha
	FileStatus           int                   `json:"fileStatus"`
	Hashes               []FileHash            `json:"hashes"`
	FileDate             time.Time             `json:"fileDate"`
	FileLength           int64                 `json:"fileLength"`
	DownloadCount        int64                 `json:"downloadCount"`
	DownloadURL          string                `json:"downloadUrl"`
	GameVersions         []string              `json:"gameVersions"`
	SortableGameVersions []SortableGameVersion `json:"sortableGameVersions"`
	Dependencies         []FileDependency      `json:"dependencies"`
	AlternateFileID      int                   `json:"alternateFileId"`
	IsServerPack         bool                  `json:"isServerPack"`
	FileFingerprint      int64                 `json:"fileFingerprint"`
	Modules              []FileModule          `json:"modules"`
}

// FileHash contains hash info for a file
type FileHash struct {
	Value string `json:"value"`
	Algo  int    `json:"algo"` // 1=SHA1, 2=MD5
}

// SortableGameVersion contains structured version info
type SortableGameVersion struct {
	GameVersionName        string    `json:"gameVersionName"`
	GameVersionPadded      string    `json:"gameVersionPadded"`
	GameVersion            string    `json:"gameVersion"`
	GameVersionReleaseDate time.Time `json:"gameVersionReleaseDate"`
	GameVersionTypeID      int       `json:"gameVersionTypeId"`
}

// FileDependency represents a file's dependency on another mod
type FileDependency struct {
	ModID        int `json:"modId"`
	RelationType int `json:"relationType"` // 1=EmbeddedLibrary, 2=OptionalDependency, 3=RequiredDependency, 4=Tool, 5=Incompatible, 6=Include
}

// FileModule represents a module within a mod file
type FileModule struct {
	Name        string `json:"name"`
	Fingerprint int64  `json:"fingerprint"`
}

// FileIndex contains file index info for latest files
type FileIndex struct {
	GameVersion       string `json:"gameVersion"`
	FileID            int    `json:"fileId"`
	Filename          string `json:"filename"`
	ReleaseType       int    `json:"releaseType"`
	GameVersionTypeID int    `json:"gameVersionTypeId"`
	ModLoader         int    `json:"modLoader"` // 0=Any, 1=Forge, 2=Cauldron, 3=LiteLoader, 4=Fabric, 5=Quilt, 6=NeoForge
}

// Game represents a game from the CurseForge API
type Game struct {
	ID           int        `json:"id"`
	Name         string     `json:"name"`
	Slug         string     `json:"slug"`
	DateModified time.Time  `json:"dateModified"`
	Assets       GameAssets `json:"assets"`
	Status       int        `json:"status"`
	APIStatus    int        `json:"apiStatus"`
}

// GameAssets contains image assets for a game
type GameAssets struct {
	IconURL  string `json:"iconUrl"`
	TileURL  string `json:"tileUrl"`
	CoverURL string `json:"coverUrl"`
}

// StringDownloadURL is the response for the download URL endpoint
type StringDownloadURL struct {
	Data string `json:"data"`
}

// Dependency relation types
const (
	RelationEmbeddedLibrary    = 1
	RelationOptionalDependency = 2
	RelationRequiredDependency = 3
	RelationTool               = 4
	RelationIncompatible       = 5
	RelationInclude            = 6
)

// Release types
const (
	ReleaseTypeRelease = 1
	ReleaseTypeBeta    = 2
	ReleaseTypeAlpha   = 3
)

// Mod loader types
const (
	ModLoaderAny        = 0
	ModLoaderForge      = 1
	ModLoaderCauldron   = 2
	ModLoaderLiteLoader = 3
	ModLoaderFabric     = 4
	ModLoaderQuilt      = 5
	ModLoaderNeoForge   = 6
)
