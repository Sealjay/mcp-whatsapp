package mcp

import (
	"testing"
	"time"
)

func TestParseTimestamp(t *testing.T) {
	ref := time.Date(2026, 4, 18, 15, 4, 5, 0, time.UTC)
	refDateOnly := time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		in      string
		want    time.Time
		wantErr bool
	}{
		{
			name: "RFC3339 with Z",
			in:   "2026-04-18T15:04:05Z",
			want: ref,
		},
		{
			name: "RFC3339 with offset",
			in:   "2026-04-18T16:04:05+01:00",
			want: ref,
		},
		{
			name: "RFC3339Nano",
			in:   "2026-04-18T15:04:05.123456789Z",
			want: time.Date(2026, 4, 18, 15, 4, 5, 123456789, time.UTC),
		},
		{
			name: "sqlite-style space-separated",
			in:   "2026-04-18 15:04:05",
			want: ref,
		},
		{
			name: "date-only",
			in:   "2026-04-18",
			want: refDateOnly,
		},
		{
			name: "bare T-format no zone",
			in:   "2026-04-18T15:04:05",
			want: ref,
		},
		{
			name:    "garbage string",
			in:      "not a timestamp",
			wantErr: true,
		},
		{
			name:    "empty",
			in:      "",
			wantErr: true,
		},
		{
			name:    "wrong order",
			in:      "18/04/2026",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTimestamp(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseTimestamp(%q) expected error, got %v", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTimestamp(%q) unexpected error: %v", tc.in, err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("parseTimestamp(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}
