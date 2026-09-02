// unsplash-search queries the Unsplash photo API and prints candidate images
// with the attribution the API guidelines require.
//
// It keeps a ledger of photos already used, so repeat queries on the same topic
// stop resurfacing the same stock photo. Call -mark after you actually use an
// image to add it to that ledger.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	apiBase = "https://api.unsplash.com"
	utmQ    = "?utm_source=unsplash-search-cli&utm_medium=referral"
)

// defaultLedgerPath is fixed rather than resolved relative to the binary,
// because `go run` builds into a temp directory and a repo-relative path would
// silently miss. Override it with $UNSPLASH_LEDGER_PATH or -ledger.
const defaultLedgerPath = "/tmp/common-skills/unsplash-search/used-photos.json"

// ledger is the on-disk dedupe record. ResetAt is when it was last cleared.
// Clearing is manual, through -reset-ledger.
type ledger struct {
	ResetAt time.Time            `json:"reset_at"`
	Photos  map[string]time.Time `json:"photos"`
}

func (l *ledger) has(slug string) bool {
	_, ok := l.Photos[slug]
	return ok
}

// envOr reads a string setting, falling back to def when unset or empty.
func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// Photo mirrors a single Unsplash photo result, with attribution fields
// required by the Unsplash API guidelines.
type Photo struct {
	ID              string            `json:"id"`
	Description     *string           `json:"description"`
	AltDescription  *string           `json:"alt_description"`
	Width           int               `json:"width"`
	Height          int               `json:"height"`
	Color           string            `json:"color"`
	BlurHash        *string           `json:"blur_hash"`
	URLs            map[string]string `json:"urls"`
	User            User              `json:"user"`
	photographerURL string
}

type User struct {
	Name     string `json:"name"`
	Username string `json:"username"`
}

type photoResult struct {
	Results []Photo `json:"results"`
	Total   int     `json:"total"`
}

func main() {
	var (
		query       = flag.String("query", "", "search keyword(s), e.g. \"mountain sunset\"")
		accessKey   = flag.String("access-key", "", "Unsplash API access key (defaults to $UNSPLASH_ACCESS_KEY)")
		perPage     = flag.Int("n", 10, "results per page (1-30)")
		page        = flag.Int("page", 1, "page number")
		order       = flag.String("order", "relevant", "sort: relevant or latest")
		color       = flag.String("color", "", "color filter: black_and_white, black, white, yellow, orange, red, purple, magenta, green, teal, blue")
		orientation = flag.String("orientation", "", "filter: landscape, portrait, squarish")
		content     = flag.String("content-filter", "low", "safety filter: low or high")
		download    = flag.String("download", "", "track a photo download by id (required when you save/use the image)")
		raw         = flag.Bool("raw", false, "print only image URLs")
		noAttrib    = flag.Bool("no-attrib", false, "omit attribution lines")
		ledgerPath  = flag.String("ledger", envOr("UNSPLASH_LEDGER_PATH", defaultLedgerPath), "path to the used-photos ledger ($UNSPLASH_LEDGER_PATH)")
		noDedupe    = flag.Bool("no-dedupe", false, "don't filter out photos already recorded in the ledger")
		mark        = flag.String("mark", "", "record an image URL as used, so later searches skip it")
		resetLedger = flag.Bool("reset-ledger", false, "clear the ledger now and exit")
	)
	flag.Parse()

	key := *accessKey
	if key == "" {
		key = os.Getenv("UNSPLASH_ACCESS_KEY")
	}
	if key == "" {
		log.Fatal("missing access key: pass -access-key or set $UNSPLASH_ACCESS_KEY")
	}

	client := &http.Client{Timeout: 30 * time.Second}

	if *resetLedger {
		l := ledger{ResetAt: time.Now(), Photos: map[string]time.Time{}}
		if err := saveLedger(*ledgerPath, &l); err != nil {
			log.Fatalf("reset ledger: %v", err)
		}
		fmt.Fprintf(os.Stderr, "cleared %s\n", *ledgerPath)
		return
	}

	if *mark != "" {
		if err := appendLedger(*ledgerPath, photoSlug(*mark)); err != nil {
			log.Fatalf("mark: %v", err)
		}
		fmt.Fprintf(os.Stderr, "recorded in %s\n", *ledgerPath)
		return
	}

	if *download != "" {
		u, err := trackDownload(client, key, *download)
		if err != nil {
			log.Fatalf("download: %v", err)
		}
		fmt.Println(u)
		return
	}

	if strings.TrimSpace(*query) == "" {
		log.Fatal("-query is required for search")
	}

	wantN := clamp(*perPage, 1, 30)
	used := &ledger{Photos: map[string]time.Time{}}
	if !*noDedupe {
		used = loadLedger(*ledgerPath)
	}

	var fresh []Photo
	skipped := 0
	firstPage := *page
	lastPageFetched := *page
	lastTotal := 0
	for attempt := 0; attempt < 5 && len(fresh) < wantN; attempt++ {
		lastPageFetched = firstPage + attempt
		data, err := search(client, key, *query, wantN, lastPageFetched, *order, *color, *orientation, *content)
		if err != nil {
			log.Fatalf("search: %v", err)
		}
		lastTotal = data.Total
		for _, p := range data.Results {
			if used.has(photoSlug(p.URLs["regular"])) {
				skipped++
				continue
			}
			fresh = append(fresh, p)
			if len(fresh) >= wantN {
				break
			}
		}
		if len(data.Results) == 0 || lastPageFetched*wantN >= data.Total {
			break // no more pages to try
		}
	}

	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "skipped %d already-used photo(s) (see %s)\n", skipped, *ledgerPath)
	}
	if lastPageFetched > firstPage {
		fmt.Fprintf(os.Stderr, "%d results (pages %d-%d, %d per page)\n", lastTotal, firstPage, lastPageFetched, wantN)
	} else {
		fmt.Fprintf(os.Stderr, "%d results (page %d, %d per page)\n", lastTotal, firstPage, wantN)
	}
	for i, p := range fresh {
		if *raw {
			fmt.Println(p.URLs["regular"])
			continue
		}
		desc := ""
		if p.AltDescription != nil {
			desc = *p.AltDescription
		}
		fmt.Printf("[%d] %s  (%dx%d)  %s\n", i+1, p.ID, p.Width, p.Height, desc)
		fmt.Printf("    regular: %s\n", p.URLs["regular"])
		fmt.Printf("    raw:     %s\n", p.URLs["raw"])
		fmt.Printf("    thumb:   %s\n", p.URLs["thumb"])
		fmt.Printf("    color:   %s\n", p.Color)
		if !*noAttrib {
			fmt.Printf("    credit:  %s\n", p.attributionText())
			fmt.Printf("    html:    %s\n", p.attributionHTML())
		}
		fmt.Println()
	}
}

