// text-to-svg renders text as SVG outline paths using a font's own glyph
// outlines, with no browser and no rasterization.
//
// Fonts come from a local file or from the Google Fonts API. Glyphs are read
// with golang.org/x/image/font/sfnt, whose segment coordinates already run
// y-down like SVG, so the outlines need no mirroring.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const (
	// Both endpoints are the ones fonts.google.com uses for itself and need no
	// API key. The developer API at googleapis.com/webfonts does need one, and
	// buys nothing extra here.
	metadataURL   = "https://fonts.google.com/metadata/fonts"
	cssAPI        = "https://fonts.googleapis.com/css2"
	defaultOutDir = "/tmp/common-skills/text-to-svg"
)

// fontCacheDir holds downloaded Google fonts. CJK families run to tens of
// megabytes, so re-downloading one per run would be wasteful.
var fontCacheDir = filepath.Join(defaultOutDir, "fonts")

// familyMeta is one entry from the metadata endpoint. Fonts is keyed by weight,
// so its keys are the available variants.
type familyMeta struct {
	Family   string         `json:"family"`
	Category string         `json:"category"`
	Subsets  []string       `json:"subsets"`
	Fonts    map[string]any `json:"fonts"`
}

type metadataResponse struct {
	FamilyMetadataList []familyMeta `json:"familyMetadataList"`
}

// weights returns the available variants, numerically ordered. The metadata
// spells italics "700i"; both that and "700italic" are accepted by -variant.
func (f familyMeta) weights() []string {
	out := make([]string, 0, len(f.Fonts))
	for w := range f.Fonts {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i])
		b, _ := strconv.Atoi(out[j])
		if a != b {
			return a < b
		}
		return out[i] < out[j]
	})
	return out
}

