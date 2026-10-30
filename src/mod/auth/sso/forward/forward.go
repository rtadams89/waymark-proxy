package forward

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/utils"
)

type AuthRouterOptions struct {
	// Address of the forward auth endpoint.
	Address string

	// ResponseHeaders is a list of headers to be copied from the response if provided by the forward auth endpoint to
	// the request.
	ResponseHeaders []string

	// ResponseClientHeaders is a list of headers to be copied from the response if provided by the forward auth
	// endpoint to the response to the client.
	ResponseClientHeaders []string

	// RequestHeaders is a list of headers to be copied from the request to the authorization server. If empty all
	// headers are copied.
	RequestHeaders []string

	// RequestIncludedCookies is a list of cookie keys that if defined will be the only cookies sent in the request to
	// the authorization server.
	RequestIncludedCookies []string

	// RequestExcludedCookies is a list of cookie keys that should be removed from every request sent to the upstream.
	RequestExcludedCookies []string

	// RequestIncludeBody enables copying the request body to the request to the authorization server.
	RequestIncludeBody bool

	// UseXOriginalHeaders is a boolean that determines if the X-Original-* headers should be used instead of the
	// X-Forwarded-* headers.
	UseXOriginalHeaders bool

	// IgnoredPaths is a list of request path prefixes that are served without
	// forward auth.
	IgnoredPaths []string

	Logger   *logger.Logger
	Database *database.Database
}

type AuthRouter struct {
	client  *http.Client
	options *AuthRouterOptions
}

// NewAuthRouter creates a new AuthRouter object
func NewAuthRouter(options *AuthRouterOptions) *AuthRouter {
	options.Database.NewTable(DatabaseTable)

	options.Database.Read(DatabaseTable, DatabaseKeyAddress, &options.Address)

	responseHeaders, responseClientHeaders, requestHeaders, requestIncludedCookies, requestExcludedCookies := "", "", "", "", ""
	ignoredPaths := ""

	options.Database.Read(DatabaseTable, DatabaseKeyResponseHeaders, &responseHeaders)
	options.Database.Read(DatabaseTable, DatabaseKeyResponseClientHeaders, &responseClientHeaders)
	options.Database.Read(DatabaseTable, DatabaseKeyRequestHeaders, &requestHeaders)
	options.Database.Read(DatabaseTable, DatabaseKeyRequestIncludedCookies, &requestIncludedCookies)
	options.Database.Read(DatabaseTable, DatabaseKeyRequestExcludedCookies, &requestExcludedCookies)
	options.Database.Read(DatabaseTable, DatabaseKeyRequestIncludeBody, &options.RequestIncludeBody)
	options.Database.Read(DatabaseTable, DatabaseKeyUseXOriginalHeaders, &options.UseXOriginalHeaders)
	options.Database.Read(DatabaseTable, DatabaseKeyIgnoredPaths, &ignoredPaths)

	options.ResponseHeaders = cleanSplit(responseHeaders)
	options.ResponseClientHeaders = cleanSplit(responseClientHeaders)
	options.RequestHeaders = cleanSplit(requestHeaders)
	options.RequestIncludedCookies = cleanSplit(requestIncludedCookies)
	options.RequestExcludedCookies = cleanSplit(requestExcludedCookies)
	options.IgnoredPaths = cleanSplit(ignoredPaths)

	r := &AuthRouter{
		client: &http.Client{
			CheckRedirect: func(r *http.Request, via []*http.Request) (err error) {
				return http.ErrUseLastResponse
			},
		},
		options: options,
	}

	r.logOptions()

	return r
}

// HandleAPIOptions is the internal handler for setting the options.
func (ar *AuthRouter) HandleAPIOptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ar.handleOptionsGET(w, r)
	case http.MethodPost:
		ar.handleOptionsPOST(w, r)
	case http.MethodDelete:
		ar.handleOptionsDelete(w, r)
	default:
		ar.handleOptionsMethodNotAllowed(w, r)
	}
}

