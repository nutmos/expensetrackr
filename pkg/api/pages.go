package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/report"
)

// pageResources are the browsable resources of the web page. Each gets
// history-friendly URLs, all served by the same index.html (the client-side
// router in static/router.js picks the view from location.pathname):
//
//	/<res>              list (browse only)
//	/<res>/new          add form
//	/<res>/:uid/edit    edit form (uid must look like a UUID, else 404)
//
// "/" is the transactions list; /login and /register are the signed-out pages.
var pageResources = []string{"transactions", "balances", "categories"}

func (s *Server) registerPageRoutes(r *gin.Engine) {
	r.GET("/", s.index)
	// Signed-out pages (issue #32): log in, and create account (first run).
	r.GET("/login", s.index)
	r.GET("/register", s.index)
	// The signed-in user's preferences.
	r.GET("/settings", s.index)
	// Reports (issue #136): /reports opens the first one; /reports/<id> is
	// one of report.Catalog (anything else is a 404).
	r.GET("/reports", s.index)
	r.GET("/reports/:report", func(c *gin.Context) {
		if !report.Known(c.Param("report")) {
			c.JSON(http.StatusNotFound, errorBody{Error: "not found"})
			return
		}
		s.index(c)
	})
	for _, res := range pageResources {
		r.GET("/"+res, s.index)
		r.GET("/"+res+"/new", s.index)
		r.GET("/"+res+"/:uid/edit", s.editPage)
	}
}

// editPage serves index.html for /<res>/:uid/edit when :uid has UUID shape
// (any case; the client lowercases it). Whether the record exists is checked
// by the page itself through the API, so a deleted uid shows a message there.
func (s *Server) editPage(c *gin.Context) {
	if !common_type.NormalizeUID(c.Param("uid")).Valid() {
		c.JSON(http.StatusNotFound, errorBody{Error: "not found"})
		return
	}
	s.index(c)
}
