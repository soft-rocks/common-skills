// edge-tts synthesizes speech to mp3 through the websocket endpoint behind
//
// Microsoft Edge's read-aloud feature. No account, no key, no quota.
//
// Each run writes an mp3, a matching single-cue .srt, and an index.json entry
// recording the hash, paths, duration and voice.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

const (
	trustedToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	wsBase       = "wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1"
	listBase     = "https://speech.platform.bing.com/consumer/speech/synthesize/readaloud/voices/list"
	winEpoch     = 11644473600 // seconds between the Unix and Windows FILETIME epochs

	// defaultOutDir keeps output out of the working tree. Pass -out to write
	// into the project that needs the audio.
	defaultOutDir = "/tmp/common-skills/edge-tts"

	defaultVoice = "fr-FR-VivienneMultilingualNeural"
	audioFormat  = "audio-24khz-48kbitrate-mono-mp3"
)

// knownVersions holds Sec-MS-GEC-Version strings observed to work. The endpoint
// rejects versions it considers too old: as of September 2026, 130 and 131 both
// return 403 while 140 is accepted. This is the first thing to update when
// Microsoft raises the floor. They are tried in order and the first that
// completes a handshake wins.
var knownVersions = []string{
	"140.0.3485.54",
	"139.0.3405.86",
	"138.0.3351.83",
	"137.0.3296.68",
}

type IndexEntry struct {
	Hash         string  `json:"hash"`          // sha256(text)
	Text         string  `json:"text"`          // original subtitle or narration text
	Audio        string  `json:"audio"`         // audio file path
	SubtitleFile string  `json:"subtitle_file"` // .srt path in the same directory
	Duration     float64 `json:"duration"`      // seconds, from ffprobe, 0 when unavailable
	Voice        string  `json:"voice"`
	Tone         string  `json:"tone"`  // always empty; this endpoint takes no tone instruction
	Speed        float64 `json:"speed"` // derived from -rate, where +0% is 1
	Pitch        float64 `json:"pitch"` // derived from -pitch, where +0Hz is 0
	Model        string  `json:"model"`
	JobID        string  `json:"job_id"` // holds the X-RequestId
	CreatedAt    string  `json:"created_at"`
	Rate         string  `json:"rate"`   // the raw flag value, such as "+0%"
	Volume       string  `json:"volume"` // the raw flag value, such as "+0%"
}

type IndexFile struct {
	Entries []IndexEntry `json:"entries"`
}

