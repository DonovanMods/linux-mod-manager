package icarus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const defaultFirestoreBaseURL = "https://firestore.googleapis.com/v1"

// firestoreDoc is a decoded Firestore document: ID is the last path segment
// of its resource name, Fields is already unwrapped via decodeFields.
type firestoreDoc struct {
	ID     string
	Fields map[string]any
}

type firestoreClient struct {
	projectID  string
	httpClient *http.Client
	baseURL    string // overridable in tests; defaults to defaultFirestoreBaseURL
}

func newFirestoreClient(projectID string, httpClient *http.Client) *firestoreClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &firestoreClient{projectID: projectID, httpClient: httpClient, baseURL: defaultFirestoreBaseURL}
}

func (c *firestoreClient) documentsURL() string {
	return fmt.Sprintf("%s/projects/%s/databases/(default)/documents", c.baseURL, c.projectID)
}

// listCollection fetches every document in collection, following
// nextPageToken until exhausted (the catalog reads Firestore unauthenticated
// and public, with no server-side query support in play — see the design
// doc's "fetch-all + filter client-side" decision).
func (c *firestoreClient) listCollection(ctx context.Context, collection string) ([]firestoreDoc, error) {
	var all []firestoreDoc
	pageToken := ""
	for {
		url := fmt.Sprintf("%s/%s?pageSize=200", c.documentsURL(), collection)
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}
		var page struct {
			Documents []struct {
				Name   string         `json:"name"`
				Fields map[string]any `json:"fields"`
			} `json:"documents"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.getJSON(ctx, url, &page); err != nil {
			return nil, fmt.Errorf("listing %s: %w", collection, err)
		}
		for _, d := range page.Documents {
			all = append(all, firestoreDoc{ID: lastPathSegment(d.Name), Fields: decodeFields(d.Fields)})
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return all, nil
}

// getDocument fetches a single document by ID.
func (c *firestoreClient) getDocument(ctx context.Context, collection, docID string) (*firestoreDoc, error) {
	url := fmt.Sprintf("%s/%s/%s", c.documentsURL(), collection, docID)
	var doc struct {
		Name   string         `json:"name"`
		Fields map[string]any `json:"fields"`
	}
	if err := c.getJSON(ctx, url, &doc); err != nil {
		return nil, fmt.Errorf("fetching %s/%s: %w", collection, docID, err)
	}
	return &firestoreDoc{ID: lastPathSegment(doc.Name), Fields: decodeFields(doc.Fields)}, nil
}

func (c *firestoreClient) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func lastPathSegment(resourceName string) string {
	parts := strings.Split(resourceName, "/")
	return parts[len(parts)-1]
}