func main() {
	text := flag.String("text", "Hello", "text to render, \\n separates lines")
	fontFamily := flag.String("font", "", "Google Fonts family name, for example \"Noto Serif KR\"")
	variant := flag.String("variant", "regular", "font variant, for example regular, 700, italic")
	fontFile := flag.String("font-file", "", "local .ttf or .otf file, skips the Google Fonts API entirely")
	size := flag.Float64("size", 72, "font size in SVG units")
	fill := flag.String("fill", "#000000", "fill colour, or none")
	stroke := flag.String("stroke", "", "stroke colour, empty for no stroke")
	strokeWidth := flag.Float64("stroke-width", 1, "stroke width")
	lineHeight := flag.Float64("line-height", 1.2, "line height as a multiple of size")
	letterSpacing := flag.Float64("letter-spacing", 0, "extra space between characters, in SVG units")
	padding := flag.Float64("padding", 0, "padding around the text, in SVG units")
	background := flag.String("bg", "", "background rectangle colour, empty for none")
	separate := flag.Bool("separate", false, "emit one path element per character instead of one combined path")
	kern := flag.Bool("kern", true, "apply the font's kerning pairs")
	precision := flag.Int("precision", 2, "decimal places in path coordinates")
	out := flag.String("out", filepath.Join(defaultOutDir, "text.svg"), "output .svg path, or - for stdout")
	list := flag.String("list", "", "list Google Fonts families matching this substring, or * for all, then exit")
	flag.Parse()

	if *list != "" {
		if err := listFamilies(*list); err != nil {
			log.Fatalf("list: %v", err)
		}
		return
	}

	if *fontFile == "" && *fontFamily == "" {
		log.Fatal("pass -font-file for a local font, or -font for a Google Fonts family")
	}

	data, err := loadFontBytes(*fontFile, *fontFamily, *variant)
	if err != nil {
		log.Fatalf("load font: %v", err)
	}
	f, err := sfnt.Parse(data)
	if err != nil {
		log.Fatalf("parse font: %v", err)
	}

	svg, err := render(f, renderOptions{
		Text:          strings.ReplaceAll(*text, "\\n", "\n"),
		Size:          *size,
		Fill:          *fill,
		Stroke:        *stroke,
		StrokeWidth:   *strokeWidth,
		LineHeight:    *lineHeight,
		LetterSpacing: *letterSpacing,
		Padding:       *padding,
		Background:    *background,
		Separate:      *separate,
		Kern:          *kern,
		Precision:     *precision,
	})
	if err != nil {
		log.Fatalf("render: %v", err)
	}

	if *out == "-" {
		fmt.Print(svg)
		return
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}
	if err := os.WriteFile(*out, []byte(svg), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", *out, len(svg))
}

type renderOptions struct {
	Text          string
	Size          float64
	Fill          string
	Stroke        string
	StrokeWidth   float64
	LineHeight    float64
	LetterSpacing float64
	Padding       float64
	Background    string
	Separate      bool
	Kern          bool
	Precision     int
}

// glyphRun is one character placed on a line, kept so -separate can emit each
// as its own path element.
type glyphRun struct {
	Char rune
	Data string
}

func render(f *sfnt.Font, o renderOptions) (string, error) {
	var buf sfnt.Buffer
	ppem := fixed.Int26_6(o.Size * 64)

	metrics, err := f.Metrics(&buf, ppem, font.HintingNone)
	if err != nil {
		return "", fmt.Errorf("font metrics: %w", err)
	}
	ascent := f26(metrics.Ascent)
	descent := f26(metrics.Descent)

	lines := strings.Split(o.Text, "\n")
	var runs []glyphRun
	widest := 0.0

	for lineIndex, line := range lines {
		baseline := ascent + float64(lineIndex)*o.Size*o.LineHeight
		penX := 0.0
		var prev sfnt.GlyphIndex
		havePrev := false

		for _, r := range line {
			gi, err := f.GlyphIndex(&buf, r)
			if err != nil {
				return "", fmt.Errorf("glyph index for %q: %w", r, err)
			}
			if gi == 0 {
				fmt.Fprintf(os.Stderr, "warning: %q is not in this font, skipping\n", r)
				continue
			}

			if havePrev && o.Kern {
				k, err := f.Kern(&buf, prev, gi, ppem, font.HintingNone)
				if err == nil {
					penX += f26(k)
				}
			}

			segs, err := f.LoadGlyph(&buf, gi, ppem, nil)
			if err != nil {
				return "", fmt.Errorf("load glyph %q: %w", r, err)
			}
			if d := pathData(segs, penX, baseline, o.Precision); d != "" {
				runs = append(runs, glyphRun{Char: r, Data: d})
			}

			adv, err := f.GlyphAdvance(&buf, gi, ppem, font.HintingNone)
			if err != nil {
				return "", fmt.Errorf("advance for %q: %w", r, err)
			}
			penX += f26(adv) + o.LetterSpacing
			prev, havePrev = gi, true
		}

		// The trailing letter-spacing is not part of the visible line.
		w := penX
		if len(line) > 0 {
			w -= o.LetterSpacing
		}
		if w > widest {
			widest = w
		}
	}

	width := widest + 2*o.Padding
	height := ascent + descent + float64(len(lines)-1)*o.Size*o.LineHeight + 2*o.Padding
	return buildSVG(runs, width, height, o), nil
}

// f26 converts a 26.6 fixed point value to float64.
func f26(v fixed.Int26_6) float64 { return float64(v) / 64 }

// pathData converts one glyph's segments to SVG path data, offset to its
// position on the page. sfnt segment coordinates already run y-down, the same
// as SVG, so no mirroring is needed; only the baseline offset is applied.
func pathData(segs []sfnt.Segment, dx, baseline float64, prec int) string {
	var b strings.Builder
	n := func(v fixed.Int26_6, offset float64) string {
		return trim(f26(v)+offset, prec)
	}
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			// Each MoveTo starts a new contour, so close the previous one.
			// Leaving them open is invisible under a fill but shows up as
			// missing edges as soon as a stroke is applied.
			if b.Len() > 0 {
				b.WriteString("Z")
			}
			fmt.Fprintf(&b, "M%s %s", n(s.Args[0].X, dx), n(s.Args[0].Y, baseline))
		case sfnt.SegmentOpLineTo:
			fmt.Fprintf(&b, "L%s %s", n(s.Args[0].X, dx), n(s.Args[0].Y, baseline))
		case sfnt.SegmentOpQuadTo:
			fmt.Fprintf(&b, "Q%s %s %s %s",
				n(s.Args[0].X, dx), n(s.Args[0].Y, baseline),
				n(s.Args[1].X, dx), n(s.Args[1].Y, baseline))
		case sfnt.SegmentOpCubeTo:
			fmt.Fprintf(&b, "C%s %s %s %s %s %s",
				n(s.Args[0].X, dx), n(s.Args[0].Y, baseline),
				n(s.Args[1].X, dx), n(s.Args[1].Y, baseline),
				n(s.Args[2].X, dx), n(s.Args[2].Y, baseline))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	// Close the final contour.
	b.WriteString("Z")
	return b.String()
}

// trim formats a number with at most prec decimals and no trailing zeros.
func trim(v float64, prec int) string {
	s := fmt.Sprintf("%.*f", prec, v)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s
}

func buildSVG(runs []glyphRun, width, height float64, o renderOptions) string {
	var attrs []string
	if o.Fill != "" && o.Fill != "none" {
		attrs = append(attrs, fmt.Sprintf(`fill="%s"`, o.Fill))
	} else {
		attrs = append(attrs, `fill="none"`)
	}
	if o.Stroke != "" {
		attrs = append(attrs, fmt.Sprintf(`stroke="%s"`, o.Stroke))
		attrs = append(attrs, fmt.Sprintf(`stroke-width="%s"`, trim(o.StrokeWidth, 3)))
	}
	shared := strings.Join(attrs, " ")

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s">`+"\n",
		trim(width, 3), trim(height, 3), trim(width, 3), trim(height, 3))

	if o.Background != "" {
		fmt.Fprintf(&b, `  <rect width="100%%" height="100%%" fill="%s"/>`+"\n", o.Background)
	}

	shift := ""
	if o.Padding != 0 {
		shift = fmt.Sprintf(` transform="translate(%s %s)"`, trim(o.Padding, 3), trim(o.Padding, 3))
	}
	fmt.Fprintf(&b, `  <g %s%s>`+"\n", shared, shift)

	if o.Separate {
		for i, r := range runs {
			fmt.Fprintf(&b, `    <path id="c%d" data-char="%s" d="%s"/>`+"\n", i, escapeAttr(string(r.Char)), r.Data)
		}
	} else {
		var all strings.Builder
		for _, r := range runs {
			all.WriteString(r.Data)
		}
		fmt.Fprintf(&b, `    <path d="%s"/>`+"\n", all.String())
	}

	b.WriteString("  </g>\n</svg>\n")
	return b.String()
}

