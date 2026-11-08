package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sealjay/mcp-whatsapp/internal/client"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

func TestListChatsByLabelPaginationOverflowThroughReadOnlyMCP(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const labelID = "1"
	if err := st.UpsertLabel(context.Background(), labelID, "Lead", 0, false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatLabel(context.Background(), labelID, "447700000001@s.whatsapp.net", true); err != nil {
		t.Fatal(err)
	}

	c, err := client.New(context.Background(), client.Config{
		StoreDir:      dir,
		Store:         st,
		Logger:        client.NewStderrLogger("test", "ERROR", false),
		LabelsEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	srv := NewReadOnlyServer(c, nil, true)
	mux := http.NewServeMux()
	srv.AttachHTTP(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_chats_by_label","arguments":{"label":"1","limit":%d,"page":3,"include_last_message":false}}}`,
		int(^uint(0)>>2)+1,
	)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, httpServer.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("MCP request: %v", err)
	}
	defer resp.Body.Close()

	var envelope struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode MCP response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("MCP request returned JSON-RPC error: %s", envelope.Error.Message)
	}
	if envelope.Result == nil || envelope.Result.IsError {
		t.Fatalf("MCP tool returned error result: %+v", envelope.Result)
	}
	if len(envelope.Result.Content) != 1 || envelope.Result.Content[0].Text != "[]" {
		t.Fatalf("MCP result = %+v, want an empty array", envelope.Result.Content)
	}
}
