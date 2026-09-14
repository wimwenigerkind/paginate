package ginx

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wimwenigerkind/paginate"
)

// Query parameter names. They are the wire contract, shared across services.
const (
	ParamLimit  = "limit"
	ParamCursor = "cursor"
	ParamSort   = "sort"
	ParamPage   = "page"
)

// unparseable is handed to paginate in place of a number we could not read, so
// that "limit=banana" is refused with the same typed error as "limit=-1"
// instead of being silently treated as "no limit given".
const unparseable = -1

// Bind reads limit, cursor and sort from the query string.
//
// It does not report errors of its own: anything malformed is carried into the
// Request and refused by paginate.Keyset, so there is one place where requests
// are validated and one error shape to render.
func Bind(c *gin.Context) paginate.Request {
	return paginate.Request{
		Limit:  intParam(c, ParamLimit),
		Cursor: c.Query(ParamCursor),
		Sort:   c.Query(ParamSort),
	}
}

// BindOffset reads page, limit and sort from the query string.
func BindOffset(c *gin.Context) paginate.OffsetRequest {
	return paginate.OffsetRequest{
		Page:  intParam(c, ParamPage),
		Limit: intParam(c, ParamLimit),
		Sort:  c.Query(ParamSort),
	}
}

func intParam(c *gin.Context, name string) int {
	raw := c.Query(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return unparseable
	}
	return n
}

// ErrorBody is the response rendered for a bad pagination request.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes what the client got wrong.
type ErrorDetail struct {
	// Code is paginate's stable machine-readable code, such as
	// "limit_too_large" or "invalid_cursor".
	Code string `json:"code"`
	// Param names the query parameter at fault, when there is one.
	Param string `json:"param,omitempty"`
	// Message is a human-readable explanation. It is not a stable contract;
	// branch on Code.
	Message string `json:"message"`
}

// Error maps err to an HTTP response.
//
// ok is false for anything that is not the client's fault: database failures,
// and configuration mistakes, which are a 500 and belong to the service's own
// error handling rather than to a 400.
func Error(err error) (status int, body ErrorBody, ok bool) {
	var perr *paginate.Error
	if !errors.As(err, &perr) || !perr.Code.ClientFault() {
		return 0, ErrorBody{}, false
	}
	return http.StatusBadRequest, ErrorBody{Error: ErrorDetail{
		Code:    string(perr.Code),
		Param:   perr.Param,
		Message: perr.Message(),
	}}, true
}

// Abort writes the response for a bad pagination request and reports whether
// it did. When it returns false the error was not the client's fault and the
// caller should hand it to its own error handling.
//
//	page, err := paginate.Keyset[Post](db, p, ginx.Bind(c))
//	if err != nil {
//	    if !ginx.Abort(c, err) {
//	        _ = c.Error(err)
//	    }
//	    return
//	}
//	c.JSON(http.StatusOK, page)
//
// Going through Abort rather than hand-rolling the response in each handler is
// what keeps the error contract identical across services.
func Abort(c *gin.Context, err error) bool {
	status, body, ok := Error(err)
	if !ok {
		return false
	}
	c.AbortWithStatusJSON(status, body)
	return true
}