func escapeAttr(s string) string {
	return strings.NewReplacer(`&`, "&amp;", `<`, "&lt;", `>`, "&gt;", `"`, "&quot;").Replace(s)
}

// loadFontBytes returns the font file, from a local path when given, otherwise
// from Google Fonts with a local cache.
func loadFontBytes(fontFile, family, variant string) ([]byte, error) {
	if fontFile != "" {
		return os.ReadFile(fontFile)
	}

	cached := filepath.Join(fontCacheDir, cacheName(family, variant))
	if b, err := os.ReadFile(cached); err == nil {
		return b, nil
	}

	fileURL, err := resolveFontURL(family, variant)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "downloading %s %s\n", family, variant)
	b, err := httpGet(fileURL)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(fontCacheDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cached, b, 0o644); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "cached %s (%d bytes)\n", cached, len(b))
	return b, nil
}

// resolveFontURL asks the CSS API for a family and pulls the font file URL out
// of the @font-face block it returns. The API serves TrueType to a non-browser
// client and woff2 to a modern browser, and this tool cannot read woff2, so the
// result is checked rather than assumed.
func resolveFontURL(family, variant string) (string, error) {
	weight, italic := parseVariant(variant)

	spec := family + ":wght@" + weight
	if italic {
		spec = family + ":ital,wght@1," + weight
	}
	u := cssAPI + "?family=" + url.QueryEscape(spec)

	body, err := httpGet(u)
	if err != nil {
		return "", describeFamilyError(family, variant, err)
	}
	m := fontURLPattern.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("no font file in the CSS response for %q %q", family, variant)
	}
	fileURL := string(m[1])
	if !strings.HasSuffix(fileURL, ".ttf") && !strings.HasSuffix(fileURL, ".otf") {
		return "", fmt.Errorf("Google returned %s, which this tool cannot read. Download the family and use -font-file", filepath.Ext(fileURL))
	}
	return fileURL, nil
}