func (ar *AuthRouter) handleOptionsGET(w http.ResponseWriter, r *http.Request) {
	js, _ := json.Marshal(map[string]any{
		DatabaseKeyAddress:                ar.options.Address,
		DatabaseKeyResponseHeaders:        ar.options.ResponseHeaders,
		DatabaseKeyResponseClientHeaders:  ar.options.ResponseClientHeaders,
		DatabaseKeyRequestHeaders:         ar.options.RequestHeaders,
		DatabaseKeyRequestIncludedCookies: ar.options.RequestIncludedCookies,
		DatabaseKeyRequestExcludedCookies: ar.options.RequestExcludedCookies,
		DatabaseKeyRequestIncludeBody:     ar.options.RequestIncludeBody,
		DatabaseKeyUseXOriginalHeaders:    ar.options.UseXOriginalHeaders,
		DatabaseKeyIgnoredPaths:           ar.options.IgnoredPaths,
	})

	utils.SendJSONResponse(w, string(js))

	return
}

func (ar *AuthRouter) handleOptionsPOST(w http.ResponseWriter, r *http.Request) {
	address, err := utils.PostPara(r, DatabaseKeyAddress)
	if err != nil {
		utils.SendErrorResponse(w, "address not found")

		return
	}

	responseHeaders, _ := utils.PostPara(r, DatabaseKeyResponseHeaders)
	responseClientHeaders, _ := utils.PostPara(r, DatabaseKeyResponseClientHeaders)
	requestHeaders, _ := utils.PostPara(r, DatabaseKeyRequestHeaders)
	requestIncludedCookies, _ := utils.PostPara(r, DatabaseKeyRequestIncludedCookies)
	requestExcludedCookies, _ := utils.PostPara(r, DatabaseKeyRequestExcludedCookies)
	requestIncludeBody, _ := utils.PostBool(r, DatabaseKeyRequestIncludeBody)
	useXOriginalHeaders, _ := utils.PostBool(r, DatabaseKeyUseXOriginalHeaders)
	ignoredPaths, _ := utils.PostPara(r, DatabaseKeyIgnoredPaths)

	ar.options.Address = address
	ar.options.ResponseHeaders = cleanSplit(responseHeaders)
	ar.options.ResponseClientHeaders = cleanSplit(responseClientHeaders)
	ar.options.RequestHeaders = cleanSplit(requestHeaders)
	ar.options.RequestIncludedCookies = cleanSplit(requestIncludedCookies)
	ar.options.RequestExcludedCookies = cleanSplit(requestExcludedCookies)
	ar.options.RequestIncludeBody = requestIncludeBody
	ar.options.UseXOriginalHeaders = useXOriginalHeaders
	ar.options.IgnoredPaths = cleanSplit(ignoredPaths)

	ar.options.Database.Write(DatabaseTable, DatabaseKeyAddress, address)
	ar.options.Database.Write(DatabaseTable, DatabaseKeyResponseHeaders, responseHeaders)
	ar.options.Database.Write(DatabaseTable, DatabaseKeyResponseClientHeaders, responseClientHeaders)
	ar.options.Database.Write(DatabaseTable, DatabaseKeyRequestHeaders, requestHeaders)
	ar.options.Database.Write(DatabaseTable, DatabaseKeyRequestIncludedCookies, requestIncludedCookies)
	ar.options.Database.Write(DatabaseTable, DatabaseKeyRequestExcludedCookies, requestExcludedCookies)
	ar.options.Database.Write(DatabaseTable, DatabaseKeyRequestIncludeBody, ar.options.RequestIncludeBody)
	ar.options.Database.Write(DatabaseTable, DatabaseKeyUseXOriginalHeaders, ar.options.UseXOriginalHeaders)
	ar.options.Database.Write(DatabaseTable, DatabaseKeyIgnoredPaths, ignoredPaths)

	ar.logOptions()

	utils.SendOK(w)
}

func (ar *AuthRouter) handleOptionsDelete(w http.ResponseWriter, r *http.Request) {
	ar.options.Address = ""
	ar.options.ResponseHeaders = nil
	ar.options.ResponseClientHeaders = nil
	ar.options.RequestHeaders = nil
	ar.options.RequestIncludedCookies = nil
	ar.options.RequestExcludedCookies = nil
	ar.options.RequestIncludeBody = false
	ar.options.UseXOriginalHeaders = false
	ar.options.IgnoredPaths = nil

	ar.options.Database.Delete(DatabaseTable, DatabaseKeyAddress)
	ar.options.Database.Delete(DatabaseTable, DatabaseKeyResponseHeaders)
	ar.options.Database.Delete(DatabaseTable, DatabaseKeyResponseClientHeaders)
	ar.options.Database.Delete(DatabaseTable, DatabaseKeyRequestHeaders)
	ar.options.Database.Delete(DatabaseTable, DatabaseKeyRequestIncludedCookies)
	ar.options.Database.Delete(DatabaseTable, DatabaseKeyRequestExcludedCookies)
	ar.options.Database.Delete(DatabaseTable, DatabaseKeyRequestIncludeBody)
	ar.options.Database.Delete(DatabaseTable, DatabaseKeyUseXOriginalHeaders)
	ar.options.Database.Delete(DatabaseTable, DatabaseKeyIgnoredPaths)

	utils.SendOK(w)
}