func search(client *http.Client, key, query string, perPage, page int, order, color, orientation, content string) (*photoResult, error) {
	u, err := url.Parse(apiBase + "/search/photos")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("page", strconv.Itoa(max(1, page)))
	q.Set("per_page", strconv.Itoa(clamp(perPage, 1, 30)))
	q.Set("order_by", order)
	q.Set("content_filter", content)
	if color != "" {
		q.Set("color", color)
	}
	if orientation != "" {
		q.Set("orientation", orientation)
	}
	u.RawQuery = q.Encode()

	var out photoResult
	if err := doJSON(client, key, u.String(), &out); err != nil {
		return nil, err
	}
	for i := range out.Results {
		out.Results[i].photographerURL = "https://unsplash.com/@" + out.Results[i].User.Username
	}
	return &out, nil
}

func trackDownload(client *http.Client, key, id string) (string, error) {
	u := apiBase + "/photos/" + url.PathEscape(id) + "/download"
	var resp struct {
		URL string `json:"url"`
	}
	if err := doJSON(client, key, u, &resp); err != nil {
		return "", err
	}
	return resp.URL, nil
}

func doJSON(client *http.Client, key, u string, out interface{}) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Client-ID "+key)
	req.Header.Set("Accept-Version", "v1")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("invalid access key (401)")
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("rate limit exceeded (403)")
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("not found (404)")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (p *Photo) attributionText() string {
	return fmt.Sprintf("Photo by %s on Unsplash", p.User.Name)
}

func (p *Photo) attributionHTML() string {
	return fmt.Sprintf(`Photo by <a href="%s%s">%s</a> on <a href="https://unsplash.com%s">Unsplash</a>`,
		p.photographerURL, utmQ, p.User.Name, utmQ)
}

// photoSlug extracts the stable "photo-<id>" path segment from an Unsplash
// image URL, ignoring query params (size/format vary; the slug doesn't).
func photoSlug(imageURL string) string {
	u, err := url.Parse(imageURL)
	if err != nil {
		return imageURL
	}
	return strings.TrimPrefix(u.Path, "/")
}

// loadLedger reads the used-photos ledger. A missing or corrupt file is treated
// as empty, since this is a best-effort dedupe aid rather than a source of
// truth.
func loadLedger(path string) *ledger {
	now := time.Now()
	fresh := &ledger{ResetAt: now, Photos: map[string]time.Time{}}

	data, err := os.ReadFile(path)
	if err != nil {
		return fresh
	}

	var l ledger
	if err := json.Unmarshal(data, &l); err != nil || l.Photos == nil {
		return fresh
	}
	if l.ResetAt.IsZero() {
		l.ResetAt = now
	}
	return &l
}

func saveLedger(path string, l *ledger) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// appendLedger adds one photo slug to the ledger, creating the file when it is
// missing. Recording a slug already present just refreshes nothing.
func appendLedger(path, slug string) error {
	if slug == "" {
		return fmt.Errorf("empty photo slug")
	}
	l := loadLedger(path)
	if l.has(slug) {
		return nil
	}
	l.Photos[slug] = time.Now()
	return saveLedger(path, l)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
