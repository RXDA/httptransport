package client

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/textproto"
	"reflect"
	"strings"
	"time"

	"github.com/go-courier/courier"
	"github.com/go-courier/httptransport/client/roundtrippers"
	"github.com/go-courier/httptransport/httpx"
	"github.com/go-courier/httptransport/transformers"
	"github.com/go-courier/reflectx/typesutil"
	"github.com/go-courier/statuserror"
	"github.com/sirupsen/logrus"

	"github.com/go-courier/httptransport"
)

type HttpTransport func(rt http.RoundTripper) http.RoundTripper

type Client struct {
	Protocol              string
	Host                  string
	Port                  int16
	Timeout               time.Duration
	RequestTransformerMgr *httptransport.RequestTransformerMgr
	HttpTransports        []HttpTransport
	NewError              func(resp *http.Response) error
}

func (c *Client) SetDefaults() {
	if c.RequestTransformerMgr == nil {
		c.RequestTransformerMgr = httptransport.NewRequestTransformerMgr(nil, nil)
		c.RequestTransformerMgr.SetDefaults()
	}
	if c.HttpTransports == nil {
		c.HttpTransports = []HttpTransport{roundtrippers.NewLogRoundTripper(logrus.WithField("client", ""))}
	}
	if c.NewError == nil {
		c.NewError = func(resp *http.Response) error {
			return &statuserror.StatusErr{
				Code:    resp.StatusCode * 1e6,
				Msg:     resp.Status,
				Sources: []string{resp.Request.Host},
			}
		}
	}
}

func ContextWithClient(ctx context.Context, c *http.Client) context.Context {
	return context.WithValue(ctx, "courier.Client", c)
}

func ClientFromContext(ctx context.Context) *http.Client {
	if ctx == nil {
		return nil
	}
	if c, ok := ctx.Value("courier.Client").(*http.Client); ok {
		return c
	}
	return nil
}

func (c *Client) Do(ctx context.Context, req interface{}, metas ...courier.Metadata) courier.Result {
	request, ok := req.(*http.Request)
	if !ok {
		request2, err := c.newRequest(ctx, req, metas...)
		if err != nil {
			return &Result{
				Err:            RequestFailed.StatusErr().WithDesc(err.Error()),
				NewError:       c.NewError,
				TransformerMgr: c.RequestTransformerMgr.TransformerMgr,
			}
		}
		request = request2
	}

	httpClient := ClientFromContext(ctx)
	if httpClient == nil {
		httpClient = GetShortConnClient(c.Timeout, c.HttpTransports...)
	}

	resp, err := httpClient.Do(request)
	if err != nil {
		return &Result{
			Err:            RequestFailed.StatusErr().WithDesc(err.Error()),
			NewError:       c.NewError,
			TransformerMgr: c.RequestTransformerMgr.TransformerMgr,
		}
	}
	return &Result{
		NewError:       c.NewError,
		TransformerMgr: c.RequestTransformerMgr.TransformerMgr,
		Response:       resp,
	}
}

func (c *Client) toUrl(path string) string {
	protocol := c.Protocol
	if protocol == "" {
		protocol = "http"
	}
	url := fmt.Sprintf("%s://%s", protocol, c.Host)
	if c.Port > 0 {
		url = fmt.Sprintf("%s:%d", url, c.Port)
	}
	return url + path
}

func (c *Client) newRequest(ctx context.Context, req interface{}, metas ...courier.Metadata) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	method := ""
	path := ""

	if methodDescriber, ok := req.(httptransport.MethodDescriber); ok {
		method = methodDescriber.Method()
	}

	if pathDescriber, ok := req.(httptransport.PathDescriber); ok {
		path = pathDescriber.Path()
	}

	request, err := c.RequestTransformerMgr.NewRequest(method, c.toUrl(path), req)
	if err != nil {
		return nil, RequestTransformFailed.StatusErr().WithDesc(err.Error())
	}

	request = request.WithContext(ctx)

	for k, vs := range courier.FromMetas(metas...) {
		for _, v := range vs {
			request.Header.Add(k, v)
		}
	}

	return request, nil
}

type Result struct {
	*http.Response
	transformers.TransformerMgr
	NewError func(resp *http.Response) error
	Err      error
}

func (r *Result) Into(body interface{}) (courier.Metadata, error) {
	defer func() {
		if r.Response != nil && r.Body != nil {
			r.Body.Close()
		}
	}()

	if r.Err != nil {
		return nil, r.Err
	}

	meta := courier.Metadata(r.Header)

	if !isOk(r.StatusCode) {
		body = r.NewError(r.Response)
	}

	if body == nil {
		return meta, nil
	}

	switch v := body.(type) {
	case error:
		return meta, v
	case io.Writer:
		if respWriter, ok := body.(interface{ Header() http.Header }); ok {
			header := respWriter.Header()
			for k, v := range meta {
				if strings.HasPrefix(k, "Content-") {
					header[k] = v
				}
			}
		}
		if _, err := io.Copy(v, r.Body); err != nil {
			return meta, ReadFailed.StatusErr().WithDesc(err.Error())
		}
	default:
		contentType := meta.Get(httpx.HeaderContentType)

		if contentType != "" {
			contentType, _, _ = mime.ParseMediaType(contentType)
		}

		rv := reflect.ValueOf(body)
		transformer, err := r.NewTransformer(nil, typesutil.FromRType(rv.Type()), transformers.TransformerOption{
			MIME: contentType,
		})

		if err != nil {
			return meta, ReadFailed.StatusErr().WithDesc(err.Error())
		}

		if err := transformer.DecodeFromReader(r.Body, rv, textproto.MIMEHeader(r.Header)); err != nil {
			return meta, ReadFailed.StatusErr().WithDesc(err.Error())
		}
	}

	return meta, nil
}

func isOk(code int) bool {
	return code >= http.StatusOK && code < http.StatusMultipleChoices
}

func GetShortConnClient(timeout time.Duration, httpTransports ...HttpTransport) *http.Client {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   timeout,
				KeepAlive: 0,
			}).DialContext,
			DisableKeepAlives: true,
		},
	}

	if httpTransports != nil {
		for i := range httpTransports {
			httpTransport := httpTransports[i]
			client.Transport = httpTransport(client.Transport)
		}
	}

	return client
}
