package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	confluenceMetadataLimit = 8 << 20
	confluenceBodyLimit     = 12 << 20
)

type ConfluenceClient interface {
	SearchChangedPages(ctx context.Context, since time.Time, start, limit int) (*PageSearchResult, error)
	GetPageBody(ctx context.Context, pageID string) (*ConfluencePageBody, error)
}

type ConfluenceClientConfig struct {
	BaseURL      string
	AuthMode     string
	Username     string
	Password     string
	IncludeBlogs bool
	Timeout      time.Duration
}

type ConfluencePage struct {
	ID                   string
	Type                 string
	Status               string
	Title                string
	SpaceKey             string
	CreatorUsername      string
	LastModifierUsername string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Version              int
	WebURL               string
}

type PageSearchResult struct {
	Pages []ConfluencePage
	Start int
	Limit int
	Size  int
}

type ConfluencePageBody struct {
	PageID  string
	Version int
	Storage string
}

type ConfluenceHTTPError struct {
	StatusCode int
	Message    string
}

func (e *ConfluenceHTTPError) Error() string {
	return fmt.Sprintf("confluence returned HTTP %d: %s", e.StatusCode, e.Message)
}

type confluenceRESTClient struct {
	config ConfluenceClientConfig
	http   *http.Client
}

func NewConfluenceClient(config ConfluenceClientConfig) (ConfluenceClient, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("invalid Confluence base URL")
	}
	config.BaseURL = parsed.String()
	config.AuthMode = strings.ToUpper(strings.TrimSpace(config.AuthMode))
	if config.AuthMode == "" {
		config.AuthMode = "BASIC"
	}
	if config.AuthMode != "BASIC" && config.AuthMode != "NONE" {
		return nil, errors.New("unsupported Confluence authentication mode")
	}
	if config.AuthMode == "BASIC" && (strings.TrimSpace(config.Username) == "" || config.Password == "") {
		return nil, errors.New("Confluence Basic Auth credentials are required")
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	return &confluenceRESTClient{
		config: config,
		http: &http.Client{
			Timeout: config.Timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *confluenceRESTClient) SearchChangedPages(ctx context.Context, since time.Time, start, limit int) (*PageSearchResult, error) {
	if start < 0 {
		start = 0
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	contentTypes := "type = page"
	if c.config.IncludeBlogs {
		contentTypes = "type in (page, blogpost)"
	}
	values := url.Values{}
	values.Set("cql", fmt.Sprintf(`%s and lastmodified >= "%s" order by lastmodified asc`, contentTypes, since.Format("2006-01-02 15:04")))
	values.Set("start", strconv.Itoa(start))
	values.Set("limit", strconv.Itoa(limit))
	values.Set("expand", "history,version,space")

	var response confluenceSearchResponse
	if err := c.getJSON(ctx, c.config.BaseURL+"/rest/api/content/search?"+values.Encode(), confluenceMetadataLimit, &response); err != nil {
		return nil, err
	}
	result := &PageSearchResult{Start: response.Start, Limit: response.Limit, Size: response.Size, Pages: make([]ConfluencePage, 0, len(response.Results))}
	if result.Limit <= 0 {
		result.Limit = limit
	}
	if result.Size <= 0 && len(response.Results) > 0 {
		result.Size = len(response.Results)
	}
	for _, item := range response.Results {
		createdAt, _ := parseConfluenceTime(item.History.CreatedDate)
		updatedAt, _ := parseConfluenceTime(item.Version.When)
		result.Pages = append(result.Pages, ConfluencePage{
			ID:                   item.ID,
			Type:                 strings.ToUpper(item.Type),
			Status:               strings.ToUpper(item.Status),
			Title:                strings.TrimSpace(item.Title),
			SpaceKey:             strings.TrimSpace(item.Space.Key),
			CreatorUsername:      confluenceUsername(item.History.CreatedBy),
			LastModifierUsername: confluenceUsername(item.Version.By),
			CreatedAt:            createdAt,
			UpdatedAt:            updatedAt,
			Version:              item.Version.Number,
			WebURL:               resolveConfluenceWebURL(c.config.BaseURL, response.Links.Base, item.Links.WebUI),
		})
	}
	return result, nil
}

func (c *confluenceRESTClient) GetPageBody(ctx context.Context, pageID string) (*ConfluencePageBody, error) {
	pageID = strings.TrimSpace(pageID)
	if pageID == "" || strings.ContainsAny(pageID, "/?#") {
		return nil, errors.New("invalid Confluence page ID")
	}
	values := url.Values{}
	values.Set("expand", "body.storage,version")
	var response struct {
		ID      string `json:"id"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	endpoint := c.config.BaseURL + "/rest/api/content/" + url.PathEscape(pageID) + "?" + values.Encode()
	if err := c.getJSON(ctx, endpoint, confluenceBodyLimit, &response); err != nil {
		return nil, err
	}
	return &ConfluencePageBody{PageID: response.ID, Version: response.Version.Number, Storage: response.Body.Storage.Value}, nil
}

func (c *confluenceRESTClient) getJSON(ctx context.Context, endpoint string, maximum int64, target any) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "Weekly-Confluence-Sync/1")
		if c.config.AuthMode == "BASIC" {
			request.SetBasicAuth(c.config.Username, c.config.Password)
		}
		response, err := c.http.Do(request)
		if err != nil {
			lastErr = err
		} else {
			data, readErr := readLimited(response.Body, maximum)
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if readErr != nil {
					return readErr
				}
				if err := json.Unmarshal(data, target); err != nil {
					return fmt.Errorf("decode Confluence response: %w", err)
				}
				return nil
			}
			message := strings.TrimSpace(string(bytes.TrimSpace(data)))
			if len(message) > 500 {
				message = message[:500]
			}
			lastErr = &ConfluenceHTTPError{StatusCode: response.StatusCode, Message: message}
			if response.StatusCode != http.StatusTooManyRequests && response.StatusCode < 500 {
				return lastErr
			}
		}
		if attempt < 2 {
			delay := time.Duration(200*(attempt+1)*(attempt+1)) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return lastErr
}

func readLimited(body io.Reader, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, errors.New("Confluence response is too large")
	}
	return data, nil
}

type confluenceSearchResponse struct {
	Results []struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Status string `json:"status"`
		Title  string `json:"title"`
		Space  struct {
			Key string `json:"key"`
		} `json:"space"`
		History struct {
			CreatedDate string                   `json:"createdDate"`
			CreatedBy   confluencePersonResponse `json:"createdBy"`
		} `json:"history"`
		Version struct {
			Number int                      `json:"number"`
			When   string                   `json:"when"`
			By     confluencePersonResponse `json:"by"`
		} `json:"version"`
		Links struct {
			WebUI string `json:"webui"`
		} `json:"_links"`
	} `json:"results"`
	Start int `json:"start"`
	Limit int `json:"limit"`
	Size  int `json:"size"`
	Links struct {
		Base string `json:"base"`
	} `json:"_links"`
}

type confluencePersonResponse struct {
	Username    string `json:"username"`
	UserKey     string `json:"userKey"`
	DisplayName string `json:"displayName"`
}

func confluenceUsername(person confluencePersonResponse) string {
	if value := strings.TrimSpace(person.Username); value != "" {
		return value
	}
	return strings.TrimSpace(person.UserKey)
}

func parseConfluenceTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid Confluence timestamp %q", value)
}

func resolveConfluenceWebURL(configuredBase, responseBase, webUI string) string {
	webUI = strings.TrimSpace(webUI)
	if webUI == "" {
		return ""
	}
	if parsed, err := url.Parse(webUI); err == nil && parsed.IsAbs() {
		return parsed.String()
	}
	base := strings.TrimRight(configuredBase, "/")
	configuredURL, configuredErr := url.Parse(configuredBase)
	responseURL, responseErr := url.Parse(strings.TrimSpace(responseBase))
	if configuredErr == nil && responseErr == nil && responseURL.IsAbs() && strings.EqualFold(configuredURL.Scheme, responseURL.Scheme) && strings.EqualFold(configuredURL.Host, responseURL.Host) {
		base = strings.TrimRight(responseURL.String(), "/")
	}
	return base + "/" + strings.TrimLeft(webUI, "/")
}
