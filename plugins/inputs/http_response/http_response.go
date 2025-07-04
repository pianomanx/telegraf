//go:generate ../../../tools/readme_config_includer/generator
package http_response

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/benbjohnson/clock"
	"github.com/seancfoley/ipaddress-go/ipaddr"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/plugins/common/cookie"
	"github.com/influxdata/telegraf/plugins/common/tls"
	"github.com/influxdata/telegraf/plugins/inputs"
)

//go:embed sample.conf
var sampleConfig string

const (
	// defaultResponseBodyMaxSize is the default maximum response body size, in bytes.
	// if the response body is over this size, we will raise a body_read_error.
	defaultResponseBodyMaxSize = 32 * 1024 * 1024
)

type HTTPResponse struct {
	URLs            []string            `toml:"urls"`
	HTTPProxy       string              `toml:"http_proxy"`
	Body            string              `toml:"body"`
	BodyForm        map[string][]string `toml:"body_form"`
	Method          string              `toml:"method"`
	ResponseTimeout config.Duration     `toml:"response_timeout"`
	HTTPHeaderTags  map[string]string   `toml:"http_header_tags"`
	Headers         map[string]string   `toml:"headers"`
	FollowRedirects bool                `toml:"follow_redirects"`
	// Absolute path to file with Bearer token
	BearerToken         string      `toml:"bearer_token"`
	ResponseBodyField   string      `toml:"response_body_field"`
	ResponseBodyMaxSize config.Size `toml:"response_body_max_size"`
	ResponseStringMatch string      `toml:"response_string_match"`
	ResponseStatusCode  int         `toml:"response_status_code"`
	Interface           string      `toml:"interface"`
	// HTTP Basic Auth Credentials
	Username config.Secret `toml:"username"`
	Password config.Secret `toml:"password"`
	tls.ClientConfig
	cookie.CookieAuthConfig

	Log telegraf.Logger `toml:"-"`

	compiledStringMatch *regexp.Regexp
	clients             []client
}

type client struct {
	httpClient httpClient
	address    string
}

type httpClient interface {
	// Do implements [http.Client]
	Do(req *http.Request) (*http.Response, error)
}

func (*HTTPResponse) SampleConfig() string {
	return sampleConfig
}

func (h *HTTPResponse) Init() error {
	// Compile the body regex if it exists
	if h.ResponseStringMatch != "" {
		var err error
		h.compiledStringMatch, err = regexp.Compile(h.ResponseStringMatch)
		if err != nil {
			return fmt.Errorf("failed to compile regular expression %q: %w", h.ResponseStringMatch, err)
		}
	}

	// Set default values
	if h.ResponseTimeout < config.Duration(time.Second) {
		h.ResponseTimeout = config.Duration(time.Second * 5)
	}
	if h.Method == "" {
		h.Method = "GET"
	}

	if len(h.URLs) == 0 {
		h.URLs = []string{"http://localhost"}
	}

	h.clients = make([]client, 0, len(h.URLs))
	for _, u := range h.URLs {
		addr, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("%q is not a valid address: %w", u, err)
		}

		if addr.Scheme != "http" && addr.Scheme != "https" {
			return fmt.Errorf("%q is not a valid address: only http and https types are supported", u)
		}

		cl, err := h.createHTTPClient(*addr)
		if err != nil {
			return err
		}

		h.clients = append(h.clients, client{httpClient: cl, address: u})
	}

	return nil
}

func (h *HTTPResponse) Gather(acc telegraf.Accumulator) error {
	for _, c := range h.clients {
		// Prepare data
		var fields map[string]interface{}
		var tags map[string]string

		// Gather data
		fields, tags, err := h.httpGather(c)
		if err != nil {
			acc.AddError(err)
			continue
		}

		// Add metrics
		acc.AddFields("http_response", fields, tags)
	}

	return nil
}

// Set the proxy. A configured proxy overwrites the system-wide proxy.
func getProxyFunc(httpProxy string) func(*http.Request) (*url.URL, error) {
	if httpProxy == "" {
		return http.ProxyFromEnvironment
	}
	proxyURL, err := url.Parse(httpProxy)
	if err != nil {
		return func(_ *http.Request) (*url.URL, error) {
			return nil, errors.New("bad proxy: " + err.Error())
		}
	}
	return func(*http.Request) (*url.URL, error) {
		return proxyURL, nil
	}
}