func main() {
	text := flag.String("text", "", "text to synthesize")
	textFile := flag.String("text-file", "", "read the text from a file instead; use this or -text, not both")
	voice := flag.String("voice", defaultVoice, "voice name; use -list to see what is available")
	rate := flag.String("rate", "+0%", "speaking rate offset, such as +20% or -10%")
	pitch := flag.String("pitch", "+0Hz", "pitch offset, such as +10Hz")
	volume := flag.String("volume", "+0%", "volume offset")
	out := flag.String("out", "", "output mp3 path (default: "+defaultOutDir+"/<hash>.mp3)")
	indexPath := flag.String("index", "", "index.json path, defaults to the audio file directory")
	maxRunes := flag.Int("max", 380, "characters per segment; longer input is split at sentence ends")
	retries := flag.Int("retry", 3, "retries per segment")
	edgeVersion := flag.String("edge-version", "", "force a Sec-MS-GEC-Version; empty tries knownVersions in order")
	list := flag.Bool("list", false, "list the available voices, then exit")
	locale := flag.String("locale", "", "with -list, show only this locale prefix, such as zh- or en-US")
	flag.Parse()

	if *list {
		if err := listVoices(*locale, *edgeVersion); err != nil {
			fatal(err)
		}
		return
	}

	body := *text
	if *textFile != "" {
		if body != "" {
			fatal(fmt.Errorf("pass -text or -text-file, not both"))
		}
		b, err := os.ReadFile(*textFile)
		if err != nil {
			fatal(err)
		}
		body = string(b)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		fatal(fmt.Errorf("nothing to synthesize: pass -text or -text-file"))
	}

	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(defaultOutDir, hashText(body)[:16]+".mp3")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fatal(err)
	}
	idxPath := *indexPath
	if idxPath == "" {
		idxPath = filepath.Join(filepath.Dir(outPath), "index.json")
	}

	chunks := splitText(body, *maxRunes)
	if len(chunks) > 1 {
		fmt.Fprintf(os.Stderr,
			"note: %d characters exceeds the %d limit, split into %d segments.\n"+
				"      Segments are a signal the source text wants breaking up at the source.\n",
			utf8.RuneCountInString(body), *maxRunes, len(chunks))
	}

	ver := *edgeVersion
	for i, chunk := range chunks {
		path := outPath
		if len(chunks) > 1 {
			ext := filepath.Ext(outPath)
			path = fmt.Sprintf("%s_%03d%s", strings.TrimSuffix(outPath, ext), i+1, ext)
		}

		audio, reqID, usedVer, err := synthRetry(chunk, *voice, *rate, *pitch, *volume, ver, *retries)
		if err != nil {
			fatal(fmt.Errorf("segment %d/%d failed: %w", i+1, len(chunks), err))
		}
		ver = usedVer // later segments reuse the version that worked

		if err := os.WriteFile(path, audio, 0o644); err != nil {
			fatal(err)
		}
		dur := probeDuration(path)
		srtPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".srt"
		if err := writeSRT(srtPath, chunk, dur); err != nil {
			fatal(err)
		}
		entry := IndexEntry{
			Hash:         hashText(chunk),
			Text:         chunk,
			Audio:        path,
			SubtitleFile: srtPath,
			Duration:     dur,
			Voice:        *voice,
			Speed:        percentToFactor(*rate),
			Pitch:        hzToFloat(*pitch),
			Model:        "edge",
			JobID:        reqID,
			CreatedAt:    time.Now().Format(time.RFC3339),
			Rate:         *rate,
			Volume:       *volume,
		}
		if err := appendIndex(idxPath, entry); err != nil {
			fatal(err)
		}
		fmt.Printf("%s  %.2fs  %d KB  %s\n", path, dur, len(audio)/1024, *voice)

		if i < len(chunks)-1 {
			time.Sleep(time.Second)
		}
	}
	fmt.Printf("index: %s\n", idxPath)
}

// synthRetry wraps synth with retries. When no version is forced it tries
// knownVersions in order and returns the one that worked, so later segments in
// the same run skip the search.
func synthRetry(text, voice, rate, pitch, volume, ver string, retries int) ([]byte, string, string, error) {
	versions := knownVersions
	if ver != "" {
		versions = []string{ver}
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			wait := time.Duration(attempt*2) * time.Second
			fmt.Fprintf(os.Stderr, "retry %d after %s: %v\n", attempt, wait, lastErr)
			time.Sleep(wait)
		}
		for _, v := range versions {
			audio, reqID, err := synth(text, voice, rate, pitch, volume, v)
			if err == nil {
				return audio, reqID, v, nil
			}
			lastErr = err
		}
	}
	if ver == "" {
		lastErr = fmt.Errorf("%w\n(every known Sec-MS-GEC-Version was rejected. Microsoft has "+
			"probably raised the floor again. Check the current Edge version, pass it "+
			"with -edge-version, and add it to knownVersions in main.go once it works)", lastErr)
	}
	return nil, "", "", lastErr
}

