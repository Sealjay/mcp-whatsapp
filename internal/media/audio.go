package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// AnalyzeOggOpus returns the unclamped duration rounded up to seconds and a
// 64-byte waveform. Truncated or invalid Ogg/Opus streams return an error.
func AnalyzeOggOpus(data []byte) (duration uint32, waveform []byte, err error) {
	if len(data) < 4 || string(data[0:4]) != "OggS" {
		return 0, nil, fmt.Errorf("not a valid Ogg file (missing OggS signature)")
	}

	var lastGranule uint64
	var preSkip uint16
	var foundOpusHead bool
	var ended bool
	var unfinishedPacket bool

	for i := 0; i < len(data); {
		if ended {
			return 0, nil, errors.New("unexpected data after Ogg end of stream")
		}
		if len(data)-i < 27 {
			return 0, nil, errors.New("truncated Ogg page header")
		}

		if string(data[i:i+4]) != "OggS" {
			return 0, nil, errors.New("invalid Ogg page signature")
		}
		if data[i+4] != 0 {
			return 0, nil, errors.New("unsupported Ogg page version")
		}

		granulePos := binary.LittleEndian.Uint64(data[i+6 : i+14])
		numSegments := int(data[i+26])

		if numSegments > len(data)-i-27 {
			return 0, nil, errors.New("truncated Ogg segment table")
		}
		segmentTable := data[i+27 : i+27+numSegments]

		pageSize := 27 + numSegments
		for _, segLen := range segmentTable {
			pageSize += int(segLen)
		}
		if pageSize > len(data)-i {
			return 0, nil, errors.New("truncated Ogg page body")
		}

		if !foundOpusHead {
			pageData := data[i+27+numSegments : i+pageSize]
			if !bytes.HasPrefix(pageData, []byte("OpusHead")) {
				return 0, nil, errors.New("missing OpusHead identification header")
			}
			if len(pageData) < 19 {
				return 0, nil, errors.New("truncated OpusHead identification header")
			}
			preSkip = binary.LittleEndian.Uint16(pageData[10:12])
			foundOpusHead = true
		}

		ended = data[i+5]&4 != 0
		if numSegments > 0 {
			unfinishedPacket = segmentTable[numSegments-1] == 255
		}
		if ended && unfinishedPacket {
			return 0, nil, errors.New("unfinished packet at Ogg end of stream")
		}
		if granulePos == math.MaxUint64 {
			if ended {
				return 0, nil, errors.New("undefined final Ogg granule position")
			}
		} else {
			if granulePos > math.MaxInt64 || granulePos < lastGranule {
				return 0, nil, errors.New("invalid Ogg granule position")
			}
			lastGranule = granulePos
		}

		i += pageSize
	}

	if !ended {
		return 0, nil, errors.New("missing Ogg end of stream")
	}
	if lastGranule <= uint64(preSkip) {
		return 0, nil, errors.New("Ogg granule position does not exceed Opus pre-skip")
	}

	// Opus granules always count 48 kHz samples, regardless of the input-rate hint.
	seconds := (lastGranule - uint64(preSkip) + 47999) / 48000
	if seconds > math.MaxUint32 {
		return 0, nil, errors.New("Ogg duration exceeds the supported range")
	}
	duration = uint32(seconds)

	waveform = PlaceholderWaveform(duration)
	return duration, waveform, nil
}

// PlaceholderWaveform generates a deterministic 64-byte synthetic waveform
// based on duration. Used when we can't derive a real waveform.
func PlaceholderWaveform(duration uint32) []byte {
	const waveformLength = 64
	waveform := make([]byte, waveformLength)

	r := rand.New(rand.NewPCG(uint64(duration), 0))

	baseAmplitude := 35.0
	d := int(duration)
	if d > 120 {
		d = 120
	}
	frequencyFactor := float64(d) / 30.0

	for i := range waveform {
		pos := float64(i) / float64(waveformLength)

		val := baseAmplitude * math.Sin(pos*math.Pi*frequencyFactor*8)
		val += (baseAmplitude / 2) * math.Sin(pos*math.Pi*frequencyFactor*16)

		val += (r.Float64() - 0.5) * 15

		fadeInOut := math.Sin(pos * math.Pi)
		val = val * (0.7 + 0.3*fadeInOut)

		val = val + 50

		if val < 0 {
			val = 0
		} else if val > 100 {
			val = 100
		}

		waveform[i] = byte(val)
	}

	return waveform
}

// ConvertToOpusOgg shells out to ffmpeg to convert any audio file to opus/ogg,
// writing the output to a temp file. Returns the temp file path. The caller
// is responsible for removing the file.
func ConvertToOpusOgg(ctx context.Context, inputPath string) (outputPath string, err error) {
	if _, lookErr := exec.LookPath("ffmpeg"); lookErr != nil {
		return "", errors.New("ffmpeg not found on PATH; install ffmpeg to use voice messages")
	}

	tmp, err := os.CreateTemp("", "mcp-whatsapp-audio-*.ogg")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	// Flag-injection guard: ffmpeg treats any arg starting with `-` as a
	// flag, so a file whose basename happens to start with `-` (e.g. from
	// a malicious or malformed path that made it past media_path
	// allowlisting) could inject options. Rewrite to `./<basename>` so the
	// leading `.` forces ffmpeg to read it as a path.
	safeInput := sanitizeFFmpegInputPath(inputPath)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", safeInput,
		"-c:a", "libopus",
		"-b:a", "32k",
		"-vbr", "on",
		"-compression_level", "10",
		"-y",
		tmpPath,
	)
	cmd.Env = minimalEnv()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg conversion failed: %w: %s", err, sanitizeStderr(stderr.String()))
	}

	return tmpPath, nil
}

// sanitizeFFmpegInputPath rewrites paths whose basename begins with `-` so
// that ffmpeg treats them as files rather than option flags. Absolute paths
// pass through unchanged (they already begin with `/`); relative paths get a
// `./` prefix when the basename would otherwise begin with `-`.
//
// Examples:
//
//	"/tmp/foo.mp3"      -> "/tmp/foo.mp3"
//	"/tmp/-weird.mp3"   -> "/tmp/-weird.mp3"  (basename-leading-dash, absolute is safe)
//	"-weird.mp3"        -> "./-weird.mp3"
//	"./-weird.mp3"      -> "./-weird.mp3"
func sanitizeFFmpegInputPath(p string) string {
	if p == "" {
		return p
	}
	// Absolute paths already start with `/` (or a drive letter on Windows,
	// but we don't run on Windows), which ffmpeg parses as a filename.
	if filepath.IsAbs(p) {
		return p
	}
	// Relative paths that already lead with `./` or `../` are fine.
	if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
		return p
	}
	if strings.HasPrefix(p, "-") {
		return "./" + p
	}
	return p
}

var safeEnvKeys = []string{"PATH", "TMPDIR", "TEMP", "TMP", "HOME"}

// minimalEnv returns a restricted environment for child processes, carrying
// only the variables ffmpeg needs (path resolution, temp dirs, home for codec
// config) while excluding credentials, tokens, and other sensitive values.
func minimalEnv() []string {
	env := make([]string, 0, len(safeEnvKeys))
	for _, k := range safeEnvKeys {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// absPathRe matches absolute file paths (Unix-style).
var absPathRe = regexp.MustCompile(`/[\w/.\-]+`)

// sanitizeStderr strips absolute file paths from ffmpeg stderr output and
// truncates to a maximum of 200 characters. This prevents leaking local
// filesystem layout to MCP callers.
func sanitizeStderr(s string) string {
	s = absPathRe.ReplaceAllString(s, "<path>")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
