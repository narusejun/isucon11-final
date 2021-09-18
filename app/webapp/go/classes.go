package main

import (
	"fmt"
	"time"
)

type (
	etag struct {
		value string
		valid bool
	}
)

var (
	classesEtag_uc map[string]map[string]*etag
	classesEtag_cu map[string]map[string]*etag
)

func (h *handlers) setClassesEtag(courseID string, userID string) string {
	classesEtag_u, ok := classesEtag_cu[courseID]
	if !ok {
		classesEtag_u = make(map[string]*etag)
		classesEtag_cu[courseID] = classesEtag_u
	}

	classesEtag_c, ok := classesEtag_uc[userID]
	if !ok {
		classesEtag_c = make(map[string]*etag)
		classesEtag_uc[userID] = classesEtag_c
	}

	etag := &etag{
		value: fmt.Sprintf("W/\"%s\"", time.Now().Format(time.RFC3339)),
		valid: true,
	}

	classesEtag_u[userID] = etag
	classesEtag_c[courseID] = etag

	return etag.value
}
func (h *handlers) getClassesEtag(courseID string, userID string) string {
	classesEtag_u, ok := classesEtag_cu[courseID]
	if !ok {
		return ""
	}

	etag, ok := classesEtag_u[userID]
	if !ok {
		return ""
	}

	if !etag.valid {
		return ""
	}

	return etag.value
}

func (h *handlers) discardClassesEtagByUser(userID string) {
	classesEtag_c, ok := classesEtag_uc[userID]
	if !ok {
		return
	}

	for _, etag := range classesEtag_c {
		etag.valid = false
	}
}
func (h *handlers) discardClassesEtagByCource(courceId string) {
	classesEtag_u, ok := classesEtag_cu[courceId]
	if !ok {
		return
	}

	for _, etag := range classesEtag_u {
		etag.valid = false
	}
}
