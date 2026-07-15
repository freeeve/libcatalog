package overdrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// DefaultBaseURL is the public OverDrive "thunder" v2 API base. Live mode pages
// through GET {base}/libraries/{library}/media.
const DefaultBaseURL = "https://thunder.api.overdrive.com/v2"

// defaultPerPage is the page size requested when none is configured.
const defaultPerPage = 200

// defaultRate is the polite minimum gap between live requests when none is set,
// so a full crawl does not hammer the public API.
const defaultRate = time.Second

// mediaPage is the thunder /media response: the items plus the pagination
// cursors the crawl needs (Next is nil past the last page).
type mediaPage struct {
	TotalItems int    `json:"totalItems"`
	Items      []Item `json:"items"`
	Links      struct {
		Last struct {
			Page int `json:"page"`
		} `json:"last"`
		Next *struct {
			Page int `json:"page"`
		} `json:"next"`
	} `json:"links"`
}

// liveFetcher pages the OverDrive thunder API for a library and, when writeDir
// is set, mirrors each raw page into the page-*.json cache layout ReadCache
// consumes -- so a live run can seed the cache a later offline build reuses.
type liveFetcher struct {
	baseURL  string
	library  string
	perPage  int
	writeDir string
	rate     time.Duration
	client   *http.Client
}

// items crawls every page in order until the API reports the end (no Next
// cursor, or the current page reached Last), returning all items. It observes
// ctx cancellation between and during requests, unlike the local cache read.
func (lf liveFetcher) items(ctx context.Context) ([]Item, error) {
	client := lf.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var items []Item
	for page := 1; ; page++ {
		if page > 1 && lf.rate > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(lf.rate):
			}
		}
		p, raw, err := lf.fetchPage(ctx, client, page)
		if err != nil {
			return nil, err
		}
		items = append(items, p.Items...)
		if lf.writeDir != "" {
			if err := writePage(lf.writeDir, page, raw); err != nil {
				return nil, err
			}
		}
		if p.Links.Next == nil || page >= p.Links.Last.Page {
			break
		}
	}
	return items, nil
}

// fetchPage requests one page and returns the parsed page plus its raw bytes
// (for the optional cache mirror).
func (lf liveFetcher) fetchPage(ctx context.Context, client *http.Client, page int) (*mediaPage, []byte, error) {
	u, err := url.Parse(fmt.Sprintf("%s/libraries/%s/media", lf.baseURL, url.PathEscape(lf.library)))
	if err != nil {
		return nil, nil, fmt.Errorf("overdrive: build url: %w", err)
	}
	perPage := lf.perPage
	if perPage <= 0 {
		perPage = defaultPerPage
	}
	q := u.Query()
	q.Set("page", fmt.Sprintf("%d", page))
	q.Set("perPage", fmt.Sprintf("%d", perPage))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("overdrive: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "libcat-overdrive/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("overdrive: fetch page %d: %w", page, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("overdrive: read page %d: %w", page, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, body, fmt.Errorf("overdrive: page %d: status %d", page, resp.StatusCode)
	}
	var p mediaPage
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, body, fmt.Errorf("overdrive: decode page %d: %w", page, err)
	}
	return &p, body, nil
}

// writePage mirrors one raw page into dir as page-NNNNN.json (the layout
// ReadCache globs), written atomically so a partial file never reads as a page.
func writePage(dir string, page int, raw []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("overdrive: cache dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, fmt.Sprintf("page-%05d.json", page))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("overdrive: write %s: %w", path, err)
	}
	return os.Rename(tmp, path)
}
