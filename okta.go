package okta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"reflect"

	"github.com/google/go-querystring/query"
)

const (
	baseURL = "https://%s.okta.com/"
)

type service struct {
	client *Client
}

// A Client interacts with Okta.
type Client struct {
	client *http.Client

	BaseURL *url.URL

	organisation string
	apiToken     string

	// User agent used when communicating with the Okta api.
	UserAgent string

	common service // Reuse a single struct instead of allocating one for each service on the heap.

	User  *UserService
	Group *GroupService
}

// New returns a new Okta client.
func New(apiToken, organisation string) *Client {
	c := &Client{
		client:       http.DefaultClient,
		apiToken:     apiToken,
		organisation: organisation,
	}
	c.common.client = c
	c.BaseURL, _ = url.Parse(buildURL(baseURL, organisation))
	c.User = (*UserService)(&c.common)
	c.Group = (*GroupService)(&c.common)

	return c
}

// addOptions adds the parameters in opt as URL query parameters to s. opt
// must be a struct whose fields may contain "url" tags.
func addOptions(s string, opt interface{}) (string, error) {
	v := reflect.ValueOf(opt)
	if v.Kind() == reflect.Ptr && v.IsNil() {
		return s, nil
	}

	u, err := url.Parse(s)
	if err != nil {
		return s, err
	}

	qs, err := query.Values(opt)
	if err != nil {
		return s, err
	}

	u.RawQuery = qs.Encode()
	return u.String(), nil
}

// AddAuthorization injects the Authorization header to the request.
// If the client doesn't has an oauthToken, a new token is issed.
// If the token is expired, it is automatically refreshed.
func (c *Client) AddAuthorization(ctx context.Context, req *http.Request) error {
	if c.apiToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("SSWS %s", c.apiToken))
	}

	return nil
}

// Do sends an API request and returns the API response. The API response is
// JSON decoded and stored in the value pointed to by v, or returned as an
// error if an API error has occurred. If v implements the io.Writer
// interface, the raw response body will be written to v, without attempting to
// first decode it.
//
// The provided ctx must be non-nil. If it is canceled or times out,
// ctx.Err() will be returned.
func (c *Client) Do(ctx context.Context, req *http.Request, v interface{}) (*Response, error) {
	req = req.WithContext(ctx)

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		// If we got an error, and the context has been canceled,
		// the context's error is probably more useful.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		return nil, err
	}

	defer func() {
		// Drain up to 512 bytes and close the body to let the Transport reuse the connection.
		_, _ = io.CopyN(ioutil.Discard, resp.Body, 512)
		_ = resp.Body.Close()
	}()
	response := newResponse(resp)

	err = checkResponse(resp)
	if err != nil {
		// even though there was an error, we still return the response
		// in case the caller wants to inspect it further.
		return response, err
	}

	if v != nil {
		if w, ok := v.(io.Writer); ok {
			io.Copy(w, resp.Body)
		} else {
			err = json.NewDecoder(resp.Body).Decode(v)
			if err == io.EOF {
				err = nil // ignore EOF errors caused by empty response body.
			}
		}
	}

	return response, err
}

func newResponse(resp *http.Response) *Response {
	return &Response{Response: resp}
}

// NewRequest instantiate a new http.Request from a method, url and body.
// The body (if provided) is automatically Marshalled into JSON.
func (c *Client) NewRequest(method, urlStr string, body interface{}) (*http.Request, error) {
	rel, err := url.Parse(urlStr)
	if err != nil {
		return nil, err
	}

	u := c.BaseURL.ResolveReference(rel)

	var buf io.ReadWriter
	if body != nil {
		buf = new(bytes.Buffer)
		err := json.NewEncoder(buf).Encode(body)
		if err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequest(method, u.String(), buf)
	if err != nil {
		return nil, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	return req, nil
}

// checkResponse checks the *http.Response.
// HTTP status codes ranging from 200 to 299 are considered are successes.
// Otherwise an error happen, and the error gets unmarshalled and returned into the error.
func checkResponse(r *http.Response) error {
	if c := r.StatusCode; 200 <= c && c <= 299 {
		return nil
	}

	errorResponse := &ErrorResponse{Response: r}
	data, err := ioutil.ReadAll(r.Body)
	if err == nil && data != nil {
		errorResponse.Code = int64(r.StatusCode)
		errorResponse.Type = http.StatusText(r.StatusCode)
		errorResponse.Message = string(data)
	}

	// TODO: handle the different errors here, such as MFA, Rate limit, etc...
	return errorResponse
}

// Response embeds a *http.Response.
type Response struct {
	*http.Response
}

// An ErrorResponse reports an error caused by an API request.
type ErrorResponse struct {
	Response *http.Response // HTTP response that caused this error
	Code     int64
	Type     string
	Message  string
}

func (r *ErrorResponse) Error() string {
	return fmt.Sprintf("%v %v: Okta responsed with code %d, type %v and message %v",
		r.Response.Request.Method, r.Response.Request.URL,
		r.Response.StatusCode, r.Type, r.Message)
}

func buildURL(baseURL string, args ...interface{}) string {
	return fmt.Sprintf(baseURL, args...)
}
