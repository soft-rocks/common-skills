package main

// SVG downloader for Iconify (https://icon-sets.iconify.design/).
// That site is only a browser for https://api.iconify.design, and this CLI
// calls the same API directly.
//
//   search  find icons, across all sets or within one
//   sets    list icon sets
//   list    list icon names inside one set
//   get     save the named icons as .svg files

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const apiBase = "https://api.iconify.design"

var httpClient = &http.Client{Timeout: 30 * time.Second}

type collection struct {
	Name     string `json:"name"`
	Total    int    `json:"total"`
	Category string `json:"category"`
	Palette  bool   `json:"palette"`
	Author   struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"author"`
	License struct {
		Title string `json:"title"`
		SPDX  string `json:"spdx"`
		URL   string `json:"url"`
	} `json:"license"`
	Samples []string `json:"samples"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "search":
		cmdSearch(os.Args[2:])
	case "sets":
		cmdSets(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "get":
		cmdGet(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// defaultOutDir keeps downloads out of the working tree. Pass -out to write
// icons into the project that needs them.
const defaultOutDir = "/tmp/common-skills/iconify-svg"

func usage() {
	fmt.Fprint(os.Stderr, `iconify-svg - download SVG icons from Iconify

  go run main.go search -q <keywords> [-prefix <set>] [-limit 64]
  go run main.go sets [-q <keywords>]
  go run main.go list -prefix <set> [-q <keywords>] [-limit 200]
  go run main.go get <prefix:name> [<prefix:name> ...] -out <dir> [-color "#333"] [-size 24]

Examples:
  go run main.go search -q "scale of justice" -limit 40
  go run main.go get mdi:scale-balance tabler:gavel -out ./assets/icons -color "#1f3a5f" -size 48
`)
}

// getJSON calls the API and decodes the response into v.
func getJSON(path string, q url.Values, v any) error {
	u := apiBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := httpClient.Get(u)
	if err != nil {
		return fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("parse %s: %w", u, err)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// ---------- search ----------

func cmdSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	q := fs.String("q", "", "keywords, required, English only, may be several words such as \"scale of justice\"")
	prefix := fs.String("prefix", "", "search only this icon set, for example mdi")
	limit := fs.Int("limit", 64, "maximum results, the API caps this at 999")
	fs.Parse(args)

	if *q == "" {
		fatal(fmt.Errorf("search requires -q"))
	}
	params := url.Values{"query": {*q}, "limit": {fmt.Sprint(*limit)}}
	if *prefix != "" {
		params.Set("prefix", *prefix)
	}
	var res struct {
		Icons       []string              `json:"icons"`
		Total       int                   `json:"total"`
		Collections map[string]collection `json:"collections"`
	}
	if err := getJSON("/search", params, &res); err != nil {
		fatal(err)
	}
	if len(res.Icons) == 0 {
		fmt.Printf("no results for %q. Try different English wording; Iconify only matches English.\n", *q)
		return
	}

	// Group by icon set, so icons from one set stay together and are easier to compare.
	byPrefix := map[string][]string{}
	var order []string
	for _, icon := range res.Icons {
		p, n, ok := strings.Cut(icon, ":")
		if !ok {
			continue
		}
		if _, seen := byPrefix[p]; !seen {
			order = append(order, p)
		}
		byPrefix[p] = append(byPrefix[p], n)
	}

	fmt.Printf("query %q, %d results\n\n", *q, res.Total)
	for _, p := range order {
		c := res.Collections[p]
		title := c.Name
		if title == "" {
			title = p
		}
		fmt.Printf("%s (%s, license %s)\n", p, title, licenseOf(c))
		for _, n := range byPrefix[p] {
			fmt.Printf("  %s:%s\n", p, n)
		}
		fmt.Println()
	}
	fmt.Println("once you have picked: go run main.go get <prefix:name> ... -out <dir>")
}

func licenseOf(c collection) string {
	switch {
	case c.License.SPDX != "":
		return c.License.SPDX
	case c.License.Title != "":
		return c.License.Title
	default:
		return "unspecified"
	}
}

// ---------- sets ----------

func cmdSets(args []string) {
	fs := flag.NewFlagSet("sets", flag.ExitOnError)
	q := fs.String("q", "", "only show sets whose name or prefix contains this")
	fs.Parse(args)

	var cols map[string]collection
	if err := getJSON("/collections", nil, &cols); err != nil {
		fatal(err)
	}
	prefixes := make([]string, 0, len(cols))
	for p := range cols {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	needle := strings.ToLower(*q)
	n := 0
	for _, p := range prefixes {
		c := cols[p]
		if needle != "" && !strings.Contains(strings.ToLower(p), needle) && !strings.Contains(strings.ToLower(c.Name), needle) {
			continue
		}
		n++
		fmt.Printf("%-28s %-42s %6d icons  license %s\n", p, c.Name, c.Total, licenseOf(c))
	}
	fmt.Printf("\n%d sets shown, %d in total\n", n, len(cols))
}

// ---------- list ----------

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	prefix := fs.String("prefix", "", "icon set prefix, required, for example mdi")
	q := fs.String("q", "", "only show icons whose name contains this")
	limit := fs.Int("limit", 200, "maximum icons to print, 0 for all")
	fs.Parse(args)

	if *prefix == "" {
		fatal(fmt.Errorf("list requires -prefix"))
	}
	var res struct {
		Prefix        string              `json:"prefix"`
		Title         string              `json:"title"`
		Total         int                 `json:"total"`
		Uncategorized []string            `json:"uncategorized"`
		Categories    map[string][]string `json:"categories"`
		Hidden        []string            `json:"hidden"`
	}
	if err := getJSON("/collection", url.Values{"prefix": {*prefix}}, &res); err != nil {
		fatal(err)
	}

	names := append([]string{}, res.Uncategorized...)
	cats := make([]string, 0, len(res.Categories))
	for c := range res.Categories {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	for _, c := range cats {
		names = append(names, res.Categories[c]...)
	}

	// One icon can appear under several categories, so deduplicate before printing.
	needle := strings.ToLower(*q)
	seen := map[string]bool{}
	matched := names[:0:0]
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		if needle == "" || strings.Contains(strings.ToLower(n), needle) {
			matched = append(matched, n)
		}
	}

	fmt.Printf("%s (%s) has %d icons, %d matched\n\n", res.Prefix, res.Title, res.Total, len(matched))
	for i, n := range matched {
		if *limit > 0 && i >= *limit {
			fmt.Printf("\n%d more not shown. Narrow with -q or print all with -limit 0\n", len(matched)-*limit)
			break
		}
		fmt.Printf("  %s:%s\n", res.Prefix, n)
	}
}

// ---------- get ----------

func cmdGet(args []string) {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	out := fs.String("out", defaultOutDir, "output directory, created when missing")
	name := fs.String("name", "", "custom file name without .svg, only valid for a single icon")
	color := fs.String("color", "", "replace currentColor with this colour, for example \"#1f3a5f\" or red")
	size := fs.Int("size", 0, "set both width and height, 0 keeps the original size")
	width := fs.Int("width", 0, "width, overrides -size")
	height := fs.Int("height", 0, "height, overrides -size")
	flip := fs.String("flip", "", "flip: horizontal, vertical, or horizontal,vertical")
	rotate := fs.Int("rotate", 0, "rotation, only 90, 180 or 270")
	box := fs.Bool("box", false, "add a transparent frame equal to the viewBox, for aligning several icons")
	// flag stops at the first non-flag argument, so peel arguments alternately.
	// That lets prefix:name values and flags be interleaved in any order.
	var icons []string
	rest := args
	for {
		fs.Parse(rest)
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		icons = append(icons, rest[0])
		rest = rest[1:]
	}
	if len(icons) == 0 {
		fatal(fmt.Errorf("get needs at least one prefix:name, for example mdi:home"))
	}
	if *rotate != 0 && *rotate != 90 && *rotate != 180 && *rotate != 270 {
		fatal(fmt.Errorf("-rotate accepts only 90, 180 or 270, got %d", *rotate))
	}
	if *name != "" && len(icons) > 1 {
		fatal(fmt.Errorf("-name works with a single icon, got %d", len(icons)))
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}

	params := url.Values{}
	if *color != "" {
		params.Set("color", *color)
	}
	w, h := *width, *height
	if *size > 0 {
		if w == 0 {
			w = *size
		}
		if h == 0 {
			h = *size
		}
	}
	if w > 0 {
		params.Set("width", fmt.Sprint(w))
	}
	if h > 0 {
		params.Set("height", fmt.Sprint(h))
	}
	if *flip != "" {
		params.Set("flip", *flip)
	}
	if *rotate != 0 {
		params.Set("rotate", fmt.Sprintf("%ddeg", *rotate))
	}
	if *box {
		params.Set("box", "true")
	}

	ok, failed := 0, 0
	for i, icon := range icons {
		if i > 0 {
			time.Sleep(200 * time.Millisecond) // stay polite to a public API
		}
		p, n, found := strings.Cut(icon, ":")
		if !found || p == "" || n == "" {
			fmt.Fprintf(os.Stderr, "skipping %q: expected prefix:name\n", icon)
			failed++
			continue
		}
		file := *name
		if file == "" {
			file = p + "-" + n
		}
		dst := filepath.Join(*out, file+".svg")
		if err := download(p, n, params, dst); err != nil {
			fmt.Fprintf(os.Stderr, "%s failed: %v\n", icon, err)
			failed++
			continue
		}
		ok++
	}
	fmt.Printf("\nsaved %d, failed %d, directory %s\n", ok, failed, *out)
	if failed > 0 {
		os.Exit(1)
	}
}

func download(prefix, name string, params url.Values, dst string) error {
	u := fmt.Sprintf("%s/%s/%s.svg", apiBase, prefix, name)
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	resp, err := httpClient.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// A misspelled name returns 404 with the plain text "Not found", not SVG.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(string(body), "<svg") {
		return fmt.Errorf("response is not SVG: %s", strings.TrimSpace(string(body)))
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s:%s -> %s (%d bytes)\n", prefix, name, dst, len(body))
	return nil
}
