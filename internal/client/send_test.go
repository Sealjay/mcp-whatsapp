package client

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// TestParseRecipient covers the recipient-string → types.JID conversion used
// by every send tool. The headline behaviour: a leading `+` on a bare phone
// number is stripped (so E.164-with-plus is accepted as a courtesy), and any
// other non-digit input on the bare-number path is rejected with a clear
// error instead of silently constructing an invalid JID. Fully-qualified JIDs
// containing `@` are delegated to whatsmeow's own parser unchanged.
func TestParseRecipient(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantUser   string
		wantServer string
		wantErr    string // substring expected in error message when set
	}{
		{
			name:       "bare digits",
			in:         "447967960994",
			wantUser:   "447967960994",
			wantServer: types.DefaultUserServer,
		},
		{
			name:       "leading plus is stripped",
			in:         "+447967960994",
			wantUser:   "447967960994",
			wantServer: types.DefaultUserServer,
		},
		{
			name:    "empty rejected",
			in:      "",
			wantErr: "recipient",
		},
		{
			name:    "bare plus rejected",
			in:      "+",
			wantErr: "recipient",
		},
		{
			name:    "spaces rejected",
			in:      "447 967 960994",
			wantErr: "recipient",
		},
		{
			name:    "punctuation rejected",
			in:      "447-967-960994",
			wantErr: "recipient",
		},
		{
			name:    "plus with spaces rejected",
			in:      "+44 7967 960994",
			wantErr: "recipient",
		},
		{
			name:    "non-digit rejected",
			in:      "abc",
			wantErr: "recipient",
		},
		{
			name:       "fully qualified user JID passes through",
			in:         "447967960994@s.whatsapp.net",
			wantUser:   "447967960994",
			wantServer: types.DefaultUserServer,
		},
		{
			name:       "group JID passes through",
			in:         "120363025246125486@g.us",
			wantUser:   "120363025246125486",
			wantServer: types.GroupServer,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRecipient(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseRecipient(%q): want error containing %q, got JID %+v", tc.in, tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseRecipient(%q): error = %q, want substring %q", tc.in, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRecipient(%q): unexpected error %v", tc.in, err)
			}
			if got.User != tc.wantUser {
				t.Errorf("parseRecipient(%q): User = %q, want %q", tc.in, got.User, tc.wantUser)
			}
			if got.Server != tc.wantServer {
				t.Errorf("parseRecipient(%q): Server = %q, want %q", tc.in, got.Server, tc.wantServer)
			}
		})
	}
}

func TestMediaTypeFromExtDocuments(t *testing.T) {
	cases := map[string]string{
		"/x/a.pdf":  "application/pdf",
		"/x/a.PDF":  "application/pdf",
		"/x/a.docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"/x/a.xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"/x/a.csv":  "text/csv",
		"/x/a.zip":  "application/zip",
		"/x/noext":  "application/octet-stream",
		"/x/a.zzzz": "application/octet-stream",
	}
	for path, want := range cases {
		mt, got := mediaTypeFromExt(path)
		if mt != whatsmeow.MediaDocument {
			t.Errorf("%s: media type = %v, want document", path, mt)
		}
		if got != want {
			t.Errorf("%s: mime = %q, want %q", path, got, want)
		}
	}
}

// TestOutgoingDocumentMessageFileName checks that an outgoing document keeps
// the existing title and MIME type, and sets FileName from the sanitised
// source filename.
func TestOutgoingDocumentMessageFileName(t *testing.T) {
	uploaded := whatsmeow.UploadResponse{
		URL:           "https://mmg.example/doc",
		DirectPath:    "/v/doc",
		MediaKey:      []byte{1, 2},
		FileSHA256:    []byte{3, 4},
		FileEncSHA256: []byte{5, 6},
		FileLength:    42,
	}
	cases := []struct {
		name      string
		path      string
		caption   string
		wantTitle string
		wantFile  string
		wantMIME  string
		fallback  bool
	}{
		{
			name:      "source basename is the title and filename",
			path:      filepath.Join("uploads", "lease.pdf"),
			caption:   "signed lease",
			wantTitle: "lease.pdf",
			wantFile:  "lease.pdf",
			wantMIME:  "application/pdf",
		},
		{
			name:      "extension case is preserved on the name",
			path:      filepath.Join("uploads", "Q3 report (final).PDF"),
			wantTitle: "Q3 report (final).PDF",
			wantFile:  "Q3 report (final).PDF",
			wantMIME:  "application/pdf",
		},
		{
			name:      "directory prefix is not part of the filename",
			path:      filepath.Join("..", "etc", "passwd.pdf"),
			wantTitle: "passwd.pdf",
			wantFile:  "passwd.pdf",
			wantMIME:  "application/pdf",
		},
		{
			name:      "degenerate basename keeps the raw title",
			path:      ".",
			wantTitle: ".",
			wantMIME:  "application/octet-stream",
			fallback:  true,
		},
		{
			name:      "parent basename keeps the raw title",
			path:      "..",
			wantTitle: "..",
			wantMIME:  "application/octet-stream",
			fallback:  true,
		},
		{
			name:      "null byte keeps the raw title",
			path:      "evil\x00name.pdf",
			wantTitle: filepath.Base("evil\x00name.pdf"),
			wantMIME:  "application/pdf",
			fallback:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, mimeType := mediaTypeFromExt(tc.path)
			if mimeType != tc.wantMIME {
				t.Fatalf("mediaTypeFromExt(%q) mime = %q, want %q", tc.path, mimeType, tc.wantMIME)
			}
			doc := outgoingDocumentMessage(tc.path, tc.caption, mimeType, uploaded)
			if doc.GetTitle() != tc.wantTitle {
				t.Errorf("title = %q, want %q", doc.GetTitle(), tc.wantTitle)
			}
			if doc.GetMimetype() != mimeType {
				t.Errorf("mimetype = %q, want the existing type %q", doc.GetMimetype(), mimeType)
			}
			if tc.fallback {
				if !strings.HasPrefix(doc.GetFileName(), "document_") {
					t.Errorf("filename = %q, want sanitised document_* fallback", doc.GetFileName())
				}
				if doc.GetFileName() == doc.GetTitle() {
					t.Errorf("filename = %q, want it sanitised away from the raw title", doc.GetFileName())
				}
			} else if doc.GetFileName() != tc.wantFile {
				t.Errorf("filename = %q, want %q", doc.GetFileName(), tc.wantFile)
			}
			if doc.GetCaption() != tc.caption {
				t.Errorf("caption = %q, want %q", doc.GetCaption(), tc.caption)
			}
			if doc.GetURL() != uploaded.URL || doc.GetDirectPath() != uploaded.DirectPath || doc.GetFileLength() != uploaded.FileLength {
				t.Errorf("upload location = (%q, %q, %d), want (%q, %q, %d)",
					doc.GetURL(), doc.GetDirectPath(), doc.GetFileLength(),
					uploaded.URL, uploaded.DirectPath, uploaded.FileLength)
			}
			if !bytes.Equal(doc.GetMediaKey(), uploaded.MediaKey) ||
				!bytes.Equal(doc.GetFileSHA256(), uploaded.FileSHA256) ||
				!bytes.Equal(doc.GetFileEncSHA256(), uploaded.FileEncSHA256) {
				t.Errorf("upload digests were not copied onto the document")
			}
		})
	}
}
