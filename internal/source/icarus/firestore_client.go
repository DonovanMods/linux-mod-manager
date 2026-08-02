package icarus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		reqURL := fmt.Sprintf("%s/%s?pageSize=200", c.documentsURL(), collection)
		if pageToken != "" {
			reqURL += "&pageToken=" + url.QueryEscape(pageToken)
		}
		var page struct {
			Documents []struct {
				Name   string         `json:"name"`
				Fields map[string]any `json:"fields"`
			} `json:"documents"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.getJSON(ctx, reqURL, &page); err != nil {
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
		// Read a capped snippet for the error message, then keep draining
		// to EOF before Close so the underlying connection stays eligible
		// for reuse — net/http only pools an HTTP/1.x connection once its
		// body has been read to EOF; returning immediately here left
		// whatever the server sent unread, forcing the transport to close
		// the connection instead of reusing it for the next request. A bare
		// "HTTP %d" left a 403 (or any other failure) undiagnosable: a
		// Firestore permission error, a quota message, and a malformed
		// request all look identical without the URL and body.
		const errBodySnippetCap = 512
		snippetBytes, _ := io.ReadAll(io.LimitReader(resp.Body, errBodySnippetCap))
		_, _ = io.Copy(io.Discard, resp.Body)
		snippet := strings.TrimSpace(string(snippetBytes))
		if snippet == "" {
			return fmt.Errorf("icarus: GET %s: HTTP %d", url, resp.StatusCode)
		}
		return fmt.Errorf("icarus: GET %s: HTTP %d: %s", url, resp.StatusCode, snippet)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func lastPathSegment(resourceName string) string {
	parts := strings.Split(resourceName, "/")
	return parts[len(parts)-1]
}
