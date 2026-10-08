package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

// Optimistic locking (balances and transactions).
//
// Every balance and transaction has a version (1 on create, +1 on every
// change). Responses for a single record carry it in the JSON "version" field
// and in an ETag header ("3"). PUT and PATCH must say which version they were
// based on, either as an If-Match header (preferred; `"3"`, and `W/"3"` is
// accepted too) or as "version" in the JSON body:
//
//   - neither sent:                 428 Precondition Required (code version_required)
//   - malformed / both disagree:    400
//   - stale (record changed since): 409 Conflict (code version_conflict) with
//     the current record in "current" and its ETag
//
// DELETE accepts an optional If-Match with the same 409 on a stale version.

const (
	codeVersionRequired = "version_required"
	codeVersionConflict = "version_conflict"
)

func setETag(c *gin.Context, version int64) {
	c.Header("ETag", strconv.Quote(strconv.FormatInt(version, 10)))
}

// parseETag accepts a single entity tag holding a positive version number:
// "3" or W/"3".
func parseETag(s string) (int64, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "W/")
	if len(s) < 3 || s[0] != '"' || s[len(s)-1] != '"' {
		return 0, false
	}
	v, err := strconv.ParseInt(s[1:len(s)-1], 10, 64)
	if err != nil || v < 1 {
		return 0, false
	}
	return v, true
}

// clientVersion returns the version the client based its write on, from the
// If-Match header (preferred) or bodyVersion. If both are sent they must
// agree. When required and neither is present it writes 428. On any failure
// a response has been written and ok is false. A zero version with ok means
// "not sent" (only possible when !required).
func clientVersion(c *gin.Context, bodyVersion *int64, required bool) (version int64, ok bool) {
	h := strings.TrimSpace(c.GetHeader("If-Match"))
	var hv int64
	if h != "" {
		v, ok := parseETag(h)
		if !ok {
			c.JSON(http.StatusBadRequest, errorBody{Error: `If-Match must be a single version entity tag such as "3" (the ETag header or "version" field of your last read)`})
			return 0, false
		}
		hv = v
	}
	if bodyVersion != nil && *bodyVersion < 1 {
		c.JSON(http.StatusBadRequest, errorBody{Error: `field "version" must be a positive integer`})
		return 0, false
	}
	switch {
	case h != "" && bodyVersion != nil && *bodyVersion != hv:
		c.JSON(http.StatusBadRequest, errorBody{Error: fmt.Sprintf(`If-Match ("%d") and the body "version" (%d) disagree; send one, or the same value in both`, hv, *bodyVersion)})
		return 0, false
	case h != "":
		return hv, true
	case bodyVersion != nil:
		return *bodyVersion, true
	case required:
		c.JSON(http.StatusPreconditionRequired, errorBody{
			Error: `this update needs the version it is based on: send an If-Match header with the ETag of your last read (e.g. If-Match: "3") or "version" in the body`,
			Code:  codeVersionRequired,
		})
		return 0, false
	}
	return 0, true
}

// takeVersion removes "version" from a PATCH body and returns it (nil if
// absent or null).
func takeVersion(patch map[string]json.RawMessage) (*int64, error) {
	raw, ok := patch["version"]
	delete(patch, "version")
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var v int64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, &validate.RequestError{Msg: `field "version" must be a positive integer`}
	}
	return &v, nil
}

// writeVersionConflict answers 409 with the current record and its ETag.
func writeVersionConflict(c *gin.Context, what string, current any, version int64) {
	setETag(c, version)
	c.JSON(http.StatusConflict, errorBody{
		Error: fmt.Sprintf("version conflict: this %s was changed since you read it (now at version %d); nothing was saved. Reload it, re-apply your change and send the new version", what, version),
		Code:  codeVersionConflict,
		Fields: map[string]string{
			"version": fmt.Sprintf("is stale; the current version is %d", version),
		},
		Current: current,
	})
}