var fontURLPattern = regexp.MustCompile(`url\((https://[^)]+)\)`)

// parseVariant accepts every spelling in play: "regular", "italic", a bare
// weight such as "700", the metadata style "700i" that -list prints, and the
// older "700italic". Returning them all keeps the listing and the flag honest
// about each other.
func parseVariant(variant string) (weight string, italic bool) {
	v := strings.ToLower(strings.TrimSpace(variant))
	switch v {
	case "", "regular", "normal":
		return "400", false
	case "italic", "i":
		return "400", true
	}
	if strings.HasSuffix(v, "italic") {
		return defaultWeight(strings.TrimSuffix(v, "italic")), true
	}
	if strings.HasSuffix(v, "i") {
		return defaultWeight(strings.TrimSuffix(v, "i")), true
	}
	return defaultWeight(v), false
}

func defaultWeight(w string) string {
	if w == "" {
		return "400"
	}
	return w
}

// describeFamilyError turns the CSS API's flat 400 into something actionable by
// checking the family against the metadata list.
func describeFamilyError(family, variant string, cause error) error {
	list, mErr := fetchMetadata()
	if mErr != nil {
		return cause
	}
	needle := strings.ToLower(family)
	for _, it := range list.FamilyMetadataList {
		if strings.EqualFold(it.Family, family) {
			return fmt.Errorf("variant %q not available for %q, have: %s",
				variant, it.Family, strings.Join(it.weights(), ", "))
		}
	}
	var near []string
	for _, it := range list.FamilyMetadataList {
		if strings.Contains(strings.ToLower(it.Family), needle) {
			near = append(near, it.Family)
		}
	}
	if len(near) > 0 {
		sort.Strings(near)
		return fmt.Errorf("no family named %q. Did you mean: %s", family, strings.Join(near, ", "))
	}
	return fmt.Errorf("no family named %q, and nothing similar. Use -list to search", family)
}

// cacheName keys the cache on the resolved weight rather than the spelling the
// caller used, so "700i" and "700italic" share one cached file.
func cacheName(family, variant string) string {
	safe := strings.NewReplacer(" ", "-", "/", "-", string(filepath.Separator), "-").Replace(family)
	weight, italic := parseVariant(variant)
	suffix := weight
	if italic {
		suffix += "i"
	}
	return fmt.Sprintf("%s-%s.ttf", strings.ToLower(safe), suffix)
}

func fetchMetadata() (*metadataResponse, error) {
	b, err := httpGet(metadataURL)
	if err != nil {
		return nil, err
	}
	var out metadataResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode font metadata: %w", err)
	}
	return &out, nil
}

func listFamilies(needle string) error {
	list, err := fetchMetadata()
	if err != nil {
		return err
	}
	n := strings.ToLower(needle)
	count := 0
	for _, it := range list.FamilyMetadataList {
		if n != "*" && !strings.Contains(strings.ToLower(it.Family), n) {
			continue
		}
		fmt.Printf("%-34s %-10s %s\n", it.Family, it.Category, strings.Join(it.weights(), ", "))
		count++
	}
	fmt.Fprintf(os.Stderr, "%d of %d families matched %q\n", count, len(list.FamilyMetadataList), needle)
	return nil
}

func httpGet(u string) ([]byte, error) {
	client := &http.Client{Timeout: 120 * time.Second}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "text-to-svg (+https://github.com/soft-rocks/common-skills)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET %s: %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}
