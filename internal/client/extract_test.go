package client

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func TestExtractTextContent_Nil(t *testing.T) {
	if got := extractTextContent(nil); got != "" {
		t.Fatalf("extractTextContent(nil) = %q, want empty string", got)
	}
}

func TestExtractTextContent_Conversation(t *testing.T) {
	msg := &waProto.Message{Conversation: proto.String("hi")}
	if got := extractTextContent(msg); got != "hi" {
		t.Fatalf("extractTextContent(Conversation=\"hi\") = %q, want \"hi\"", got)
	}
}

func TestExtractTextContent_ExtendedText(t *testing.T) {
	msg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String("x"),
		},
	}
	if got := extractTextContent(msg); got != "x" {
		t.Fatalf("extractTextContent(ExtendedTextMessage.Text=\"x\") = %q, want \"x\"", got)
	}
}

func TestExtractTextContent_ConversationWinsOverExtendedText(t *testing.T) {
	// Conversation is checked first, so when both present the conversation wins.
	msg := &waProto.Message{
		Conversation: proto.String("primary"),
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String("fallback"),
		},
	}
	if got := extractTextContent(msg); got != "primary" {
		t.Fatalf("extractTextContent = %q, want \"primary\"", got)
	}
}

func TestExtractMediaInfo_Nil(t *testing.T) {
	mediaType, filename, url, directPath, mediaKey, sha, encSHA, length := extractMediaInfo(nil)
	if mediaType != "" || filename != "" || url != "" || directPath != "" ||
		mediaKey != nil || sha != nil || encSHA != nil || length != 0 {
		t.Fatalf("extractMediaInfo(nil) should return zero values, got (%q,%q,%q,%q,%v,%v,%v,%d)",
			mediaType, filename, url, directPath, mediaKey, sha, encSHA, length)
	}
}

func TestExtractMediaInfo_Image(t *testing.T) {
	wantURL := "https://example.com/pic.jpg"
	wantDirectPath := "/v/t62.7118-24/abc.enc?ccb=11-4&oh=x&oe=y&_nc_sid=z"
	wantKey := []byte{0x01, 0x02}
	wantSHA := []byte{0x03, 0x04}
	wantEncSHA := []byte{0x05, 0x06}
	wantLen := uint64(1234)

	msg := &waProto.Message{
		ImageMessage: &waProto.ImageMessage{
			URL:           proto.String(wantURL),
			DirectPath:    proto.String(wantDirectPath),
			MediaKey:      wantKey,
			FileSHA256:    wantSHA,
			FileEncSHA256: wantEncSHA,
			FileLength:    proto.Uint64(wantLen),
		},
	}

	mediaType, filename, url, directPath, mediaKey, sha, encSHA, length := extractMediaInfo(msg)

	if mediaType != "image" {
		t.Errorf("mediaType = %q, want \"image\"", mediaType)
	}
	if filename != "image_0304.jpg" {
		t.Errorf("filename = %q, want stable hash-based filename", filename)
	}
	if url != wantURL {
		t.Errorf("url = %q, want %q", url, wantURL)
	}
	if directPath != wantDirectPath {
		t.Errorf("directPath = %q, want %q", directPath, wantDirectPath)
	}
	if string(mediaKey) != string(wantKey) {
		t.Errorf("mediaKey = %v, want %v", mediaKey, wantKey)
	}
	if string(sha) != string(wantSHA) {
		t.Errorf("fileSHA256 = %v, want %v", sha, wantSHA)
	}
	if string(encSHA) != string(wantEncSHA) {
		t.Errorf("fileEncSHA256 = %v, want %v", encSHA, wantEncSHA)
	}
	if length != wantLen {
		t.Errorf("fileLength = %d, want %d", length, wantLen)
	}
}

func TestExtractMediaInfo_GeneratedFilenamesAreStable(t *testing.T) {
	sha := []byte{0xaa, 0xbb, 0xcc}
	tests := []struct {
		name string
		want string
		msg  *waProto.Message
	}{
		{"image", "image_aabbcc.jpg", &waProto.Message{ImageMessage: &waProto.ImageMessage{FileSHA256: sha}}},
		{"video", "video_aabbcc.mp4", &waProto.Message{VideoMessage: &waProto.VideoMessage{FileSHA256: sha}}},
		{"audio", "audio_aabbcc.ogg", &waProto.Message{AudioMessage: &waProto.AudioMessage{FileSHA256: sha}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, first, _, _, _, _, _, _ := extractMediaInfo(tc.msg)
			_, second, _, _, _, _, _, _ := extractMediaInfo(tc.msg)
			if first != tc.want || second != tc.want {
				t.Fatalf("filenames not stable: %q %q, want %q", first, second, tc.want)
			}
		})
	}
}

// TestPreferredDirectPath: whichever value the download layer hands to
// whatsmeow as DirectPath, it must prefer the stored protobuf DirectPath
// when one is present, and only fall back to string-parsing the URL for
// legacy rows written before the direct_path column existed.
func TestPreferredDirectPath(t *testing.T) {
	const storedCanonical = "/v/t62.7118-24/new.enc?ccb=11-4&oh=SIG&oe=EXP&_nc_sid=SID"
	const legacyURL = "https://mmg.whatsapp.net/v/t62.7118-24/legacy.enc?ccb=x"

	if got := preferredDirectPath(storedCanonical, legacyURL); got != storedCanonical {
		t.Errorf("with stored: got %q, want %q (stored wins)", got, storedCanonical)
	}

	// Empty stored → fall back to URL parse. Exact expected value here is
	// whatever extractDirectPathFromURL currently returns; verify by
	// comparing against a fresh call to keep this test tolerant of PR-#22
	// behaviour changes.
	want := extractDirectPathFromURL(legacyURL)
	if got := preferredDirectPath("", legacyURL); got != want {
		t.Errorf("empty stored: got %q, want extractDirectPathFromURL result %q", got, want)
	}
}

func TestExtractDirectPathFromURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// WhatsApp's CDN URLs carry the signed auth params in the query
			// (ccb/oh/oe/_nc_sid). They MUST be preserved: whatsmeow builds
			// the download URL as `https://<host><directPath>&hash=…`, which
			// assumes directPath already ends in `?…`. Stripping the query
			// yields a malformed URL that the CDN answers with HTTP 403.
			name: "cdn url with query — query preserved for signed CDN auth",
			in:   "https://mmg.whatsapp.net/v/t62.7118-24/abc.enc?ccb=11-4&oh=01_Q5xx&oe=6A6220F0&_nc_sid=5e03e0&mms3=true",
			want: "/v/t62.7118-24/abc.enc?ccb=11-4&oh=01_Q5xx&oe=6A6220F0&_nc_sid=5e03e0&mms3=true",
		},
		{
			name: "cdn url without query",
			in:   "https://mmg.whatsapp.net/v/t62.7118-24/abc.enc",
			want: "/v/t62.7118-24/abc.enc",
		},
		{
			name: "not a url",
			in:   "not-a-url",
			want: "not-a-url",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractDirectPathFromURL(tc.in); got != tc.want {
				t.Fatalf("extractDirectPathFromURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
