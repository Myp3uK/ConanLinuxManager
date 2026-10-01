package mods

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDetailsReadsTitlesSizesAndUpdateTimes(t *testing.T) {
	var gotForm string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		_ = r.ParseForm()
		gotForm = r.Form.Encode()
		w.Header().Set("Content-Type", "application/json")
		// file_size arrives quoted here and bare in other Steam answers, and
		// both have to read the same way.
		_, _ = w.Write([]byte(`{"response":{"result":1,"resultcount":3,"publishedfiledetails":[
			{"publishedfileid":"1159180273","result":1,"title":"Pippi - User & Server Management","file_size":"48271360","time_updated":1756000000,"consumer_app_id":440900},
			{"publishedfileid":"2050780234","result":1,"title":"LBPR","file_size":1024,"time_updated":1700000000,"consumer_app_id":440900},
			{"publishedfileid":"999","result":9}]}}`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL}
	items, err := client.Details(context.Background(), []string{"1159180273", "2050780234", "999"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotForm, "itemcount=3") || !strings.Contains(gotForm, "publishedfileids%5B0%5D=1159180273") {
		t.Errorf("unexpected form: %s", gotForm)
	}
	pippi := items["1159180273"]
	if pippi.Title != "Pippi - User & Server Management" || pippi.Size != 48271360 || pippi.AppID != GameAppID {
		t.Errorf("unexpected item: %+v", pippi)
	}
	if !pippi.Updated.Equal(time.Unix(1756000000, 0).UTC()) {
		t.Errorf("Updated = %v", pippi.Updated)
	}
	if items["2050780234"].Size != 1024 {
		t.Errorf("a bare number should read the same as a quoted one: %+v", items["2050780234"])
	}
	// An unknown item is reported, not fatal: one wrong id must not hide the
	// rest of the list.
	if !items["999"].Missing {
		t.Errorf("item 999 should be missing: %+v", items["999"])
	}
}

func TestDetailsMarksBannedItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"publishedfiledetails":[
			{"publishedfileid":"111111","result":1,"title":"Gone","banned":true,"time_updated":1700000000,"consumer_app_id":440900}]}}`))
	}))
	defer server.Close()
	items, err := Client{BaseURL: server.URL}.Details(context.Background(), []string{"111111"})
	if err != nil {
		t.Fatal(err)
	}
	if !items["111111"].Missing {
		t.Errorf("a banned item cannot be downloaded: %+v", items["111111"])
	}
}

func TestDetailsSplitsLongLists(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		var body strings.Builder
		body.WriteString(`{"response":{"publishedfiledetails":[`)
		for i := 0; ; i++ {
			id := r.Form.Get("publishedfileids[" + itoa(i) + "]")
			if id == "" {
				break
			}
			if i > 0 {
				body.WriteString(",")
			}
			body.WriteString(`{"publishedfileid":"` + id + `","result":1,"title":"m","time_updated":1,"consumer_app_id":440900}`)
		}
		body.WriteString(`]}}`)
		_, _ = w.Write([]byte(body.String()))
	}))
	defer server.Close()
	ids := make([]string, maxDetailsPerCall+5)
	for i := range ids {
		ids[i] = itoa(1000000 + i)
	}
	items, err := Client{BaseURL: server.URL}.Details(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d; want 2", calls)
	}
	if len(items) != len(ids) {
		t.Errorf("got %d of %d items", len(items), len(ids))
	}
}

func TestCollectionReturnsChildrenInOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"result":1,"resultcount":1,"collectiondetails":[
			{"publishedfileid":"3000","result":1,"children":[
				{"publishedfileid":"111111","sortorder":0,"filetype":0},
				{"publishedfileid":"222222","sortorder":1,"filetype":0}]}]}}`))
	}))
	defer server.Close()
	ids, err := Client{BaseURL: server.URL}.Collection(context.Background(), "3000")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "111111" || ids[1] != "222222" {
		t.Fatalf("unexpected children: %v", ids)
	}
}

func TestCollectionRejectsWhatIsNotACollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"result":1,"resultcount":1,"collectiondetails":[{"publishedfileid":"3000","result":9}]}}`))
	}))
	defer server.Close()
	if ids, err := (Client{BaseURL: server.URL}).Collection(context.Background(), "3000"); err == nil {
		t.Errorf("expected an error, got %v", ids)
	}
}

func TestPostReportsSteamFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := (Client{BaseURL: server.URL}).Details(context.Background(), []string{"111111"}); err == nil ||
		!strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
