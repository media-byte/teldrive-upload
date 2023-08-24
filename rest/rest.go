package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sync"
)

type Client struct {
	mu           sync.RWMutex
	c            *http.Client
	rootURL      string
	errorHandler func(resp *http.Response) error
	headers      map[string]string
	signer       SignerFn
}

func NewClient(c *http.Client) *Client {
	api := &Client{
		c:            c,
		errorHandler: defaultErrorHandler,
		headers:      make(map[string]string),
	}
	return api
}

func checkClose(c io.Closer, err *error) {
	cerr := c.Close()
	if *err == nil {
		*err = cerr
	}
}

func ReadBody(resp *http.Response) (result []byte, err error) {
	defer checkClose(resp.Body, &err)
	return io.ReadAll(resp.Body)
}

func defaultErrorHandler(resp *http.Response) (err error) {
	body, err := ReadBody(resp)
	if err != nil {
		return fmt.Errorf("error reading error out of body: %w", err)
	}
	return fmt.Errorf("HTTP error %v (%v) returned body: %q", resp.StatusCode, resp.Status, body)
}

func (api *Client) SetErrorHandler(fn func(resp *http.Response) error) *Client {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.errorHandler = fn
	return api
}

func (api *Client) SetRoot(RootURL string) *Client {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.rootURL = RootURL
	return api
}

func (api *Client) SetHeader(key, value string) *Client {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.headers[key] = value
	return api
}

func (api *Client) RemoveHeader(key string) *Client {
	api.mu.Lock()
	defer api.mu.Unlock()
	delete(api.headers, key)
	return api
}

type SignerFn func(*http.Request) error

func (api *Client) SetSigner(signer SignerFn) *Client {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.signer = signer
	return api
}

func (api *Client) SetUserPass(UserName, Password string) *Client {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	req.SetBasicAuth(UserName, Password)
	api.SetHeader("Authorization", req.Header.Get("Authorization"))
	return api
}

func (api *Client) SetCookie(cks ...*http.Cookie) *Client {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	for _, ck := range cks {
		req.AddCookie(ck)
	}
	api.SetHeader("Cookie", req.Header.Get("Cookie"))
	return api
}

// Opts contains parameters for Call, CallJSON, etc.
type Opts struct {
	Method                string // GET, POST, etc.
	Path                  string // relative to RootURL
	RootURL               string // override RootURL passed into SetRoot()
	Body                  io.Reader
	GetBody               func() (io.ReadCloser, error) // body builder, needed to enable low-level HTTP/2 retries
	NoResponse            bool                          // set to close Body
	ContentType           string
	ContentLength         *int64
	ContentRange          string
	ExtraHeaders          map[string]string // extra headers, start them with "*" for don't canonicalise
	UserName              string            // username for Basic Auth
	Password              string            // password for Basic Auth
	IgnoreStatus          bool              // if set then we don't check error status or parse error body
	MultipartParams       url.Values        // if set do multipart form upload with attached file
	MultipartMetadataName string            // ..this is used for the name of the metadata form part if set
	MultipartContentName  string            // ..name of the parameter which is the attached file
	MultipartFileName     string            // ..name of the file for the attached file
	Parameters            url.Values        // any parameters for the final URL
	TransferEncoding      []string          // transfer encoding, set to "identity" to disable chunked encoding
	Trailer               *http.Header      // set the request trailer
	Close                 bool              // set to close the connection after this transaction
	NoRedirect            bool              // if this is set then the client won't follow redirects
}

func (o *Opts) Copy() *Opts {
	newOpts := *o
	return &newOpts
}

const drainLimit = 10 * 1024 * 1024

func drainAndClose(r io.ReadCloser) (err error) {
	_, readErr := io.CopyN(io.Discard, r, drainLimit)
	if readErr == io.EOF {
		readErr = nil
	}
	err = r.Close()
	if readErr != nil {
		return readErr
	}
	return err
}

func checkDrainAndClose(r io.ReadCloser, err *error) {
	cerr := drainAndClose(r)
	if *err == nil {
		*err = cerr
	}
}