// synth runs one full synthesis: handshake, send config, send SSML, then read
// audio frames until turn.end.
func synth(text, voice, rate, pitch, volume, ver string) ([]byte, string, error) {
	reqID := randomHex16()
	url := fmt.Sprintf("%s?TrustedClientToken=%s&Sec-MS-GEC=%s&Sec-MS-GEC-Version=1-%s&ConnectionId=%s",
		wsBase, trustedToken, secMSGEC(), ver, randomHex16())

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 20 * time.Second
	conn, resp, err := dialer.Dial(url, browserHeaders(ver))
	if err != nil {
		if resp != nil {
			return nil, "", fmt.Errorf("handshake failed (version %s, HTTP %s): %w", ver, resp.Status, err)
		}
		return nil, "", fmt.Errorf("handshake failed (version %s): %w", ver, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))

	ts := time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
	config := "X-Timestamp:" + ts +
		"\r\nContent-Type:application/json; charset=utf-8\r\nPath:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false",` +
		`"wordBoundaryEnabled":"false"},"outputFormat":"` + audioFormat + `"}}}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(config)); err != nil {
		return nil, "", fmt.Errorf("send config failed: %w", err)
	}

	ssml := fmt.Sprintf(
		`<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='%s'>`+
			`<voice name='%s'><prosody pitch='%s' rate='%s' volume='%s'>%s</prosody></voice></speak>`,
		voiceLocale(voice), voice, pitch, rate, volume, escapeXML(text))
	request := "X-RequestId:" + reqID +
		"\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:" + ts + "Z\r\nPath:ssml\r\n\r\n" + ssml
	if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		return nil, "", fmt.Errorf("send SSML failed: %w", err)
	}

	var audio []byte
	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			if len(audio) > 0 {
				break // the server closes once done; audio already received counts as success
			}
			return nil, "", fmt.Errorf("read failed: %w", err)
		}
		if msgType == websocket.TextMessage {
			if strings.Contains(string(msg), "Path:turn.end") {
				break
			}
			continue
		}
		// Binary frame: two bytes of header length (big endian), then the header,
		// then the audio.
		if len(msg) < 2 {
			continue
		}
		headerLen := int(msg[0])<<8 | int(msg[1])
		if 2+headerLen > len(msg) {
			continue
		}
		if strings.Contains(string(msg[2:2+headerLen]), "Path:audio") {
			audio = append(audio, msg[2+headerLen:]...)
		}
	}
	if len(audio) == 0 {
		return nil, "", fmt.Errorf("connected but received no audio; check the voice name %q against -list", voice)
	}
	return audio, reqID, nil
}

func listVoices(locale, ver string) error {
	versions := knownVersions
	if ver != "" {
		versions = []string{ver}
	}
	var lastErr error
	for _, v := range versions {
		url := fmt.Sprintf("%s?trustedclienttoken=%s&Sec-MS-GEC=%s&Sec-MS-GEC-Version=1-%s",
			listBase, trustedToken, secMSGEC(), v)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}
		for k, vals := range browserHeaders(v) {
			req.Header.Set(k, vals[0])
		}
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("version %s: HTTP %s", v, resp.Status)
			continue
		}
		var voices []struct {
			ShortName string `json:"ShortName"`
			Gender    string `json:"Gender"`
			Locale    string `json:"Locale"`
			VoiceTag  struct {
				VoicePersonalities []string `json:"VoicePersonalities"`
			} `json:"VoiceTag"`
		}
		if err := json.Unmarshal(data, &voices); err != nil {
			return fmt.Errorf("parse voice list failed: %w", err)
		}
		n := 0
		for _, vo := range voices {
			if locale != "" && !strings.HasPrefix(vo.Locale, locale) {
				continue
			}
			n++
			fmt.Printf("%-34s %-7s %-14s %s\n", vo.ShortName, vo.Gender, vo.Locale,
				strings.Join(vo.VoiceTag.VoicePersonalities, ", "))
		}
		fmt.Fprintf(os.Stderr, "\n%d shown of %d total (Sec-MS-GEC-Version 1-%s)\n", n, len(voices), v)
		return nil
	}
	return lastErr
}

// secMSGEC computes the anti-replay token the endpoint requires: a Windows
// FILETIME rounded down to five minutes, concatenated with TrustedClientToken,
// hashed with SHA256 and rendered as uppercase hex.
func secMSGEC() string {
	ticks := time.Now().Unix() + winEpoch
	ticks -= ticks % 300
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d%s", ticks*10000000, trustedToken)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func browserHeaders(ver string) map[string][]string {
	major := strings.SplitN(ver, ".", 2)[0]
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
		major + ".0.0.0 Safari/537.36 Edg/" + major + ".0.0.0"
	return map[string][]string{
		"Pragma":          {"no-cache"},
		"Cache-Control":   {"no-cache"},
		"Origin":          {"chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold"},
		"Accept-Encoding": {"gzip, deflate, br"},
		"Accept-Language": {"en-US,en;q=0.9"},
		"User-Agent":      {ua},
	}
}

