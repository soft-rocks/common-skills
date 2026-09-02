// ondoku-tts calls ondoku3.com's advanced-tts API, polls the returned job
// until it finishes, and downloads the resulting mp3.
//
// It first GETs the beta page to obtain a fresh Django csrftoken cookie, then
// reuses that token as both the cookie and the x-csrftoken header on the POST.
// That token exchange is what the endpoint requires; no captured cookie string
// has to be pasted in by hand.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	pageURL = "https://ondoku3.com/zh-hant/advanced-tts-beta/"
	apiURL  = "https://ondoku3.com/api/advanced-tts/"
	origin  = "https://ondoku3.com"
	// The server does not inspect this, verified against both the page and the
	// API, so identify the tool honestly instead of pinning a browser version.
	userAgent = "ondoku3-tts (+https://github.com/soft-rocks/common-skills)"

	minTextChars = 10
	maxTextChars = 400

	defaultOutDir = "/tmp/common-skills/ondoku3-tts"

	// Pacing for the rate limited upstream API. These are fixed rather than
	// exposed as flags: they exist to stay under the limit, not to be tuned.
	batchDelay = 15 * time.Second // between jobs in a -batch run
	chunkDelay = 3 * time.Second  // between chunks of one long job
	jobRetries = 2                // retries per job, with growing backoff
)

// voiceGender lists every voice option available on the advanced-tts-beta
// page (scraped from its embedded #voice-base-info JSON), keyed by voice
// name with its listed gender category.
var voiceGender = map[string]string{
	"Anna": "female", "Chloe": "female", "Ellis": "female", "Emma": "female",
	"Flora": "female", "Iris": "female", "Lena": "female", "Luna": "female",
	"Misa": "female", "Ruby": "female", "Sophie": "female", "Tina": "female",
	"Ash": "male", "Chris": "male", "Eden": "male", "Gray": "male",
	"Hope": "male", "Hugo": "male", "Kai": "male", "Leo": "male",
	"Noah": "male", "Reid": "male", "Roy": "male", "Sam": "male", "Yann": "male",
}

type ttsRequest struct {
	Text        string  `json:"text"`
	Voice       string  `json:"voice"`
	Tone        string  `json:"tone"`
	Speed       float64 `json:"speed"`
	Pitch       float64 `json:"pitch"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	Seed        int64   `json:"seed"`
}

type jobCreated struct {
	JobID          string `json:"job_id"`
	JobToken       string `json:"job_token"`
	MinPollAfterMs int    `json:"min_poll_after_ms"`
	PollURL        string `json:"poll_url"`
	Status         string `json:"status"`
}

type jobStatus struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	URL    string `json:"url"`
	Error  string `json:"error"`
	Code   string `json:"code"`
}

// IndexEntry records how one TTS synthesis maps to its audio file, hash,
// subtitle and duration, so callers can cache results and align tracks.
type IndexEntry struct {
	Hash         string  `json:"hash"`          // sha256(text)
	Text         string  `json:"text"`          // original subtitle or narration text
	Audio        string  `json:"audio"`         // absolute or relative path
	SubtitleFile string  `json:"subtitle_file"` // .srt path in the same directory
	Duration     float64 `json:"duration"`      // seconds, from ffprobe, 0 when unavailable
	Voice        string  `json:"voice"`
	Tone         string  `json:"tone"`
	Speed        float64 `json:"speed"`
	Pitch        float64 `json:"pitch"`
	Model        string  `json:"model"`
	JobID        string  `json:"job_id"`
	CreatedAt    string  `json:"created_at"` // RFC3339
}

type IndexFile struct {
	Entries []IndexEntry `json:"entries"`
}

// resolvedJob is one fully specified synthesis job, after batch entries and
// command line flags have been merged.
type resolvedJob struct {
	Text        string
	Voice       string
	Tone        string
	Speed       float64
	Pitch       float64
	Model       string
	Temperature float64
	Seed        int64
	Out         string
	Index       string
}

// JobSpec is one entry in a -batch job file. Every field is optional and falls
// back to the matching command line flag, so an entry can carry only text.
// Numeric fields are pointers because 0 is a meaningful pitch, seed and
// temperature and must be distinguishable from an absent field.
type JobSpec struct {
	Text        string   `json:"text"`
	Voice       string   `json:"voice"`
	Tone        string   `json:"tone"`
	Speed       *float64 `json:"speed"`
	Pitch       *float64 `json:"pitch"`
	Model       string   `json:"model"`
	Temperature *float64 `json:"temperature"`
	Seed        *int64   `json:"seed"`
	Out         string   `json:"out"`
	Index       string   `json:"index"`
}

func (s JobSpec) resolve(d resolvedJob) resolvedJob {
	j := d
	if s.Text != "" {
		j.Text = s.Text
	}
	if s.Voice != "" {
		j.Voice = s.Voice
	}
	if s.Tone != "" {
		j.Tone = s.Tone
	}
	if s.Speed != nil {
		j.Speed = *s.Speed
	}
	if s.Pitch != nil {
		j.Pitch = *s.Pitch
	}
	if s.Model != "" {
		j.Model = s.Model
	}
	if s.Temperature != nil {
		j.Temperature = *s.Temperature
	}
	if s.Seed != nil {
		j.Seed = *s.Seed
	}
	if s.Out != "" {
		j.Out = s.Out
	}
	if s.Index != "" {
		j.Index = s.Index
	}
	return j
}

// loadBatch reads a job file containing either a bare array of jobs or an
// object with a "jobs" key.
func loadBatch(path string) ([]JobSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		var specs []JobSpec
		if err := json.Unmarshal(raw, &specs); err != nil {
			return nil, fmt.Errorf("parse job array: %w", err)
		}
		return specs, nil
	}
	var wrapper struct {
		Jobs []JobSpec `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("parse job object: %w", err)
	}
	return wrapper.Jobs, nil
}

