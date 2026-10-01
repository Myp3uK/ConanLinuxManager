package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GameAppID is the Steam app that owns the Conan Exiles Workshop. Mods are
// published against the game even though the manager installs the dedicated
// server, which is a separate app.
const GameAppID = 440900

// steamAPI is Steam's public interface. These two endpoints need no API key,
// so the manager can name mods and spot updates without the operator having to
// register one.
const steamAPI = "https://api.steampowered.com"

// Client asks Steam about Workshop items. The HTTP client is supplied by the
// caller, which is what lets the lookup go through the notification proxy on a
// host where Steam's API is blocked.
type Client struct {
	HTTPClient *http.Client
	// BaseURL overrides Steam's address in tests.
	BaseURL string
}

// ItemDetails is what Steam says about one Workshop item.
type ItemDetails struct {
	WorkshopID string
	Title      string
	// Updated is when the author last changed the item.
	Updated time.Time
	Size    int64
	// AppID is the game the item was published for, so a mod for another game
	// can be refused instead of silently never loading.
	AppID int
	// Missing is set for an item Steam does not know: a wrong id, or one that
	// was taken down.
	Missing bool
}

const steamTimeout = 20 * time.Second

func (c Client) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: steamTimeout}
}

func (c Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return steamAPI
}

// maxDetailsPerCall bounds one request. Steam accepts more, but a shorter body
// keeps a partial failure small.
const maxDetailsPerCall = 50

// Details looks up items by id. Items Steam does not know come back with
// Missing set rather than as an error, so one bad id does not hide the rest.
func (c Client) Details(ctx context.Context, ids []string) (map[string]ItemDetails, error) {
	result := map[string]ItemDetails{}
	for start := 0; start < len(ids); start += maxDetailsPerCall {
		end := start + maxDetailsPerCall
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		form := url.Values{}
		form.Set("itemcount", strconv.Itoa(len(batch)))
		for i, id := range batch {
			form.Set(fmt.Sprintf("publishedfileids[%d]", i), id)
		}
		var payload struct {
			Response struct {
				Result  int `json:"result"`
				Details []struct {
					ID       string   `json:"publishedfileid"`
					Result   int      `json:"result"`
					Title    string   `json:"title"`
					FileSize flexInt  `json:"file_size"`
					Updated  flexInt  `json:"time_updated"`
					AppID    flexInt  `json:"consumer_app_id"`
					Creator  flexInt  `json:"creator_app_id"`
					Banned   jsonBool `json:"banned"`
				} `json:"publishedfiledetails"`
			} `json:"response"`
		}
		if err := c.post(ctx, "/ISteamRemoteStorage/GetPublishedFileDetails/v1/", form, &payload); err != nil {
			return nil, err
		}
		for _, item := range payload.Response.Details {
			details := ItemDetails{WorkshopID: item.ID, Title: strings.TrimSpace(item.Title)}
			// Result 1 is success; anything else means Steam has nothing to
			// show, most often a deleted or mistyped item.
			if item.Result != 1 || bool(item.Banned) {
				details.Missing = true
				result[item.ID] = details
				continue
			}
			details.Size = int64(item.FileSize)
			details.AppID = int(item.AppID)
			if details.AppID == 0 {
				details.AppID = int(item.Creator)
			}
			if seconds := int64(item.Updated); seconds > 0 {
				details.Updated = time.Unix(seconds, 0).UTC()
			}
			result[item.ID] = details
		}
	}
	return result, nil
}

// Collection returns the item ids of a Workshop collection, in the order the
// author arranged them. Adding a collection is how an operator moves a whole
// modpack to a server without copying twenty ids by hand.
func (c Client) Collection(ctx context.Context, id string) ([]string, error) {
	form := url.Values{}
	form.Set("collectioncount", "1")
	form.Set("publishedfileids[0]", id)
	var payload struct {
		Response struct {
			Collections []struct {
				ID       string `json:"publishedfileid"`
				Result   int    `json:"result"`
				Children []struct {
					ID        string  `json:"publishedfileid"`
					SortOrder flexInt `json:"sortorder"`
					FileType  flexInt `json:"filetype"`
				} `json:"children"`
			} `json:"collectiondetails"`
		} `json:"response"`
	}
	if err := c.post(ctx, "/ISteamRemoteStorage/GetCollectionDetails/v1/", form, &payload); err != nil {
		return nil, err
	}
	if len(payload.Response.Collections) == 0 || payload.Response.Collections[0].Result != 1 {
		return nil, fmt.Errorf("Steam knows no collection %s", id)
	}
	children := payload.Response.Collections[0].Children
	ids := make([]string, 0, len(children))
	for _, child := range children {
		if child.ID != "" {
			ids = append(ids, child.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("collection %s is empty", id)
	}
	return ids, nil
}

func (c Client) post(ctx context.Context, path string, form url.Values, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.client().Do(request)
	if err != nil {
		return fmt.Errorf("ask Steam about Workshop items: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Steam answered HTTP %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(into); err != nil {
		return fmt.Errorf("read Steam's answer: %w", err)
	}
	return nil
}

// flexInt reads a field Steam sends as a number in one call and as a quoted
// string in another; file_size has done both.
type flexInt int64

func (f *flexInt) UnmarshalJSON(data []byte) error {
	text := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if text == "" || text == "null" {
		return nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		// A float such as 1.0e9 is still a count of bytes or seconds.
		asFloat, floatErr := strconv.ParseFloat(text, 64)
		if floatErr != nil {
			return fmt.Errorf("not a number: %s", text)
		}
		value = int64(asFloat)
	}
	*f = flexInt(value)
	return nil
}

// jsonBool reads a flag Steam sends as true/false or as 0/1.
type jsonBool bool

func (b *jsonBool) UnmarshalJSON(data []byte) error {
	text := strings.Trim(strings.TrimSpace(string(data)), `"`)
	*b = text == "true" || text == "1"
	return nil
}
