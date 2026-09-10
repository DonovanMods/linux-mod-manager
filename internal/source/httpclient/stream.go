// Package httpclient: this file is the streaming half.
//
// DoJSON and DoForm read a whole response into memory and decode it, which
// is right for every mod-source API call lmm makes except one: Thunderstore
// publishes a single unpaginated community document (329 MB decoded for the
// largest community on the site, #360), and the only way to turn that into
// an index without holding it is to read the body incrementally. So this
// method hands the live response back instead of decoding it.
package httpclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// DoStream performs a request against baseURL+path and returns the LIVE
// response for the caller to read incrementally and CLOSE. It is DoJSON
// with the decode - and the MaxResponseBytes cap, which cannot mean
// anything for a stream - left to the caller: the same auth injection, the
// same ErrorMapper hook, the same 401 -> domain.ErrAuthRequired mapping and
// the same capped error-body read.
//
// header carries per-request headers the caller needs (a conditional GET's
// If-Modified-Since). A nil map sends none. Accept-Encoding is deliberately
// NOT among them: net/http negotiates gzip itself and decompresses
// transparently, and setting it by hand would turn that off and hand the
// caller compressed bytes.
//
// Two rules a caller must know:
//
//   - 304 Not Modified comes back as a RESPONSE, not an error. It is the
//     answer a conditional GET is asking for, and a caller that could not
//     see it would re-download an unchanged document every time. Every
//     other non-2xx is mapped exactly as DoJSON maps it.
//   - On an error the body is already closed and the returned response is
//     nil; on success closing it is the caller's job.
func (c *Client) DoStream(ctx context.Context, method, path string, header map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.authURL(path), nil)
	if err != nil {
		return nil, c.requestError("creating request", path, err)
	}
	c.applyAuthHeader(req)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.requestError("executing request", path, err)
	}
	if resp.StatusCode == http.StatusNotModified || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return resp, nil
	}

	errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("API error (status %d); reading body: %w", resp.StatusCode, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("API error (status %d); closing body: %w", resp.StatusCode, closeErr)
	}
	// Redacted before the mapper, not after: a mapper is free to interpolate
	// the body into its own message, and an upstream error page that echoes
	// the request back is exactly how a key reaches a terminal.
	errBody = []byte(c.redact(string(errBody)))
	if c.errorMapper != nil {
		if mapped := c.errorMapper(resp.StatusCode, errBody, path); mapped != nil {
			return nil, mapped
		}
	}
	return nil, c.statusError(resp.StatusCode, errBody)
}
