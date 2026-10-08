package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// pageResources are the browsable resources of the web page. Each gets
// history-friendly URLs, all served by the same index.html (the client-side
// router in static/router.js picks the view from location.pathname):
//
//	/<res>              list (browse only)
//	/<res>/new          add form
//	/<res>/:uid/edit    edit form (uid must look like a UUID, else 404)
//
// "/" is the transactions list.
var pageResources = []string{"transactions", "balances", "categories"}

func (s *Server) registerPageRoutes(r *gin.Engine) {
	r.GET("/", s.index)
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
	if !uuidRE.MatchString(strings.ToLower(c.Param("uid"))) {
		c.JSON(http.StatusNotFound, errorBody{Error: "not found"})
		return
	}
	s.index(c)
}