// createHTTPClient creates an http client which will time out at the specified
// timeout period and can follow redirects if specified
func (h *HTTPResponse) createHTTPClient(address url.URL) (*http.Client, error) {
	tlsCfg, err := h.ClientConfig.TLSConfig()
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{}

	if h.Interface != "" {
		dialer.LocalAddr, err = localAddress(h.Interface, address)
		if err != nil {
			return nil, err
		}
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             getProxyFunc(h.HTTPProxy),
			DialContext:       dialer.DialContext,
			DisableKeepAlives: true,
			TLSClientConfig:   tlsCfg,
		},
		Timeout: time.Duration(h.ResponseTimeout),
	}

	if !h.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	if h.CookieAuthConfig.URL != "" {
		if err := h.CookieAuthConfig.Start(client, h.Log, clock.New()); err != nil {
			return nil, err
		}
	}

	return client, nil
}

func localAddress(interfaceName string, address url.URL) (net.Addr, error) {
	i, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return nil, err
	}

	addrs, err := i.Addrs()
	if err != nil {
		return nil, err
	}

	urlInIPv6, zone := isURLInIPv6(address)
	for _, addr := range addrs {
		if naddr, ok := addr.(*net.IPNet); ok {
			ipNetInIPv6 := isIPNetInIPv6(naddr)

			// choose interface address in the same format as server address
			if ipNetInIPv6 == urlInIPv6 {
				// leaving port set to zero to let kernel pick, but set zone
				return &net.TCPAddr{IP: naddr.IP, Zone: zone}, nil
			}
		}
	}

	return nil, fmt.Errorf("cannot create local address for interface %q and server address %q", interfaceName, address.String())
}

// isURLInIPv6 returns (true, zoneName) only when URL is in IPv6 format.
// For other cases (host part of url cannot be successfully validated, doesn't contain address at all or is in IPv4 format), it returns (false, "").
func isURLInIPv6(address url.URL) (bool, string) {
	host := ipaddr.NewHostName(address.Host)
	if err := host.Validate(); err != nil {
		return false, ""
	}
	if hostAddr := host.AsAddress(); hostAddr != nil {
		if ipv6 := hostAddr.ToIPv6(); ipv6 != nil {
			return true, ipv6.GetZone().String()
		}
	}

	return false, ""
}

// isIPNetInIPv6 returns true only when IPNet can be represented in IPv6 format.
// For other cases (address cannot be successfully parsed or is in IPv4 format), it returns false.
func isIPNetInIPv6(address *net.IPNet) bool {
	ipAddr, err := ipaddr.NewIPAddressFromNetIPNet(address)
	return err == nil && ipAddr.ToIPv6() != nil
}

func setResult(resultString string, fields map[string]interface{}, tags map[string]string) {
	resultCodes := map[string]int{
		"success":                       0,
		"response_string_mismatch":      1,
		"body_read_error":               2,
		"connection_failed":             3,
		"timeout":                       4,
		"dns_error":                     5,
		"response_status_code_mismatch": 6,
	}

	tags["result"] = resultString
	fields["result_type"] = resultString
	fields["result_code"] = resultCodes[resultString]
}

func setError(err error, fields map[string]interface{}, tags map[string]string) error {
	var timeoutError net.Error
	if errors.As(err, &timeoutError) && timeoutError.Timeout() {
		setResult("timeout", fields, tags)
		return timeoutError
	}

	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return nil
	}

	var opErr *net.OpError
	if errors.As(urlErr, &opErr) {
		var dnsErr *net.DNSError
		var parseErr *net.ParseError

		if errors.As(opErr, &dnsErr) {
			setResult("dns_error", fields, tags)
			return dnsErr
		} else if errors.As(opErr, &parseErr) {
			// Parse error has to do with parsing of IP addresses, so we
			// group it with address errors
			setResult("address_error", fields, tags)
			return parseErr
		}
	}

	return nil
}