func voiceNames() []string {
	names := make([]string, 0, len(voiceGender))
	for name := range voiceGender {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validateJob checks a job without contacting the API, so a bad entry in a
// batch file fails before any request is sent.
func validateJob(j resolvedJob) error {
	if _, ok := voiceGender[j.Voice]; !ok {
		return fmt.Errorf("unknown voice %q, must be one of: %s", j.Voice, strings.Join(voiceNames(), ", "))
	}
	chunks := splitText(j.Text, 380)
	if len(chunks) == 0 {
		return fmt.Errorf("no text to synthesize")
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c); n < minTextChars {
			return fmt.Errorf("chunk %d text length must be between %d and %d characters, got %d: %q", i+1, minTextChars, maxTextChars, n, c)
		}
	}
	return nil
}

func main() {
	text := flag.String("text", "This is a short sample used to check that synthesis works.", "text to synthesize")
	voice := flag.String("voice", "Hugo", "voice name (see voiceGender map in source for the full list)")
	tone := flag.String("tone", "Read this clearly and calmly, at a natural pace.", "tone/style instruction")
	speed := flag.Float64("speed", 1.2, "speaking speed")
	pitch := flag.Float64("pitch", -0.5, "pitch shift")
	model := flag.String("model", "flash", "model name")
	temperature := flag.Float64("temperature", 1, "sampling temperature")
	seed := flag.Int64("seed", 18, "random seed")
	out := flag.String("out", "", "output mp3 path (default: "+defaultOutDir+"/<job_id>.mp3)")
	indexPath := flag.String("index", "", "index json path (default: <out_dir>/index.json, empty to disable)")
	pollInterval := flag.Duration("poll-interval", 2*time.Second, "delay between poll attempts")
	pollTimeout := flag.Duration("poll-timeout", 60*time.Second, "give up waiting for the job after this long")
	batchPath := flag.String("batch", "", "path to a JSON job file; runs every job sequentially")
	flag.Parse()

	defaults := resolvedJob{
		Text:        *text,
		Voice:       *voice,
		Tone:        *tone,
		Speed:       *speed,
		Pitch:       *pitch,
		Model:       *model,
		Temperature: *temperature,
		Seed:        *seed,
		Out:         *out,
		Index:       *indexPath,
	}

	var jobs []resolvedJob
	if *batchPath != "" {
		specs, err := loadBatch(*batchPath)
		if err != nil {
			log.Fatalf("read batch file %s: %v", *batchPath, err)
		}
		if len(specs) == 0 {
			log.Fatalf("batch file %s contains no jobs", *batchPath)
		}
		for _, sp := range specs {
			jobs = append(jobs, sp.resolve(defaults))
		}
	} else {
		jobs = []resolvedJob{defaults}
	}

	// Validate every job before the first request, so a typo late in a batch
	// does not surface after minutes of synthesis.
	for i, j := range jobs {
		if err := validateJob(j); err != nil {
			log.Fatalf("job %d/%d: %v", i+1, len(jobs), err)
		}
	}
	if len(jobs) > 1 {
		fmt.Printf("batch: %d jobs, %s between jobs\n", len(jobs), batchDelay)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatalf("create cookie jar: %v", err)
	}
	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
	}

	csrfToken, err := fetchCSRFToken(client, jar)
	if err != nil {
		log.Fatalf("fetch csrf token: %v", err)
	}
	fmt.Printf("obtained csrftoken: %s\n", csrfToken)

	var batchDuration float64
	var failures []string
	for i, j := range jobs {
		if len(jobs) > 1 {
			fmt.Printf("\n=== job %d/%d ===\n", i+1, len(jobs))
		}

		// The upstream model fails intermittently with tts_retry_exhausted, so
		// retry with growing backoff before giving up on a job.
		var d float64
		var err error
		for attempt := 0; attempt <= jobRetries; attempt++ {
			d, err = runJob(client, csrfToken, j, *pollInterval, *pollTimeout)
			if err == nil {
				break
			}
			fmt.Fprintf(os.Stderr, "job %d/%d attempt %d failed: %v\n", i+1, len(jobs), attempt+1, err)
			if attempt < jobRetries {
				wait := time.Duration(attempt+1) * batchDelay
				fmt.Printf("retrying job %d/%d in %s\n", i+1, len(jobs), wait)
				time.Sleep(wait)
			}
		}
		batchDuration += d

		if err != nil {
			// A single job keeps the old behaviour and fails immediately. A batch
			// records the failure and carries on, so one bad entry does not
			// discard every job after it.
			if len(jobs) == 1 {
				log.Fatalf("%v", err)
			}
			failures = append(failures, fmt.Sprintf("job %d: %v", i+1, err))
			fmt.Fprintf(os.Stderr, "job %d/%d gave up after %d attempts, continuing\n", i+1, len(jobs), jobRetries+1)
		}

		// The API is rate limited per request, so pause between jobs as well as
		// between chunks. Keep this sequential and never fan out.
		if i < len(jobs)-1 {
			fmt.Printf("waiting %s before the next job\n", batchDelay)
			time.Sleep(batchDelay)
		}
	}

	if len(jobs) > 1 {
		fmt.Printf("\nbatch complete: %d/%d jobs succeeded, total duration ~%.2fs\n", len(jobs)-len(failures), len(jobs), batchDuration)
	}
	if len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintln(os.Stderr, f)
		}
		os.Exit(1)
	}
}

