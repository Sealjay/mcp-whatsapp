package client

import (
	"strings"
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
	if !strings.HasPrefix(filename, "image_") || !strings.HasSuffix(filename, ".jpg") {
		t.Errorf("filename = %q, want pattern image_*.jpg", filename)
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
			name: "cdn url with query",
			in:   "https://mmg.whatsapp.net/v/t62.7118-24/abc.enc?ccb=x",
			want: "/v/t62.7118-24/abc.enc",
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
