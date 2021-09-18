package main

import (
	"golang.org/x/sync/singleflight"
	"sync"
)

var (
	announcementMap sync.Map
	announcementS   singleflight.Group
)

func (h *handlers) getAnnouncementDetail(ID string) (AnnouncementDetail, error) {
	detailI, ok := announcementMap.Load(ID)
	if ok {
		return detailI.(AnnouncementDetail), nil
	}
	detail, err, _ := announcementS.Do(ID, func() (interface{}, error) {
		var announcement AnnouncementDetail
		query := "SELECT `announcements`.`id`, `courses`.`id` AS `course_id`, `courses`.`name` AS `course_name`, `announcements`.`title`, `announcements`.`message`" +
			" FROM `announcements`" +
			" JOIN `courses` ON `courses`.`id` = `announcements`.`course_id`" +
			" WHERE `announcements`.`id` = ?"
		if err := h.DB.Get(&announcement, query, ID); err != nil {
			return nil, err
		}
		announcementMap.Store(ID, announcement)
		return announcement, nil
	})
	if err != nil {
		return AnnouncementDetail{}, err
	}
	return detail.(AnnouncementDetail), nil
}

type userCourses struct {
	courses map[string]bool
	mu      sync.Mutex
}

var (
	userCoursesMap      = map[string]*userCourses{}
	userCoursesMapMutex sync.Mutex
)

func (h *handlers) isUserRegistered(userID string, courseID string) (bool, error) {
	userCoursesMapMutex.Lock()
	cources, ok := userCoursesMap[userID]
	if !ok {
		cources = &userCourses{
			courses: map[string]bool{},
		}
		userCoursesMap[userID] = cources
	}
	userCoursesMapMutex.Unlock()

	cources.mu.Lock()
	defer cources.mu.Unlock()

	registered, ok := cources.courses[courseID]
	if !ok {
		var registrationCount int
		if err := h.DB.Get(&registrationCount, "SELECT COUNT(*) FROM `registrations` WHERE `user_id` = ? AND `course_id` = ? LIMIT 1", userID, courseID); err != nil {
			return false, err
		}
		if registrationCount == 0 {
			if _, c := h.getCourse(courseID); c.Status != StatusRegistration {
				cources.courses[courseID] = false
			}
			return false, nil
		}
		cources.courses[courseID] = true
		return true, nil
	}
	return registered, nil
}