// runJob synthesizes one job and returns the total audio duration it produced.
func runJob(client *http.Client, csrfToken string, j resolvedJob, pollInterval, pollTimeout time.Duration) (float64, error) {
	// Split long input on sentence boundaries (. ? ! ;) into chunks of at most 380 runes.
	chunks := splitText(j.Text, 380)
	if len(chunks) > 1 {
		fmt.Printf("text split into %d chunks (total %d chars)\n", len(chunks), utf8.RuneCountInString(j.Text))
	}

	// Synthesize one chunk at a time.
	var totalDuration float64
	var outPaths []string
	var chunkDurations []float64
	var chunkTexts []string
	baseOut := j.Out
	for idx := 0; idx < len(chunks); idx++ {
		chunk := chunks[idx]
		fmt.Printf("\n--- chunk %d/%d (%d chars) ---\n", idx+1, len(chunks), utf8.RuneCountInString(chunk))

		reqBody := ttsRequest{
			Text:        chunk,
			Voice:       j.Voice,
			Tone:        j.Tone,
			Speed:       j.Speed,
			Pitch:       j.Pitch,
			Model:       j.Model,
			Temperature: j.Temperature,
			Seed:        j.Seed,
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			return totalDuration, fmt.Errorf("marshal request body: %w", err)
		}

		respBody, status, err := postTTS(client, csrfToken, body)
		if err != nil {
			return totalDuration, fmt.Errorf("post tts request (chunk %d): %w", idx+1, err)
		}
		fmt.Printf("status: %s\n", status)
		printPrettyOrRaw(respBody)

		// On quota_exceeded, resplit using the reported available budget and retry.
		if strings.Contains(status, "429") || strings.Contains(string(respBody), "quota_exceeded") {
			var q struct {
				Available int    `json:"available"`
				Code      string `json:"code"`
			}
			_ = json.Unmarshal(respBody, &q)
			avail := q.Available
			if avail > 10 && avail < utf8.RuneCountInString(chunk) {
				fmt.Printf("quota exceeded, available=%d, splitting chunk %d and retrying\n", avail, idx+1)
				subs := splitText(chunk, avail)
				if len(subs) > 1 {
					// Replace the current chunk with the smaller ones.
					newChunks := make([]string, 0, len(chunks)+len(subs)-1)
					newChunks = append(newChunks, chunks[:idx]...)
					newChunks = append(newChunks, subs...)
					newChunks = append(newChunks, chunks[idx+1:]...)
					chunks = newChunks
					idx-- // retry the first sub-chunk
					time.Sleep(3 * time.Second)
					continue
				}
			}
			// If it cannot be split or still fails, wait and retry once.
			fmt.Printf("quota exceeded, waiting 5s before retry\n")
			time.Sleep(5 * time.Second)
			idx--
			continue
		}

		var job jobCreated
		if err := json.Unmarshal(respBody, &job); err != nil || job.JobID == "" || job.PollURL == "" {
			return totalDuration, fmt.Errorf("job creation response did not contain a poll_url/job_id: %v", err)
		}

		if job.MinPollAfterMs > 0 {
			time.Sleep(time.Duration(job.MinPollAfterMs) * time.Millisecond)
		}

		final, err := pollJob(client, job, pollInterval, pollTimeout)
		if err != nil {
			return totalDuration, fmt.Errorf("poll job (chunk %d): %w", idx+1, err)
		}

		if final.Status != "succeeded" {
			return totalDuration, fmt.Errorf("job did not succeed (chunk %d): status=%s code=%s error=%s", idx+1, final.Status, final.Code, final.Error)
		}

		fmt.Printf("audio url: %s\n", final.URL)

		// Decide the output path for this chunk.
		var outPath string
		if len(chunks) == 1 {
			if baseOut != "" {
				outPath = baseOut
			} else {
				outPath = filepath.Join(defaultOutDir, final.JobID+".mp3")
			}
		} else {
			// Multiple chunks: append _001, _002 to -out when given, otherwise use job_id.
			if baseOut != "" {
				ext := filepath.Ext(baseOut)
				base := strings.TrimSuffix(baseOut, ext)
				outPath = fmt.Sprintf("%s_%03d%s", base, idx+1, ext)
			} else {
				outPath = filepath.Join(defaultOutDir, fmt.Sprintf("%s_%03d.mp3", final.JobID, idx+1))
			}
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return totalDuration, fmt.Errorf("create output dir: %w", err)
		}
		if err := downloadFile(client, final.URL, outPath); err != nil {
			return totalDuration, fmt.Errorf("download audio (chunk %d): %w", idx+1, err)
		}
		fmt.Printf("saved to: %s\n", outPath)
		outPaths = append(outPaths, outPath)

		// --- index / subtitle / duration ---
		hash := hashText(chunk)
		duration := probeDuration(outPath)
		totalDuration += duration
		chunkDurations = append(chunkDurations, duration)
		chunkTexts = append(chunkTexts, chunk)
		srtPath := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".srt"
		if err := writeSRT(srtPath, chunk, duration); err != nil {
			log.Printf("write srt: %v", err)
		} else {
			fmt.Printf("subtitle: %s\n", srtPath)
		}

		idxPath := j.Index
		if idxPath == "" {
			idxPath = filepath.Join(filepath.Dir(outPath), "index.json")
		}
		entry := IndexEntry{
			Hash:         hash,
			Text:         chunk,
			Audio:        outPath,
			SubtitleFile: srtPath,
			Duration:     duration,
			Voice:        j.Voice,
			Tone:         j.Tone,
			Speed:        j.Speed,
			Pitch:        j.Pitch,
			Model:        j.Model,
			JobID:        final.JobID,
			CreatedAt:    time.Now().Format(time.RFC3339),
		}
		if err := appendIndex(idxPath, entry); err != nil {
			log.Printf("write index: %v", err)
		} else {
			fmt.Printf("index: %s (hash=%s duration=%.2fs)\n", idxPath, hash[:12], duration)
		}

		// Pause between chunks to stay under the rate limit.
		if idx < len(chunks)-1 {
			time.Sleep(chunkDelay)
		}
	}

	if len(chunks) > 1 {
		fmt.Printf("\ncompleted %d chunks, total duration ~%.2fs\n", len(chunks), totalDuration)
		fmt.Printf("outputs: %s\n", strings.Join(outPaths, ", "))
		// Write one combined .srt covering every chunk.
		var combinedSRT string
		if baseOut != "" {
			combinedSRT = strings.TrimSuffix(baseOut, filepath.Ext(baseOut)) + ".srt"
		} else if len(outPaths) > 0 {
			combinedSRT = filepath.Join(filepath.Dir(outPaths[0]), "combined.srt")
		}
		if combinedSRT != "" {
			var sb strings.Builder
			var cursor float64
			for i, txt := range chunkTexts {
				d := chunkDurations[i]
				if d <= 0 {
					d = float64(utf8.RuneCountInString(txt)) * 0.15
					if d < 1 {
						d = 1
					}
				}
				end := cursor + d
				sb.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n", i+1, formatSRTTime(cursor), formatSRTTime(end), txt))
				cursor = end
			}
			if err := os.WriteFile(combinedSRT, []byte(sb.String()), 0o644); err != nil {
				log.Printf("write combined srt: %v", err)
			} else {
				fmt.Printf("combined subtitle: %s\n", combinedSRT)
			}
		}
		// Merge the chunks when ffmpeg is available.
		if _, err := exec.LookPath("ffmpeg"); err == nil && baseOut != "" {
			concatList := filepath.Join(filepath.Dir(baseOut), "concat.txt")
			var lines []string
			for _, p := range outPaths {
				lines = append(lines, fmt.Sprintf("file '%s'", p))
			}
			_ = os.WriteFile(concatList, []byte(strings.Join(lines, "\n")), 0o644)
			merged := strings.TrimSuffix(baseOut, filepath.Ext(baseOut)) + "_merged.mp3"
			cmd := exec.Command("ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", concatList, "-c", "copy", merged)
			if out, err := cmd.CombinedOutput(); err != nil {
				fmt.Printf("ffmpeg concat failed (optional): %v\n%s\n", err, out)
			} else {
				fmt.Printf("merged: %s\n", merged)
				_ = os.Remove(concatList)
			}
		}
	}

	return totalDuration, nil
}