func DecodeJSON(resp *http.Response, result interface{}) (err error) {
	defer checkDrainAndClose(resp.Body, &err)
	decoder := json.NewDecoder(resp.Body)
	return decoder.Decode(result)
}

func DecodeXML(resp *http.Response, result interface{}) (err error) {
	defer checkDrainAndClose(resp.Body, &err)
	decoder := xml.NewDecoder(resp.Body)

	decoder.Strict = false
	decoder.Entity = xml.HTMLEntity
	return decoder.Decode(result)
}

func ClientWithNoRedirects(c *http.Client) *http.Client {
	clientCopy := *c
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clientCopy
}

func (api *Client) Call(ctx context.Context, opts *Opts) (resp *http.Response, err error) {
	api.mu.RLock()
	defer api.mu.RUnlock()
	if opts == nil {
		return nil, errors.New("call() called with nil opts")
	}
	url := api.rootURL
	if opts.RootURL != "" {
		url = opts.RootURL
	}
	if url == "" {
		return nil, errors.New("RootURL not set")
	}
	url += opts.Path
	if opts.Parameters != nil && len(opts.Parameters) > 0 {
		url += "?" + opts.Parameters.Encode()
	}
	body := NoCloser(opts.Body)

	if opts.ContentLength != nil && *opts.ContentLength == 0 {
		body = nil
	}
	req, err := http.NewRequestWithContext(ctx, opts.Method, url, body)
	if err != nil {
		return
	}
	headers := make(map[string]string)
	// Set default headers
	for k, v := range api.headers {
		headers[k] = v
	}
	if opts.ContentType != "" {
		headers["Content-Type"] = opts.ContentType
	}
	if opts.ContentLength != nil {
		req.ContentLength = *opts.ContentLength
	}
	if opts.ContentRange != "" {
		headers["Content-Range"] = opts.ContentRange
	}
	if len(opts.TransferEncoding) != 0 {
		req.TransferEncoding = opts.TransferEncoding
	}
	if opts.GetBody != nil {
		req.GetBody = opts.GetBody
	}
	if opts.Trailer != nil {
		req.Trailer = *opts.Trailer
	}
	if opts.Close {
		req.Close = true
	}
	// Set any extra headers
	for k, v := range opts.ExtraHeaders {
		headers[k] = v
	}

	// Now set the headers
	for k, v := range headers {
		if k != "" && v != "" {
			if k[0] == '*' {
				// Add non-canonical version if header starts with *
				k = k[1:]
				req.Header[k] = append(req.Header[k], v)
			} else {
				req.Header.Add(k, v)
			}
		}
	}

	if opts.UserName != "" || opts.Password != "" {
		req.SetBasicAuth(opts.UserName, opts.Password)
	}
	var c *http.Client
	if opts.NoRedirect {
		c = ClientWithNoRedirects(api.c)
	} else {
		c = api.c
	}
	if api.signer != nil {
		api.mu.RUnlock()
		err = api.signer(req)
		api.mu.RLock()
		if err != nil {
			return nil, fmt.Errorf("signer failed: %w", err)
		}
	}
	api.mu.RUnlock()
	resp, err = c.Do(req)
	api.mu.RLock()
	if err != nil {
		return nil, err
	}
	if !opts.IgnoreStatus {
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			err = api.errorHandler(resp)
			if err.Error() == "" {
				// replace empty errors with something
				err = fmt.Errorf("http error %d: %v", resp.StatusCode, resp.Status)
			}
			return resp, err
		}
	}
	if opts.NoResponse {
		return resp, drainAndClose(resp.Body)
	}
	return resp, nil
}

