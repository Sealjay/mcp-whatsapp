package media

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixturePath is populated by TestMain if ffmpeg is available. Empty otherwise.
var fixturePath string

func TestMain(m *testing.M) {
	// Try to generate a 3-second 440Hz ogg/opus sample for AnalyzeOggOpus /
	// ConvertToOpusOgg tests. If ffmpeg isn't present, leave fixturePath
	// empty and tests will skip.
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		tmpDir, err := os.MkdirTemp("", "mcp-whatsapp-media-test-*")
		if err == nil {
			path := filepath.Join(tmpDir, "3s-440hz.ogg")
			cmd := exec.Command("ffmpeg",
				"-f", "lavfi",
				"-i", "sine=frequency=440:duration=3",
				"-c:a", "libopus",
				"-b:a", "32k",
				"-y",
				path,
			)
			if err := cmd.Run(); err == nil {
				if info, err := os.Stat(path); err == nil && info.Size() > 0 {
					fixturePath = path
				}
			}
			defer os.RemoveAll(tmpDir)
		}
	}
	os.Exit(m.Run())
}

func TestPlaceholderWaveform_Length(t *testing.T) {
	cases := []struct {
		name     string
		duration uint32
	}{
		{"typical", 5},
		{"zero clamps to 1", 0},
		{"over max clamps to 300", 500},
		{"exactly one", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := PlaceholderWaveform(tc.duration)
			if len(w) != 64 {
				t.Fatalf("expected length 64, got %d", len(w))
			}
		})
	}
}

func TestPlaceholderWaveform_Deterministic(t *testing.T) {
	a := PlaceholderWaveform(5)
	b := PlaceholderWaveform(5)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("byte %d differs: %d vs %d", i, a[i], b[i])
		}
	}
}

func TestPlaceholderWaveform_ByteRange(t *testing.T) {
	for _, d := range []uint32{1, 5, 30, 120, 300} {
		w := PlaceholderWaveform(d)
		for i, b := range w {
			if b > 100 {
				t.Fatalf("duration=%d byte[%d]=%d > 100", d, i, b)
			}
		}
	}
}

func TestAnalyzeOggOpus_InvalidBytes(t *testing.T) {
	_, _, err := AnalyzeOggOpus([]byte("hello"))
	if err == nil {
		t.Fatal("expected error for non-Ogg input")
	}
	if !strings.Contains(err.Error(), "OggS") {
		t.Fatalf("expected error to mention OggS, got %v", err)
	}
}

func TestAnalyzeOggOpus_TruncatedHeader(t *testing.T) {
	if _, _, err := AnalyzeOggOpus([]byte("OggS")); err == nil {
		t.Fatal("expected an error for a truncated Ogg header")
	}
}

func oggPage(sequence uint32, granule uint64, flags byte, payload []byte) []byte {
	page := make([]byte, 28+len(payload))
	copy(page, "OggS")
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], 1)
	binary.LittleEndian.PutUint32(page[18:22], sequence)
	page[26] = 1
	page[27] = byte(len(payload))
	copy(page[28:], payload)
	return page
}

func opusPages(granule uint64, rate uint32) []byte {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8], head[9] = 1, 1
	binary.LittleEndian.PutUint16(head[10:12], 312)
	binary.LittleEndian.PutUint32(head[12:16], rate)
	return append(oggPage(0, 0, 2, head), oggPage(1, granule, 4, []byte{0})...)
}

func TestAnalyzeOggOpus_Duration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples uint64
		rate    uint32
		want    uint32
	}{
		{"pre-skip", 3 * 48000, 48000, 3},
		{"input rate is only a hint", 3 * 48000, 24000, 3},
		{"unspecified input rate", 3 * 48000, 0, 3},
		{"long audio is not clamped", 601 * 48000, 48000, 601},
		{"fractional second rounds up", 600*48000 + 1, 48000, 601},
	} {
		t.Run(tc.name, func(t *testing.T) {
			duration, waveform, err := AnalyzeOggOpus(opusPages(tc.samples+312, tc.rate))
			if err != nil || duration != tc.want || len(waveform) != 64 {
				t.Fatalf("duration=%d waveform=%d err=%v; want %d seconds", duration, len(waveform), err, tc.want)
			}
		})
	}
}

func TestAnalyzeOggOpus_MalformedPages(t *testing.T) {
	valid := opusPages(48000+312, 48000)
	truncatedBody := append([]byte(nil), valid[:47]...)
	truncatedBody[27] = 255
	noEOS := append([]byte(nil), valid...)
	noEOS[47+5] = 0
	backwards := append(oggPage(0, 0, 2, valid[28:47]), oggPage(1, 48000+312, 0, []byte{0})...)
	backwards = append(backwards, oggPage(2, 24000+312, 4, []byte{0})...)
	unfinished := append(oggPage(0, 0, 2, valid[28:47]), oggPage(1, 48000+312, 4, make([]byte, 255))...)
	emptyEOS := make([]byte, 27)
	copy(emptyEOS, "OggS")
	emptyEOS[5] = 4
	binary.LittleEndian.PutUint64(emptyEOS[6:14], 48000+312)
	unfinishedThenEmptyEOS := append(oggPage(0, 0, 2, valid[28:47]), oggPage(1, math.MaxUint64, 0, make([]byte, 255))...)
	unfinishedThenEmptyEOS = append(unfinishedThenEmptyEOS, emptyEOS...)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"truncated segment table", append(valid[:26:26], 2, 19)},
		{"truncated page body", truncatedBody},
		{"truncated OpusHead", oggPage(0, 0, 2, []byte("OpusHead"))},
		{"missing OpusHead", oggPage(0, 48000, 4, []byte("not opus"))},
		{"granule below pre-skip", opusPages(100, 48000)},
		{"undefined final granule", opusPages(math.MaxUint64, 48000)},
		{"duration overflows uint32", opusPages((uint64(math.MaxUint32)+1)*48000+312, 48000)},
		{"missing end of stream", noEOS},
		{"backwards granule", backwards},
		{"unfinished final packet", unfinished},
		{"unfinished packet before empty EOS", unfinishedThenEmptyEOS},
		{"trailing truncated page", append(valid[:len(valid):len(valid)], []byte("OggS")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := AnalyzeOggOpus(tc.data); err == nil {
				t.Fatal("expected a parse error")
			}
		})
	}
}