// voiceLocale takes the locale off the front of a voice name, so
// fr-FR-VivienneMultilingualNeural yields fr-FR. Multilingual voices detect the
// language from the SSML text itself, so this tag only has to be well formed.
func voiceLocale(voice string) string {
	parts := strings.Split(voice, "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return "en-US"
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func randomHex16() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%016x", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}

// percentToFactor turns "+20%" into 1.2 for the index.json speed field.
func percentToFactor(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(s, "+"), "%"), 64)
	if err != nil {
		return 1
	}
	return 1 + v/100
}

func hzToFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(s, "+"), "Hz"), 64)
	if err != nil {
		return 0
	}
	return v
}

func hashText(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// probeDuration reads the audio duration with ffprobe and returns 0 on failure.
func probeDuration(path string) float64 {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return 0
	}
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path).Output()
	if err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return f
}

// writeSRT writes a single-cue .srt spanning 00:00:00,000 -> duration.
func writeSRT(path, text string, duration float64) error {
	if duration <= 0 {
		duration = float64(utf8.RuneCountInString(text)) * 0.15 // rough estimate: 0.15s per rune
		if duration < 1 {
			duration = 1
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("1\n%s --> %s\n%s\n", formatSRTTime(0), formatSRTTime(duration), text)
	return os.WriteFile(path, []byte(content), 0o644)
}

func formatSRTTime(sec float64) string {
	totalMs := int(sec * 1000)
	ms := totalMs % 1000
	totalSec := totalMs / 1000
	s := totalSec % 60
	totalMin := totalSec / 60
	m := totalMin % 60
	h := totalMin / 60
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// splitText breaks text into chunks of at most max runes, preferring sentence ends.
func splitText(text string, max int) []string {
	text = strings.TrimSpace(text)
	if max <= 0 || utf8.RuneCountInString(text) <= max {
		return []string{text}
	}
	// Full width stops end a sentence on their own. ASCII stops count only when
	// whitespace or the end of the text follows, so a decimal point or an
	// abbreviation does not split a sentence in half.
	var sentences []string
	var cur strings.Builder
	runes := []rune(text)
	for i, r := range runes {
		cur.WriteRune(r)
		end := strings.ContainsRune("。！？；\n", r)
		if !end && strings.ContainsRune(".!?;", r) {
			end = i == len(runes)-1 || unicode.IsSpace(runes[i+1])
		}
		if end {
			sentences = append(sentences, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		sentences = append(sentences, cur.String())
	}
	var chunks []string
	var buf strings.Builder
	bufLen := 0
	for _, s := range sentences {
		sLen := utf8.RuneCountInString(s)
		if sLen > max {
			if bufLen > 0 {
				chunks = append(chunks, strings.TrimSpace(buf.String()))
				buf.Reset()
				bufLen = 0
			}
			runes := []rune(s)
			for i := 0; i < len(runes); i += max {
				end := i + max
				if end > len(runes) {
					end = len(runes)
				}
				chunks = append(chunks, string(runes[i:end]))
			}
			continue
		}
		if bufLen+sLen > max {
			chunks = append(chunks, strings.TrimSpace(buf.String()))
			buf.Reset()
			bufLen = 0
		}
		buf.WriteString(s)
		bufLen += sLen
	}
	if bufLen > 0 {
		chunks = append(chunks, strings.TrimSpace(buf.String()))
	}
	var out []string
	for _, c := range chunks {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// appendIndex adds entry to index.json with a read-modify-write.
func appendIndex(path string, entry IndexEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var idx IndexFile
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &idx)
		if idx.Entries == nil {
			var arr []IndexEntry
			if err2 := json.Unmarshal(data, &arr); err2 == nil {
				idx.Entries = arr
			}
		}
	}
	// Move an existing entry with the same hash to the end, treating it as an update.
	filtered := make([]IndexEntry, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		if e.Hash != entry.Hash {
			filtered = append(filtered, e)
		}
	}
	idx.Entries = append(filtered, entry)
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