// HTTPGather gathers all fields and returns any errors it encounters
func (h *HTTPResponse) httpGather(cl client) (map[string]interface{}, map[string]string, error) {
	// Prepare fields and tags
	fields := make(map[string]interface{})
	tags := map[string]string{"server": cl.address, "method": h.Method}

	var body io.Reader
	if h.Body != "" {
		body = strings.NewReader(h.Body)
	} else if len(h.BodyForm) != 0 {
		values := url.Values{}
		for k, vs := range h.BodyForm {
			for _, v := range vs {
				values.Add(k, v)
			}
		}
		body = strings.NewReader(values.Encode())
	}

	request, err := http.NewRequest(h.Method, cl.address, body)
	if err != nil {
		return nil, nil, err
	}

	if _, uaPresent := h.Headers["User-Agent"]; !uaPresent {
		request.Header.Set("User-Agent", internal.ProductToken())
	}

	if h.BearerToken != "" {
		token, err := os.ReadFile(h.BearerToken)
		if err != nil {
			return nil, nil, err
		}
		bearer := "Bearer " + strings.Trim(string(token), "\n")
		request.Header.Add("Authorization", bearer)
	}

	for key, val := range h.Headers {
		request.Header.Add(key, val)
		if key == "Host" {
			request.Host = val
		}
	}

	if err := h.setRequestAuth(request); err != nil {
		return nil, nil, err
	}

	// Start Timer
	start := time.Now()
	resp, err := cl.httpClient.Do(request)
	responseTime := time.Since(start).Seconds()

	// If an error in returned, it means we are dealing with a network error, as
	// HTTP error codes do not generate errors in the net/http library
	if err != nil {
		// Log error
		h.Log.Debugf("Network error while polling %s: %s", cl.address, err.Error())

		// Get error details
		if setError(err, fields, tags) == nil {
			// Any error not recognized by `set_error` is considered a "connection_failed"
			setResult("connection_failed", fields, tags)
		}

		return fields, tags, nil
	}

	if _, ok := fields["response_time"]; !ok {
		fields["response_time"] = responseTime
	}

	// This function closes the response body, as
	// required by the net/http library
	defer resp.Body.Close()

	// Add the response headers
	for headerName, tag := range h.HTTPHeaderTags {
		headerValues, foundHeader := resp.Header[headerName]
		if foundHeader && len(headerValues) > 0 {
			tags[tag] = headerValues[0]
		}
	}

	// Set log the HTTP response code
	tags["status_code"] = strconv.Itoa(resp.StatusCode)
	fields["http_response_code"] = resp.StatusCode

	if h.ResponseBodyMaxSize == 0 {
		h.ResponseBodyMaxSize = config.Size(defaultResponseBodyMaxSize)
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, int64(h.ResponseBodyMaxSize)+1))
	// Check first if the response body size exceeds the limit.
	if err == nil && int64(len(bodyBytes)) > int64(h.ResponseBodyMaxSize) {
		h.setBodyReadError("The body of the HTTP Response is too large", bodyBytes, fields, tags)
		return fields, tags, nil
	} else if err != nil {
		h.setBodyReadError("Failed to read body of HTTP Response : "+err.Error(), bodyBytes, fields, tags)
		return fields, tags, nil
	}

	// Add the body of the response if expected
	if len(h.ResponseBodyField) > 0 {
		// Check that the content of response contains only valid utf-8 characters.
		if !utf8.Valid(bodyBytes) {
			h.setBodyReadError("The body of the HTTP Response is not a valid utf-8 string", bodyBytes, fields, tags)
			return fields, tags, nil
		}
		fields[h.ResponseBodyField] = string(bodyBytes)
	}
	fields["content_length"] = len(bodyBytes)

	var success = true

	// Check the response for a regex
	if h.ResponseStringMatch != "" {
		if h.compiledStringMatch.Match(bodyBytes) {
			fields["response_string_match"] = 1
		} else {
			success = false
			setResult("response_string_mismatch", fields, tags)
			fields["response_string_match"] = 0
		}
	}

	// Check the response status code
	if h.ResponseStatusCode > 0 {
		if resp.StatusCode == h.ResponseStatusCode {
			fields["response_status_code_match"] = 1
		} else {
			success = false
			setResult("response_status_code_mismatch", fields, tags)
			fields["response_status_code_match"] = 0
		}
	}

	if success {
		setResult("success", fields, tags)
	}

	return fields, tags, nil
}

// Set result in case of a body read error
func (h *HTTPResponse) setBodyReadError(errorMsg string, bodyBytes []byte, fields map[string]interface{}, tags map[string]string) {
	h.Log.Debug(errorMsg)
	setResult("body_read_error", fields, tags)
	fields["content_length"] = len(bodyBytes)
	if h.ResponseStringMatch != "" {
		fields["response_string_match"] = 0
	}
}

func (h *HTTPResponse) setRequestAuth(request *http.Request) error {
	if h.Username.Empty() || h.Password.Empty() {
		return nil
	}

	username, err := h.Username.Get()
	if err != nil {
		return fmt.Errorf("getting username failed: %w", err)
	}
	defer username.Destroy()
	password, err := h.Password.Get()
	if err != nil {
		return fmt.Errorf("getting password failed: %w", err)
	}
	defer password.Destroy()
	request.SetBasicAuth(username.String(), password.String())

	return nil
}

func init() {
	inputs.Add("http_response", func() telegraf.Input {
		return &HTTPResponse{}
	})
}