func MultipartUpload(ctx context.Context, in io.Reader, params url.Values, contentName, fileName string) (io.ReadCloser, string, int64, error) {
	bodyReader, bodyWriter := io.Pipe()
	writer := multipart.NewWriter(bodyWriter)
	contentType := writer.FormDataContentType()

	// Create a Multipart Writer as base for calculating the Content-Length
	buf := &bytes.Buffer{}
	dummyMultipartWriter := multipart.NewWriter(buf)
	err := dummyMultipartWriter.SetBoundary(writer.Boundary())
	if err != nil {
		return nil, "", 0, err
	}

	for key, vals := range params {
		for _, val := range vals {
			err := dummyMultipartWriter.WriteField(key, val)
			if err != nil {
				return nil, "", 0, err
			}
		}
	}
	if in != nil {
		_, err = dummyMultipartWriter.CreateFormFile(contentName, fileName)
		if err != nil {
			return nil, "", 0, err
		}
	}

	err = dummyMultipartWriter.Close()
	if err != nil {
		return nil, "", 0, err
	}

	multipartLength := int64(buf.Len())

	// Make sure we close the pipe writer to release the reader on context cancel
	quit := make(chan struct{})
	go func() {
		select {
		case <-quit:
			break
		case <-ctx.Done():
			_ = bodyWriter.CloseWithError(ctx.Err())
		}
	}()

	// Pump the data in the background
	go func() {
		defer close(quit)

		var err error

		for key, vals := range params {
			for _, val := range vals {
				err = writer.WriteField(key, val)
				if err != nil {
					_ = bodyWriter.CloseWithError(fmt.Errorf("create metadata part: %w", err))
					return
				}
			}
		}

		if in != nil {
			part, err := writer.CreateFormFile(contentName, fileName)
			if err != nil {
				_ = bodyWriter.CloseWithError(fmt.Errorf("failed to create form file: %w", err))
				return
			}

			_, err = io.Copy(part, in)
			if err != nil {
				_ = bodyWriter.CloseWithError(fmt.Errorf("failed to copy data: %w", err))
				return
			}
		}

		err = writer.Close()
		if err != nil {
			_ = bodyWriter.CloseWithError(fmt.Errorf("failed to close form: %w", err))
			return
		}

		_ = bodyWriter.Close()
	}()

	return bodyReader, contentType, multipartLength, nil
}

func (api *Client) CallJSON(ctx context.Context, opts *Opts, request interface{}, response interface{}) (resp *http.Response, err error) {
	return api.callCodec(ctx, opts, request, response, json.Marshal, DecodeJSON, "application/json")
}

func (api *Client) CallXML(ctx context.Context, opts *Opts, request interface{}, response interface{}) (resp *http.Response, err error) {
	return api.callCodec(ctx, opts, request, response, xml.Marshal, DecodeXML, "application/xml")
}

type marshalFn func(v interface{}) ([]byte, error)
type decodeFn func(resp *http.Response, result interface{}) (err error)

func (api *Client) callCodec(ctx context.Context, opts *Opts, request interface{}, response interface{}, marshal marshalFn, decode decodeFn, contentType string) (resp *http.Response, err error) {
	var requestBody []byte
	// Marshal the request if given
	if request != nil {
		requestBody, err = marshal(request)
		if err != nil {
			return nil, err
		}
		// Set the body up as a marshalled object if no body passed in
		if opts.Body == nil {
			opts = opts.Copy()
			opts.ContentType = contentType
			opts.Body = bytes.NewBuffer(requestBody)
		}
	}
	if opts.MultipartParams != nil || opts.MultipartContentName != "" {
		params := opts.MultipartParams
		if params == nil {
			params = url.Values{}
		}
		if opts.MultipartMetadataName != "" {
			params.Add(opts.MultipartMetadataName, string(requestBody))
		}
		opts = opts.Copy()

		var overhead int64
		opts.Body, opts.ContentType, overhead, err = MultipartUpload(ctx, opts.Body, params, opts.MultipartContentName, opts.MultipartFileName)
		if err != nil {
			return nil, err
		}
		if opts.ContentLength != nil {
			*opts.ContentLength += overhead
		}
	}
	resp, err = api.Call(ctx, opts)
	if err != nil {
		return resp, err
	}
	// if opts.NoResponse is set, resp.Body will have been closed by Call()
	if response == nil || opts.NoResponse {
		return resp, nil
	}
	err = decode(resp, response)
	return resp, err
}