// hashText returns the sha256 hex of text, used for cache lookups.
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
	s := strings.TrimSpace(string(out))
	f, err := strconv.ParseFloat(s, 64)
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
	if utf8.RuneCountInString(text) <= max {
		return []string{text}
	}
	// Split by paragraph first.
	var sentences []string
	var cur strings.Builder
	for _, r := range text {
		cur.WriteRune(r)
		if strings.ContainsRune("。！？；\n", r) {
			sentences = append(sentences, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		sentences = append(sentences, cur.String())
	}
	// Then merge chunks up to max.
	var chunks []string
	var buf strings.Builder
	bufLen := 0
	for _, s := range sentences {
		sLen := utf8.RuneCountInString(s)
		if sLen > max {
			// A single sentence exceeds max, hard-split it.
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
	// Drop empty chunks.
	var out []string
	for _, c := range chunks {
		c = strings.TrimSpace(c)
		if c != "" {
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
		// Backward compatibility: the file may be a bare array instead of an object.
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
	filtered = append(filtered, entry)
	idx.Entries = filtered
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// pollJob repeatedly GETs job.PollURL (authenticated via the X-Job-Token
// header) until the job reaches a terminal status or pollTimeout elapses.
func pollJob(client *http.Client, job jobCreated, interval, timeout time.Duration) (*jobStatus, error) {
	deadline := time.Now().Add(timeout)
	pollURL := origin + job.PollURL

	for {
		req, err := http.NewRequest(http.MethodGet, pollURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Referer", pageURL)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Job-Token", job.JobToken)

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		var status jobStatus
		if err := json.Unmarshal(respBody, &status); err != nil {
			return nil, fmt.Errorf("decode poll response: %w (body: %s)", err, respBody)
		}
		fmt.Printf("poll: status=%s\n", status.Status)

		switch status.Status {
		case "succeeded", "failed":
			return &status, nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for job %s (last status: %s)", job.JobID, status.Status)
		}
		time.Sleep(interval)
	}
}

func downloadFile(client *http.Client, fileURL, path string) error {
	resp, err := client.Get(fileURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status downloading file: %s", resp.Status)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// fetchCSRFToken GETs the beta page so the server sets a csrftoken cookie,
// then reads it back out of the cookie jar.
func fetchCSRFToken(client *http.Client, jar *cookiejar.Jar) (string, error) {
	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status fetching page: %s", resp.Status)
	}

	u, _ := url.Parse(pageURL)
	for _, c := range jar.Cookies(u) {
		if c.Name == "csrftoken" {
			return c.Value, nil
		}
	}
	return "", fmt.Errorf("csrftoken cookie not found in response")
}

func postTTS(client *http.Client, csrfToken string, body []byte) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}

	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", pageURL)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-CSRFToken", csrfToken)
	// Cookie jar already attaches csrftoken (and any other) cookies automatically.

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return respBody, resp.Status, nil
}

func printPrettyOrRaw(data []byte) {
	var v any
	if err := json.Unmarshal(data, &v); err == nil {
		pretty, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(pretty))
		return
	}
	fmt.Println(string(data))
}