func TestAnalyzeOggOpus_ValidSynthetic(t *testing.T) {
	if fixturePath == "" {
		t.Skip("ffmpeg not available; skipping synthetic Ogg test")
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dur, wf, err := AnalyzeOggOpus(data)
	if err != nil {
		t.Fatalf("AnalyzeOggOpus: %v", err)
	}
	if len(wf) != 64 {
		t.Fatalf("expected waveform length 64, got %d", len(wf))
	}
	if dur < 1 {
		t.Fatalf("expected duration >= 1, got %d", dur)
	}
}

func TestSanitizeFFmpegInputPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/tmp/foo.mp3", "/tmp/foo.mp3"},
		{"/tmp/-weird.mp3", "/tmp/-weird.mp3"}, // absolute: safe as-is
		{"-weird.mp3", "./-weird.mp3"},         // bare leading dash: prefix
		{"./-weird.mp3", "./-weird.mp3"},       // already prefixed
		{"../foo/-weird.mp3", "../foo/-weird.mp3"},
		{"foo.mp3", "foo.mp3"},
		{"", ""},
	}
	for _, tc := range cases {
		got := sanitizeFFmpegInputPath(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeFFmpegInputPath(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestConvertToOpusOgg_RejectsFlagLikeBasename(t *testing.T) {
	// End-to-end: create a temp file whose basename begins with `-`. If
	// ffmpeg is available and we did NOT sanitise the path, ffmpeg would
	// reject it with "Unrecognized option"; with the sanitiser it reads
	// the file normally.
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; skipping flag-injection guard test")
	}
	if fixturePath == "" {
		t.Skip("ffmpeg present but fixture generation failed; skipping")
	}
	src, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	tmpDir, err := os.MkdirTemp("", "mcp-whatsapp-flag-guard-*")
	if err != nil {
		t.Fatalf("mktemp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	// Use a relative path so the leading dash is actually at the
	// beginning of the arg ffmpeg sees.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	dangerous := "-sneaky.ogg"
	if err := os.WriteFile(dangerous, src, 0o644); err != nil {
		t.Fatalf("write fixture copy: %v", err)
	}
	out, err := ConvertToOpusOgg(context.Background(), dangerous)
	if err != nil {
		t.Fatalf("ConvertToOpusOgg with dash-prefixed basename: %v", err)
	}
	defer os.Remove(out)
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("expected non-empty output, got size 0")
	}
}

func TestConvertToOpusOgg(t *testing.T) {
	ctx := context.Background()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		// Without ffmpeg, ConvertToOpusOgg must error out with a message
		// mentioning ffmpeg.
		_, convErr := ConvertToOpusOgg(ctx, "/nonexistent/input.mp3")
		if convErr == nil {
			t.Fatal("expected error when ffmpeg is missing")
		}
		if !strings.Contains(convErr.Error(), "ffmpeg") {
			t.Fatalf("expected error mentioning ffmpeg, got %v", convErr)
		}
		return
	}

	if fixturePath == "" {
		t.Skip("ffmpeg present but fixture generation failed; skipping")
	}
	out, err := ConvertToOpusOgg(ctx, fixturePath)
	if err != nil {
		t.Fatalf("ConvertToOpusOgg: %v", err)
	}
	defer os.Remove(out)

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("expected non-empty output file, got size 0")
	}
}

func TestMinimalEnv(t *testing.T) {
	const sentinel = "MCP_WHATSAPP_TEST_SENTINEL_12345"
	t.Setenv(sentinel, "should-not-appear")

	env := minimalEnv()
	for _, entry := range env {
		if strings.HasPrefix(entry, sentinel+"=") {
			t.Fatalf("minimalEnv leaked sentinel variable: %s", entry)
		}
	}

	hasPath := false
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			hasPath = true
		}
	}
	if !hasPath {
		t.Fatal("minimalEnv must include PATH")
	}
}

func TestConvertToOpusOgg_EnvIsolation(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; skipping env isolation test")
	}
	if fixturePath == "" {
		t.Skip("ffmpeg present but fixture generation failed; skipping")
	}

	const sentinel = "MCP_WHATSAPP_TEST_SENTINEL_12345"
	t.Setenv(sentinel, "should-not-reach-ffmpeg")

	out, err := ConvertToOpusOgg(context.Background(), fixturePath)
	if err != nil {
		t.Fatalf("ConvertToOpusOgg: %v", err)
	}
	defer os.Remove(out)

	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected valid output file after env-isolated run, stat=%v size=%d", err, info.Size())
	}
}
