package category

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nutmos/expensetrackr/pkg/validate"
)

// Input is the payload for POST /api/categories and PUT /api/categories/:uid.
type Input struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`

	// IgnoredUID lets a previous response be round-tripped; never used.
	IgnoredUID string `json:"uid,omitempty"`
}

const (
	MaxNameLen        = 100
	MaxDescriptionLen = 1000
)

// Validate checks the input and returns a normalized Category (no UID/ID/times).
func (in Input) Validate() (Category, error) {
	fields := map[string]string{}
	var c Category

	if nameErr := c.inputNameField(in.Name); nameErr != "" {
		fields["name"] = nameErr
	}

	if typeErr := c.inputTypeField(in.Type); typeErr != "" {
		fields["type"] = typeErr
	}

	if descErr := c.inputDescriptionField(in.Description); descErr != "" {
		fields["description"] = descErr
	}

	if len(fields) > 0 {
		return Category{}, &validate.ValidationError{Fields: fields}
	}
	return c, nil
}

func (c *Category) inputNameField(in string) string {
	c.Name = strings.TrimSpace(in)
	if c.Name == "" {
		return "is required"
	}
	if utf8.RuneCountInString(c.Name) > MaxNameLen {
		return fmt.Sprintf("must be at most %d characters", MaxNameLen)
	}
	return ""
}

func (c *Category) inputTypeField(in string) string {
	if t, ok := ParseType(in); ok {
		c.Type = t
		return ""
	}
	if strings.TrimSpace(in) == "" {
		return "is required (expense or income)"
	} else {
		return "must be one of: expense, income"
	}
}

func (c *Category) inputDescriptionField(in string) string {
	c.Description = strings.TrimSpace(in)
	if utf8.RuneCountInString(c.Description) > MaxDescriptionLen {
		return fmt.Sprintf("must be at most %d characters", MaxDescriptionLen)
	}
	return ""
}