func (ar *AuthRouter) handleOptionsMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

	return
}

// HandleAuthProviderRouting is the internal handler for Forward Auth authentication.
func (ar *AuthRouter) HandleAuthProviderRouting(w http.ResponseWriter, r *http.Request) error {
	// Ignored paths skip forward auth
	if ar.IsIgnoredPath(r.RequestURI) {
		headerReplaceIncluded(nil, r.Header, ar.options.ResponseHeaders)
		return nil
	}

	if ar.options.Address == "" {
		return ar.handle500Error(w, nil, "Address not set")
	}

	req, err := http.NewRequest(http.MethodGet, ar.options.Address, nil)
	if err != nil {
		return ar.handle500Error(w, err, "Unable to create request")
	}

	headerCopyIncluded(r.Header, req.Header, ar.options.RequestHeaders, true)
	headerCookieRedact(r, ar.options.RequestIncludedCookies, false)

	if ar.options.UseXOriginalHeaders {
		rSetXOriginalHeaders(r, req)
	} else {
		rSetXForwardedHeaders(r, req)
	}

	if ar.options.RequestIncludeBody {
		if err = rCopyBody(r, req); err != nil {
			return ar.handle500Error(w, err, "Unable to perform forwarded auth due to a request copy error")
		}
	}

	respForwarded, err := ar.client.Do(req)
	if err != nil {
		return ar.handle500Error(w, err, "Unable to perform forwarded auth due to a request error")
	}

	defer respForwarded.Body.Close()

	// A 2xx response lets the request through
	if respForwarded.StatusCode >= http.StatusOK && respForwarded.StatusCode < http.StatusMultipleChoices {
		if len(ar.options.ResponseClientHeaders) != 0 {
			headerCopyIncluded(respForwarded.Header, w.Header(), ar.options.ResponseClientHeaders, false)
		}

		headerCookieRedact(r, ar.options.RequestExcludedCookies, true)

		if len(ar.options.ResponseHeaders) != 0 {
			// Copies the configured response headers to the upstream request
			headerReplaceIncluded(respForwarded.Header, r.Header, ar.options.ResponseHeaders)
		}

		return nil
	}

	// Returns the unsuccessful response to the client
	headerCopyExcluded(respForwarded.Header, w.Header(), nil)

	if ar.options.UseXOriginalHeaders && respForwarded.StatusCode == 401 && respForwarded.Header.Get(HeaderLocation) != "" {
		w.WriteHeader(http.StatusFound)
	} else {
		w.WriteHeader(respForwarded.StatusCode)
	}

	if _, err = io.Copy(w, respForwarded.Body); err != nil {
		return ar.handle500Error(w, err, "Unable to copy response")
	}

	return ErrUnauthorized
}

// handle500Error sends a 500 response and logs err.
func (ar *AuthRouter) handle500Error(w http.ResponseWriter, err error, message string) error {
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

	ar.options.Logger.PrintAndLog(LogTitle, message, err)

	return ErrInternalServerError
}

func (ar *AuthRouter) logOptions() {
	ar.options.Logger.PrintAndLog(LogTitle, fmt.Sprintf("Forward Authz Options -> Address: %s, Response Headers: %s, Response Client Headers: %s, Request Headers: %s, Request Included Cookies: %s, Request Excluded Cookies: %s, Request Include Body: %t, Use X-Original Headers: %t, Ignored Paths: %s", ar.options.Address, strings.Join(ar.options.ResponseHeaders, ";"), strings.Join(ar.options.ResponseClientHeaders, ";"), strings.Join(ar.options.RequestHeaders, ";"), strings.Join(ar.options.RequestIncludedCookies, ";"), strings.Join(ar.options.RequestExcludedCookies, ";"), ar.options.RequestIncludeBody, ar.options.UseXOriginalHeaders, strings.Join(ar.options.IgnoredPaths, ";")), nil)
}
